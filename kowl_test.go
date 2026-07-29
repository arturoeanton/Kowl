package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// invoke runs the CLI with args and returns its exit code plus both streams.
func invoke(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr safeBuffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// -h is a successful invocation. It used to exit 255, because os.Exit(-1) is truncated
// to 255 and go-flags reports help as an error.
func TestHelpExitsSuccessfully(t *testing.T) {
	code, stdout, stderr := invoke(t, "-h")

	if code != exitOK {
		t.Fatalf("-h exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	for _, flag := range []string{"--filename", "--javascript", "--interval", "--flagNotWatcher"} {
		if !strings.Contains(stdout, flag) {
			t.Fatalf("help output is missing %s:\n%s", flag, stdout)
		}
	}
}

// -w disables the watcher and -m 0 disables polling, so together nothing observes
// anything. That combination used to reach a channel receive with no senders and crash
// with "all goroutines are asleep - deadlock!".
func TestWatcherAndPollingBothDisabledIsUsageError(t *testing.T) {
	code, _, stderr := invoke(t, "-f", "/tmp/observed", "-j", "example.js", "-m", "0", "-w")

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "nothing to do") {
		t.Fatalf("stderr %q does not explain why the combination is rejected", stderr)
	}
}

func TestNegativeIntervalIsUsageError(t *testing.T) {
	code, _, stderr := invoke(t, "-f", "/tmp/observed", "-j", "example.js", "-m", "-5s")

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "-m") {
		t.Fatalf("stderr %q does not mention the offending flag", stderr)
	}
}

func TestMissingRequiredFlagsIsUsageError(t *testing.T) {
	code, _, stderr := invoke(t, "-f", "/tmp/observed")

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "javascript") {
		t.Fatalf("stderr %q does not name the missing flag", stderr)
	}
}

func TestUnknownFlagIsUsageError(t *testing.T) {
	code, _, stderr := invoke(t, "-f", "/tmp/observed", "-j", "example.js", "--invented")

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "invented") {
		t.Fatalf("stderr %q does not name the unknown flag", stderr)
	}
}

// A broken script is reported before Kowl starts watching, instead of silently doing
// nothing on every single event.
func TestScriptThatDoesNotParseFailsAtStartup(t *testing.T) {
	script := writeScript(t, `function write( { this is not javascript }`)

	code, _, stderr := invoke(t, "-f", "/tmp/observed", "-j", script)

	if code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	if !strings.Contains(stderr, script) {
		t.Fatalf("stderr %q does not name the offending script", stderr)
	}
}

func TestUnreadableScriptFailsAtStartup(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.js")

	code, _, stderr := invoke(t, "-f", "/tmp/observed", "-j", missing)

	if code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	if !strings.Contains(stderr, "reading script") {
		t.Fatalf("stderr %q does not explain the read failure", stderr)
	}
}

// A script with no recognised hook can never do anything, so say so rather than idle.
func TestScriptWithoutKnownHooksFailsAtStartup(t *testing.T) {
	script := writeScript(t, `function helper() { return 1 }`)

	code, _, stderr := invoke(t, "-f", "/tmp/observed", "-j", script)

	if code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	if !strings.Contains(stderr, "none of the known hooks") {
		t.Fatalf("stderr %q does not explain that no hooks are defined", stderr)
	}
}

func TestUnknownLogLevelIsUsageError(t *testing.T) {
	code, _, stderr := invoke(t, "-f", "/tmp/observed", "-j", "example.js", "--log-level", "verbose")

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "verbose") {
		t.Fatalf("stderr %q does not name the rejected level", stderr)
	}
}

func TestBrokenGlobIsUsageError(t *testing.T) {
	code, _, stderr := invoke(t, "-f", "[unterminated", "-j", "example.js")

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "unterminated") {
		t.Fatalf("stderr %q does not name the bad pattern", stderr)
	}
}

