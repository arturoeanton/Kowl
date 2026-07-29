package main

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// selfTriggerSettle bounds how long Kowl remembers what a hook wrote. Past it the
// record is dropped even if nothing has touched the file since.
const selfTriggerSettle = 5 * time.Second

// defaultQueueSize is how many events may be waiting for a hook before Kowl starts
// dropping them. Deep enough to ride out a slow hook, shallow enough that a permanently
// stuck one is noticed rather than buffered forever.
const defaultQueueSize = 1024

// failureRepeatWindow is how long the same failure is collapsed before it is reported
// again, with a count. A script that does not parse fails for every event, which at a
// short poll interval means several lines a second saying the same thing.
const failureRepeatWindow = 10 * time.Second

// debouncedOps are the operations an editor emits in bursts. A single save can produce
// several of them, and running the hook on each one is both wasteful and racy: the
// first WRITE often arrives while the file is still half written.
var debouncedOps = map[string]bool{"WRITE": true, "CREATE": true, "CHMOD": true}

// dispatcher sits between the watcher and the Runner. It collapses bursts of
// filesystem events, drops the events a hook caused by writing to the file it was
// woken for, and reports whatever the hook fails at.
type dispatcher struct {
	// run invokes the hook and reports the paths it changed through Kowl's helpers.
	run      func(op, name string) (written []string, err error)
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
	// lastFailure, repeats and failureAt collapse a failure that keeps happening.
	lastFailure string
	repeats     int
	failureAt   time.Time

	// queue hands events to the worker. Everything upstream — the fsnotify reader
	// goroutines and the supervisor — only ever puts an event on it, so a slow hook
	// cannot stall watch bookkeeping or back up the kernel's event queue behind it.
	queue  chan queuedEvent
	worker sync.WaitGroup
	// submitted and handled count events in and out of the queue. Their difference is
	// the backlog, and both are reported at shutdown.
	submitted atomic.Int64
	handled   atomic.Int64

	mu       sync.Mutex
	pending  map[string]*debounceEntry
	closed   bool
	dropped  int
	reported time.Time
	wg       sync.WaitGroup
}

// queuedEvent is one event waiting for its hook.
type queuedEvent struct{ op, name string }

// writeRecord is the state a hook left a file in, and when.
type writeRecord struct {
	stamp fileStamp
	at    time.Time
}

type debounceEntry struct {
	timer    *time.Timer
	deadline time.Time
}

func newDispatcher(run func(op, name string) (
	[]string, error), logger *Logger, debounce time.Duration, selfTrigger bool) *dispatcher {
	return newDispatcherWithQueue(run, logger, debounce, selfTrigger, defaultQueueSize)
}

func newDispatcherWithQueue(run func(op, name string) (
	[]string, error), logger *Logger, debounce time.Duration, selfTrigger bool, queueSize int) *dispatcher {
	d := &dispatcher{
		run:         run,
		logger:      logger,
		debounce:    debounce,
		settle:      selfTriggerSettle,
		selfTrigger: selfTrigger,
		wrote:       make(map[string]writeRecord),
		pending:     make(map[string]*debounceEntry),
		queue:       make(chan queuedEvent, queueSize),
	}
	d.worker.Add(1)
	go d.serve()
	return d
}

// serve runs queued events one at a time, in the order they arrived. Once Close has
// been called the rest of the queue is drained without running: shutting down is not the
// time to work through a backlog.
func (d *dispatcher) serve() {
	defer d.worker.Done()
	for event := range d.queue {
		d.mu.Lock()
		closed := d.closed
		d.mu.Unlock()
		if !closed {
			d.invoke(event.op, event.name)
		}
		d.handled.Add(1)
	}
}

