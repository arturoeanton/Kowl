package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/robertkrimen/otto"
	"gopkg.in/h2non/gentleman.v2"
	"gopkg.in/h2non/gentleman.v2/plugins/body"
	"gopkg.in/h2non/gentleman.v2/plugins/timeout"

	"Kowl/js"
)

// Defaults for the limits a hook runs under.
const (
	defaultHookTimeout = 30 * time.Second
	defaultExecTimeout = 60 * time.Second
	defaultHTTPTimeout = 30 * time.Second
	defaultMaxOutput   = 1 << 20 // 1 MiB of stdout and of stderr per command
)

// vmConfig bounds what a hook is allowed to do, so a single misbehaving script cannot
// hang the watcher or exhaust memory.
type vmConfig struct {
	execTimeout time.Duration
	httpTimeout time.Duration
	maxOutput   int
	// logger receives everything a script logs. Script output goes through the same
	// leveled, timestamped stream as Kowl's own, so --log-level governs both.
	logger *Logger
	// writes collects the paths a hook changes through the helpers below, which is how
	// Kowl knows an event is one a hook caused rather than a real change.
	writes *writeLog
}

func defaultVMConfig() vmConfig {
	return vmConfig{
		execTimeout: defaultExecTimeout,
		httpTimeout: defaultHTTPTimeout,
		maxOutput:   defaultMaxOutput,
		logger:      NewLogger(os.Stderr, LevelInfo, FormatText),
		writes:      &writeLog{},
	}
}

// writeLog collects the paths a hook changed through Kowl's own helpers. Guessing from
// timestamps cannot tell a hook's write apart from someone else's; this can.
type writeLog struct {
	mu    sync.Mutex
	paths []string
}

func (w *writeLog) add(path string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.paths = append(w.paths, path)
}

// take returns everything recorded since the last call and starts over.
func (w *writeLog) take() []string {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	paths := w.paths
	w.paths = nil
	return paths
}

// newVM builds an otto VM with every k* binding installed.
//
// Helpers report failure by throwing a JavaScript exception rather than by returning a
// status code, so a hook that ignores errors fails loudly instead of carrying on with
// an error string where its data should be.
func newVM(cfg vmConfig) *otto.Otto {
	vm := otto.New()

	cli := gentleman.New()
	cli.Use(timeout.Request(cfg.httpTimeout))

	bindings := map[string]interface{}{
		"kExec": bindExec(cfg),

		"kFileToString": bind1(js.ReadFile),
		"kStringToFile": recording2(cfg.writes, js.WriteFile),
		"kAppendFile":   recording2(cfg.writes, js.AppendFile),
		"kRemoveFile":   recording1(cfg.writes, js.RemoveFile),

		"kFileExists": js.Exists,
		"kStat":       bindStat,
		"kListDir":    bindListDir,
		"kGlob":       bind1(js.Glob),
		"kMkdirAll":   recording1(cfg.writes, js.MkdirAll),
		"kRemoveAll":  recording1(cfg.writes, js.RemoveAll),
		"kCopyFile":   recordingBoth(cfg.writes, js.CopyFile),
		"kMoveFile":   recordingBoth(cfg.writes, js.MoveFile),

		"kEncrypt": bind2(js.Encrypt),
		"kDecrypt": bind2(js.Decrypt),

		"kCli":        cli,
		"kBodyJSON":   body.JSON,
		"kBodyXML":    body.XML,
		"kBodyString": body.String,

		"kGetEnv":   os.Getenv,
		"kSetEnv":   bind2Void(func(key, value string) error { return os.Setenv(key, value) }),
		"kHostname": bind0(os.Hostname),
		"kGetpid":   os.Getpid,
		"kGetppid":  os.Getppid,
		"kGetgid":   os.Getgid,
		"kGetuid":   os.Getuid,
		"kGetegid":  os.Getegid,
		"kArgs":     os.Args,

		"kNow": time.Now,

		"kDebug": bindLog(cfg.logger.Debugf),
		"kLog":   bindLog(cfg.logger.Infof),
		"kWarn":  bindLog(cfg.logger.Warnf),
		"kError": bindLog(cfg.logger.Errorf),
	}
	for name, value := range bindings {
		// Set only fails on an invalid identifier, and every name here is a literal.
		_ = vm.Set(name, value)
	}
	bindConsole(vm, cfg.logger)
	return vm
}

