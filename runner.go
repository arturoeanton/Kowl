package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/robertkrimen/otto"
	"gopkg.in/h2non/gentleman.v2"
	"gopkg.in/h2non/gentleman.v2/plugins/body"

	"Kowl/js"
)

// ErrHookNotDefined is returned by Runner.Run when the script parses cleanly but does
// not implement a function for the dispatched operation. Scripts are only expected to
// implement the hooks they care about, so callers treat this as a normal outcome.
var ErrHookNotDefined = errors.New("hook not defined")

// hookNames lists every function name Kowl dispatches to, in the order they are
// reported at startup.
var hookNames = []string{"exist", "create", "write", "remove", "rename", "chmod", "ticker", "not_found"}

// Runner loads the user script into a fresh otto VM for every dispatched event. The
// script is re-read on each Run, so edits take effect without restarting Kowl.
type Runner struct {
	scriptPath string
}

// NewRunner returns a Runner bound to the JavaScript file at scriptPath.
func NewRunner(scriptPath string) *Runner {
	return &Runner{scriptPath: scriptPath}
}

// Run loads the script and invokes the hook matching op, lowercased: WRITE calls
// write(), NOT_FOUND calls not_found(), and so on. Every failure is returned rather
// than logged or fatal, so the caller decides what is worth reporting and Kowl keeps
// running when a single event goes wrong.
func (r *Runner) Run(op, name string) error {
	vm, err := r.load()
	if err != nil {
		return err
	}

	hook := strings.ToLower(op)
	fn, err := vm.Get(hook)
	if err != nil || !fn.IsFunction() {
		return fmt.Errorf("%s(): %w", hook, ErrHookNotDefined)
	}
	if _, err := fn.Call(otto.NullValue(), name, op, os.Args); err != nil {
		return fmt.Errorf("%s() failed: %w", hook, err)
	}
	return nil
}

// DefinedHooks reports which of hookNames the script implements. It doubles as the
// startup check: a script that does not parse is reported before Kowl starts watching,
// instead of failing silently on every event.
func (r *Runner) DefinedHooks() ([]string, error) {
	vm, err := r.load()
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

// load reads the script from disk and evaluates it in a new VM.
func (r *Runner) load() (*otto.Otto, error) {
	code, err := os.ReadFile(r.scriptPath)
	if err != nil {
		return nil, fmt.Errorf("reading script: %w", err)
	}
	vm := newVM()
	if _, err := vm.Run(string(code)); err != nil {
		return nil, fmt.Errorf("loading %s: %w", r.scriptPath, err)
	}
	return vm, nil
}

// newVM builds an otto VM with every k* binding installed. Each event gets its own VM,
// so nothing a hook stores in a global survives to the next event; use the process
// environment (kSetEnv/kGetEnv) for state that must persist.
func newVM() *otto.Otto {
	vm := otto.New()
	cli := gentleman.New()

	bindings := map[string]interface{}{
		"kExec": js.KExec,

		"kFileToString": js.KFileToString,
		"kStringToFile": js.KStringToFile,
		"kAppendFile":   js.KAppendFile,
		"kRemoveFile":   js.KRemoveFile,

		"kEncrypt": js.KEncrypt,
		"kDecrypt": js.KDecrypt,

		"kCli":        cli,
		"kBodyJSON":   body.JSON,
		"kBodyXML":    body.XML,
		"kBodyString": body.String,

		"kGetEnv":   os.Getenv,
		"kSetEnv":   os.Setenv,
		"kHostname": os.Hostname,
		"kGetpid":   os.Getpid,
		"kGetppid":  os.Getppid,
		"kGetgid":   os.Getgid,
		"kGetuid":   os.Getuid,
		"kGetegid":  os.Getegid,
		"kArgs":     os.Args,

		"kNow": time.Now,
	}
	for name, value := range bindings {
		// Set only fails on an invalid identifier, and every name here is a literal.
		_ = vm.Set(name, value)
	}
	return vm
}
