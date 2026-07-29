package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestEndToEndWatchAndDispatch wires the real Runner, dispatcher, supervisor and poller
// together the way run does, and drives them with a real file.
func TestEndToEndWatchAndDispatch(t *testing.T) {
	dir := t.TempDir()
	observed := filepath.Join(dir, "observed.txt")
	journal := filepath.Join(dir, "journal.txt")

	script := writeScript(t, `
		function exist(name, op)  { kAppendFile(op + "\n", `+quote(journal)+`) }
		function write(name, op)  { kAppendFile(op + "\n", `+quote(journal)+`) }
		function remove(name, op) { kAppendFile(op + "\n", `+quote(journal)+`) }
		function not_found(name, op) { kAppendFile(op + "\n", `+quote(journal)+`) }`)

	runner := NewRunner(script)
	hooks, err := runner.DefinedHooks()
	if err != nil {
		t.Fatalf("DefinedHooks: %v", err)
	}
	if len(hooks) != 4 {
		t.Fatalf("script defines %v, want four hooks", hooks)
	}

	logs := &safeBuffer{}
	logger := NewLogger(logs, LevelError, FormatText)
	events := newDispatcher(runner.Run, logger, 20*time.Millisecond, false)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		Supervise(ctx, WatchConfig{Patterns: []string{observed}, Interval: 10 * time.Millisecond, MaxWatches: 64}, events.Dispatch, logger)
	}()
	go func() {
		defer wg.Done()
		Poll(ctx, []string{observed}, 20*time.Millisecond, events.Dispatch, logger)
	}()

	// The file does not exist yet, so polling reports it missing.
	waitFor(t, 3*time.Second, "NOT_FOUND", func() bool {
		return strings.Contains(journalOf(t, journal), "NOT_FOUND")
	})

	if err := os.WriteFile(observed, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "EXIST", func() bool {
		return strings.Contains(journalOf(t, journal), "EXIST")
	})

	if err := os.WriteFile(observed, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "WRITE", func() bool {
		return strings.Contains(journalOf(t, journal), "WRITE")
	})

	if err := os.Remove(observed); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "REMOVE", func() bool {
		return strings.Contains(journalOf(t, journal), "REMOVE")
	})

	cancel()
	wg.Wait()
	events.Close()

	if logs.String() != "" {
		t.Fatalf("errors were reported during a run that should be clean:\n%s", logs.String())
	}
}

// A hook that rewrites the file it was woken for must settle instead of looping.
func TestEndToEndHookRewritingTheObservedFileSettles(t *testing.T) {
	dir := t.TempDir()
	observed := filepath.Join(dir, "config.txt")
	journal := filepath.Join(dir, "journal.txt")

	if err := os.WriteFile(observed, []byte("port=9999"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The rewrite the README used to guard with an environment-variable flag.
	script := writeScript(t, `
		function exist(name, op) { kAppendFile("watching\n", `+quote(journal)+`) }
		function write(name, op) {
			kAppendFile("rewrote\n", `+quote(journal)+`)
			kStringToFile("port=8080", name)
		}`)

	runner := NewRunner(script)
	logger := NewLogger(&safeBuffer{}, LevelError, FormatText)
	events := newDispatcher(runner.Run, logger, 20*time.Millisecond, false)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Supervise(ctx, WatchConfig{Patterns: []string{observed}, Interval: 10 * time.Millisecond, MaxWatches: 64}, events.Dispatch, logger)
	}()

	// The observer is only attached on the first supervisor tick; writing before that
	// would race with it and the event would be missed.
	waitFor(t, 3*time.Second, "the observer to attach", func() bool {
		return strings.Contains(journalOf(t, journal), "watching")
	})

	if err := os.WriteFile(observed, []byte("port=1234"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "the hook to run", func() bool {
		return strings.Contains(journalOf(t, journal), "rewrote")
	})

	// Give any feedback loop time to show itself.
	time.Sleep(500 * time.Millisecond)
	cancel()
	<-done
	events.Close()

	if got := strings.Count(journalOf(t, journal), "rewrote"); got > 2 {
		t.Fatalf("the hook ran %d times for one external write: it is retriggering itself", got)
	}
	if got := readFile(t, observed); got != "port=8080" {
		t.Fatalf("observed file contains %q, want the rewritten value", got)
	}
}

// TestBinaryRunsAndStopsCleanly builds Kowl and drives the real process, which is the
// only way to cover signal handling and the exit code.
func TestBinaryRunsAndStopsCleanly(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}

	dir := t.TempDir()
	binary := filepath.Join(dir, "kowl")
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building kowl: %v\n%s", err, out)
	}

	observed := filepath.Join(dir, "observed.txt")
	if err := os.WriteFile(observed, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := writeScript(t, `function write(name, op) { console.log("saw", op) }
		function exist(name, op) { console.log("saw", op) }`)

	cmd := exec.Command(binary, "-f", observed, "-j", script, "-m", "0", "--debounce", "10ms")
	var output safeBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting kowl: %v", err)
	}

	// Wait for the observer to attach, not just for kowl to start: the watcher is only
	// built on the first supervisor tick.
	waitFor(t, 5*time.Second, "kowl to attach its watcher", func() bool {
		return strings.Contains(output.String(), "saw EXIST")
	})
	if err := os.WriteFile(observed, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the write hook to run", func() bool {
		return strings.Contains(output.String(), "saw WRITE")
	})

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signalling kowl: %v", err)
	}

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("kowl exited with %v\n%s", err, output.String())
		}
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("kowl did not stop after SIGTERM\n%s", output.String())
	}

	if !strings.Contains(output.String(), "stopped") {
		t.Fatalf("kowl did not report a clean shutdown:\n%s", output.String())
	}
}

