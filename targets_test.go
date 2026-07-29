package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pathRecorder records the paths a dispatch was called with, per operation.
type pathRecorder struct{ recorder }

func (p *pathRecorder) dispatch(op, name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ops = append(p.ops, op+" "+filepath.Base(name))
}

func (p *pathRecorder) countOp(op string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, entry := range p.ops {
		if strings.HasPrefix(entry, op+" ") {
			n++
		}
	}
	return n
}

func (p *pathRecorder) has(entry string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, got := range p.ops {
		if got == entry {
			return true
		}
	}
	return false
}

func TestValidatePatternsRejectsBrokenGlobs(t *testing.T) {
	if err := ValidatePatterns([]string{"good.txt", "[unterminated"}); err == nil {
		t.Fatal("ValidatePatterns accepted an unterminated character class")
	}
	if err := ValidatePatterns([]string{"good.txt", "logs/*.log", "dir/"}); err != nil {
		t.Fatalf("ValidatePatterns rejected valid patterns: %v", err)
	}
}

func TestResolveExpandsGlobsAndSkipsMissingPaths(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.log", "b.log", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := resolve([]string{
		filepath.Join(dir, "*.log"),
		filepath.Join(dir, "c.txt"),
		filepath.Join(dir, "missing.txt"),
	})

	want := []string{
		filepath.Join(dir, "a.log"),
		filepath.Join(dir, "b.log"),
		filepath.Join(dir, "c.txt"),
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("resolve = %v, want %v", got, want)
	}
}

func TestResolveDeduplicatesOverlappingPatterns(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.log")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got := resolve([]string{file, filepath.Join(dir, "*.log"), filepath.Join(dir, "a.*")})
	if len(got) != 1 || got[0] != file {
		t.Fatalf("resolve = %v, want exactly %v", got, []string{file})
	}
}

func TestSuperviseWatchesEveryFileGivenWithRepeatedFlags(t *testing.T) {
	dir := t.TempDir()
	one := filepath.Join(dir, "one.txt")
	two := filepath.Join(dir, "two.txt")
	for _, file := range []string{one, two} {
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	rec := &pathRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &safeBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Supervise(ctx, []string{one, two}, 10*time.Millisecond, rec.dispatch, logs.logger())
	}()

	waitFor(t, 2*time.Second, "EXIST for both files", func() bool { return rec.countOp("EXIST") == 2 })

	if err := os.WriteFile(two, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, "WRITE on two.txt", func() bool { return rec.has("WRITE two.txt") })

	cancel()
	<-done
}

func TestSuperviseWatchesFilesMatchedByAGlob(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.log", "b.log", "ignored.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	rec := &pathRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &safeBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Supervise(ctx, []string{filepath.Join(dir, "*.log")}, 10*time.Millisecond, rec.dispatch, logs.logger())
	}()

	waitFor(t, 2*time.Second, "EXIST for both logs", func() bool { return rec.countOp("EXIST") == 2 })
	if rec.has("EXIST ignored.txt") {
		t.Fatal("a file outside the glob was watched")
	}

	cancel()
	<-done
}

// A glob must pick up a file created after Kowl started.
func TestSuperviseStartsWatchingNewGlobMatches(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.log"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	rec := &pathRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &safeBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Supervise(ctx, []string{filepath.Join(dir, "*.log")}, 10*time.Millisecond, rec.dispatch, logs.logger())
	}()

	waitFor(t, 2*time.Second, "EXIST for the first log", func() bool { return rec.countOp("EXIST") == 1 })

	if err := os.WriteFile(filepath.Join(dir, "b.log"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, "EXIST for the new log", func() bool { return rec.has("EXIST b.log") })

	cancel()
	<-done
}

// Watching a directory reports events for the files inside it, which is how a save that
// writes a new file and renames it over the old one is caught reliably.
func TestSuperviseWatchingADirectoryReportsItsChildren(t *testing.T) {
	dir := t.TempDir()

	rec := &pathRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &safeBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Supervise(ctx, []string{dir}, 10*time.Millisecond, rec.dispatch, logs.logger())
	}()

	waitFor(t, 2*time.Second, "EXIST for the directory", func() bool { return rec.countOp("EXIST") == 1 })

	if err := os.WriteFile(filepath.Join(dir, "created.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, "CREATE for the new child", func() bool { return rec.has("CREATE created.txt") })

	cancel()
	<-done
}

func TestPollReportsEveryGlobMatch(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.log", "b.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	rec := &pathRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &safeBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Poll(ctx, []string{filepath.Join(dir, "*.log")}, 10*time.Millisecond, rec.dispatch, logs.logger())
	}()

	waitFor(t, 2*time.Second, "TICKER for both files", func() bool {
		return rec.has("TICKER a.log") && rec.has("TICKER b.log")
	})

	cancel()
	<-done
}

// A pattern matching nothing is reported once per tick, named by the pattern itself.
func TestPollReportsNotFoundPerPattern(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "here.txt")
	if err := os.WriteFile(present, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	absent := filepath.Join(dir, "missing.txt")

	rec := &pathRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &safeBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Poll(ctx, []string{present, absent}, 10*time.Millisecond, rec.dispatch, logs.logger())
	}()

	waitFor(t, 2*time.Second, "both a TICKER and a NOT_FOUND", func() bool {
		return rec.has("TICKER here.txt") && rec.has("NOT_FOUND missing.txt")
	})

	cancel()
	<-done
}
