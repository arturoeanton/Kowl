package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// calls records what a dispatcher passed through to the Runner.
type calls struct {
	mu     sync.Mutex
	events []string
	hook   func(op, name string) error
	// written is what the fake hook reports having changed through Kowl's helpers.
	written []string
}

func (c *calls) run(op, name string) ([]string, error) {
	c.mu.Lock()
	c.events = append(c.events, op+" "+name)
	hook, written := c.hook, c.written
	c.mu.Unlock()
	if hook != nil {
		return written, hook(op, name)
	}
	return written, nil
}

func (c *calls) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func (c *calls) all() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.events, ",")
}

func TestDispatchPassesUndebouncedOpsStraightThrough(t *testing.T) {
	recorded := &calls{}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 50*time.Millisecond, false)
	defer d.Close()

	for _, op := range []string{"EXIST", "TICKER", "NOT_FOUND", "REMOVE", "RENAME"} {
		d.Dispatch(op, "/tmp/observed")
	}
	d.idle(t)

	if got := recorded.count(); got != 5 {
		t.Fatalf("ran %d hooks, want 5 (%s)", got, recorded.all())
	}
}

// A single editor save emits several write events; the hook should run once.
func TestDispatchCollapsesABurstOfWrites(t *testing.T) {
	recorded := &calls{}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 60*time.Millisecond, false)
	defer d.Close()

	for i := 0; i < 10; i++ {
		d.Dispatch("WRITE", "/tmp/observed")
		time.Sleep(5 * time.Millisecond)
	}

	waitForCount(t, recorded, 1)
	time.Sleep(120 * time.Millisecond)
	if got := recorded.count(); got != 1 {
		t.Fatalf("ran %d hooks for one burst, want 1 (%s)", got, recorded.all())
	}
}

// Debouncing must delay, not drop: the hook still runs after the burst settles.
func TestDispatchRunsAfterTheBurstSettles(t *testing.T) {
	recorded := &calls{}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 40*time.Millisecond, false)
	defer d.Close()

	d.Dispatch("WRITE", "/tmp/observed")
	if got := recorded.count(); got != 0 {
		t.Fatalf("hook ran immediately despite the debounce window (%s)", recorded.all())
	}
	waitForCount(t, recorded, 1)
}

func TestDispatchKeepsSeparateFilesApart(t *testing.T) {
	recorded := &calls{}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 40*time.Millisecond, false)
	defer d.Close()

	d.Dispatch("WRITE", "/tmp/one")
	d.Dispatch("WRITE", "/tmp/two")

	waitForCount(t, recorded, 2)
	if got := recorded.all(); !strings.Contains(got, "/tmp/one") || !strings.Contains(got, "/tmp/two") {
		t.Fatalf("dispatched %q, want both files", got)
	}
}

func TestDispatchWithoutDebounceRunsImmediately(t *testing.T) {
	recorded := &calls{}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 0, false)
	defer d.Close()

	d.Dispatch("WRITE", "/tmp/observed")
	d.idle(t)

	if got := recorded.count(); got != 1 {
		t.Fatalf("ran %d hooks with debouncing off, want 1", got)
	}
}

// A hook that writes the file it was woken for used to wake itself again, forever. The
// README worked around it with an environment-variable flag.
func TestDispatchIgnoresEventsCausedByTheHookItself(t *testing.T) {
	file := filepath.Join(t.TempDir(), "observed.txt")
	if err := os.WriteFile(file, []byte("start"), 0o644); err != nil {
		t.Fatal(err)
	}

	recorded := &calls{hook: func(op, name string) error {
		return os.WriteFile(name, []byte("rewritten by the hook"), 0o644)
	}}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 0, false)
	defer d.Close()

	// The first event is genuine and runs the hook, which rewrites the file. The next
	// event is the one that write produced.
	d.Dispatch("WRITE", file)
	d.Dispatch("WRITE", file)
	d.Dispatch("WRITE", file)
	d.idle(t)

	if got := recorded.count(); got != 1 {
		t.Fatalf("ran %d hooks, want 1: the hook is retriggering itself (%s)", got, recorded.all())
	}
}

