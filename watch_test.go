package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// safeBuffer collects log output from watcher goroutines. It is safe to read after the
// test finishes, unlike t.Logf.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// logger returns a Logger that keeps everything, so tests can assert on what was
// reported as well as on what was dispatched.
func (s *safeBuffer) logger() *Logger {
	return NewLogger(s, LevelDebug)
}

// errorLogger keeps only failures, for tests asserting that nothing went wrong.
func (s *safeBuffer) errorLogger() *Logger {
	return NewLogger(s, LevelError)
}

// recorder records dispatched operations.
type recorder struct {
	mu  sync.Mutex
	ops []string
}

func (r *recorder) dispatch(op, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ops = append(r.ops, op)
}

func (r *recorder) count(op string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, got := range r.ops {
		if got == op {
			n++
		}
	}
	return n
}

func (r *recorder) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.ops, ",")
}

// waitFor polls cond until it holds or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

func TestPollDispatchesTickerWhileFileExists(t *testing.T) {
	file := filepath.Join(t.TempDir(), "observed.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &safeBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Poll(ctx, []string{file}, 10*time.Millisecond, rec.dispatch, logs.logger())
	}()

	waitFor(t, 2*time.Second, "TICKER dispatches", func() bool { return rec.count("TICKER") >= 3 })
	cancel()
	<-done

	if got := rec.count("NOT_FOUND"); got != 0 {
		t.Fatalf("NOT_FOUND dispatched %d times for a file that exists (%s)", got, rec.all())
	}
}

func TestPollDispatchesNotFoundWhileFileMissing(t *testing.T) {
	file := filepath.Join(t.TempDir(), "never-created.txt")

	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &safeBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Poll(ctx, []string{file}, 10*time.Millisecond, rec.dispatch, logs.logger())
	}()

	waitFor(t, 2*time.Second, "NOT_FOUND dispatches", func() bool { return rec.count("NOT_FOUND") >= 3 })
	cancel()
	<-done

	if got := rec.count("TICKER"); got != 0 {
		t.Fatalf("TICKER dispatched %d times for a missing file (%s)", got, rec.all())
	}
}

func TestPollStopsOnContextCancel(t *testing.T) {
	file := filepath.Join(t.TempDir(), "observed.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	logs := &safeBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Poll(ctx, []string{file}, 10*time.Millisecond, func(op, name string) {}, logs.logger())
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Poll did not return after its context was cancelled")
	}
}

func TestSuperviseDispatchesExistThenWrite(t *testing.T) {
	file := filepath.Join(t.TempDir(), "observed.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &safeBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Supervise(ctx, WatchConfig{Patterns: []string{file}, Interval: 10 * time.Millisecond, MaxWatches: 64}, rec.dispatch, logs.logger())
	}()

	waitFor(t, 2*time.Second, "EXIST", func() bool { return rec.count("EXIST") == 1 })

	if err := os.WriteFile(file, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, "WRITE", func() bool { return rec.count("WRITE") >= 1 })

	cancel()
	<-done

	if first := strings.SplitN(rec.all(), ",", 2)[0]; first != "EXIST" {
		t.Fatalf("first dispatch was %q, want EXIST (%s)", first, rec.all())
	}
}

// fsnotify watches inodes, so the observer has to be rebuilt when the file is recreated.
func TestSuperviseRestartsObserverAfterDeleteAndRecreate(t *testing.T) {
	file := filepath.Join(t.TempDir(), "observed.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &safeBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Supervise(ctx, WatchConfig{Patterns: []string{file}, Interval: 10 * time.Millisecond, MaxWatches: 64}, rec.dispatch, logs.logger())
	}()

	waitFor(t, 2*time.Second, "initial EXIST", func() bool { return rec.count("EXIST") == 1 })

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	// Give the supervisor a few ticks to notice the file is gone and tear down.
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 2*time.Second, "EXIST after recreate", func() bool { return rec.count("EXIST") == 2 })

	// The rebuilt observer must actually be watching the new inode.
	if err := os.WriteFile(file, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, "WRITE on the recreated file", func() bool { return rec.count("WRITE") >= 1 })

	cancel()
	<-done
}

