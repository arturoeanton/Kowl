package main

import (
	"context"
	"fmt"
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

// resolve returns the existing paths matched by the patterns, deduplicated and sorted.
// A pattern with no wildcards resolves to itself when it exists, so plain filenames and
// globs go through the same path.
func resolve(patterns []string) []string {
	seen := make(map[string]bool)
	var paths []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			// Rejected by ValidatePatterns at startup; nothing useful to do per tick.
			continue
		}
		for _, match := range matches {
			if !seen[match] {
				seen[match] = true
				paths = append(paths, match)
			}
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
// renaming it over the old one.
//
// EXIST is dispatched synchronously each time an observer is established, so it is
// always ordered before any event that observer produces.
func Supervise(ctx context.Context, patterns []string, interval time.Duration, dispatch Dispatch, logger *Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	observers := make(map[string]*observerHandle)
	defer func() {
		for path, handle := range observers {
			handle.stop()
			delete(observers, path)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			present := resolve(patterns)
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
				dispatch("EXIST", path)
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
