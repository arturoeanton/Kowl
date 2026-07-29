package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watchRetryWindow is how long a path that cannot be watched is retried quietly before
// the failure is reported again, with a count.
const watchRetryWindow = 30 * time.Second

// Dispatch runs the hook for an operation on a path. It never fails: a Dispatch is
// responsible for reporting its own errors so that one bad event cannot stop watching.
type Dispatch func(op, name string)

// ValidatePatterns rejects patterns that filepath.Match cannot parse, so a typo is
// reported at startup rather than silently matching nothing forever.
func ValidatePatterns(patterns []string) error {
	for _, pattern := range patterns {
		if _, err := filepath.Match(pattern, ""); err != nil {
			return fmt.Errorf("invalid pattern %q: %w", pattern, err)
		}
	}
	return nil
}

// excluded reports whether a path is covered by one of the exclude patterns.
//
// A pattern with no separator in it is matched against the base name, so
// --exclude node_modules covers the directory wherever in the tree it turns up. One
// with a separator is matched against the whole path, so --exclude '/srv/app/tmp/*'
// stays specific to that place.
//
// A pattern that matches nothing as a glob still excludes a path it equals exactly, so
// a name containing [ or * can be excluded by writing it out.
func excluded(patterns []string, path string) bool {
	base := filepath.Base(path)
	for _, pattern := range patterns {
		subject := base
		if strings.ContainsRune(pattern, filepath.Separator) {
			subject = path
		}
		if matched, err := filepath.Match(pattern, subject); err == nil && matched {
			return true
		}
		if pattern == subject {
			return true
		}
	}
	return false
}

// match resolves one pattern to the paths it names.
//
// filepath.Glob treats [, * and ? as metacharacters, so a file actually called
// report[1].pdf — which is how a browser names a second download — would be searched for
// as report1.pdf and reported missing while sitting right there. A pattern that matches
// nothing is therefore retried as a literal path, the way a shell leaves an unmatched
// glob alone.
func match(pattern string) []string {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		// Rejected by ValidatePatterns at startup; nothing useful to do per tick.
		return nil
	}
	if len(matches) > 0 {
		return matches
	}
	if _, err := os.Lstat(pattern); err == nil {
		return []string{pattern}
	}
	return nil
}

// WatchConfig is what Supervise needs to know about the paths it is keeping watchers on.
type WatchConfig struct {
	// Patterns are the -f values: files, directories or globs.
	Patterns []string
	// Interval is how often the patterns are re-resolved.
	Interval time.Duration
	// Recursive expands a matched directory to the whole tree below it.
	Recursive bool
	// MaxWatches caps how many paths are watched at once, so a recursive watch over a
	// large tree cannot exhaust the process's file descriptors.
	MaxWatches int
	// Exclude drops matching paths, and stops a recursive walk from descending into
	// matching directories.
	Exclude []string
	// observe builds the observer for one path. Nil means the real one; tests replace
	// it to drive cases a real watcher cannot be made to produce on demand.
	observe observer
}

// observer starts watching a path and returns a channel closed once it has stopped.
type observer func(ctx context.Context, path string, dispatch Dispatch, logger *Logger) (<-chan struct{}, error)

// resolved is what one pass over the patterns found.
type resolved struct {
	// Paths are the matching paths, deduplicated and sorted, at most the limit.
	Paths []string
	// FirstDropped is a path the limit kept out, as an example for the report.
	FirstDropped string
	// Truncated says the limit was reached and there was more to find.
	Truncated bool
}

