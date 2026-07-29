package main

import (
	"context"
	"fmt"
	"os"
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
}

func defaultVMConfig() vmConfig {
	return vmConfig{
		execTimeout: defaultExecTimeout,
		httpTimeout: defaultHTTPTimeout,
		maxOutput:   defaultMaxOutput,
	}
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
		"kStringToFile": bind2Void(js.WriteFile),
		"kAppendFile":   bind2Void(js.AppendFile),
		"kRemoveFile":   bind1Void(js.RemoveFile),

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
	}
	for name, value := range bindings {
		// Set only fails on an invalid identifier, and every name here is a literal.
		_ = vm.Set(name, value)
	}
	return vm
}

func bindExec(cfg vmConfig) func(otto.FunctionCall) otto.Value {
	return func(call otto.FunctionCall) otto.Value {
		name := argString(call, 0, "kExec: command name")
		args := make([]string, 0, len(call.ArgumentList))
		for i := 1; i < len(call.ArgumentList); i++ {
			args = append(args, argString(call, i, fmt.Sprintf("kExec: argument %d", i)))
		}

		ctx, cancel := context.WithTimeout(context.Background(), cfg.execTimeout)
		defer cancel()

		result, err := js.Exec(ctx, cfg.maxOutput, name, args...)
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
