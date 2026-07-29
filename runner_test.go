package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

	if _, err := NewRunner(script).Run("WRITE", "/tmp/observed"); err != nil {
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

	if _, err := NewRunner(script).Run("NOT_FOUND", "/tmp/gone"); err != nil {
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

	_, err := NewRunner(script).Run("WRITE", "/tmp/observed")
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

	_, err := NewRunner(script).Run("WRITE", "/tmp/observed")
	if err == nil {
		t.Fatal("Run returned nil for a hook that throws")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error %q does not carry the thrown message", err)
	}
}

func TestRunReportsUndefinedHookDistinctly(t *testing.T) {
	script := writeScript(t, `function ticker(name, op) {}`)

	_, err := NewRunner(script).Run("WRITE", "/tmp/observed")
	if !errors.Is(err, ErrHookNotDefined) {
		t.Fatalf("Run error = %v, want ErrHookNotDefined", err)
	}
}

// A hook name bound to something that is not callable is a missing hook, not a crash.
func TestRunTreatsNonFunctionHookAsUndefined(t *testing.T) {
	script := writeScript(t, `var write = 42;`)

	_, err := NewRunner(script).Run("WRITE", "/tmp/observed")
	if !errors.Is(err, ErrHookNotDefined) {
		t.Fatalf("Run error = %v, want ErrHookNotDefined", err)
	}
}

// An unreadable script used to call log.Fatalln from a goroutine and kill the daemon.
// Reaching the assertions below at all is the regression test.
func TestRunReportsUnreadableScriptInsteadOfExiting(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.js")

	_, err := NewRunner(missing).Run("WRITE", "/tmp/observed")
	if err == nil {
		t.Fatal("Run returned nil for a script that cannot be read")
	}
	if !strings.Contains(err.Error(), "reading script") {
		t.Fatalf("error %q does not explain the read failure", err)
	}
}

// Editing the script must take effect without restarting Kowl.
func TestRunReloadsScriptAfterItChangesOnDisk(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.txt")
	script := writeScript(t, `function write() { kStringToFile("first", `+quote(out)+`) }`)
	runner := NewRunner(script)

	if _, err := runner.Run("WRITE", "x"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if got := readFile(t, out); got != "first" {
		t.Fatalf("first run wrote %q, want %q", got, "first")
	}

	rewriteScript(t, script, `function write() { kStringToFile("second", `+quote(out)+`) }`)
	if _, err := runner.Run("WRITE", "x"); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if got := readFile(t, out); got != "second" {
		t.Fatalf("second run wrote %q, want %q", got, "second")
	}
}

// The VM is kept between events, so a hook can hold state in an ordinary global
// instead of smuggling it through the process environment.
func TestRunKeepsGlobalsBetweenEvents(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.txt")
	script := writeScript(t, `
		var seen = 0
		function write() {
			seen = seen + 1
			kStringToFile(String(seen), `+quote(out)+`)
		}`)
	runner := NewRunner(script)

	for i := 0; i < 3; i++ {
		if _, err := runner.Run("WRITE", "x"); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
	}
	if got := readFile(t, out); got != "3" {
		t.Fatalf("counter reached %q after three events, want %q", got, "3")
	}
}

// Reloading resets the script's globals, which is the documented trade-off for
// picking up edits live.
func TestRunResetsGlobalsWhenScriptIsReloaded(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.txt")
	body := `
		var seen = 0
		function write() {
			seen = seen + 1
			kStringToFile(String(seen), ` + quote(out) + `)
		}`
	script := writeScript(t, body)
	runner := NewRunner(script)

	if _, err := runner.Run("WRITE", "x"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	rewriteScript(t, script, body+"\n// touched")
	if _, err := runner.Run("WRITE", "x"); err != nil {
		t.Fatalf("Run after reload: %v", err)
	}

	if got := readFile(t, out); got != "1" {
		t.Fatalf("counter is %q after a reload, want it reset to %q", got, "1")
	}
}

// A hook that never returns used to hold its goroutine forever.
func TestRunInterruptsHookThatExceedsTimeout(t *testing.T) {
	script := writeScript(t, `function write() { while (true) {} }`)
	runner := NewRunner(script)
	runner.timeout = 200 * time.Millisecond

	start := time.Now()
	_, err := runner.Run("WRITE", "x")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Run returned nil for a hook that never returns")
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("error %q does not explain that the hook was interrupted", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Run took %s, so the watchdog did not interrupt the hook", elapsed)
	}
}

// After an interrupt the VM is discarded, so the next event still works.
func TestRunRecoversAfterAnInterruptedHook(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.txt")
	script := writeScript(t, `
		function write() { while (true) {} }
		function ticker() { kStringToFile("alive", `+quote(out)+`) }`)
	runner := NewRunner(script)
	runner.timeout = 200 * time.Millisecond

	if _, err := runner.Run("WRITE", "x"); err == nil {
		t.Fatal("expected the runaway hook to be interrupted")
	}
	if _, err := runner.Run("TICKER", "x"); err != nil {
		t.Fatalf("Run after an interrupt: %v", err)
	}
	if got := readFile(t, out); got != "alive" {
		t.Fatalf("ticker wrote %q after an interrupt, want %q", got, "alive")
	}
}

// The watcher and the poller dispatch from separate goroutines. Enabling underscore
// while building each VM raced on otto's unlocked registry; run this with -race.
// Serialised hooks also mean a counter in a global lands on exactly one increment per
// event, with no lost updates.
func TestRunIsSafeForConcurrentUse(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.txt")
	script := writeScript(t, `
		var seen = 0
		function write(name, op) {
			var doubled = _.map([1, 2, 3], function (n) { return n * 2 })
			if (doubled[2] !== 6) { throw new Error("underscore not available") }
			seen = seen + 1
			kStringToFile(String(seen), `+quote(out)+`)
		}`)
	runner := NewRunner(script)

	const goroutines = 32
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := runner.Run("WRITE", "/tmp/observed"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent Run: %v", err)
	}
	if got := readFile(t, out); got != "32" {
		t.Fatalf("counter reached %q after %d events, want %q", got, goroutines, "32")
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

// rewriteScript replaces a script's contents and moves its modification time forward,
// so the change is visible regardless of filesystem timestamp resolution.
func rewriteScript(t *testing.T, path, code string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(code), 0o644); err != nil {
		t.Fatalf("rewriting script: %v", err)
	}
	later := time.Now().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatalf("touching script: %v", err)
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

// The third hook argument used to be os.Args, which told a script nothing about the
// event. It now describes the file the event is about.
func TestRunPassesAnEventDescribingTheFile(t *testing.T) {
	dir := t.TempDir()
	observed := filepath.Join(dir, "observed.txt")
	out := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(observed, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}

	script := writeScript(t, `
		function write(name, op, event) {
			kStringToFile([
				event.path, event.op, event.name, event.dir,
				event.exists, event.isDir, event.size,
				event.modTime === "" ? "no-time" : "has-time"
			].join("|"), `+quote(out)+`)
		}`)

	if _, err := NewRunner(script).Run("WRITE", observed); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := strings.Join([]string{
		observed, "WRITE", "observed.txt", dir, "true", "false", "5", "has-time",
	}, "|")
	if got := readFile(t, out); got != want {
		t.Fatalf("event = %q, want %q", got, want)
	}
}

// On a REMOVE the path is already gone, so exists says the other fields mean nothing.
func TestRunEventReportsAMissingPath(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone.txt")
	out := filepath.Join(dir, "out.txt")

	script := writeScript(t, `
		function remove(name, op, event) {
			kStringToFile(event.exists + "|" + event.size + "|" + event.name, `+quote(out)+`)
		}`)

	if _, err := NewRunner(script).Run("REMOVE", gone); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := readFile(t, out), "false|0|gone.txt"; got != want {
		t.Fatalf("event = %q, want %q", got, want)
	}
}

func TestRunEventReportsADirectory(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")

	script := writeScript(t, `
		function create(name, op, event) {
			kStringToFile(event.isDir + "|" + event.exists, `+quote(out)+`)
		}`)

	if _, err := NewRunner(script).Run("CREATE", dir); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := readFile(t, out), "true|true"; got != want {
		t.Fatalf("event = %q, want %q", got, want)
	}
}

// Statements outside a function are code too. A loop among them used to hang Kowl
// forever with nothing reported: --hook-timeout only covered the call into a hook.
func TestRunInterruptsARunawayScriptTopLevel(t *testing.T) {
	script := writeScript(t, `
		while (true) {}
		function write(name, op) {}`)
	runner := NewRunner(script)
	runner.timeout = 200 * time.Millisecond

	start := time.Now()
	_, err := runner.Run("WRITE", "x")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Run returned nil for a script whose top level never finishes")
	}
	if !strings.Contains(err.Error(), "top level") {
		t.Fatalf("error %q does not say the top level was the problem", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Run took %s, so the watchdog did not interrupt the load", elapsed)
	}
}

// The same load happens during the startup check, which runs before anything is logged.
func TestDefinedHooksInterruptsARunawayScriptTopLevel(t *testing.T) {
	script := writeScript(t, `while (true) {}`)
	runner := NewRunner(script)
	runner.timeout = 200 * time.Millisecond

	start := time.Now()
	_, err := runner.DefinedHooks()

	if err == nil {
		t.Fatal("DefinedHooks returned nil for a script whose top level never finishes")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("DefinedHooks took %s, so the watchdog did not interrupt the load", elapsed)
	}
}

// An interrupted load must not leave a half-built VM behind for the next event.
func TestRunRecoversAfterAnInterruptedLoad(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.txt")
	script := writeScript(t, `
		while (true) {}
		function write(name, op) {}`)
	runner := NewRunner(script)
	runner.timeout = 200 * time.Millisecond

	if _, err := runner.Run("WRITE", "x"); err == nil {
		t.Fatal("expected the runaway load to be interrupted")
	}

	rewriteScript(t, script, `function write(name, op) { kStringToFile("alive", `+quote(out)+`) }`)
	if _, err := runner.Run("WRITE", "x"); err != nil {
		t.Fatalf("Run after an interrupted load: %v", err)
	}
	if got := readFile(t, out); got != "alive" {
		t.Fatalf("hook wrote %q after an interrupted load, want %q", got, "alive")
	}
}

// A slow but finite top level must not be cut short.
func TestRunAllowsATopLevelThatFinishesInTime(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.txt")
	script := writeScript(t, `
		var total = 0
		for (var i = 0; i < 20000; i++) { total = total + i }
		function write(name, op) { kStringToFile(String(total), `+quote(out)+`) }`)
	runner := NewRunner(script)
	runner.timeout = 10 * time.Second

	if _, err := runner.Run("WRITE", "x"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := readFile(t, out); got != "199990000" {
		t.Fatalf("top level computed %q, want %q", got, "199990000")
	}
}

// Kowl tells a hook's own writes apart from real changes by recording what the helpers
// touched, which is exact where comparing timestamps only guesses.
func TestRunReportsThePathsAHookWroteThroughHelpers(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	appended := filepath.Join(dir, "appended.txt")

	script := writeScript(t, `
		function write(name, op) {
			kStringToFile("x", `+quote(target)+`);
			kAppendFile("y", `+quote(appended)+`);
			kFileToString(`+quote(target)+`);
		}`)

	written, err := NewRunner(script).Run("WRITE", "/tmp/observed")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := strings.Join(written, ","), target+","+appended; got != want {
		t.Fatalf("written = %q, want %q", got, want)
	}
}

// Reading a file is not writing it.
func TestRunReportsNoWritesForAReadOnlyHook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := writeScript(t, `function write(name, op) { kFileToString(`+quote(path)+`) }`)

	written, err := NewRunner(script).Run("WRITE", "/tmp/observed")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(written) != 0 {
		t.Fatalf("written = %v for a hook that only read", written)
	}
}

// A hook that wrote and then threw still wrote.
func TestRunReportsWritesMadeBeforeAHookFailed(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target.txt")
	script := writeScript(t, `
		function write(name, op) {
			kStringToFile("x", `+quote(target)+`);
			throw new Error("boom");
		}`)

	written, err := NewRunner(script).Run("WRITE", "/tmp/observed")
	if err == nil {
		t.Fatal("expected the hook to fail")
	}
	if len(written) != 1 || written[0] != target {
		t.Fatalf("written = %v, want %v", written, []string{target})
	}
}

// One hook's writes must not be attributed to the next.
func TestRunDoesNotCarryWritesBetweenEvents(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target.txt")
	script := writeScript(t, `
		function write(name, op) { kStringToFile("x", `+quote(target)+`) }
		function ticker(name, op) {}`)
	runner := NewRunner(script)

	if _, err := runner.Run("WRITE", "/tmp/observed"); err != nil {
		t.Fatal(err)
	}
	written, err := runner.Run("TICKER", "/tmp/observed")
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Fatalf("the ticker hook was credited with %v", written)
	}
}

// Copy and move touch two paths, and either may be watched.
func TestRunReportsBothSidesOfACopyAndMove(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	copied := filepath.Join(dir, "copied.txt")
	if err := os.WriteFile(source, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := writeScript(t, `
		function write(name, op) { kCopyFile(`+quote(source)+`, `+quote(copied)+`) }`)

	written, err := NewRunner(script).Run("WRITE", "/tmp/observed")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := strings.Join(written, ","), source+","+copied; got != want {
		t.Fatalf("written = %q, want both sides %q", got, want)
	}
}

// Kowl reloads on its own when the file changes. Reload covers what that leaves out: a
// script whose behaviour depends on something other than its own bytes.
func TestReloadRereadsAnUnchangedScript(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.txt")
	key := "KOWL_TEST_RELOAD"
	t.Setenv(key, "first")

	script := writeScript(t, `
		var captured = kGetEnv("`+key+`")
		function write(name, op) { kStringToFile(captured, `+quote(out)+`) }`)
	runner := NewRunner(script)

	if _, err := runner.Run("WRITE", "x"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, out); got != "first" {
		t.Fatalf("hook wrote %q, want %q", got, "first")
	}

	// The file is untouched, so nothing about it tells Kowl to reload.
	t.Setenv(key, "second")
	if _, err := runner.Run("WRITE", "x"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, out); got != "first" {
		t.Fatalf("hook wrote %q without a reload, want the captured %q", got, "first")
	}

	hooks, err := runner.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if strings.Join(hooks, ",") != "write" {
		t.Fatalf("Reload reported hooks %v, want write", hooks)
	}
	if _, err := runner.Run("WRITE", "x"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, out); got != "second" {
		t.Fatalf("hook wrote %q after a reload, want %q", got, "second")
	}
}

func TestReloadReportsAScriptThatNoLongerParses(t *testing.T) {
	script := writeScript(t, `function write(name, op) {}`)
	runner := NewRunner(script)
	if _, err := runner.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	rewriteScript(t, script, `function write( {`)

	if _, err := runner.Reload(); err == nil {
		t.Fatal("Reload returned nil for a script that no longer parses")
	}
}
