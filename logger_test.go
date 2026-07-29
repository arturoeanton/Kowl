package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixedLogger(out *safeBuffer, level Level) *Logger {
	return fixedFormatLogger(out, level, FormatText)
}

func fixedFormatLogger(out *safeBuffer, level Level, format Format) *Logger {
	logger := NewLogger(out, level, format)
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
		{LevelDebug, []string{"a debug", "an info", "a warn", "an error"}, nil},
		{LevelInfo, []string{"an info", "a warn", "an error"}, []string{"a debug"}},
		{LevelWarn, []string{"a warn", "an error"}, []string{"a debug", "an info"}},
		{LevelError, []string{"an error"}, []string{"a debug", "an info", "a warn"}},
	}
	for _, tt := range tests {
		t.Run(tt.level.String(), func(t *testing.T) {
			out := &safeBuffer{}
			logger := fixedLogger(out, tt.level)
			logger.Debugf("a debug")
			logger.Infof("an info")
			logger.Warnf("a warn")
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
		{"warn", LevelWarn},
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

func TestLoggerJSONFormatWritesOneObjectPerLine(t *testing.T) {
	out := &safeBuffer{}
	logger := fixedFormatLogger(out, LevelDebug, FormatJSON)
	logger.Infof("watching %s", "/tmp/foo")
	logger.Errorf("it broke")

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("wrote %d lines, want 2:\n%s", len(lines), out.String())
	}

	var first logEntry
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first line is not JSON: %v\n%s", err, lines[0])
	}
	if first.Level != "info" || first.Message != "watching /tmp/foo" {
		t.Fatalf("first line = %+v, want the info message", first)
	}
	if first.Time != "2024-03-01T12:00:00Z" {
		t.Fatalf("first line time = %q", first.Time)
	}

	var second logEntry
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("second line is not JSON: %v\n%s", err, lines[1])
	}
	if second.Level != "error" {
		t.Fatalf("second line level = %q, want error", second.Level)
	}
}

// A message containing quotes, newlines or backslashes must stay parseable.
func TestLoggerJSONFormatEscapesTheMessage(t *testing.T) {
	out := &safeBuffer{}
	fixedFormatLogger(out, LevelDebug, FormatJSON).Infof(`he said "hi"` + "\n" + `path\to\file`)

	var entry logEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &entry); err != nil {
		t.Fatalf("line is not JSON: %v\n%s", err, out.String())
	}
	if !strings.Contains(entry.Message, `"hi"`) || !strings.Contains(entry.Message, `path\to\file`) {
		t.Fatalf("message round-tripped as %q", entry.Message)
	}
}

func TestLoggerJSONFormatRespectsTheLevel(t *testing.T) {
	out := &safeBuffer{}
	logger := fixedFormatLogger(out, LevelError, FormatJSON)
	logger.Debugf("dropped")
	logger.Errorf("kept")

	if strings.Contains(out.String(), "dropped") {
		t.Fatalf("an error-level JSON logger kept a debug line:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "kept") {
		t.Fatalf("an error-level JSON logger dropped the error:\n%s", out.String())
	}
}

// Concurrent writers must not interleave halves of two JSON objects on one line.
func TestLoggerJSONFormatIsSafeForConcurrentUse(t *testing.T) {
	out := &safeBuffer{}
	logger := fixedFormatLogger(out, LevelDebug, FormatJSON)

	const writers, each = 8, 25
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				logger.Infof("a message with spaces and a quote \"")
			}
		}()
	}
	wg.Wait()

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != writers*each {
		t.Fatalf("wrote %d lines, want %d", len(lines), writers*each)
	}
	for i, line := range lines {
		var entry logEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("line %d is not JSON: %v\n%s", i, err, line)
		}
	}
}

func TestParseFormat(t *testing.T) {
	tests := []struct {
		input string
		want  Format
	}{
		{"text", FormatText},
		{"json", FormatJSON},
		{"JSON", FormatJSON},
		{"Text", FormatText},
	}
	for _, tt := range tests {
		got, err := ParseFormat(tt.input)
		if err != nil {
			t.Fatalf("ParseFormat(%q): %v", tt.input, err)
		}
		if got != tt.want {
			t.Fatalf("ParseFormat(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseFormatRejectsUnknownNames(t *testing.T) {
	if _, err := ParseFormat("logfmt"); err == nil {
		t.Fatal("ParseFormat returned nil error for an unknown format")
	}
}
