package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Dispatch runs the hook for an operation on a file. It never fails: a Dispatch is
// responsible for reporting its own errors so that one bad event cannot stop watching.
type Dispatch func(op, name string)

// Poll fires TICKER while the observed file exists and NOT_FOUND while it does not.
// It returns when ctx is cancelled.
func Poll(ctx context.Context, filename string, interval time.Duration, dispatch Dispatch, logger *log.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := os.Stat(filename); err != nil {
				if !os.IsNotExist(err) {
					logger.Printf("stat %s: %v", filename, err)
				}
				dispatch("NOT_FOUND", filename)
			} else {
				dispatch("TICKER", filename)
			}
		}
	}
}

// Supervise keeps exactly one fsnotify observer alive for filename. fsnotify watches
// inodes rather than paths, so the observer is torn down when the file disappears and a
// fresh one is started when it comes back. Tearing it down is what releases the
// watcher's file descriptor and goroutine; letting it linger leaked one of each per
// delete/recreate cycle until the process ran out of watches.
//
// EXIST is dispatched synchronously each time an observer is established, so it is
// always ordered before any event that observer produces.
func Supervise(ctx context.Context, filename string, interval time.Duration, dispatch Dispatch, logger *log.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var (
		cancel context.CancelFunc
		done   <-chan struct{}
	)
	stop := func() {
		if cancel == nil {
			return
		}
		cancel()
		<-done
		cancel, done = nil, nil
	}
	defer stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, err := os.Stat(filename)
			switch {
			case err != nil:
				stop()
			case cancel == nil:
				observerCtx, observerCancel := context.WithCancel(ctx)
				observerDone, err := observe(observerCtx, filename, dispatch, logger)
				if err != nil {
					// The file can vanish between the Stat above and the Add below.
					// Leave cancel nil so the next tick retries instead of leaving a
					// live observer that watches nothing.
					observerCancel()
					logger.Printf("%v (retrying)", err)
					continue
				}
				cancel, done = observerCancel, observerDone
				dispatch("EXIST", filename)
			}
		}
	}
}

// observe starts watching filename and translates fsnotify events into hook
// dispatches. The returned channel is closed once the observer has stopped and its
// watcher has been closed.
func observe(ctx context.Context, filename string, dispatch Dispatch, logger *log.Logger) (<-chan struct{}, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("creating watcher for %s: %w", filename, err)
	}
	if err := watcher.Add(filename); err != nil {
		watcher.Close()
		return nil, fmt.Errorf("watching %s: %w", filename, err)
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
				logger.Printf("watcher error on %s: %v", filename, err)
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