// The limits are what stop one bad hook from hanging the watcher, so a value that
// disables them is a mistake rather than a shortcut.
func TestNonPositiveLimitsAreUsageErrors(t *testing.T) {
	tests := []struct{ flag, value string }{
		{"--hook-timeout", "0s"},
		{"--exec-timeout", "0s"},
		{"--http-timeout", "-1s"},
		{"--max-output", "0"},
		{"--debounce", "-1s"},
	}
	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			code, _, stderr := invoke(t, "-f", "/tmp/observed", "-j", "example.js", tt.flag, tt.value)

			if code != exitUsage {
				t.Fatalf("%s %s exit code = %d, want %d", tt.flag, tt.value, code, exitUsage)
			}
			if !strings.Contains(stderr, tt.flag) {
				t.Fatalf("stderr %q does not name %s", stderr, tt.flag)
			}
		})
	}
}

func TestFilenameFlagIsRepeatable(t *testing.T) {
	// Reaching the script check means both -f values were accepted by the parser.
	missing := filepath.Join(t.TempDir(), "does-not-exist.js")

	code, _, stderr := invoke(t, "-f", "/tmp/one", "-f", "/tmp/two", "-j", missing)

	if code != exitError {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitError, stderr)
	}
	if !strings.Contains(stderr, "reading script") {
		t.Fatalf("stderr %q suggests the -f values were not accepted", stderr)
	}
}

// -m is a duration like every other timing flag, so a bare number is rejected rather
// than quietly meaning something else.
func TestIntervalRequiresAUnit(t *testing.T) {
	code, _, stderr := invoke(t, "-f", "/tmp/observed", "-j", "example.js", "-m", "1000")

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "1000") {
		t.Fatalf("stderr %q does not name the rejected value", stderr)
	}
}

func TestIntervalAcceptsDurations(t *testing.T) {
	// Reaching the script check means the value parsed.
	missing := filepath.Join(t.TempDir(), "does-not-exist.js")

	for _, value := range []string{"500ms", "2s", "1m", "0"} {
		t.Run(value, func(t *testing.T) {
			code, _, stderr := invoke(t, "-f", "/tmp/observed", "-j", missing, "-m", value)
			if code != exitError {
				t.Fatalf("-m %s exit code = %d, want %d (stderr: %s)", value, code, exitError, stderr)
			}
		})
	}
}

func TestNonPositiveMaxWatchesIsUsageError(t *testing.T) {
	code, _, stderr := invoke(t, "-f", "/tmp/observed", "-j", "example.js", "--max-watches", "0")

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "--max-watches") {
		t.Fatalf("stderr %q does not name the flag", stderr)
	}
}

func TestUnknownLogFormatIsUsageError(t *testing.T) {
	code, _, stderr := invoke(t, "-f", "/tmp/observed", "-j", "example.js", "--log-format", "logfmt")

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "logfmt") {
		t.Fatalf("stderr %q does not name the rejected format", stderr)
	}
}

// Kowl takes no positional arguments. One used to be accepted silently, so
// `kowl -f a b -j s.js` watched only a and never said b had been dropped.
func TestPositionalArgumentIsUsageError(t *testing.T) {
	code, _, stderr := invoke(t, "-f", "/tmp/one", "/tmp/two", "-j", "example.js")

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "/tmp/two") {
		t.Fatalf("stderr %q does not name the argument that was dropped", stderr)
	}
	if !strings.Contains(stderr, "-f") {
		t.Fatalf("stderr %q does not say what to do instead", stderr)
	}
}

func TestPositionalArgumentAfterASeparatorIsAlsoRejected(t *testing.T) {
	code, _, stderr := invoke(t, "-f", "/tmp/one", "-j", "example.js", "--", "leftover")

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitUsage, stderr)
	}
}