// journalOf reads a journal file that may not exist yet.
func journalOf(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// The whole process, including whatever a script logs, must be parseable as JSON when
// --log-format json is set.
func TestBinaryEmitsParseableJSONLogs(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}

	dir := t.TempDir()
	binary := filepath.Join(dir, "kowl")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("building kowl: %v\n%s", err, out)
	}

	observed := filepath.Join(dir, "observed.txt")
	if err := os.WriteFile(observed, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := writeScript(t, `function exist(name, op) { console.log("from the script") }`)

	cmd := exec.Command(binary, "-f", observed, "-j", script, "-m", "0", "--log-format", "json")
	var output safeBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting kowl: %v", err)
	}

	waitFor(t, 5*time.Second, "the script to log", func() bool {
		return strings.Contains(output.String(), "from the script")
	})
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("kowl exited with %v\n%s", err, output.String())
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) < 3 {
		t.Fatalf("got %d lines, want the startup, script and shutdown lines:\n%s", len(lines), output.String())
	}
	sawScript := false
	for i, line := range lines {
		var entry struct {
			Time    string `json:"time"`
			Level   string `json:"level"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("line %d is not JSON: %v\n%s", i, err, line)
		}
		if entry.Time == "" || entry.Level == "" {
			t.Fatalf("line %d is missing time or level: %s", i, line)
		}
		if entry.Message == "from the script" {
			sawScript = true
		}
	}
	if !sawScript {
		t.Fatalf("the script's own line was not one of the JSON objects:\n%s", output.String())
	}
}

// The startup check runs before anything is logged, so a runaway top level used to hang
// the process with no output at all and no way to tell why.
func TestBinaryRejectsARunawayScriptInsteadOfHanging(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}

	dir := t.TempDir()
	binary := filepath.Join(dir, "kowl")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("building kowl: %v\n%s", err, out)
	}

	observed := filepath.Join(dir, "observed.txt")
	if err := os.WriteFile(observed, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	script := writeScript(t, "while (true) {}\nfunction write(name, op) {}")

	cmd := exec.Command(binary, "-f", observed, "-j", script, "--hook-timeout", "1s")
	var output safeBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting kowl: %v", err)
	}

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	select {
	case err := <-waited:
		if err == nil {
			t.Fatalf("kowl exited 0 for a script it could not load\n%s", output.String())
		}
	case <-time.After(20 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("kowl hung on a script whose top level never finishes\n%s", output.String())
	}

	if !strings.Contains(output.String(), "top level") {
		t.Fatalf("kowl did not say why it gave up:\n%s", output.String())
	}
}

// A slow hook used to hold the supervisor's goroutine, so watch bookkeeping stopped:
// paths that appeared were not picked up until every queued hook had finished.
func TestEndToEndSlowHookDoesNotStallTheSupervisor(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "journal.txt")

	// The hook is slow enough that, run inline, three files would take over a second.
	script := writeScript(t, `
		function exist(name, op, event) {
			kAppendFile(event.name + "\n", `+quote(journal)+`);
			kExec("sleep", "0.4");
		}`)

	runner := NewRunner(script)
	logger := NewLogger(&safeBuffer{}, LevelError, FormatText)
	events := newDispatcher(runner.Run, logger, 0, false)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Supervise(ctx, WatchConfig{
			Patterns:   []string{filepath.Join(dir, "*.txt")},
			Interval:   20 * time.Millisecond,
			MaxWatches: 64,
		}, events.Dispatch, logger)
	}()

	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Every path must be queued promptly, well inside the time the hooks take to run.
	waitFor(t, 2*time.Second, "all three paths to be queued", func() bool {
		return events.submitted.Load() >= 3
	})

	cancel()
	<-done
	events.Close()
}

// Two watched files whose hooks write each other used to wake each other forever:
// suppression only covered the file the hook was woken for.
func TestEndToEndTwoFilesWritingEachOtherSettle(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	journal := filepath.Join(dir, "journal.txt")
	for _, path := range []string{a, b} {
		if err := os.WriteFile(path, []byte("start"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	script := writeScript(t, `
		var runs = 0;
		function exist(name, op, event) { kAppendFile("watching " + event.name + "\n", `+quote(journal)+`) }
		function write(name, op, event) {
			runs = runs + 1;
			kAppendFile("run " + runs + " for " + event.name + "\n", `+quote(journal)+`);
			var other = event.name === "a.txt" ? `+quote(b)+` : `+quote(a)+`;
			kStringToFile("touched by " + event.name, other);
		}`)

	runner := NewRunner(script)
	logger := NewLogger(&safeBuffer{}, LevelError, FormatText)
	events := newDispatcher(runner.Run, logger, 20*time.Millisecond, false)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Supervise(ctx, WatchConfig{
			Patterns:   []string{filepath.Join(dir, "?.txt")},
			Interval:   10 * time.Millisecond,
			MaxWatches: 64,
		}, events.Dispatch, logger)
	}()

	waitFor(t, 3*time.Second, "both files to be watched", func() bool {
		return strings.Count(journalOf(t, journal), "watching") == 2
	})

	// One external write kicks it off.
	if err := os.WriteFile(a, []byte("edited by hand"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "the first hook to run", func() bool {
		return strings.Contains(journalOf(t, journal), "run 1")
	})

	// Give a ping-pong every chance to show itself.
	time.Sleep(1500 * time.Millisecond)
	cancel()
	<-done
	events.Close()

	runs := strings.Count(journalOf(t, journal), "run ")
	if runs > 4 {
		t.Fatalf("the hooks ran %d times for one external edit: they are waking each other\n%s",
			runs, journalOf(t, journal))
	}
}

// SIGHUP has to reload the script without restarting the process.
func TestBinaryReloadsOnSIGHUP(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}

	dir := t.TempDir()
	binary := filepath.Join(dir, "kowl")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("building kowl: %v\n%s", err, out)
	}

	observed := filepath.Join(dir, "observed.txt")
	if err := os.WriteFile(observed, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	script := writeScript(t, `function exist(name, op) { console.log("hello from the script") }`)

	cmd := exec.Command(binary, "-f", observed, "-j", script, "-m", "0")
	var output safeBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting kowl: %v", err)
	}
	defer func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
	}()

	waitFor(t, 5*time.Second, "kowl to start watching", func() bool {
		return strings.Contains(output.String(), "hello from the script")
	})

	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatalf("signalling kowl: %v", err)
	}
	waitFor(t, 5*time.Second, "the reload to be reported", func() bool {
		return strings.Contains(output.String(), "reloaded")
	})

	// SIGHUP must not be mistaken for a request to stop.
	if cmd.ProcessState != nil {
		t.Fatalf("kowl exited on SIGHUP\n%s", output.String())
	}
	if !strings.Contains(output.String(), "hooks: exist") {
		t.Fatalf("the reload did not report what the script defines:\n%s", output.String())
	}
}