// Every delete/recreate cycle used to leak one watcher goroutine and one file
// descriptor, because the observer's for/select never exited and its deferred Close
// never ran. After many cycles only one observer should be alive.
func TestSuperviseDoesNotLeakObserversAcrossCycles(t *testing.T) {
	file := filepath.Join(t.TempDir(), "observed.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	baseline := runtime.NumGoroutine()

	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &safeBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Supervise(ctx, WatchConfig{Patterns: []string{file}, Interval: 10 * time.Millisecond, MaxWatches: 64}, rec.dispatch, logs.logger())
	}()

	const cycles = 6
	for i := 1; i <= cycles; i++ {
		waitFor(t, 3*time.Second, "EXIST for cycle", func() bool { return rec.count("EXIST") == i })
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		time.Sleep(60 * time.Millisecond)
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 3*time.Second, "final EXIST", func() bool { return rec.count("EXIST") == cycles+1 })

	// One supervisor plus one live observer (which itself starts a small, fixed number
	// of fsnotify goroutines). A leak would add roughly two per cycle.
	const allowance = 5
	waitFor(t, 3*time.Second, "goroutine count to settle", func() bool {
		return runtime.NumGoroutine() <= baseline+allowance
	})

	cancel()
	<-done
}

func TestSuperviseStopsAndReleasesObserverOnCancel(t *testing.T) {
	file := filepath.Join(t.TempDir(), "observed.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	baseline := runtime.NumGoroutine()

	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	logs := &safeBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Supervise(ctx, WatchConfig{Patterns: []string{file}, Interval: 10 * time.Millisecond, MaxWatches: 64}, rec.dispatch, logs.logger())
	}()

	waitFor(t, 2*time.Second, "EXIST", func() bool { return rec.count("EXIST") == 1 })

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Supervise did not return after its context was cancelled")
	}

	waitFor(t, 2*time.Second, "observer goroutines to exit", func() bool {
		return runtime.NumGoroutine() <= baseline+1
	})
}

// A file that never appears must not start an observer, and must not spin the log.
func TestSuperviseWaitsForFileToAppear(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "late.txt")

	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &safeBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Supervise(ctx, WatchConfig{Patterns: []string{file}, Interval: 10 * time.Millisecond, MaxWatches: 64}, rec.dispatch, logs.errorLogger())
	}()

	time.Sleep(100 * time.Millisecond)
	if got := rec.count("EXIST"); got != 0 {
		t.Fatalf("EXIST dispatched %d times before the file existed", got)
	}

	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, "EXIST once the file appears", func() bool { return rec.count("EXIST") == 1 })

	cancel()
	<-done

	if logs.String() != "" {
		t.Fatalf("unexpected errors while waiting for the file: %s", logs.String())
	}
}

// fsnotify can set several bits on one event; a switch would report only the first.
func TestOperationsReportsEveryOpOnTheEvent(t *testing.T) {
	tests := []struct {
		name string
		op   fsnotify.Op
		want string
	}{
		{"write", fsnotify.Write, "WRITE"},
		{"create", fsnotify.Create, "CREATE"},
		{"remove", fsnotify.Remove, "REMOVE"},
		{"rename", fsnotify.Rename, "RENAME"},
		{"chmod", fsnotify.Chmod, "CHMOD"},
		{"combined", fsnotify.Write | fsnotify.Chmod, "WRITE,CHMOD"},
		{"none", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.Join(operations(fsnotify.Event{Op: tt.op}), ",")
			if got != tt.want {
				t.Fatalf("operations(%v) = %q, want %q", tt.op, got, tt.want)
			}
		})
	}
}

func TestObserveReportsUnwatchableFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.txt")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &safeBuffer{}

	if _, err := observe(ctx, missing, func(op, name string) {}, logs.logger()); err == nil {
		t.Fatal("observe returned nil error for a file that cannot be watched")
	}
}
