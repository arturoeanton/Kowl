package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// writeScript drops code into a temporary .js file and returns its path.
func writeScript(t *testing.T, code string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script.js")
	if err := os.WriteFile(path, []byte(code), 0o644); err != nil {
		t.Fatalf("writing script: %v", err)
	}
	return path
}

func TestRunInvokesHookWithNameAndOp(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.txt")
	script := writeScript(t, `
		function write(name, op, args) {
			kStringToFile(name + "|" + op, `+quote(out)+`)
		}`)

	if err := NewRunner(script).Run("WRITE", "/tmp/observed"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := readFile(t, out)
	if want := "/tmp/observed|WRITE"; got != want {
		t.Fatalf("hook received %q, want %q", got, want)
	}
}

func TestRunLowercasesOpToFindHook(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.txt")
	script := writeScript(t, `function not_found(name, op) { kStringToFile(op, `+quote(out)+`) }`)

	if err := NewRunner(script).Run("NOT_FOUND", "/tmp/gone"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := readFile(t, out); got != "NOT_FOUND" {
		t.Fatalf("not_found() got op %q, want %q", got, "NOT_FOUND")
	}
}

// A script that does not parse used to produce no output at all and no error, so a
// typo left Kowl running while doing nothing.
func TestRunReportsScriptThatDoesNotParse(t *testing.T) {
	script := writeScript(t, `function write( { this is not javascript }`)

	err := NewRunner(script).Run("WRITE", "/tmp/observed")
	if err == nil {
		t.Fatal("Run returned nil for a script that does not parse")
	}
	if errors.Is(err, ErrHookNotDefined) {
		t.Fatalf("parse failure reported as a missing hook: %v", err)
	}
	if !strings.Contains(err.Error(), script) {
		t.Fatalf("error %q does not name the offending script", err)
	}
}

// A hook that throws must surface the JavaScript error, not be swallowed.
func TestRunReportsHookFailure(t *testing.T) {
	script := writeScript(t, `function write(name, op) { throw new Error("boom") }`)

	err := NewRunner(script).Run("WRITE", "/tmp/observed")
	if err == nil {
		t.Fatal("Run returned nil for a hook that throws")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error %q does not carry the thrown message", err)
	}
}

func TestRunReportsUndefinedHookDistinctly(t *testing.T) {
	script := writeScript(t, `function ticker(name, op) {}`)

	err := NewRunner(script).Run("WRITE", "/tmp/observed")
	if !errors.Is(err, ErrHookNotDefined) {
		t.Fatalf("Run error = %v, want ErrHookNotDefined", err)
	}
}

// A hook name bound to something that is not callable is a missing hook, not a crash.
func TestRunTreatsNonFunctionHookAsUndefined(t *testing.T) {
	script := writeScript(t, `var write = 42;`)

	err := NewRunner(script).Run("WRITE", "/tmp/observed")
	if !errors.Is(err, ErrHookNotDefined) {
		t.Fatalf("Run error = %v, want ErrHookNotDefined", err)
	}
}

// An unreadable script used to call log.Fatalln from a goroutine and kill the daemon.
// Reaching the assertions below at all is the regression test.
func TestRunReportsUnreadableScriptInsteadOfExiting(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.js")

	err := NewRunner(missing).Run("WRITE", "/tmp/observed")
	if err == nil {
		t.Fatal("Run returned nil for a script that cannot be read")
	}
	if !strings.Contains(err.Error(), "reading script") {
		t.Fatalf("error %q does not explain the read failure", err)
	}
}

// The script is re-read per event, which is what makes edits take effect live.
func TestRunRereadsScriptBetweenEvents(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.txt")
	script := writeScript(t, `function write() { kStringToFile("first", `+quote(out)+`) }`)
	runner := NewRunner(script)

	if err := runner.Run("WRITE", "x"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if got := readFile(t, out); got != "first" {
		t.Fatalf("first run wrote %q, want %q", got, "first")
	}

	if err := os.WriteFile(script, []byte(`function write() { kStringToFile("second", `+quote(out)+`) }`), 0o644); err != nil {
		t.Fatalf("rewriting script: %v", err)
	}
	if err := runner.Run("WRITE", "x"); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if got := readFile(t, out); got != "second" {
		t.Fatalf("second run wrote %q, want %q", got, "second")
	}
}

// The watcher and the poller dispatch from separate goroutines. Enabling underscore
// from inside the VM setup raced on otto's unlocked registry; run this with -race.
func TestRunIsSafeForConcurrentUse(t *testing.T) {
	script := writeScript(t, `
		function write(name, op) {
			var doubled = _.map([1, 2, 3], function (n) { return n * 2 })
			if (doubled[2] !== 6) { throw new Error("underscore not available") }
		}`)
	runner := NewRunner(script)

	const goroutines = 32
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := runner.Run("WRITE", "/tmp/observed"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent Run: %v", err)
	}
}

func TestDefinedHooksListsOnlyImplementedHooks(t *testing.T) {
	script := writeScript(t, `
		function write(name, op) {}
		function ticker(name, op) {}
		function helper() {}`)

	hooks, err := NewRunner(script).DefinedHooks()
	if err != nil {
		t.Fatalf("DefinedHooks: %v", err)
	}
	if got := strings.Join(hooks, ","); got != "write,ticker" {
		t.Fatalf("DefinedHooks = %q, want %q", got, "write,ticker")
	}
}

func TestDefinedHooksFailsOnScriptThatDoesNotParse(t *testing.T) {
	script := writeScript(t, `function write( {`)

	if _, err := NewRunner(script).DefinedHooks(); err == nil {
		t.Fatal("DefinedHooks returned nil error for a script that does not parse")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// quote renders a path as a JavaScript string literal.
func quote(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}