// Suppression must not swallow a genuine change that happens after the hook wrote.
func TestDispatchStillRunsAfterSomethingElseChangesTheFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "observed.txt")
	if err := os.WriteFile(file, []byte("start"), 0o644); err != nil {
		t.Fatal(err)
	}

	recorded := &calls{hook: func(op, name string) error {
		return os.WriteFile(name, []byte("rewritten by the hook"), 0o644)
	}}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 0, false)
	defer d.Close()

	d.Dispatch("WRITE", file)
	d.idle(t)

	// Someone else edits the file, so the next event is not the hook's own write.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(file, []byte("edited by someone else"), 0o644); err != nil {
		t.Fatal(err)
	}
	d.Dispatch("WRITE", file)
	d.idle(t)

	if got := recorded.count(); got != 2 {
		t.Fatalf("ran %d hooks, want 2: a genuine change was suppressed (%s)", got, recorded.all())
	}
}

func TestDispatchSelfTriggerFlagDisablesSuppression(t *testing.T) {
	file := filepath.Join(t.TempDir(), "observed.txt")
	if err := os.WriteFile(file, []byte("start"), 0o644); err != nil {
		t.Fatal(err)
	}

	recorded := &calls{hook: func(op, name string) error {
		return os.WriteFile(name, []byte("rewritten"), 0o644)
	}}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 0, true)
	defer d.Close()

	d.Dispatch("WRITE", file)
	d.Dispatch("WRITE", file)
	d.idle(t)

	if got := recorded.count(); got != 2 {
		t.Fatalf("ran %d hooks with --self-trigger, want 2", got)
	}
}

// A hook that leaves the file alone must not suppress anything.
func TestDispatchDoesNotSuppressWhenTheHookChangesNothing(t *testing.T) {
	file := filepath.Join(t.TempDir(), "observed.txt")
	if err := os.WriteFile(file, []byte("start"), 0o644); err != nil {
		t.Fatal(err)
	}

	recorded := &calls{}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 0, false)
	defer d.Close()

	d.Dispatch("WRITE", file)
	d.Dispatch("WRITE", file)
	d.idle(t)

	if got := recorded.count(); got != 2 {
		t.Fatalf("ran %d hooks, want 2: a read-only hook suppressed the next event", got)
	}
}

// TICKER is not a filesystem event, so it is never suppressed even when a hook writes
// the file on every tick.
func TestDispatchNeverSuppressesTicker(t *testing.T) {
	file := filepath.Join(t.TempDir(), "observed.txt")
	if err := os.WriteFile(file, []byte("start"), 0o644); err != nil {
		t.Fatal(err)
	}

	recorded := &calls{hook: func(op, name string) error {
		return os.WriteFile(name, []byte(time.Now().String()), 0o644)
	}}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 0, false)
	defer d.Close()

	for i := 0; i < 3; i++ {
		d.Dispatch("TICKER", file)
	}
	d.idle(t)

	if got := recorded.count(); got != 3 {
		t.Fatalf("ran %d ticker hooks, want 3", got)
	}
}

func TestDispatchReportsHookFailures(t *testing.T) {
	logs := &safeBuffer{}
	recorded := &calls{hook: func(op, name string) error { return errors.New("hook exploded") }}
	d := newDispatcher(recorded.run, NewLogger(logs, LevelInfo, FormatText), 0, false)
	defer d.Close()

	d.Dispatch("WRITE", "/tmp/observed")
	d.idle(t)

	if !strings.Contains(logs.String(), "hook exploded") {
		t.Fatalf("failure was not reported:\n%s", logs.String())
	}
}

// A script only implements the hooks it cares about, so a missing one is not an error.
func TestDispatchDoesNotReportUndefinedHooksAsErrors(t *testing.T) {
	logs := &safeBuffer{}
	recorded := &calls{hook: func(op, name string) error {
		return fmt.Errorf("chmod(): %w", ErrHookNotDefined)
	}}
	d := newDispatcher(recorded.run, NewLogger(logs, LevelInfo, FormatText), 0, false)
	defer d.Close()

	d.Dispatch("CHMOD", "/tmp/observed")
	d.idle(t)

	if logs.String() != "" {
		t.Fatalf("a missing hook was reported as a problem:\n%s", logs.String())
	}
}

// Close must not leave a debounce timer to fire into a shutting-down process.
func TestCloseCancelsPendingEvents(t *testing.T) {
	recorded := &calls{}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), time.Second, false)

	d.Dispatch("WRITE", "/tmp/observed")
	d.Close()

	if got := recorded.count(); got != 0 {
		t.Fatalf("ran %d hooks after Close, want 0", got)
	}
	time.Sleep(50 * time.Millisecond)
	if got := recorded.count(); got != 0 {
		t.Fatalf("a debounce timer fired %d hooks after Close", got)
	}
}

