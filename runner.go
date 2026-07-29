package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// ErrHookNotDefined is returned by Runner.Run when the script parses cleanly but does
// not implement a function for the dispatched operation. Scripts are only expected to
// implement the hooks they care about, so callers treat this as a normal outcome.
var ErrHookNotDefined = errors.New("hook not defined")

// hookNames lists every function name Kowl dispatches to, in the order they are
// reported at startup.
var hookNames = []string{"exist", "create", "write", "remove", "rename", "chmod", "ticker", "not_found"}

// errHookTimeout is what the watchdog interrupts a VM with.
var errHookTimeout = errors.New("kowl: time is up")

// abandonGrace is how long a call gets to unwind after the watchdog interrupts it.
//
// The interrupt only takes effect between JavaScript statements, so it cannot reach a
// hook sitting inside a Go call: reading a fifo nobody writes to, or a file on a mount
// that has stopped answering. Waiting for that forever stops every later event, so past
// this grace the call is abandoned and Kowl carries on.
const abandonGrace = 5 * time.Second

// errAbandoned marks a call that outlasted even the grace period. The goroutine running
// it is still out there, so whatever VM it holds must never be used again.
var errAbandoned = errors.New("abandoned")

// runBounded runs fn with the VM's interrupt armed, and stops waiting for it once the
// grace period is up. It reports whether the call was abandoned rather than finished.
func runBounded(vm *goja.Runtime, timeout time.Duration, fn func() error) (error, bool) {
	stop := watchdog(vm, timeout)

	done := make(chan error, 1)
	go func() { done <- fn() }()

	select {
	case err := <-done:
		stop()
		return err, false
	case <-time.After(timeout + abandonGrace):
		// Deliberately not calling stop: the goroutine is still using this VM, and
		// clearing its interrupt would take away the only thing that might yet end it.
		return errAbandoned, true
	}
}

// Runner owns the JavaScript side of Kowl: one JavaScript VM, the script loaded into it,
// and the lock that keeps hooks from running concurrently.
//
// The VM is kept between events rather than rebuilt, so the script is parsed once
// instead of once per event and globals survive from one hook to the next. It is
// reloaded when the script file changes on disk, so edits still take effect without a
// restart.
type Runner struct {
	scriptPath string
	config     vmConfig
	timeout    time.Duration

	mu     sync.Mutex
	vm     *goja.Runtime
	loaded fileStamp
}

// fileStamp identifies a version of the script file cheaply.
type fileStamp struct {
	modTime time.Time
	size    int64
}

// NewRunner returns a Runner bound to the JavaScript file at scriptPath.
func NewRunner(scriptPath string) *Runner {
	return &Runner{
		scriptPath: scriptPath,
		config:     defaultVMConfig(),
		timeout:    defaultHookTimeout,
	}
}

// Run invokes the hook matching op, lowercased: WRITE calls write(), NOT_FOUND calls
// not_found(), and so on. Hooks are serialised, so a script never has two of its own
// functions running at once and can keep state in ordinary globals.
//
// It returns the paths the hook changed through Kowl's own helpers, so the caller can
// tell the events that follow apart from real changes. The list is returned even when
// the hook fails: a hook that wrote a file and then threw still wrote the file.
//
// Every failure is returned rather than logged or fatal, so the caller decides what is
// worth reporting and Kowl keeps watching when a single event goes wrong.
func (r *Runner) Run(op, name string) (written []string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Anything left from an earlier hook is not this one's doing. The log is captured
	// here so that abandoning the hook can hand it a fresh one without this losing
	// track of what it had already written.
	writes := r.config.writes
	writes.take()
	defer func() { written = writes.take() }()

	vm, err := r.ensureLoaded()
	if err != nil {
		return nil, err
	}

	hook := strings.ToLower(op)
	fn, callable := goja.AssertFunction(vm.Get(hook))
	if !callable {
		return nil, fmt.Errorf("%s(): %w", hook, ErrHookNotDefined)
	}

	event := vm.ToValue(newHookEvent(op, name))
	callErr, abandoned := runBounded(vm, r.timeout, func() error {
		_, err := fn(goja.Undefined(), vm.ToValue(name), vm.ToValue(op), event)
		return err
	})

	switch {
	case abandoned:
		// The hook is stuck somewhere Kowl cannot reach. Its goroutine keeps the VM and
		// the write log it was filling; both are replaced so nothing it does later is
		// attributed to the next hook.
		r.config.writes = &writeLog{}
		r.discard()
		r.logger().Errorf("%s() is stuck after %s and was abandoned; it may still be running", hook, r.timeout)
		return written, fmt.Errorf("%s() did not return within %s and was abandoned", hook, r.timeout)
	case callErr != nil && interrupted(callErr):
		// The VM was stopped part-way through a statement, so its state is no longer
		// trustworthy. Drop it and load a fresh one for the next event.
		r.discard()
		return nil, fmt.Errorf("%s() exceeded %s and was interrupted", hook, r.timeout)
	case callErr != nil:
		return nil, fmt.Errorf("%s() failed: %w", hook, callErr)
	}
	return nil, nil
}

