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
	}, false)

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

	got := resolve([]string{file, filepath.Join(dir, "*.log"), filepath.Join(dir, "a.*")}, false)
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
		Supervise(ctx, WatchConfig{Patterns: []string{one, two}, Interval: 10 * time.Millisecond, MaxWatches: 64}, rec.dispatch, logs.logger())
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
		Supervise(ctx, WatchConfig{Patterns: []string{filepath.Join(dir, "*.log")}, Interval: 10 * time.Millisecond, MaxWatches: 64}, rec.dispatch, logs.logger())
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
		Supervise(ctx, WatchConfig{Patterns: []string{filepath.Join(dir, "*.log")}, Interval: 10 * time.Millisecond, MaxWatches: 64}, rec.dispatch, logs.logger())
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
		Supervise(ctx, WatchConfig{Patterns: []string{dir}, Interval: 10 * time.Millisecond, MaxWatches: 64}, rec.dispatch, logs.logger())
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

// --- recursive watching ---------------------------------------------------------

// nestedTree builds dir/{a/{deep/},b/} with a file in each directory.
func nestedTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, sub := range []string{"a", filepath.Join("a", "deep"), "b"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, sub := range []string{".", "a", filepath.Join("a", "deep"), "b"} {
		if err := os.WriteFile(filepath.Join(root, sub, "file.txt"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// fsnotify does not recurse, so a tree has to be enumerated and watched a level at a
// time. Without --recursive only the directory itself is watched.
func TestResolveWithoutRecursiveStopsAtTheDirectory(t *testing.T) {
	root := nestedTree(t)

	got := resolve([]string{root}, false)

	if len(got) != 1 || got[0] != root {
		t.Fatalf("resolve = %v, want exactly %v", got, []string{root})
	}
}

func TestResolveRecursiveIncludesEverySubdirectory(t *testing.T) {
	root := nestedTree(t)

	got := resolve([]string{root}, true)

	want := []string{
		root,
		filepath.Join(root, "a"),
		filepath.Join(root, "a", "deep"),
		filepath.Join(root, "b"),
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("resolve = %v, want %v", got, want)
	}
}

// Only directories get watchers; the files inside them are covered by their parent.
func TestResolveRecursiveDoesNotListFiles(t *testing.T) {
	root := nestedTree(t)

	for _, path := range resolve([]string{root}, true) {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if !info.IsDir() {
			t.Fatalf("resolve returned the file %s, want directories only", path)
		}
	}
}

// A symlink pointing back up the tree must not send the walk into a loop.
func TestResolveRecursiveDoesNotFollowSymlinks(t *testing.T) {
	root := nestedTree(t)
	loop := filepath.Join(root, "a", "loop")
	if err := os.Symlink(root, loop); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	done := make(chan []string, 1)
	go func() { done <- resolve([]string{root}, true) }()

	select {
	case got := <-done:
		for _, path := range got {
			if strings.Contains(path, filepath.Join("loop", "a")) {
				t.Fatalf("resolve followed the symlink into %s", path)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resolve did not finish: it followed a symlink loop")
	}
}

// A file created deep in the tree must reach the hooks.
func TestSuperviseRecursiveReportsEventsInNestedDirectories(t *testing.T) {
	root := nestedTree(t)
	deep := filepath.Join(root, "a", "deep")

	rec := &pathRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &safeBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Supervise(ctx, WatchConfig{
			Patterns:   []string{root},
			Interval:   10 * time.Millisecond,
			Recursive:  true,
			MaxWatches: 64,
		}, rec.dispatch, logs.logger())
	}()

	waitFor(t, 3*time.Second, "EXIST for every directory", func() bool { return rec.countOp("EXIST") == 4 })

	if err := os.WriteFile(filepath.Join(deep, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "CREATE in the nested directory", func() bool { return rec.has("CREATE new.txt") })

	cancel()
	<-done
}

// A subdirectory created after Kowl started must start being watched too.
func TestSuperviseRecursivePicksUpNewSubdirectories(t *testing.T) {
	root := t.TempDir()

	rec := &pathRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &safeBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Supervise(ctx, WatchConfig{
			Patterns:   []string{root},
			Interval:   10 * time.Millisecond,
			Recursive:  true,
			MaxWatches: 64,
		}, rec.dispatch, logs.logger())
	}()

	waitFor(t, 3*time.Second, "EXIST for the root", func() bool { return rec.countOp("EXIST") == 1 })

	added := filepath.Join(root, "added")
	if err := os.Mkdir(added, 0o755); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "EXIST for the new subdirectory", func() bool { return rec.has("EXIST added") })

	if err := os.WriteFile(filepath.Join(added, "inside.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "CREATE inside the new subdirectory", func() bool {
		return rec.has("CREATE inside.txt")
	})

	cancel()
	<-done
}

// A recursive watch over a large tree must not be able to exhaust file descriptors.
func TestSuperviseStopsAtMaxWatches(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 8; i++ {
		if err := os.Mkdir(filepath.Join(root, "sub"+string(rune('a'+i))), 0o755); err != nil {
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
		Supervise(ctx, WatchConfig{
			Patterns:   []string{root},
			Interval:   10 * time.Millisecond,
			Recursive:  true,
			MaxWatches: 3,
		}, rec.dispatch, logs.logger())
	}()

	waitFor(t, 3*time.Second, "the limit to be reported", func() bool {
		return strings.Contains(logs.String(), "--max-watches")
	})
	time.Sleep(100 * time.Millisecond)

	if got := rec.countOp("EXIST"); got > 3 {
		t.Fatalf("watched %d paths, want at most the limit of 3", got)
	}

	cancel()
	<-done

	// The limit is reported once, not on every tick.
	if got := strings.Count(logs.String(), "--max-watches"); got != 1 {
		t.Fatalf("the limit was reported %d times, want once", got)
	}
}
