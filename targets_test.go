package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
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
	}, false, nil, 0).Paths

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

	got := resolve([]string{file, filepath.Join(dir, "*.log"), filepath.Join(dir, "a.*")}, false, nil, 0).Paths
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

	got := resolve([]string{root}, false, nil, 0).Paths

	if len(got) != 1 || got[0] != root {
		t.Fatalf("resolve = %v, want exactly %v", got, []string{root})
	}
}

func TestResolveRecursiveIncludesEverySubdirectory(t *testing.T) {
	root := nestedTree(t)

	got := resolve([]string{root}, true, nil, 0).Paths

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

	for _, path := range resolve([]string{root}, true, nil, 0).Paths {
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
	go func() { done <- resolve([]string{root}, true, nil, 0).Paths }()

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

// Walking a whole tree only to throw most of it away costs a full traversal per tick.
func TestResolveStopsSearchingOnceTheLimitIsReached(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("sub%02d", i), "deep"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	found := resolve([]string{root}, true, nil, 5)

	if len(found.Paths) != 5 {
		t.Fatalf("resolve returned %d paths, want the limit of 5", len(found.Paths))
	}
	if !found.Truncated {
		t.Fatal("Truncated is false even though the tree has far more than 5 directories")
	}
	if found.FirstDropped == "" {
		t.Fatal("FirstDropped is empty, so the report cannot say where it stopped")
	}
	for _, path := range found.Paths {
		if path == found.FirstDropped {
			t.Fatalf("FirstDropped %q is also among the kept paths", found.FirstDropped)
		}
	}
}

// A limit that is never reached must not look like truncation.
func TestResolveDoesNotReportTruncationWhenEverythingFits(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "one"), 0o755); err != nil {
		t.Fatal(err)
	}

	found := resolve([]string{root}, true, nil, 100)

	if found.Truncated {
		t.Fatal("Truncated is true for a tree that fits well inside the limit")
	}
	if found.FirstDropped != "" {
		t.Fatalf("FirstDropped = %q for a tree that fits", found.FirstDropped)
	}
	if len(found.Paths) != 2 {
		t.Fatalf("resolve returned %v, want the root and its one subdirectory", found.Paths)
	}
}

// A zero limit means no limit, which is what the plain resolve tests rely on.
func TestResolveWithoutALimitFindsEverything(t *testing.T) {
	root := nestedTree(t)

	found := resolve([]string{root}, true, nil, 0)

	if found.Truncated || len(found.Paths) != 4 {
		t.Fatalf("resolve = %v truncated=%v, want all four directories", found.Paths, found.Truncated)
	}
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
	// The report has to say where it stopped, or there is no way to tell what is
	// going unwatched.
	if !strings.Contains(logs.String(), root) {
		t.Fatalf("the report does not name a path that was left out:\n%s", logs.String())
	}

	cancel()
	<-done

	// The limit is reported once, not on every tick.
	if got := strings.Count(logs.String(), "--max-watches"); got != 1 {
		t.Fatalf("the limit was reported %d times, want once", got)
	}
}

// --- excluding paths -------------------------------------------------------------

func TestExcludedMatchesTheBaseNameWhenThePatternHasNoSeparator(t *testing.T) {
	patterns := []string{"node_modules", "*.tmp"}

	for _, path := range []string{
		"/srv/app/node_modules",
		"/deep/inside/a/tree/node_modules",
		"/srv/app/build.tmp",
	} {
		if !excluded(patterns, path) {
			t.Fatalf("excluded(%q) = false, want it covered by the base name", path)
		}
	}
	for _, path := range []string{"/srv/app/src", "/srv/node_modules_old", "/srv/app/tmp"} {
		if excluded(patterns, path) {
			t.Fatalf("excluded(%q) = true, want it kept", path)
		}
	}
}

// A pattern with a separator stays specific to one place in the tree.
func TestExcludedMatchesTheWholePathWhenThePatternHasASeparator(t *testing.T) {
	patterns := []string{"/srv/app/tmp/*"}

	if !excluded(patterns, "/srv/app/tmp/cache") {
		t.Fatal("excluded did not cover a path under the excluded directory")
	}
	if excluded(patterns, "/srv/other/tmp/cache") {
		t.Fatal("a path-anchored pattern matched somewhere else in the tree")
	}
}

func TestExcludedWithNoPatterns(t *testing.T) {
	if excluded(nil, "/anything") {
		t.Fatal("excluded said yes with no patterns")
	}
}