// logger is where the Runner reports something the caller cannot: a hook abandoned mid
// flight is not a failure of this event so much as a warning about the next ones.
func (r *Runner) logger() *Logger {
	if r.config.logger != nil {
		return r.config.logger
	}
	return NewLogger(os.Stderr, LevelError, FormatText)
}

// interrupted reports whether a failure is the watchdog stopping the VM rather than the
// script itself going wrong.
func interrupted(err error) bool {
	var stopped *goja.InterruptedError
	return errors.As(err, &stopped)
}

// newHookEvent builds the third argument every hook receives. It used to be os.Args,
// which told a script nothing it could act on; this describes the file the event is
// about, as it stands at the moment the hook runs.
//
// The path may already be gone by then, on a REMOVE or a rename, so exists says whether
// the rest of the fields mean anything.
func newHookEvent(op, path string) hookEvent {
	event := hookEvent{
		Path: path,
		Op:   op,
		Name: filepath.Base(path),
		Dir:  filepath.Dir(path),
	}
	info, err := os.Stat(path)
	if err != nil {
		return event
	}
	event.Exists = true
	event.IsDir = info.IsDir()
	event.Size = info.Size()
	event.ModTime = info.ModTime().Format(time.RFC3339Nano)
	return event
}

// hookEvent is the third argument every hook receives. Field names reach JavaScript in
// lower camel case, so this reads as {path, op, name, dir, exists, isDir, size, modTime}.
type hookEvent struct {
	Path    string
	Op      string
	Name    string
	Dir     string
	Exists  bool
	IsDir   bool
	Size    int64
	ModTime string
}

// Reload drops the loaded script so the next event reads it again, and reports what the
// fresh copy defines. Kowl already reloads on its own when the file changes; this is for
// the cases that leaves out, such as a script that pulls in state from somewhere else.
func (r *Runner) Reload() ([]string, error) {
	r.mu.Lock()
	r.discard()
	r.mu.Unlock()
	return r.DefinedHooks()
}

// DefinedHooks reports which of hookNames the script implements. It doubles as the
// startup check: a script that does not parse is reported before Kowl starts watching,
// instead of failing silently on every event.
func (r *Runner) DefinedHooks() ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	vm, err := r.ensureLoaded()
	if err != nil {
		return nil, err
	}
	var defined []string
	for _, hook := range hookNames {
		if _, callable := goja.AssertFunction(vm.Get(hook)); callable {
			defined = append(defined, hook)
		}
	}
	return defined, nil
}

// ensureLoaded returns the VM holding the current version of the script, reloading it
// if the file changed since it was last read. The caller must hold r.mu.
func (r *Runner) ensureLoaded() (*goja.Runtime, error) {
	info, err := os.Stat(r.scriptPath)
	if err != nil {
		return nil, fmt.Errorf("reading script: %w", err)
	}
	stamp := fileStamp{modTime: info.ModTime(), size: info.Size()}
	if r.vm != nil && r.loaded == stamp {
		return r.vm, nil
	}

	code, err := os.ReadFile(r.scriptPath)
	if err != nil {
		return nil, fmt.Errorf("reading script: %w", err)
	}
	vm := newVM(r.config)
	if err := r.evaluate(vm, string(code)); err != nil {
		r.discard()
		return nil, err
	}

	r.vm, r.loaded = vm, stamp
	return vm, nil
}

// evaluate runs the script's top level under the same watchdog a hook gets. Statements
// outside a function are code too: a loop among them used to hang Kowl with nothing
// reported, at startup before any log line, and on reload while holding r.mu.
func (r *Runner) evaluate(vm *goja.Runtime, code string) error {
	err, abandoned := runBounded(vm, r.timeout, func() error {
		_, err := vm.RunString(code)
		return err
	})
	switch {
	case abandoned:
		return fmt.Errorf("loading %s: top level did not return within %s and was abandoned", r.scriptPath, r.timeout)
	case err != nil && interrupted(err):
		return fmt.Errorf("loading %s: top level exceeded %s and was interrupted", r.scriptPath, r.timeout)
	case err != nil:
		return fmt.Errorf("loading %s: %w", r.scriptPath, err)
	}
	return nil
}

// watchdog stops the VM if it is still running after timeout. The returned function
// disarms it and must be called before the VM is used again.
func watchdog(vm *goja.Runtime, timeout time.Duration) func() {
	timer := time.AfterFunc(timeout, func() { vm.Interrupt(errHookTimeout) })
	return func() {
		timer.Stop()
		vm.ClearInterrupt()
	}
}

// discard forces the script to be reloaded on the next call. The caller must hold r.mu.
func (r *Runner) discard() {
	r.vm, r.loaded = nil, fileStamp{}
}