// bindConsole routes console.log and friends through Kowl's logger. otto's own console
// writes straight to stdout, which bypasses --log-level and leaves script output
// untimestamped and interleaved with Kowl's on a different stream.
func bindConsole(vm *otto.Otto, logger *Logger) {
	console, err := vm.Object(`console = {}`)
	if err != nil {
		return
	}
	for name, write := range map[string]func(string, ...interface{}){
		"debug": logger.Debugf,
		"log":   logger.Infof,
		"info":  logger.Infof,
		"warn":  logger.Warnf,
		"error": logger.Errorf,
	} {
		_ = console.Set(name, bindLog(write))
	}
}

// bindLog adapts a logger method into a JavaScript function that joins its arguments
// with spaces, the way console.log does.
func bindLog(write func(string, ...interface{})) func(otto.FunctionCall) otto.Value {
	return func(call otto.FunctionCall) otto.Value {
		parts := make([]string, 0, len(call.ArgumentList))
		for _, arg := range call.ArgumentList {
			parts = append(parts, describe(arg))
		}
		// The message is already assembled, so it must not be treated as a format.
		write("%s", strings.Join(parts, " "))
		return otto.UndefinedValue()
	}
}

// describe renders one logged value. Objects and arrays are exported so they print as
// their contents rather than as "[object Object]".
func describe(value otto.Value) string {
	if value.IsObject() {
		if exported, err := value.Export(); err == nil {
			return fmt.Sprintf("%v", exported)
		}
	}
	return value.String()
}

// execOptions reads the optional trailing object of kExec.
func execOptions(call otto.FunctionCall, value otto.Value, limit int) js.ExecOptions {
	opts := js.ExecOptions{Limit: limit}

	exported, err := value.Export()
	if err != nil {
		throwf(call.Otto, "kExec: options must be an object: %v", err)
	}
	fields, ok := exported.(map[string]interface{})
	if !ok {
		throwf(call.Otto, "kExec: options must be an object, got %T", exported)
	}

	for key, field := range fields {
		switch key {
		case "dir":
			opts.Dir = fmt.Sprintf("%v", field)
		case "stdin":
			opts.Stdin = fmt.Sprintf("%v", field)
		case "env":
			environment, ok := field.(map[string]interface{})
			if !ok {
				throwf(call.Otto, "kExec: env must be an object, got %T", field)
			}
			opts.Env = make(map[string]string, len(environment))
			for name, value := range environment {
				opts.Env[name] = fmt.Sprintf("%v", value)
			}
		default:
			throwf(call.Otto, "kExec: unknown option %q, want dir, env or stdin", key)
		}
	}
	return opts
}

// bindStat and bindListDir convert their results by hand, so scripts see plain objects
// with lowercase keys rather than wrapped Go values.

func bindStat(call otto.FunctionCall) otto.Value {
	stat, err := js.Stat(argString(call, 0, "kStat: path"))
	if err != nil {
		throwf(call.Otto, "%v", err)
	}
	return toValue(call.Otto, map[string]interface{}{
		"path":    stat.Path,
		"name":    stat.Name,
		"dir":     stat.Dir,
		"size":    stat.Size,
		"mode":    stat.Mode,
		"modTime": stat.ModTime.Format(time.RFC3339Nano),
		"isDir":   stat.IsDir,
	})
}

func bindListDir(call otto.FunctionCall) otto.Value {
	entries, err := js.ListDir(argString(call, 0, "kListDir: path"))
	if err != nil {
		throwf(call.Otto, "%v", err)
	}
	listed := make([]map[string]interface{}, 0, len(entries))
	for _, entry := range entries {
		listed = append(listed, map[string]interface{}{
			"name":  entry.Name,
			"path":  entry.Path,
			"size":  entry.Size,
			"isDir": entry.IsDir,
		})
	}
	return toValue(call.Otto, listed)
}

func bindExec(cfg vmConfig) func(otto.FunctionCall) otto.Value {
	return func(call otto.FunctionCall) otto.Value {
		name := argString(call, 0, "kExec: command name")

		// A trailing object is options rather than another argument. Strings are
		// primitives in JavaScript, so this cannot swallow a real argument.
		last := len(call.ArgumentList) - 1
		opts := js.ExecOptions{Limit: cfg.maxOutput}
		if last > 0 && call.Argument(last).IsObject() {
			opts = execOptions(call, call.Argument(last), cfg.maxOutput)
			last--
		}

		args := make([]string, 0, len(call.ArgumentList))
		for i := 1; i <= last; i++ {
			args = append(args, argString(call, i, fmt.Sprintf("kExec: argument %d", i)))
		}

		ctx, cancel := context.WithTimeout(context.Background(), cfg.execTimeout)
		defer cancel()

		result, err := js.Exec(ctx, opts, name, args...)
		if err != nil {
			throwf(call.Otto, "%v", err)
		}
		return toValue(call.Otto, map[string]interface{}{
			"stdout":    result.Stdout,
			"stderr":    result.Stderr,
			"code":      result.ExitCode,
			"truncated": result.Truncated,
		})
	}
}

