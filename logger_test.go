package main

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func fixedLogger(out *safeBuffer, level Level) *Logger {
	logger := NewLogger(out, level)
	logger.now = func() time.Time { return time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC) }
	return logger
}

func TestLoggerWritesLevelAndTimestamp(t *testing.T) {
	out := &safeBuffer{}
	fixedLogger(out, LevelDebug).Infof("watching %s", "/tmp/foo")

	got := out.String()
	if want := "2024-03-01T12:00:00Z info  watching /tmp/foo\n"; got != want {
		t.Fatalf("logged %q, want %q", got, want)
	}
}

func TestLoggerDropsMessagesBelowItsLevel(t *testing.T) {
	tests := []struct {
		level Level
		want  []string
		skip  []string
	}{
		{LevelDebug, []string{"a debug", "an info", "an error"}, nil},
		{LevelInfo, []string{"an info", "an error"}, []string{"a debug"}},
		{LevelError, []string{"an error"}, []string{"a debug", "an info"}},
	}
	for _, tt := range tests {
		t.Run(tt.level.String(), func(t *testing.T) {
			out := &safeBuffer{}
			logger := fixedLogger(out, tt.level)
			logger.Debugf("a debug")
			logger.Infof("an info")
			logger.Errorf("an error")

			got := out.String()
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Fatalf("level %s dropped %q:\n%s", tt.level, want, got)
				}
			}
			for _, unwanted := range tt.skip {
				if strings.Contains(got, unwanted) {
					t.Fatalf("level %s kept %q:\n%s", tt.level, unwanted, got)
				}
			}
		})
	}
}

func TestLoggerEnabled(t *testing.T) {
	logger := NewLogger(&safeBuffer{}, LevelInfo)
	if logger.Enabled(LevelDebug) {
		t.Fatal("debug is enabled on an info logger")
	}
	if !logger.Enabled(LevelInfo) || !logger.Enabled(LevelError) {
		t.Fatal("info or error is disabled on an info logger")
	}
}

// Kowl logs from the watcher, the poller and debounce timers at once.
func TestLoggerIsSafeForConcurrentUse(t *testing.T) {
	out := &safeBuffer{}
	logger := fixedLogger(out, LevelDebug)

	const writers, each = 8, 25
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				logger.Infof("message")
			}
		}()
	}
	wg.Wait()

	if got := strings.Count(out.String(), "\n"); got != writers*each {
		t.Fatalf("wrote %d lines, want %d", got, writers*each)
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		want  Level
	}{
		{"debug", LevelDebug},
		{"info", LevelInfo},
		{"error", LevelError},
		{"INFO", LevelInfo},
		{"Error", LevelError},
	}
	for _, tt := range tests {
		got, err := ParseLevel(tt.input)
		if err != nil {
			t.Fatalf("ParseLevel(%q): %v", tt.input, err)
		}
		if got != tt.want {
			t.Fatalf("ParseLevel(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseLevelRejectsUnknownNames(t *testing.T) {
	if _, err := ParseLevel("verbose"); err == nil {
		t.Fatal("ParseLevel returned nil error for an unknown level")
	}
}
