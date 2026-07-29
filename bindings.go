package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/dop251/goja"
	"gopkg.in/h2non/gentleman.v2"
	"gopkg.in/h2non/gentleman.v2/plugins/body"
	"gopkg.in/h2non/gentleman.v2/plugins/timeout"

	"Kowl/js"
)

// underscoreSource is evaluated in every VM so scripts can use _. goja has no importable
// underscore the way otto did, so the file is vendored under vendorjs.
//
//go:embed vendorjs/underscore-min.js
var underscoreSource string

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
	// hookTimeout bounds a whole hook, and so bounds how long kSleep may ask for.
	hookTimeout time.Duration
}

func defaultVMConfig() vmConfig {
	return vmConfig{
		execTimeout: defaultExecTimeout,
		httpTimeout: defaultHTTPTimeout,
		maxOutput:   defaultMaxOutput,
		logger:      NewLogger(os.Stderr, LevelInfo, FormatText),
		writes:      &writeLog{},
		hookTimeout: defaultHookTimeout,
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

// fieldNames presents Go struct fields to JavaScript in lower camel case, so a Go struct
// reads as {path, name, dir, ...}. Method names are left alone: the HTTP client is a Go
// object exposed directly, and uncapitalising its methods would turn URL into uRL.
type fieldNames struct{}

func (fieldNames) FieldName(_ reflect.Type, f reflect.StructField) string {
	if f.Name == "" {
		return f.Name
	}
	runes := []rune(f.Name)
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
}

func (fieldNames) MethodName(_ reflect.Type, m reflect.Method) string { return m.Name }

// newVM builds a goja runtime with every k* binding installed.
//
// A Go function returning (T, error) reaches JavaScript as one that returns T and throws
// when the error is non-nil, so a hook that ignores an error stops instead of carrying on
// with bad data. Most helpers need no wrapper at all because of that.
func newVM(cfg vmConfig) *goja.Runtime {
	vm := goja.New()
	vm.SetFieldNameMapper(fieldNames{})

	// Underscore predates the language features that replaced it, but scripts still use
	// it and it costs about a millisecond per VM.
	if _, err := vm.RunString(underscoreSource); err != nil {
		cfg.logger.Errorf("underscore could not be loaded, _ will be undefined: %v", err)
	}

	cli := gentleman.New()
	cli.Use(timeout.Request(cfg.httpTimeout))

	bindings := map[string]interface{}{
		"kExec": bindExec(cfg),

		"kFileToString": js.ReadFile,
		"kStringToFile": recordSecond(cfg.writes, js.WriteFile),
		"kAppendFile":   recordSecond(cfg.writes, js.AppendFile),
		"kRemoveFile":   recordOnly(cfg.writes, js.RemoveFile),

		"kFileExists": js.Exists,
		"kStat":       bindStat,
		"kListDir":    bindListDir,
		"kGlob":       js.Glob,
		"kMkdirAll":   recordOnly(cfg.writes, js.MkdirAll),
		"kRemoveAll":  recordOnly(cfg.writes, js.RemoveAll),
		"kCopyFile":   recordBoth(cfg.writes, js.CopyFile),
		"kMoveFile":   recordBoth(cfg.writes, js.MoveFile),

		"kEncrypt": js.Encrypt,
		"kDecrypt": js.Decrypt,

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

		"kNow":   time.Now,
		"kSleep": bindSleep(cfg),

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

// recordOnly, recordSecond and recordBoth note which paths a helper touched before
// letting it run. They stay (T, error) shaped so goja still turns a failure into a
// thrown exception.

func recordOnly(writes *writeLog, fn func(string) error) func(string) error {
	return func(path string) error {
		writes.add(path)
		return fn(path)
	}
}

// recordSecond is for the helpers that take (value, path).
func recordSecond(writes *writeLog, fn func(string, string) error) func(string, string) error {
	return func(value, path string) error {
		writes.add(path)
		return fn(value, path)
	}
}

// recordBoth records the source and the destination, since either may be watched.
func recordBoth(writes *writeLog, fn func(string, string) error) func(string, string) error {
	return func(source, destination string) error {
		writes.add(source)
		writes.add(destination)
		return fn(source, destination)
	}
}

// statView, dirEntryView and execView are what the helpers hand to a script. They exist
// so the field names are the ones a script should see, and so a timestamp arrives as a
// string rather than as a wrapped Go time.
type statView struct {
	Path    string
	Name    string
	Dir     string
	Size    int64
	Mode    string
	ModTime string
	IsDir   bool
}

type dirEntryView struct {
	Name  string
	Path  string
	Size  int64
	IsDir bool
}

type execView struct {
	Stdout    string
	Stderr    string
	Code      int
	Truncated bool
}

func bindStat(path string) (statView, error) {
	stat, err := js.Stat(path)
	if err != nil {
		return statView{}, err
	}
	return statView{
		Path:    stat.Path,
		Name:    stat.Name,
		Dir:     stat.Dir,
		Size:    stat.Size,
		Mode:    stat.Mode,
		ModTime: stat.ModTime.Format(time.RFC3339Nano),
		IsDir:   stat.IsDir,
	}, nil
}

func bindListDir(path string) ([]dirEntryView, error) {
	entries, err := js.ListDir(path)
	if err != nil {
		return nil, err
	}
	listed := make([]dirEntryView, 0, len(entries))
	for _, entry := range entries {
		listed = append(listed, dirEntryView{
			Name:  entry.Name,
			Path:  entry.Path,
			Size:  entry.Size,
			IsDir: entry.IsDir,
		})
	}
	return listed, nil
}

func bindExec(cfg vmConfig) func(goja.FunctionCall, *goja.Runtime) goja.Value {
	return func(call goja.FunctionCall, vm *goja.Runtime) goja.Value {
		name := argString(vm, call, 0, "kExec: command name")

		// A trailing object is options rather than another argument. Strings are
		// primitives in JavaScript, so this cannot swallow a real argument.
		last := len(call.Arguments) - 1
		opts := js.ExecOptions{Limit: cfg.maxOutput}
		if last > 0 && isObject(call.Argument(last)) {
			opts = execOptions(vm, call.Argument(last), cfg.maxOutput)
			last--
		}

		args := make([]string, 0, len(call.Arguments))
		for i := 1; i <= last; i++ {
			args = append(args, argString(vm, call, i, fmt.Sprintf("kExec: argument %d", i)))
		}

		ctx, cancel := context.WithTimeout(context.Background(), cfg.execTimeout)
		defer cancel()

		result, err := js.Exec(ctx, opts, name, args...)
		if err != nil {
			throw(vm, err)
		}
		return vm.ToValue(execView{
			Stdout:    result.Stdout,
			Stderr:    result.Stderr,
			Code:      result.ExitCode,
			Truncated: result.Truncated,
		})
	}
}

// execOptions reads the optional trailing object of kExec.
func execOptions(vm *goja.Runtime, value goja.Value, limit int) js.ExecOptions {
	opts := js.ExecOptions{Limit: limit}

	fields, ok := value.Export().(map[string]interface{})
	if !ok {
		throwf(vm, "kExec: options must be an object, got %T", value.Export())
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
				throwf(vm, "kExec: env must be an object, got %T", field)
			}
			opts.Env = make(map[string]string, len(environment))
			for name, value := range environment {
				opts.Env[name] = fmt.Sprintf("%v", value)
			}
		default:
			throwf(vm, "kExec: unknown option %q, want dir, env or stdin", key)
		}
	}
	return opts
}

// bindSleep waits, for a duration string like "250ms" or a number of milliseconds.
//
// The wait counts against --hook-timeout and cannot exceed it. Asking for longer is
// rejected outright rather than quietly shortened: a hook interrupted part way through
// is worse than one that never started.
func bindSleep(cfg vmConfig) func(goja.FunctionCall, *goja.Runtime) goja.Value {
	return func(call goja.FunctionCall, vm *goja.Runtime) goja.Value {
		arg := call.Argument(0)
		if goja.IsUndefined(arg) || goja.IsNull(arg) {
			throwf(vm, "kSleep: a duration is required")
		}

		var wait time.Duration
		switch value := arg.Export().(type) {
		case int64:
			wait = time.Duration(value) * time.Millisecond
		case float64:
			wait = time.Duration(value * float64(time.Millisecond))
		default:
			parsed, err := time.ParseDuration(arg.String())
			if err != nil {
				throwf(vm, "kSleep: %v", err)
			}
			wait = parsed
		}

		switch {
		case wait < 0:
			throwf(vm, "kSleep: %s is negative", wait)
		case wait > cfg.hookTimeout:
			throwf(vm, "kSleep: %s is longer than the hook timeout of %s", wait, cfg.hookTimeout)
		}

		time.Sleep(wait)
		return goja.Undefined()
	}
}

// bindConsole routes console.log and friends through Kowl's logger, so script output is
// timestamped, leveled and filtered by --log-level like everything else.
func bindConsole(vm *goja.Runtime, logger *Logger) {
	console := vm.NewObject()
	for name, write := range map[string]func(string, ...interface{}){
		"debug": logger.Debugf,
		"log":   logger.Infof,
		"info":  logger.Infof,
		"warn":  logger.Warnf,
		"error": logger.Errorf,
	} {
		_ = console.Set(name, bindLog(write))
	}
	_ = vm.Set("console", console)
}

// bindLog adapts a logger method into a JavaScript function that joins its arguments
// with spaces, the way console.log does.
func bindLog(write func(string, ...interface{})) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		parts := make([]string, 0, len(call.Arguments))
		for _, arg := range call.Arguments {
			parts = append(parts, describe(arg))
		}
		// The message is already assembled, so it must not be treated as a format.
		write("%s", strings.Join(parts, " "))
		return goja.Undefined()
	}
}

// describe renders one logged value. Objects and arrays print as their contents rather
// than as "[object Object]".
func describe(value goja.Value) string {
	if isObject(value) {
		return fmt.Sprintf("%v", value.Export())
	}
	return value.String()
}

// isObject reports whether a value is an object or an array rather than a primitive.
func isObject(value goja.Value) bool {
	if goja.IsUndefined(value) || goja.IsNull(value) {
		return false
	}
	_, ok := value.(*goja.Object)
	return ok
}

// throw aborts the running hook with a JavaScript exception the script can catch.
func throw(vm *goja.Runtime, err error) {
	panic(vm.NewGoError(err))
}

func throwf(vm *goja.Runtime, format string, args ...interface{}) {
	panic(vm.NewGoError(fmt.Errorf(format, args...)))
}

// argString reads a required string argument, throwing if it is missing.
func argString(vm *goja.Runtime, call goja.FunctionCall, index int, description string) string {
	arg := call.Argument(index)
	if goja.IsUndefined(arg) || goja.IsNull(arg) {
		throwf(vm, "%s is required", description)
	}
	return arg.String()
}
