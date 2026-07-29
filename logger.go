package main

import (
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
	// LevelInfo reports lifecycle messages and anything unexpected.
	LevelInfo
	// LevelError reports only failures.
	LevelError
)

var levelNames = map[Level]string{
	LevelDebug: "debug",
	LevelInfo:  "info",
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
	return LevelInfo, fmt.Errorf("unknown log level %q, want debug, info or error", name)
}

// Logger writes timestamped, level-prefixed lines. A Kowl process logs from the
// watcher, the poller and the debounce timers at once, so writes are serialised.
type Logger struct {
	mu    sync.Mutex
	out   io.Writer
	level Level
	now   func() time.Time
}

// NewLogger returns a Logger writing to out, dropping anything below level.
func NewLogger(out io.Writer, level Level) *Logger {
	return &Logger{out: out, level: level, now: time.Now}
}

// Debugf reports detail that is only useful when diagnosing what Kowl is doing.
func (l *Logger) Debugf(format string, args ...interface{}) { l.printf(LevelDebug, format, args...) }

// Infof reports normal progress.
func (l *Logger) Infof(format string, args ...interface{}) { l.printf(LevelInfo, format, args...) }

// Errorf reports a failure. Kowl keeps running after one.
func (l *Logger) Errorf(format string, args ...interface{}) { l.printf(LevelError, format, args...) }

func (l *Logger) printf(level Level, format string, args ...interface{}) {
	if level < l.level {
		return
	}
	message := fmt.Sprintf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.out, "%s %-5s %s\n", l.now().Format(time.RFC3339), level, message)
}
