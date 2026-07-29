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

	// Anything left from an earlier hook is not this one's doing.
	r.config.writes.take()
	defer func() { written = r.config.writes.take() }()

	vm, err := r.ensureLoaded()
	if err != nil {
		return nil, err
	}

	hook := strings.ToLower(op)
	fn, callable := goja.AssertFunction(vm.Get(hook))
	if !callable {
		return nil, fmt.Errorf("%s(): %w", hook, ErrHookNotDefined)
	}

	stop := watchdog(vm, r.timeout)
	defer stop()

	event := vm.ToValue(newHookEvent(op, name))
	if _, callErr := fn(goja.Undefined(), vm.ToValue(name), vm.ToValue(op), event); callErr != nil {
		if interrupted(callErr) {
			// The VM was stopped part-way through a statement, so its state is no
			// longer trustworthy. Drop it and load a fresh one for the next event.
			r.discard()
			return nil, fmt.Errorf("%s() exceeded %s and was interrupted", hook, r.timeout)
		}
		return nil, fmt.Errorf("%s() failed: %w", hook, callErr)
	}
	return nil, nil
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
	stop := watchdog(vm, r.timeout)
	defer stop()

	if _, err := vm.RunString(code); err != nil {
		if interrupted(err) {
			return fmt.Errorf("loading %s: top level exceeded %s and was interrupted", r.scriptPath, r.timeout)
		}
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
