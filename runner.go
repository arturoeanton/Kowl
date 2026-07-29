package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/robertkrimen/otto"
)

// ErrHookNotDefined is returned by Runner.Run when the script parses cleanly but does
// not implement a function for the dispatched operation. Scripts are only expected to
// implement the hooks they care about, so callers treat this as a normal outcome.
var ErrHookNotDefined = errors.New("hook not defined")

// hookNames lists every function name Kowl dispatches to, in the order they are
// reported at startup.
var hookNames = []string{"exist", "create", "write", "remove", "rename", "chmod", "ticker", "not_found"}

// hookTimeout is the value panicked with when a hook overruns its budget. It is
// recovered in Run and never escapes.
type hookTimeout struct{}

// Runner owns the JavaScript side of Kowl: one otto VM, the script loaded into it, and
// the lock that keeps hooks from running concurrently.
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
	vm     *otto.Otto
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
// Every failure is returned rather than logged or fatal, so the caller decides what is
// worth reporting and Kowl keeps watching when a single event goes wrong.
func (r *Runner) Run(op, name string) (err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	vm, err := r.ensureLoaded()
	if err != nil {
		return err
	}

	hook := strings.ToLower(op)
	fn, err := vm.Get(hook)
	if err != nil || !fn.IsFunction() {
		return fmt.Errorf("%s(): %w", hook, ErrHookNotDefined)
	}

	defer func() {
		caught := recover()
		if caught == nil {
			return
		}
		if _, ok := caught.(hookTimeout); !ok {
			panic(caught)
		}
		// The VM was interrupted part-way through a statement, so its state is no
		// longer trustworthy. Drop it and load a fresh one for the next event.
		r.discard()
		err = fmt.Errorf("%s() exceeded %s and was interrupted", hook, r.timeout)
	}()

	stop := watchdog(vm, r.timeout)
	defer stop()

	if _, callErr := fn.Call(otto.NullValue(), name, op, newHookEvent(op, name)); callErr != nil {
		return fmt.Errorf("%s() failed: %w", hook, callErr)
	}
	return nil
}

// newHookEvent builds the third argument every hook receives. It used to be os.Args,
// which told a script nothing it could act on; this describes the file the event is
// about, as it stands at the moment the hook runs.
//
// The path may already be gone by then, on a REMOVE or a rename, so exists says whether
// the rest of the fields mean anything.
func newHookEvent(op, path string) map[string]interface{} {
	event := map[string]interface{}{
		"path":    path,
		"op":      op,
		"name":    filepath.Base(path),
		"dir":     filepath.Dir(path),
		"exists":  false,
		"isDir":   false,
		"size":    int64(0),
		"modTime": "",
	}
	info, err := os.Stat(path)
	if err != nil {
		return event
	}
	event["exists"] = true
	event["isDir"] = info.IsDir()
	event["size"] = info.Size()
	event["modTime"] = info.ModTime().Format(time.RFC3339Nano)
	return event
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
		if fn, err := vm.Get(hook); err == nil && fn.IsFunction() {
			defined = append(defined, hook)
		}
	}
	return defined, nil
}

// ensureLoaded returns the VM holding the current version of the script, reloading it
// if the file changed since it was last read. The caller must hold r.mu.
func (r *Runner) ensureLoaded() (*otto.Otto, error) {
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
func (r *Runner) evaluate(vm *otto.Otto, code string) (err error) {
	defer func() {
		caught := recover()
		if caught == nil {
			return
		}
		if _, ok := caught.(hookTimeout); !ok {
			panic(caught)
		}
		err = fmt.Errorf("loading %s: top level exceeded %s and was interrupted", r.scriptPath, r.timeout)
	}()

	stop := watchdog(vm, r.timeout)
	defer stop()

	if _, err := vm.Run(code); err != nil {
		return fmt.Errorf("loading %s: %w", r.scriptPath, err)
	}
	return nil
}

// watchdog arms vm.Interrupt so that work outlasting timeout is stopped. The returned
// function disarms it and must be called before the VM is used again.
func watchdog(vm *otto.Otto, timeout time.Duration) func() {
	interrupt := make(chan func(), 1)
	vm.Interrupt = interrupt
	timer := time.AfterFunc(timeout, func() {
		// Buffered and non-blocking, so the timer goroutine cannot outlive the call it
		// was watching.
		select {
		case interrupt <- func() { panic(hookTimeout{}) }:
		default:
		}
	})
	return func() {
		timer.Stop()
		vm.Interrupt = nil
	}
}

// discard forces the script to be reloaded on the next call. The caller must hold r.mu.
func (r *Runner) discard() {
	r.vm, r.loaded = nil, fileStamp{}
}