// enqueue hands an event to the worker without waiting for it.
//
// The queue is bounded, so a hook that never keeps up eventually costs events. Dropping
// one and saying so is better than the alternative: blocking here stalls the fsnotify
// reader, and the kernel then drops events where nobody can see it happen.
func (d *dispatcher) enqueue(op, name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	select {
	case d.queue <- queuedEvent{op: op, name: name}:
		d.submitted.Add(1)
	default:
		d.dropped++
		// Once a second, so a sustained overload does not bury its own report.
		if now := time.Now(); now.Sub(d.reported) >= time.Second {
			d.logger.Errorf("hooks cannot keep up: dropped %d events, most recently %s on %s",
				d.dropped, op, name)
			d.reported = now
		}
	}
}

// Dispatch is the Dispatch function handed to the watcher and the poller.
func (d *dispatcher) Dispatch(op, name string) {
	if !debouncedOps[op] || d.debounce <= 0 {
		// TICKER and NOT_FOUND are already paced by -m, and EXIST, REMOVE and RENAME
		// arrive one at a time.
		d.enqueue(op, name)
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

// Close stops accepting events, cancels anything still waiting on a debounce timer, and
// waits for the hook already running to finish. Whatever was still queued is abandoned.
func (d *dispatcher) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		d.worker.Wait()
		return
	}
	d.closed = true
	for key, entry := range d.pending {
		if entry.timer.Stop() {
			delete(d.pending, key)
			d.wg.Done()
		}
	}
	dropped := d.dropped
	d.mu.Unlock()

	// Timers first: a firing one still wants to enqueue, and enqueue takes d.mu.
	d.wg.Wait()
	close(d.queue)
	d.worker.Wait()

	d.logger.Debugf("handled %d of %d queued events", d.handled.Load(), d.submitted.Load())
	if dropped > 0 {
		d.logger.Errorf("dropped %d events in total because hooks could not keep up", dropped)
	}
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
		d.enqueue(op, name)
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

	written, err := d.run(op, name)
	undefined := errors.Is(err, ErrHookNotDefined)
	switch {
	case err == nil:
		d.logger.Debugf("%s %s", op, name)
		d.lastFailure = ""
	case undefined:
		d.logger.Debugf("%s %s: no hook defined", op, name)
	default:
		d.reportFailure(op, name, err)
	}

	if d.selfTrigger || undefined {
		// A hook that does not exist cannot have written anything, so there is nothing
		// to attribute to it.
		return
	}

	// Every path the hook changed through a helper, exactly. This covers a hook woken
	// for one file that writes another: without it the two wake each other forever.
	for _, path := range written {
		d.remember(path)
	}

	// The file the hook was woken for may also have changed through kExec, which leaves
	// no record. Compare it, accepting that a change made by something else during the
	// hook looks the same from here.
	after, hasAfter := stampOf(name)
	switch {
	case !hasAfter:
		delete(d.wrote, name)
	case !hadBefore || before != after:
		d.remember(name)
	}
	d.pruneWrites()
}

// reportFailure logs a hook failure, collapsing one that keeps repeating. A script that
// does not parse fails identically for every event; saying so once and then counting is
// more use than the same line several times a second. The caller must hold d.runMu.
func (d *dispatcher) reportFailure(op, name string, err error) {
	message := err.Error()
	if message != d.lastFailure {
		d.logger.Errorf("%s %s: %v", op, name, err)
		d.lastFailure = message
		d.failureAt = time.Now()
		d.repeats = 0
		return
	}

	d.repeats++
	if time.Since(d.failureAt) < failureRepeatWindow {
		d.logger.Debugf("%s %s: %v", op, name, err)
		return
	}
	d.logger.Errorf("%s %s: %v (and %d more like it)", op, name, err, d.repeats)
	d.failureAt = time.Now()
	d.repeats = 0
}

// remember records the state a path was left in, so the event that change produces can
// be recognised as the hook's own. The caller must hold d.runMu.
func (d *dispatcher) remember(path string) {
	stamp, exists := stampOf(path)
	if !exists {
		// The hook deleted it; there is no state to match a later event against.
		delete(d.wrote, path)
		return
	}
	d.wrote[path] = writeRecord{stamp: stamp, at: time.Now()}
	d.logger.Debugf("a hook changed %s, ignoring the events it caused", path)
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