func TestCloseWaitsForARunningHook(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	recorded := &calls{hook: func(op, name string) error {
		close(started)
		time.Sleep(100 * time.Millisecond)
		close(finished)
		return nil
	}}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 10*time.Millisecond, false)

	d.Dispatch("WRITE", "/tmp/observed")
	<-started
	d.Close()

	select {
	case <-finished:
	default:
		t.Fatal("Close returned while a hook was still running")
	}
}

func TestDispatchAfterCloseIsANoOp(t *testing.T) {
	recorded := &calls{}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 10*time.Millisecond, false)
	d.Close()

	d.Dispatch("WRITE", "/tmp/observed")
	time.Sleep(50 * time.Millisecond)

	if got := recorded.count(); got != 0 {
		t.Fatalf("ran %d hooks after Close", got)
	}
}

// Events arrive from the watcher and the poller at the same time.
func TestDispatchIsSafeForConcurrentUse(t *testing.T) {
	recorded := &calls{}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 20*time.Millisecond, false)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d.Dispatch("WRITE", "/tmp/observed")
			d.Dispatch("TICKER", "/tmp/observed")
		}(i)
	}
	wg.Wait()
	d.Close()
}

// idle blocks until the worker has finished every event that reached the queue.
// Counting is what makes this reliable: an empty channel only means the worker took the
// event, not that it ran the hook.
func (d *dispatcher) idle(t *testing.T) {
	t.Helper()
	waitFor(t, 3*time.Second, "the queue to drain", func() bool {
		return d.handled.Load() == d.submitted.Load()
	})
}

func waitForCount(t *testing.T, recorded *calls, want int) {
	t.Helper()
	waitFor(t, 2*time.Second, "hook to run", func() bool { return recorded.count() >= want })
}

// --- queueing ------------------------------------------------------------------

// The fsnotify reader goroutines and the supervisor call Dispatch. If it waited for the
// hook, a slow one would stall watch bookkeeping and back the kernel's event queue up
// behind it.
func TestDispatchDoesNotWaitForTheHook(t *testing.T) {
	release := make(chan struct{})
	recorded := &calls{hook: func(op, name string) error {
		<-release
		return nil
	}}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 0, false)
	defer func() { close(release); d.Close() }()

	d.Dispatch("TICKER", "/tmp/observed")
	waitForCount(t, recorded, 1) // the first hook is now blocked

	start := time.Now()
	for i := 0; i < 50; i++ {
		d.Dispatch("TICKER", "/tmp/observed")
	}
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("50 dispatches took %s while a hook was running: Dispatch is waiting on it", elapsed)
	}
}

// Events must reach the hooks in the order they happened.
func TestQueuePreservesOrder(t *testing.T) {
	recorded := &calls{}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 0, false)
	defer d.Close()

	want := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		name := "/tmp/file" + string(rune('a'+i))
		d.Dispatch("TICKER", name)
		want = append(want, "TICKER "+name)
	}
	d.idle(t)

	if got := recorded.all(); got != strings.Join(want, ",") {
		t.Fatalf("events arrived out of order:\n got %s\nwant %s", got, strings.Join(want, ","))
	}
}

// A hook that never keeps up costs events. Dropping one and saying so beats blocking
// the reader and letting the kernel drop them where nobody can see it.
func TestQueueDropsAndReportsWhenItFillsUp(t *testing.T) {
	release := make(chan struct{})
	recorded := &calls{hook: func(op, name string) error {
		<-release
		return nil
	}}
	logs := &safeBuffer{}
	d := newDispatcherWithQueue(recorded.run, NewLogger(logs, LevelError, FormatText), 0, false, 4)
	defer func() { close(release); d.Close() }()

	waitForCount(t, recorded, 0)
	for i := 0; i < 100; i++ {
		d.Dispatch("TICKER", "/tmp/observed")
	}

	waitFor(t, 2*time.Second, "the overload to be reported", func() bool {
		return strings.Contains(logs.String(), "cannot keep up")
	})
	if !strings.Contains(logs.String(), "dropped") {
		t.Fatalf("the report does not say events were dropped:\n%s", logs.String())
	}
}

// The report is rate limited, so an overload does not bury its own explanation.
func TestQueueOverloadIsReportedAtMostOncePerSecond(t *testing.T) {
	release := make(chan struct{})
	recorded := &calls{hook: func(op, name string) error {
		<-release
		return nil
	}}
	logs := &safeBuffer{}
	d := newDispatcherWithQueue(recorded.run, NewLogger(logs, LevelError, FormatText), 0, false, 2)
	defer func() { close(release); d.Close() }()

	for i := 0; i < 500; i++ {
		d.Dispatch("TICKER", "/tmp/observed")
	}

	if got := strings.Count(logs.String(), "cannot keep up"); got > 1 {
		t.Fatalf("reported the overload %d times for one burst, want at most once", got)
	}
}

