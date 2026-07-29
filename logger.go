package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Level controls how much a Logger reports.
type Level int

const (
	// LevelDebug reports every event, including the ones Kowl decided to ignore.
	LevelDebug Level = iota
	// LevelInfo reports lifecycle messages and whatever a script logs.
	LevelInfo
	// LevelWarn reports something a script or Kowl wants attention for.
	LevelWarn
	// LevelError reports only failures.
	LevelError
)

var levelNames = map[Level]string{
	LevelDebug: "debug",
	LevelInfo:  "info",
	LevelWarn:  "warn",
	LevelError: "error",
}

func (l Level) String() string {
	if name, ok := levelNames[l]; ok {
		return name
	}
	return fmt.Sprintf("Level(%d)", int(l))
}

// ParseLevel converts a level name such as "info" into a Level.
func ParseLevel(name string) (Level, error) {
	for level, candidate := range levelNames {
		if strings.EqualFold(name, candidate) {
			return level, nil
		}
	}
	return LevelInfo, fmt.Errorf("unknown log level %q, want debug, info, warn or error", name)
}

// Format is how a Logger renders a line.
type Format int

const (
	// FormatText is one human-readable line per message.
	FormatText Format = iota
	// FormatJSON is one JSON object per line, for log collectors.
	FormatJSON
)

var formatNames = map[Format]string{
	FormatText: "text",
	FormatJSON: "json",
}

func (f Format) String() string {
	if name, ok := formatNames[f]; ok {
		return name
	}
	return fmt.Sprintf("Format(%d)", int(f))
}

// ParseFormat converts a format name such as "json" into a Format.
func ParseFormat(name string) (Format, error) {
	for format, candidate := range formatNames {
		if strings.EqualFold(name, candidate) {
			return format, nil
		}
	}
	return FormatText, fmt.Errorf("unknown log format %q, want text or json", name)
}

// Logger writes timestamped, leveled lines. A Kowl process logs from the watcher, the
// poller and the debounce timers at once, so writes are serialised.
type Logger struct {
	mu     sync.Mutex
	out    io.Writer
	level  Level
	format Format
	now    func() time.Time
}

// NewLogger returns a Logger writing to out, dropping anything below level.
func NewLogger(out io.Writer, level Level, format Format) *Logger {
	return &Logger{out: out, level: level, format: format, now: time.Now}
}

// Debugf reports detail that is only useful when diagnosing what Kowl is doing.
func (l *Logger) Debugf(format string, args ...interface{}) { l.printf(LevelDebug, format, args...) }

// Infof reports normal progress.
func (l *Logger) Infof(format string, args ...interface{}) { l.printf(LevelInfo, format, args...) }

// Warnf reports something worth attention that is not a failure.
func (l *Logger) Warnf(format string, args ...interface{}) { l.printf(LevelWarn, format, args...) }

// Errorf reports a failure. Kowl keeps running after one.
func (l *Logger) Errorf(format string, args ...interface{}) { l.printf(LevelError, format, args...) }

func (l *Logger) printf(level Level, format string, args ...interface{}) {
	if level < l.level {
		return
	}
	entry := logEntry{
		Time:    l.now().Format(time.RFC3339),
		Level:   level.String(),
		Message: fmt.Sprintf(format, args...),
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.format == FormatJSON {
		// Marshal cannot fail for three strings, but a partial line would be worse
		// than none, so only write what encoded.
		if encoded, err := json.Marshal(entry); err == nil {
			l.out.Write(append(encoded, '\n'))
		}
		return
	}
	fmt.Fprintf(l.out, "%s %-5s %s\n", entry.Time, entry.Level, entry.Message)
}

// logEntry is one line, in the shape the JSON format writes.
type logEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}