func TestBrokenExcludePatternIsUsageError(t *testing.T) {
	code, _, stderr := invoke(t, "-f", "/tmp/observed", "-j", "example.js", "-x", "[unterminated")

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "unterminated") {
		t.Fatalf("stderr %q does not name the bad pattern", stderr)
	}
}

// --- SIGHUP handling ---------------------------------------------------------------

// serveReloads is what SIGHUP reaches. The end-to-end test drives it through a real
// process, which proves the signal arrives but covers none of the reporting below.
func TestServeReloadsReportsWhatTheFreshCopyDefines(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.txt")
	key := "KOWL_TEST_SERVE_RELOAD"
	t.Setenv(key, "first")

	script := writeScript(t, `
		const captured = kGetEnv("`+key+`")
		function write(name, op) { kStringToFile(captured, `+quote(out)+`) }`)
	runner := NewRunner(script)
	if _, err := runner.Run("WRITE", "x"); err != nil {
		t.Fatal(err)
	}

	logs := &safeBuffer{}
	signals := make(chan os.Signal, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveReloads(ctx, signals, runner, NewLogger(logs, LevelInfo, FormatText))
	}()

	t.Setenv(key, "second")
	signals <- syscall.SIGHUP

	waitFor(t, 2*time.Second, "the reload to be reported", func() bool {
		return strings.Contains(logs.String(), "reloaded")
	})
	if !strings.Contains(logs.String(), "hooks: write") {
		t.Fatalf("the reload did not say what the script defines:\n%s", logs.String())
	}

	cancel()
	<-done

	// The reload has to have taken effect, not just been announced.
	if _, err := runner.Run("WRITE", "x"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, out); got != "second" {
		t.Fatalf("hook wrote %q after the reload, want %q", got, "second")
	}
}

func TestServeReloadsReportsAScriptThatNoLongerParses(t *testing.T) {
	script := writeScript(t, `function write(name, op) {}`)
	runner := NewRunner(script)

	logs := &safeBuffer{}
	signals := make(chan os.Signal, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serveReloads(ctx, signals, runner, NewLogger(logs, LevelInfo, FormatText))

	rewriteScript(t, script, `function write( {`)
	signals <- syscall.SIGHUP

	waitFor(t, 2*time.Second, "the failure to be reported", func() bool {
		return strings.Contains(logs.String(), "reload failed")
	})
}

// Editing every hook out of a script leaves it running but useless, which is worth
// saying out loud.
func TestServeReloadsReportsAScriptWithNoHooksLeft(t *testing.T) {
	script := writeScript(t, `function write(name, op) {}`)
	runner := NewRunner(script)

	logs := &safeBuffer{}
	signals := make(chan os.Signal, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serveReloads(ctx, signals, runner, NewLogger(logs, LevelInfo, FormatText))

	rewriteScript(t, script, `function helper() { return 1 }`)
	signals <- syscall.SIGHUP

	waitFor(t, 2*time.Second, "the empty script to be reported", func() bool {
		return strings.Contains(logs.String(), "none of the known hooks")
	})
}

func TestServeReloadsStopsWithItsContext(t *testing.T) {
	runner := NewRunner(writeScript(t, `function write(name, op) {}`))
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		serveReloads(ctx, make(chan os.Signal), runner, NewLogger(&safeBuffer{}, LevelError, FormatText))
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveReloads did not return after its context was cancelled")
	}
}

// An empty pattern matches nothing and reports the emptiness forever, which is never
// what someone meant to ask for.
func TestEmptyPatternIsUsageError(t *testing.T) {
	for _, flag := range []string{"-f", "-x"} {
		t.Run(flag, func(t *testing.T) {
			args := []string{"-f", "/tmp/observed", "-j", "example.js", flag, ""}
			code, _, stderr := invoke(t, args...)

			if code != exitUsage {
				t.Fatalf("%s '' exit code = %d, want %d", flag, code, exitUsage)
			}
			if !strings.Contains(stderr, "empty pattern") {
				t.Fatalf("stderr %q does not explain the problem", stderr)
			}
		})
	}
}

// --- --check and --once ------------------------------------------------------------

// Checking a script should not require telling Kowl what to watch.
func TestCheckValidatesAScriptWithoutWatchingAnything(t *testing.T) {
	script := writeScript(t, `
		function write(name, op) {}
		function ticker(name, op) {}`)

	code, stdout, stderr := invoke(t, "-j", script, "--check")

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "write, ticker") {
		t.Fatalf("stdout %q does not report what the script defines", stdout)
	}
}

