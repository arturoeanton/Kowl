package main

import (
	"context"
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
	logger := NewLogger(logs, LevelError)
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
	logger := NewLogger(&safeBuffer{}, LevelError)
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
