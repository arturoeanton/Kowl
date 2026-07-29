package main

import (
	"path/filepath"
	"strings"
	"testing"
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
	for _, flag := range []string{"--filename", "--javascript", "--millisecond", "--flagNotWatcher"} {
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
	code, _, stderr := invoke(t, "-f", "/tmp/observed", "-j", "example.js", "-m", "-5")

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

// The bundled example must stay in sync with the hook names Kowl dispatches.
func TestExampleScriptDefinesEveryKnownHook(t *testing.T) {
	hooks, err := NewRunner("example.js").DefinedHooks()
	if err != nil {
		t.Fatalf("loading example.js: %v", err)
	}
	if got, want := strings.Join(hooks, ","), strings.Join(hookNames, ","); got != want {
		t.Fatalf("example.js defines %q, want %q", got, want)
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
