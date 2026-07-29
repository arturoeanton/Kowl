package main

import (
	"errors"
	"os"
	"sync"
	"time"
)

// selfTriggerSettle bounds how long Kowl remembers what a hook wrote. Past it the
// record is dropped even if nothing has touched the file since.
const selfTriggerSettle = 5 * time.Second

// debouncedOps are the operations an editor emits in bursts. A single save can produce
// several of them, and running the hook on each one is both wasteful and racy: the
// first WRITE often arrives while the file is still half written.
var debouncedOps = map[string]bool{"WRITE": true, "CREATE": true, "CHMOD": true}

// dispatcher sits between the watcher and the Runner. It collapses bursts of
// filesystem events, drops the events a hook caused by writing to the file it was
// woken for, and reports whatever the hook fails at.
type dispatcher struct {
	run      func(op, name string) error
	logger   *Logger
	debounce time.Duration
	settle   time.Duration
	// selfTrigger disables self-trigger suppression, letting a hook that writes the
	// observed file wake itself again.
	selfTrigger bool

	// runMu serialises invoke end to end, so the record of what a hook wrote is always
	// in place before the next event is tested against it.
	runMu sync.Mutex
	// wrote maps a file to the state a hook last left it in.
	wrote map[string]writeRecord

	mu      sync.Mutex
	pending map[string]*debounceEntry
	closed  bool
	wg      sync.WaitGroup
}

// writeRecord is the state a hook left a file in, and when.
type writeRecord struct {
	stamp fileStamp
	at    time.Time
}

type debounceEntry struct {
	timer    *time.Timer
	deadline time.Time
}

func newDispatcher(run func(op, name string) error, logger *Logger, debounce time.Duration, selfTrigger bool) *dispatcher {
	return &dispatcher{
		run:         run,
		logger:      logger,
		debounce:    debounce,
		settle:      selfTriggerSettle,
		selfTrigger: selfTrigger,
		wrote:       make(map[string]writeRecord),
		pending:     make(map[string]*debounceEntry),
	}
}

// Dispatch is the Dispatch function handed to the watcher and the poller.
func (d *dispatcher) Dispatch(op, name string) {
	if !debouncedOps[op] || d.debounce <= 0 {
		// TICKER and NOT_FOUND are already paced by -m, and EXIST, REMOVE and RENAME
		// arrive one at a time.
		d.invoke(op, name)
		return
	}

	key := op + "\x00" + name
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	if entry, ok := d.pending[key]; ok {
		// Another event in the same burst: push the deadline out instead of queueing.
		entry.deadline = time.Now().Add(d.debounce)
		d.logger.Debugf("debouncing %s on %s", op, name)
		return
	}
	entry := &debounceEntry{deadline: time.Now().Add(d.debounce)}
	d.pending[key] = entry
	d.wg.Add(1)
	entry.timer = time.AfterFunc(d.debounce, func() { d.fire(key, op, name) })
}

// Close cancels anything still waiting on a debounce timer and waits for the hooks
// already running to finish.
func (d *dispatcher) Close() {
	d.mu.Lock()
	d.closed = true
	for key, entry := range d.pending {
		if entry.timer.Stop() {
			delete(d.pending, key)
			d.wg.Done()
		}
	}
	d.mu.Unlock()
	d.wg.Wait()
}

// fire runs a debounced event, or reschedules it if more events arrived while it was
// waiting.
func (d *dispatcher) fire(key, op, name string) {
	d.mu.Lock()
	entry, ok := d.pending[key]
	if !ok {
		d.mu.Unlock()
		d.wg.Done()
		return
	}
	if remaining := time.Until(entry.deadline); remaining > 0 && !d.closed {
		entry.timer.Reset(remaining)
		d.mu.Unlock()
		return
	}
	delete(d.pending, key)
	closed := d.closed
	d.mu.Unlock()

	if !closed {
		d.invoke(op, name)
	}
	d.wg.Done()
}

// invoke runs the hook for one event, unless that event is one the previous hook
// caused. Afterwards it records the state the hook left the file in, so the write it
// just made does not wake it again.
//
// The whole thing is serialised: the record has to be in place before the event it
// explains is tested, and the event can arrive while the hook that caused it is still
// running.
func (d *dispatcher) invoke(op, name string) {
	d.runMu.Lock()
	defer d.runMu.Unlock()

	if d.causedByLastHook(op, name) {
		d.logger.Debugf("ignoring %s on %s: written by a hook", op, name)
		return
	}

	before, hadBefore := stampOf(name)

	err := d.run(op, name)
	switch {
	case err == nil:
		d.logger.Debugf("%s %s", op, name)
	case errors.Is(err, ErrHookNotDefined):
		d.logger.Debugf("%s %s: no hook defined", op, name)
	default:
		d.logger.Errorf("%s %s: %v", op, name, err)
	}

	if d.selfTrigger {
		return
	}
	after, hasAfter := stampOf(name)
	if !hasAfter {
		delete(d.wrote, name)
		return
	}
	if hadBefore && before == after {
		return
	}
	d.wrote[name] = writeRecord{stamp: after, at: time.Now()}
	d.pruneWrites()
	d.logger.Debugf("%s() changed %s, ignoring the events it caused", op, name)
}

// causedByLastHook reports whether the file is byte-for-byte in the state a hook left
// it in, which means this event is that hook's own write coming back. The caller must
// hold d.runMu.
func (d *dispatcher) causedByLastHook(op, name string) bool {
	if d.selfTrigger || !debouncedOps[op] {
		return false
	}
	record, ok := d.wrote[name]
	if !ok {
		return false
	}
	if time.Since(record.at) > d.settle {
		delete(d.wrote, name)
		return false
	}
	current, exists := stampOf(name)
	if !exists || current != record.stamp {
		// Something else has touched the file since; the record no longer explains it.
		delete(d.wrote, name)
		return false
	}
	return true
}

// pruneWrites drops expired records so watching a churn of short-lived files does not
// grow the map without bound. The caller must hold d.runMu.
func (d *dispatcher) pruneWrites() {
	for name, record := range d.wrote {
		if time.Since(record.at) > d.settle {
			delete(d.wrote, name)
		}
	}
}

// stampOf identifies a version of a file, and reports false when it does not exist.
func stampOf(name string) (fileStamp, bool) {
	info, err := os.Stat(name)
	if err != nil {
		return fileStamp{}, false
	}
	return fileStamp{modTime: info.ModTime(), size: info.Size()}, true
}