// resolve returns the existing paths matched by the patterns. A pattern with no
// wildcards resolves to itself when it exists, so plain filenames and globs go through
// the same path.
//
// With recursive set, a matched directory also contributes every directory below it.
// fsnotify does not recurse on its own, and watching a directory only reports its direct
// children, so a tree has to be enumerated and watched a level at a time. Subdirectories
// created later are picked up by the next resolve.
//
// The search stops as soon as limit paths are in hand. Walking a whole tree only to
// throw most of it away is not free: this runs once per tick, so an unbounded walk over
// something like a home directory would cost a full traversal every second.
func resolve(patterns []string, recursive bool, exclude []string, limit int) resolved {
	result := resolved{}
	seen := make(map[string]bool)

	// add reports whether there is room for more.
	add := func(path string) bool {
		if seen[path] {
			return true
		}
		if limit > 0 && len(result.Paths) >= limit {
			if result.FirstDropped == "" {
				result.FirstDropped = path
			}
			result.Truncated = true
			return false
		}
		seen[path] = true
		result.Paths = append(result.Paths, path)
		return true
	}

patterns:
	for _, pattern := range patterns {
		for _, match := range match(pattern) {
			if excluded(exclude, match) {
				continue
			}
			if !add(match) {
				break patterns
			}
			if !recursive {
				continue
			}
			info, err := os.Lstat(match)
			if err != nil || !info.IsDir() {
				continue
			}
			// WalkDir does not follow symlinks, so a link pointing back up the tree
			// cannot send this into a loop.
			filepath.WalkDir(match, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					// An unreadable subdirectory is skipped, not fatal.
					return fs.SkipDir
				}
				if !entry.IsDir() {
					return nil
				}
				if excluded(exclude, path) {
					// Not descended into either: skipping the walk is most of the
					// point of excluding a directory like node_modules.
					return fs.SkipDir
				}
				if !add(path) {
					return fs.SkipAll
				}
				return nil
			})
			if result.Truncated {
				break patterns
			}
		}
	}
	sort.Strings(result.Paths)
	return result
}

// Poll fires TICKER for every path a pattern currently matches, and NOT_FOUND for every
// pattern that matches nothing. It returns when ctx is cancelled.
func Poll(ctx context.Context, patterns []string, interval time.Duration, dispatch Dispatch, logger *Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, pattern := range patterns {
				matches := match(pattern)
				if len(matches) == 0 {
					dispatch("NOT_FOUND", pattern)
					continue
				}
				for _, path := range matches {
					dispatch("TICKER", path)
				}
			}
		}
	}
}

// observerHandle lets the supervisor stop one observer and wait for it to release its
// watcher.
type observerHandle struct {
	cancel context.CancelFunc
	done   <-chan struct{}
}

func (h *observerHandle) stop() {
	h.cancel()
	<-h.done
}