// Close used to stop only the debounced path, so a ticker event still ran a hook after
// the process had decided to shut down.
func TestDispatchAfterCloseIsANoOpForEveryOperation(t *testing.T) {
	recorded := &calls{}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 10*time.Millisecond, false)
	d.Close()

	for _, op := range []string{"WRITE", "TICKER", "EXIST", "REMOVE", "NOT_FOUND"} {
		d.Dispatch(op, "/tmp/observed")
	}
	time.Sleep(50 * time.Millisecond)

	if got := recorded.count(); got != 0 {
		t.Fatalf("ran %d hooks after Close (%s)", got, recorded.all())
	}
}

// Shutting down is not the time to work through a backlog.
func TestCloseAbandonsTheBacklog(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	recorded := &calls{hook: func(op, name string) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return nil
	}}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 0, false)

	for i := 0; i < 20; i++ {
		d.Dispatch("TICKER", "/tmp/observed")
	}
	<-started
	close(release)
	d.Close()

	if got := recorded.count(); got >= 20 {
		t.Fatalf("ran %d of 20 queued hooks during shutdown, want the backlog abandoned", got)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	d := newDispatcher((&calls{}).run, NewLogger(&safeBuffer{}, LevelError, FormatText), 0, false)
	d.Close()
	d.Close()
}

// Suppression used to cover only the file the hook was woken for. A hook woken for one
// file that writes another left that other file completely unprotected, so two watched
// files could wake each other forever.
func TestDispatchIgnoresEventsForAnyPathTheHookWrote(t *testing.T) {
	dir := t.TempDir()
	woken := filepath.Join(dir, "woken.txt")
	other := filepath.Join(dir, "other.txt")
	for _, path := range []string{woken, other} {
		if err := os.WriteFile(path, []byte("start"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	recorded := &calls{
		written: []string{other},
		hook: func(op, name string) error {
			return os.WriteFile(other, []byte("written by the hook"), 0o644)
		},
	}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 0, false)
	defer d.Close()

	d.Dispatch("WRITE", woken)
	d.idle(t)

	// This is the event the hook's write to the other file produced.
	d.Dispatch("WRITE", other)
	d.idle(t)

	if got := recorded.count(); got != 1 {
		t.Fatalf("ran %d hooks, want 1: the two files are waking each other (%s)", got, recorded.all())
	}
}

// A hook that does not exist cannot have written anything, so a change that happened
// while Kowl looked for it belongs to whoever really made it.
func TestDispatchDoesNotAttributeChangesToAnUndefinedHook(t *testing.T) {
	file := filepath.Join(t.TempDir(), "observed.txt")
	if err := os.WriteFile(file, []byte("start"), 0o644); err != nil {
		t.Fatal(err)
	}

	recorded := &calls{hook: func(op, name string) error {
		// Something else changes the file while Kowl is deciding there is no hook.
		if err := os.WriteFile(file, []byte("changed by someone else"), 0o644); err != nil {
			return err
		}
		return ErrHookNotDefined
	}}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 0, false)
	defer d.Close()

	d.Dispatch("CHMOD", file)
	d.idle(t)

	recorded.mu.Lock()
	recorded.hook = nil
	recorded.mu.Unlock()

	d.Dispatch("WRITE", file)
	d.idle(t)

	if got := recorded.count(); got != 2 {
		t.Fatalf("ran %d hooks, want 2: a change was blamed on a hook that does not exist", got)
	}
}

// A hook that deletes a path leaves no state to match a later event against.
func TestDispatchDoesNotSuppressAfterAHookDeletesThePath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "observed.txt")
	if err := os.WriteFile(file, []byte("start"), 0o644); err != nil {
		t.Fatal(err)
	}

	recorded := &calls{
		written: []string{file},
		hook: func(op, name string) error {
			os.Remove(file)
			return nil
		},
	}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 0, false)
	defer d.Close()

	d.Dispatch("WRITE", file)
	d.idle(t)

	// The file comes back, which is a real change nobody should suppress.
	if err := os.WriteFile(file, []byte("recreated"), 0o644); err != nil {
		t.Fatal(err)
	}
	d.Dispatch("CREATE", file)
	d.idle(t)

	if got := recorded.count(); got != 2 {
		t.Fatalf("ran %d hooks, want 2: a recreated file was suppressed", got)
	}
}

