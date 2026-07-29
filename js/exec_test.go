package js

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testLimit = 1 << 20

// plain is ExecOptions with only the output limit set.
var plain = ExecOptions{Limit: testLimit}

func TestExecCapturesStdout(t *testing.T) {
	result, err := Exec(context.Background(), plain, "echo", "hello")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); got != "hello" {
		t.Fatalf("Stdout = %q, want %q", got, "hello")
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", result.ExitCode)
	}
}

// A command that ran and failed used to have its real output thrown away and replaced
// by the Go error string, which is exactly when the output matters most.
func TestExecKeepsOutputOfFailingCommand(t *testing.T) {
	result, err := Exec(context.Background(), plain,
		"sh", "-c", "echo to-stdout; echo to-stderr 1>&2; exit 3")
	if err != nil {
		t.Fatalf("Exec returned an error for a command that ran and exited non-zero: %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); got != "to-stdout" {
		t.Fatalf("Stdout = %q, want %q", got, "to-stdout")
	}
	if got := strings.TrimSpace(result.Stderr); got != "to-stderr" {
		t.Fatalf("Stderr = %q, want %q", got, "to-stderr")
	}
	if result.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", result.ExitCode)
	}
}

func TestExecReportsCommandThatCannotStart(t *testing.T) {
	_, err := Exec(context.Background(), plain, "kowl-no-such-command-exists")
	if err == nil {
		t.Fatal("Exec returned nil error for a command that does not exist")
	}
}

// Without a deadline a hung command blocked its goroutine forever.
func TestExecStopsAtContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := Exec(ctx, plain, "sleep", "10")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Exec returned nil error for a command that outlived its context")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Exec took %s, so the deadline did not stop the command", elapsed)
	}
}

// A command producing unbounded output must not be able to exhaust memory.
func TestExecTruncatesOutputAtLimit(t *testing.T) {
	const limit = 128

	result, err := Exec(context.Background(), ExecOptions{Limit: limit}, "sh", "-c", "printf 'x%.0s' $(seq 1 5000)")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if len(result.Stdout) != limit {
		t.Fatalf("captured %d bytes of stdout, want the %d byte limit", len(result.Stdout), limit)
	}
	if !result.Truncated {
		t.Fatal("Truncated is false even though output was cut short")
	}
}

func TestExecReportsUntruncatedOutput(t *testing.T) {
	result, err := Exec(context.Background(), plain, "echo", "short")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.Truncated {
		t.Fatal("Truncated is true for output well under the limit")
	}
}

func TestExecRunsInTheGivenDirectory(t *testing.T) {
	dir := t.TempDir()

	result, err := Exec(context.Background(), ExecOptions{Limit: testLimit, Dir: dir}, "pwd")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	// macOS reports /var as a symlink to /private/var, so compare the resolved paths.
	got, err := filepath.EvalSymlinks(strings.TrimSpace(result.Stdout))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ran in %q, want %q", got, want)
	}
}

// Env is added to Kowl's own environment rather than replacing it, so a command does
// not silently lose PATH.
func TestExecAddsToTheEnvironmentRatherThanReplacingIt(t *testing.T) {
	t.Setenv("KOWL_EXEC_INHERITED", "inherited")

	result, err := Exec(context.Background(),
		ExecOptions{Limit: testLimit, Env: map[string]string{"KOWL_EXEC_ADDED": "added"}},
		"sh", "-c", "echo $KOWL_EXEC_ADDED $KOWL_EXEC_INHERITED ${PATH:+has-path}")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got, want := strings.TrimSpace(result.Stdout), "added inherited has-path"; got != want {
		t.Fatalf("environment = %q, want %q", got, want)
	}
}

func TestExecOverridesAnInheritedVariable(t *testing.T) {
	t.Setenv("KOWL_EXEC_OVERRIDE", "original")

	result, err := Exec(context.Background(),
		ExecOptions{Limit: testLimit, Env: map[string]string{"KOWL_EXEC_OVERRIDE": "replaced"}},
		"sh", "-c", "echo $KOWL_EXEC_OVERRIDE")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); got != "replaced" {
		t.Fatalf("variable = %q, want %q", got, "replaced")
	}
}

func TestExecFeedsStdin(t *testing.T) {
	result, err := Exec(context.Background(),
		ExecOptions{Limit: testLimit, Stdin: "one\ntwo\nthree\n"}, "wc", "-l")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); got != "3" {
		t.Fatalf("wc counted %q lines, want 3", got)
	}
}

// A command that reads stdin must not block when none was given.
func TestExecWithoutStdinDoesNotHang(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := Exec(ctx, plain, "cat")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.Stdout != "" {
		t.Fatalf("cat produced %q with no stdin", result.Stdout)
	}
}

func TestExecReportsAMissingDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-there")

	if _, err := Exec(context.Background(), ExecOptions{Limit: testLimit, Dir: missing}, "pwd"); err == nil {
		t.Fatal("Exec returned nil error for a working directory that does not exist")
	}
}