func TestCheckRejectsAScriptThatDoesNotParse(t *testing.T) {
	script := writeScript(t, `function write( {`)

	code, _, stderr := invoke(t, "-j", script, "--check")

	if code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	if !strings.Contains(stderr, script) {
		t.Fatalf("stderr %q does not name the script", stderr)
	}
}

func TestCheckRejectsAScriptWithNoHooks(t *testing.T) {
	script := writeScript(t, `function helper() { return 1 }`)

	code, _, stderr := invoke(t, "-j", script, "--check")

	if code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	if !strings.Contains(stderr, "none of the known hooks") {
		t.Fatalf("stderr %q does not explain the problem", stderr)
	}
}

// -j is needed even to check, and -f only when actually watching.
func TestMissingScriptIsUsageErrorEvenForCheck(t *testing.T) {
	code, _, stderr := invoke(t, "--check")

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "javascript") {
		t.Fatalf("stderr %q does not name the missing flag", stderr)
	}
}

// --once runs against what is there now and stops, rather than leaving a daemon behind.
func TestOnceRunsTheHooksAndExits(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "journal.txt")
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := writeScript(t, `
		function ticker(name, op, event) { kAppendFile(event.name + "\n", `+quote(journal)+`) }`)

	done := make(chan int, 1)
	go func() {
		var out, errOut safeBuffer
		done <- run([]string{"-f", filepath.Join(dir, "*.txt"), "-j", script, "--once"}, &out, &errOut)
	}()

	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("exit code = %d, want %d", code, exitOK)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("--once did not exit")
	}

	got := readFile(t, journal)
	for _, want := range []string{"a.txt", "b.txt"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the journal %q is missing %s", got, want)
		}
	}
}

// A pattern matching nothing still reaches not_found, so a one-shot run can act on an
// absence too.
func TestOnceReportsAPatternThatMatchesNothing(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "journal.txt")
	script := writeScript(t, `
		function not_found(name, op, event) { kAppendFile("missing:" + event.path + "\n", `+quote(journal)+`) }`)

	done := make(chan int, 1)
	go func() {
		var out, errOut safeBuffer
		done <- run([]string{"-f", filepath.Join(dir, "nowhere.txt"), "-j", script, "--once"}, &out, &errOut)
	}()

	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("exit code = %d, want %d", code, exitOK)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("--once did not exit")
	}

	if got := readFile(t, journal); !strings.Contains(got, "nowhere.txt") {
		t.Fatalf("the journal %q does not mention the missing path", got)
	}
}

// --once honours -x, so a one-shot run covers the same set a watching run would.
func TestOnceHonoursExclusions(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "journal.txt")
	for _, name := range []string{"keep.txt", "skip.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := writeScript(t, `
		function ticker(name, op, event) { kAppendFile(event.name + "\n", `+quote(journal)+`) }`)

	done := make(chan int, 1)
	go func() {
		var out, errOut safeBuffer
		done <- run([]string{"-f", filepath.Join(dir, "*.txt"), "-j", script, "-x", "skip.txt", "--once"}, &out, &errOut)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("--once did not exit")
	}

	got := readFile(t, journal)
	if !strings.Contains(got, "keep.txt") {
		t.Fatalf("the journal %q is missing the file that should have run", got)
	}
	if strings.Contains(got, "skip.txt") {
		t.Fatalf("the journal %q includes an excluded file", got)
	}
}