// bind0, bind1 and bind2 adapt Go helpers of the shape func(...) (T, error) into
// JavaScript functions that return T and throw on error. The Void variants adapt
// helpers that only return an error.

func bind0[T any](fn func() (T, error)) func(otto.FunctionCall) otto.Value {
	return func(call otto.FunctionCall) otto.Value {
		result, err := fn()
		if err != nil {
			throwf(call.Otto, "%v", err)
		}
		return toValue(call.Otto, result)
	}
}

func bind1[T any](fn func(string) (T, error)) func(otto.FunctionCall) otto.Value {
	return func(call otto.FunctionCall) otto.Value {
		result, err := fn(argString(call, 0, "argument 1"))
		if err != nil {
			throwf(call.Otto, "%v", err)
		}
		return toValue(call.Otto, result)
	}
}

func bind2[T any](fn func(string, string) (T, error)) func(otto.FunctionCall) otto.Value {
	return func(call otto.FunctionCall) otto.Value {
		result, err := fn(argString(call, 0, "argument 1"), argString(call, 1, "argument 2"))
		if err != nil {
			throwf(call.Otto, "%v", err)
		}
		return toValue(call.Otto, result)
	}
}

func bind1Void(fn func(string) error) func(otto.FunctionCall) otto.Value {
	return func(call otto.FunctionCall) otto.Value {
		if err := fn(argString(call, 0, "argument 1")); err != nil {
			throwf(call.Otto, "%v", err)
		}
		return otto.UndefinedValue()
	}
}

func bind2Void(fn func(string, string) error) func(otto.FunctionCall) otto.Value {
	return func(call otto.FunctionCall) otto.Value {
		if err := fn(argString(call, 0, "argument 1"), argString(call, 1, "argument 2")); err != nil {
			throwf(call.Otto, "%v", err)
		}
		return otto.UndefinedValue()
	}
}

// recording1, recording2 and recordingBoth wrap the helpers that change the filesystem,
// noting which paths they touched. recording2 takes (value, path); recordingBoth records
// both the source and the destination, since either may be watched.

func recording1(writes *writeLog, fn func(string) error) func(otto.FunctionCall) otto.Value {
	return func(call otto.FunctionCall) otto.Value {
		path := argString(call, 0, "argument 1")
		writes.add(path)
		if err := fn(path); err != nil {
			throwf(call.Otto, "%v", err)
		}
		return otto.UndefinedValue()
	}
}

func recording2(writes *writeLog, fn func(string, string) error) func(otto.FunctionCall) otto.Value {
	return func(call otto.FunctionCall) otto.Value {
		value := argString(call, 0, "argument 1")
		path := argString(call, 1, "argument 2")
		writes.add(path)
		if err := fn(value, path); err != nil {
			throwf(call.Otto, "%v", err)
		}
		return otto.UndefinedValue()
	}
}

func recordingBoth(writes *writeLog, fn func(string, string) error) func(otto.FunctionCall) otto.Value {
	return func(call otto.FunctionCall) otto.Value {
		source := argString(call, 0, "argument 1")
		destination := argString(call, 1, "argument 2")
		writes.add(source)
		writes.add(destination)
		if err := fn(source, destination); err != nil {
			throwf(call.Otto, "%v", err)
		}
		return otto.UndefinedValue()
	}
}

// throwf aborts the running hook with a JavaScript exception the script can catch.
func throwf(vm *otto.Otto, format string, args ...interface{}) {
	panic(vm.MakeCustomError("KowlError", fmt.Sprintf(format, args...)))
}

// argString reads a required string argument, throwing if it is missing.
func argString(call otto.FunctionCall, index int, description string) string {
	arg := call.Argument(index)
	if !arg.IsDefined() {
		throwf(call.Otto, "%s is required", description)
	}
	value, err := arg.ToString()
	if err != nil {
		throwf(call.Otto, "%s must be a string: %v", description, err)
	}
	return value
}

func toValue(vm *otto.Otto, from interface{}) otto.Value {
	value, err := vm.ToValue(from)
	if err != nil {
		throwf(vm, "converting result: %v", err)
	}
	return value
}