// dead reports whether the observer has already stopped on its own.
func (h *observerHandle) dead() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// Supervise keeps exactly one fsnotify observer alive per path the patterns match.
//
// fsnotify watches inodes rather than paths, so an observer is torn down when its path
// disappears and a fresh one is started when it comes back. Tearing it down is what
// releases the watcher's file descriptor and goroutine; letting it linger leaked one of
// each per delete/recreate cycle until the process ran out of watches.
//
// A path may be a directory, in which case fsnotify reports events for its children.
// That is the reliable way to catch editors that save by writing a new file and
// renaming it over the old one. fsnotify does not recurse, so WatchConfig.Recursive
// enumerates the tree and watches each directory in it, picking up new subdirectories on
// the next tick.
//
// EXIST goes out each time an observer is established, before that observer starts
// reading, so it is always ordered ahead of the events it announces.
func Supervise(ctx context.Context, cfg WatchConfig, dispatch Dispatch, logger *Logger) {
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	observers := make(map[string]*observerHandle)
	defer func() {
		for path, handle := range observers {
			handle.stop()
			delete(observers, path)
		}
	}()

	start := cfg.observe
	if start == nil {
		start = observe
	}

	// capped remembers whether the watch limit was already reported, so hitting it does
	// not repeat the same message every tick.
	capped := false

	// A path that cannot be watched fails again on every tick. Watching a home
	// directory with -r reaches one within seconds: a permission-denied subdirectory
	// alone would otherwise fill the log for as long as the process lives.
	failures := newRepeatFilter(watchRetryWindow)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			found := resolve(cfg.Patterns, cfg.Recursive, cfg.Exclude, cfg.MaxWatches)
			present := found.Paths
			if found.Truncated {
				if !capped {
					logger.Errorf("--max-watches limit of %d reached: %s and whatever follows it are not watched; raise the limit or narrow the patterns with -x",
						cfg.MaxWatches, found.FirstDropped)
					capped = true
				}
			} else {
				capped = false
			}

			stillThere := make(map[string]bool, len(present))
			for _, path := range present {
				stillThere[path] = true
			}

			for path, handle := range observers {
				switch {
				case !stillThere[path]:
					handle.stop()
					delete(observers, path)
					logger.Debugf("stopped watching %s", path)
				case handle.dead():
					// The observer gave up, most likely after losing events to a
					// kernel queue overflow. Drop it so the loop below builds a
					// fresh one and announces the path again.
					handle.stop()
					delete(observers, path)
					logger.Debugf("observer for %s stopped on its own, restarting", path)
				}
			}

			for _, path := range present {
				if _, watching := observers[path]; watching {
					continue
				}
				observerCtx, observerCancel := context.WithCancel(ctx)
				done, err := start(observerCtx, path, dispatch, logger)
				if err != nil {
					// The path can vanish between resolve and Add. Leave it unwatched
					// so the next tick retries, instead of keeping a live observer
					// that watches nothing.
					observerCancel()
					if suppressed, report := failures.admit(err.Error()); report {
						if suppressed > 0 {
							logger.Errorf("%v (retrying, and %d more attempts like it)", err, suppressed)
						} else {
							logger.Errorf("%v (retrying)", err)
						}
					}
					continue
				}
				observers[path] = &observerHandle{cancel: observerCancel, done: done}
				failures.reset()
				logger.Debugf("watching %s", path)
			}
		}
	}
}

// observe starts watching path and translates fsnotify events into hook dispatches. The
// returned channel is closed once the observer has stopped and its watcher is closed.
func observe(ctx context.Context, path string, dispatch Dispatch, logger *Logger) (<-chan struct{}, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("creating watcher for %s: %w", path, err)
	}
	if err := watcher.Add(path); err != nil {
		watcher.Close()
		return nil, fmt.Errorf("watching %s: %w", path, err)
	}

	// EXIST goes out before the reader starts, so it cannot be overtaken by an event
	// from the very watcher it announces.
	dispatch("EXIST", path)

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer watcher.Close()

		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				for _, op := range operations(event) {
					dispatch(op, event.Name)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				if fatalWatcherError(err) {
					// Events were lost, so this watcher can no longer be trusted to
					// describe the path. Stop: the supervisor notices and starts a
					// fresh one, which announces the path again with EXIST so a
					// script can resynchronise.
					logger.Errorf("lost events on %s: %v, starting over", path, err)
					return
				}
				// Any other watcher error is not fatal: report it and keep observing.
				logger.Errorf("watcher error on %s: %v", path, err)
			}
		}
	}()
	return done, nil
}

// fatalWatcherError reports whether an error means the watcher has stopped describing
// its path faithfully, rather than being a one-off that can be logged and shrugged off.
func fatalWatcherError(err error) bool {
	// An overflow means the kernel dropped events nobody will ever see.
	return errors.Is(err, fsnotify.ErrEventOverflow)
}

// operations returns every op set on an event. fsnotify can report several at once, so
// matching only the first one would silently drop the rest.
func operations(event fsnotify.Event) []string {
	var ops []string
	if event.Op&fsnotify.Write == fsnotify.Write {
		ops = append(ops, "WRITE")
	}
	if event.Op&fsnotify.Create == fsnotify.Create {
		ops = append(ops, "CREATE")
	}
	if event.Op&fsnotify.Remove == fsnotify.Remove {
		ops = append(ops, "REMOVE")
	}
	if event.Op&fsnotify.Rename == fsnotify.Rename {
		ops = append(ops, "RENAME")
	}
	if event.Op&fsnotify.Chmod == fsnotify.Chmod {
		ops = append(ops, "CHMOD")
	}
	return ops
}
