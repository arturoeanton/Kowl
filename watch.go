package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/fsnotify/fsnotify"
)

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
}

// resolve returns the existing paths matched by the patterns, deduplicated and sorted.
// A pattern with no wildcards resolves to itself when it exists, so plain filenames and
// globs go through the same path.
//
// With recursive set, a matched directory also contributes every directory below it.
// fsnotify does not recurse on its own, and watching a directory only reports its direct
// children, so a tree has to be enumerated and watched a level at a time. Subdirectories
// created later are picked up by the next resolve.
func resolve(patterns []string, recursive bool) []string {
	seen := make(map[string]bool)
	var paths []string
	add := func(path string) {
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}

	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			// Rejected by ValidatePatterns at startup; nothing useful to do per tick.
			continue
		}
		for _, match := range matches {
			add(match)
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
				if entry.IsDir() {
					add(path)
				}
				return nil
			})
		}
	}
	sort.Strings(paths)
	return paths
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
				matches, err := filepath.Glob(pattern)
				if err != nil || len(matches) == 0 {
					dispatch("NOT_FOUND", pattern)
					continue
				}
				for _, match := range matches {
					dispatch("TICKER", match)
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

	// capped remembers whether the watch limit was already reported, so hitting it does
	// not repeat the same message every tick.
	capped := false

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			present := resolve(cfg.Patterns, cfg.Recursive)
			if cfg.MaxWatches > 0 && len(present) > cfg.MaxWatches {
				if !capped {
					logger.Errorf("%d paths match but the limit is %d: watching the first %d, raise --max-watches to cover the rest",
						len(present), cfg.MaxWatches, cfg.MaxWatches)
					capped = true
				}
				present = present[:cfg.MaxWatches]
			} else {
				capped = false
			}

			stillThere := make(map[string]bool, len(present))
			for _, path := range present {
				stillThere[path] = true
			}

			for path, handle := range observers {
				if !stillThere[path] {
					handle.stop()
					delete(observers, path)
					logger.Debugf("stopped watching %s", path)
				}
			}

			for _, path := range present {
				if _, watching := observers[path]; watching {
					continue
				}
				observerCtx, observerCancel := context.WithCancel(ctx)
				done, err := observe(observerCtx, path, dispatch, logger)
				if err != nil {
					// The path can vanish between resolve and Add. Leave it unwatched
					// so the next tick retries, instead of keeping a live observer
					// that watches nothing.
					observerCancel()
					logger.Errorf("%v (retrying)", err)
					continue
				}
				observers[path] = &observerHandle{cancel: observerCancel, done: done}
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
				// A watcher error is not fatal: report it and keep observing.
				logger.Errorf("watcher error on %s: %v", path, err)
			}
		}
	}()
	return done, nil
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