// A script that does not parse fails identically for every event. At a short poll
// interval that was several identical lines a second, for as long as the process ran.
func TestRepeatedFailureIsReportedOnceThenCounted(t *testing.T) {
	logs := &safeBuffer{}
	recorded := &calls{hook: func(op, name string) error { return errors.New("script is broken") }}
	d := newDispatcher(recorded.run, NewLogger(logs, LevelInfo, FormatText), 0, false)
	defer d.Close()

	for i := 0; i < 50; i++ {
		d.Dispatch("TICKER", "/tmp/observed")
	}
	d.idle(t)

	if got := strings.Count(logs.String(), "script is broken"); got != 1 {
		t.Fatalf("reported the same failure %d times for 50 events, want once:\n%s", got, logs.String())
	}
}

// A different failure is worth reporting straight away.
func TestADifferentFailureIsReportedImmediately(t *testing.T) {
	logs := &safeBuffer{}
	failure := errors.New("first problem")
	recorded := &calls{hook: func(op, name string) error { return failure }}
	d := newDispatcher(recorded.run, NewLogger(logs, LevelInfo, FormatText), 0, false)
	defer d.Close()

	d.Dispatch("TICKER", "/tmp/observed")
	d.idle(t)

	recorded.mu.Lock()
	failure = errors.New("second problem")
	recorded.mu.Unlock()

	d.Dispatch("TICKER", "/tmp/observed")
	d.idle(t)

	for _, want := range []string{"first problem", "second problem"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("%q was not reported:\n%s", want, logs.String())
		}
	}
}

// Collapsing must not hide a failure that comes back after things recovered.
func TestAFailureIsReportedAgainAfterASuccess(t *testing.T) {
	logs := &safeBuffer{}
	broken := true
	recorded := &calls{hook: func(op, name string) error {
		if broken {
			return errors.New("script is broken")
		}
		return nil
	}}
	d := newDispatcher(recorded.run, NewLogger(logs, LevelInfo, FormatText), 0, false)
	defer d.Close()

	d.Dispatch("TICKER", "/tmp/observed")
	d.idle(t)

	recorded.mu.Lock()
	broken = false
	recorded.mu.Unlock()
	d.Dispatch("TICKER", "/tmp/observed")
	d.idle(t)

	recorded.mu.Lock()
	broken = true
	recorded.mu.Unlock()
	d.Dispatch("TICKER", "/tmp/observed")
	d.idle(t)

	if got := strings.Count(logs.String(), "script is broken"); got != 2 {
		t.Fatalf("reported the returning failure %d times, want 2:\n%s", got, logs.String())
	}
}

// The suppressed repeats are still visible when asked for.
func TestSuppressedRepeatsAreLoggedAtDebug(t *testing.T) {
	logs := &safeBuffer{}
	recorded := &calls{hook: func(op, name string) error { return errors.New("script is broken") }}
	d := newDispatcher(recorded.run, NewLogger(logs, LevelDebug, FormatText), 0, false)
	defer d.Close()

	for i := 0; i < 5; i++ {
		d.Dispatch("TICKER", "/tmp/observed")
	}
	d.idle(t)

	if got := strings.Count(logs.String(), "script is broken"); got != 5 {
		t.Fatalf("debug kept %d of 5 repeats:\n%s", got, logs.String())
	}
}

// The record of what a hook wrote expires, so watching a churn of short-lived files does
// not grow the map without bound.
func TestWriteRecordsExpire(t *testing.T) {
	file := filepath.Join(t.TempDir(), "observed.txt")
	if err := os.WriteFile(file, []byte("start"), 0o644); err != nil {
		t.Fatal(err)
	}

	recorded := &calls{
		written: []string{file},
		hook:    func(op, name string) error { return os.WriteFile(name, []byte("by the hook"), 0o644) },
	}
	d := newDispatcher(recorded.run, NewLogger(&safeBuffer{}, LevelError, FormatText), 0, false)
	defer d.Close()
	d.settle = 50 * time.Millisecond

	d.Dispatch("WRITE", file)
	d.idle(t)
	if len(d.wrote) != 1 {
		t.Fatalf("the hook's write was not recorded: %v", d.wrote)
	}

	// Past the settle window the record no longer explains anything, so the next event
	// runs and the stale entry is dropped.
	time.Sleep(80 * time.Millisecond)
	d.Dispatch("WRITE", file)
	d.idle(t)

	if got := recorded.count(); got != 2 {
		t.Fatalf("ran %d hooks, want 2: an expired record still suppressed an event", got)
	}
}