func TestResolveSkipsExcludedMatches(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"keep.log", "skip.log", "also.tmp"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := resolve([]string{filepath.Join(dir, "*")}, false, []string{"skip.log", "*.tmp"}, 0).Paths

	if len(got) != 1 || filepath.Base(got[0]) != "keep.log" {
		t.Fatalf("resolve = %v, want only keep.log", got)
	}
}

// Not descending into an excluded directory is most of the point: it is what keeps a
// recursive watch off node_modules instead of merely not watching its top level.
func TestResolveDoesNotDescendIntoAnExcludedDirectory(t *testing.T) {
	root := t.TempDir()
	buried := filepath.Join(root, "node_modules", "pkg", "deep")
	if err := os.MkdirAll(buried, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := resolve([]string{root}, true, []string{"node_modules"}, 0).Paths

	want := []string{root, filepath.Join(root, "src")}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("resolve = %v, want %v", got, want)
	}
	for _, path := range got {
		if strings.Contains(path, "node_modules") {
			t.Fatalf("resolve descended into an excluded directory: %s", path)
		}
	}
}

func TestSuperviseDoesNotWatchExcludedPaths(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"keep.log", "skip.log"} {
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
		Supervise(ctx, WatchConfig{
			Patterns:   []string{filepath.Join(dir, "*.log")},
			Interval:   10 * time.Millisecond,
			MaxWatches: 64,
			Exclude:    []string{"skip.log"},
		}, rec.dispatch, logs.logger())
	}()

	waitFor(t, 2*time.Second, "EXIST for the kept file", func() bool { return rec.has("EXIST keep.log") })
	time.Sleep(100 * time.Millisecond)

	if rec.has("EXIST skip.log") {
		t.Fatalf("an excluded path was watched (%s)", rec.all())
	}

	// The excluded file must stay excluded when it changes.
	if err := os.WriteFile(filepath.Join(dir, "skip.log"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if rec.has("WRITE skip.log") {
		t.Fatalf("an excluded path produced an event (%s)", rec.all())
	}

	cancel()
	<-done
}

// --- observers that give up --------------------------------------------------------

// An observer that stops on its own, which is what happens after the kernel loses
// events to a queue overflow, used to leave the path silently unwatched for good.
func TestSuperviseRestartsAnObserverThatStoppedOnItsOwn(t *testing.T) {
	file := filepath.Join(t.TempDir(), "observed.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// A real watcher cannot be made to overflow on demand, so stand in for one that
	// gave up: the supervisor only ever sees the done channel close.
	var mu sync.Mutex
	var current chan struct{}
	fake := func(ctx context.Context, path string, dispatch Dispatch, logger *Logger) (<-chan struct{}, error) {
		done := make(chan struct{})
		mu.Lock()
		current = done
		mu.Unlock()
		dispatch("EXIST", path)
		go func() {
			<-ctx.Done()
			select {
			case <-done:
			default:
				close(done)
			}
		}()
		return done, nil
	}

	rec := &pathRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &safeBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Supervise(ctx, WatchConfig{
			Patterns:   []string{file},
			Interval:   10 * time.Millisecond,
			MaxWatches: 64,
			observe:    fake,
		}, rec.dispatch, logs.logger())
	}()

	waitFor(t, 2*time.Second, "the first EXIST", func() bool { return rec.countOp("EXIST") == 1 })

	mu.Lock()
	close(current)
	mu.Unlock()

	waitFor(t, 3*time.Second, "the path to be announced again", func() bool {
		return rec.countOp("EXIST") >= 2
	})

	cancel()
	<-done
}

// A real observer must go the same way when the kernel reports lost events.
func TestFatalWatcherError(t *testing.T) {
	if !fatalWatcherError(fsnotify.ErrEventOverflow) {
		t.Fatal("an overflow is not treated as fatal, so lost events go unnoticed")
	}
	if !fatalWatcherError(fmt.Errorf("watching: %w", fsnotify.ErrEventOverflow)) {
		t.Fatal("a wrapped overflow is not recognised")
	}
	if fatalWatcherError(errors.New("some transient problem")) {
		t.Fatal("an ordinary error is treated as fatal, so the watcher restarts for nothing")
	}
	if fatalWatcherError(nil) {
		t.Fatal("a nil error is treated as fatal")
	}
}

func TestObserverHandleDeadReportsWhetherItStopped(t *testing.T) {
	done := make(chan struct{})
	handle := &observerHandle{cancel: func() {}, done: done}

	if handle.dead() {
		t.Fatal("dead() is true for an observer that is still running")
	}
	close(done)
	if !handle.dead() {
		t.Fatal("dead() is false for an observer that has stopped")
	}
}
