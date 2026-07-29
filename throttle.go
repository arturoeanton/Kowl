package main

import (
	"sync"
	"time"
)

// repeatFilter collapses a message that keeps happening into one report and a count.
//
// Both places that report failures need this. A script that no longer parses fails
// identically for every event, and a directory that cannot be watched fails identically
// on every tick; either one buries the rest of the log within seconds.
type repeatFilter struct {
	window time.Duration

	mu       sync.Mutex
	last     string
	repeats  int
	reported time.Time
}

func newRepeatFilter(window time.Duration) *repeatFilter {
	return &repeatFilter{window: window}
}

// admit reports whether message should be written now, and how many identical ones were
// suppressed since the last time one was. A different message is always admitted.
func (r *repeatFilter) admit(message string) (suppressed int, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	if message != r.last {
		r.last, r.repeats, r.reported = message, 0, now
		return 0, true
	}

	r.repeats++
	if now.Sub(r.reported) < r.window {
		return 0, false
	}
	suppressed, r.repeats, r.reported = r.repeats, 0, now
	return suppressed, true
}

// reset forgets what was last reported, so the same message is reported again rather
// than counted. Call it when the thing that was failing has worked.
func (r *repeatFilter) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last, r.repeats = "", 0
}
