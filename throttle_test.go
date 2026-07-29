package main

import (
	"sync"
	"testing"
	"time"
)

func TestRepeatFilterAdmitsTheFirstOfAKind(t *testing.T) {
	filter := newRepeatFilter(time.Minute)

	suppressed, ok := filter.admit("something broke")
	if !ok {
		t.Fatal("the first message was not admitted")
	}
	if suppressed != 0 {
		t.Fatalf("the first message reported %d suppressed", suppressed)
	}
}

func TestRepeatFilterHoldsBackRepeats(t *testing.T) {
	filter := newRepeatFilter(time.Minute)
	filter.admit("something broke")

	for i := 0; i < 100; i++ {
		if _, ok := filter.admit("something broke"); ok {
			t.Fatalf("repeat %d was admitted inside the window", i)
		}
	}
}

// Once the window passes the message comes back, carrying what was held.
func TestRepeatFilterReportsAgainWithACount(t *testing.T) {
	filter := newRepeatFilter(20 * time.Millisecond)
	filter.admit("something broke")
	for i := 0; i < 5; i++ {
		filter.admit("something broke")
	}

	time.Sleep(40 * time.Millisecond)

	suppressed, ok := filter.admit("something broke")
	if !ok {
		t.Fatal("the message was not admitted after the window passed")
	}
	if suppressed != 6 {
		t.Fatalf("reported %d suppressed, want the 6 that were held back", suppressed)
	}
}

// After reporting, the count starts over rather than accumulating forever.
func TestRepeatFilterCountsFromZeroAfterReporting(t *testing.T) {
	filter := newRepeatFilter(20 * time.Millisecond)
	filter.admit("something broke")
	filter.admit("something broke")
	time.Sleep(40 * time.Millisecond)
	filter.admit("something broke")

	filter.admit("something broke")
	time.Sleep(40 * time.Millisecond)
	suppressed, ok := filter.admit("something broke")

	if !ok {
		t.Fatal("the message was not admitted after the second window")
	}
	if suppressed != 2 {
		t.Fatalf("reported %d suppressed, want 2 from this window alone", suppressed)
	}
}

// A different failure is news, whatever came before it.
func TestRepeatFilterAlwaysAdmitsADifferentMessage(t *testing.T) {
	filter := newRepeatFilter(time.Minute)
	filter.admit("first problem")
	filter.admit("first problem")

	if _, ok := filter.admit("second problem"); !ok {
		t.Fatal("a different message was held back")
	}
	// The new message is now the one being collapsed.
	if _, ok := filter.admit("second problem"); ok {
		t.Fatal("a repeat of the new message was admitted")
	}
}

// Resetting is what makes a failure that returns after things recovered newsworthy.
func TestRepeatFilterResetMakesTheSameMessageNewAgain(t *testing.T) {
	filter := newRepeatFilter(time.Minute)
	filter.admit("something broke")
	if _, ok := filter.admit("something broke"); ok {
		t.Fatal("a repeat was admitted before the reset")
	}

	filter.reset()

	if _, ok := filter.admit("something broke"); !ok {
		t.Fatal("the message was not admitted after a reset")
	}
}

// The watcher reports from its own goroutine while hooks report from another.
func TestRepeatFilterIsSafeForConcurrentUse(t *testing.T) {
	filter := newRepeatFilter(time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				filter.admit("a message")
				if j%50 == 0 {
					filter.reset()
				}
			}
		}(i)
	}
	wg.Wait()
}
