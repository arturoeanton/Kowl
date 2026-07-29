package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robertkrimen/otto"
)

// evalJS runs source in a VM with the k* bindings installed and returns its value.
func evalJS(t *testing.T, source string) otto.Value {
	t.Helper()
	value, err := newVM(defaultVMConfig()).Run(source)
	if err != nil {
		t.Fatalf("running script: %v\n%s", err, source)
	}
	return value
}

// evalJSError runs source expecting it to throw, and returns the error.
func evalJSError(t *testing.T, source string) error {
	t.Helper()
	_, err := newVM(defaultVMConfig()).Run(source)
	if err == nil {
		t.Fatalf("script did not throw:\n%s", source)
	}
	return err
}

func TestKExecReturnsStructuredResult(t *testing.T) {
	value := evalJS(t, `
		var out = kExec("sh", "-c", "echo to-stdout; echo to-stderr 1>&2; exit 4")
		out.stdout.trim() + "|" + out.stderr.trim() + "|" + out.code + "|" + out.truncated`)

	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if want := "to-stdout|to-stderr|4|false"; got != want {
		t.Fatalf("kExec result = %q, want %q", got, want)
	}
}

func TestKExecThrowsWhenCommandCannotStart(t *testing.T) {
	err := evalJSError(t, `kExec("kowl-no-such-command-exists")`)
	if !strings.Contains(err.Error(), "kowl-no-such-command-exists") {
		t.Fatalf("error %q does not name the command", err)
	}
}

func TestKExecThrowsWithoutACommandName(t *testing.T) {
	err := evalJSError(t, `kExec()`)
	if !strings.Contains(err.Error(), "required") {
		t.Fatalf("error %q does not say the argument is required", err)
	}
}

// Helpers throw instead of returning a status code, so a script that ignores errors
// stops rather than carrying on with bad data.
func TestKFileToStringThrowsOnMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.txt")

	err := evalJSError(t, `kFileToString(`+quote(missing)+`)`)
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error %q does not name the file", err)
	}
}

func TestKFileToStringReturnsContentsDirectly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.txt")
	if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	value := evalJS(t, `kFileToString(`+quote(path)+`)`)
	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if got != "payload" {
		t.Fatalf("kFileToString = %q, want %q", got, "payload")
	}
}

func TestKStringToFileAndKAppendFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.txt")

	value := evalJS(t, `
		kStringToFile("one\n", `+quote(path)+`)
		kAppendFile("two\n", `+quote(path)+`)
		kFileToString(`+quote(path)+`)`)

	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if want := "one\ntwo\n"; got != want {
		t.Fatalf("file contents = %q, want %q", got, want)
	}
}

func TestKAppendFileCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.txt")

	value := evalJS(t, `
		kAppendFile("line\n", `+quote(path)+`)
		kFileToString(`+quote(path)+`)`)

	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if got != "line\n" {
		t.Fatalf("file contents = %q, want %q", got, "line\n")
	}
}

func TestKRemoveFileThrowsOnMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.txt")
	evalJSError(t, `kRemoveFile(`+quote(missing)+`)`)
}

func TestKEncryptKDecryptRoundTrip(t *testing.T) {
	value := evalJS(t, `kDecrypt("passphrase", kEncrypt("passphrase", "secret"))`)

	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if got != "secret" {
		t.Fatalf("round trip = %q, want %q", got, "secret")
	}
}

func TestKDecryptThrowsOnWrongPassphrase(t *testing.T) {
	evalJSError(t, `kDecrypt("wrong", kEncrypt("right", "secret"))`)
}

// A thrown KowlError is an ordinary JavaScript exception, so scripts can handle it.
func TestHelperErrorsAreCatchableFromJavaScript(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.txt")

	value := evalJS(t, `
		var caught = "none"
		try {
			kFileToString(`+quote(missing)+`)
		} catch (e) {
			caught = "caught"
		}
		caught`)

	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if got != "caught" {
		t.Fatalf("script observed %q, want the error to be catchable", got)
	}
}

func TestKSetEnvAndKGetEnvRoundTrip(t *testing.T) {
	key := "KOWL_TEST_BINDING"
	t.Setenv(key, "")

	value := evalJS(t, `
		kSetEnv("`+key+`", "value")
		kGetEnv("`+key+`")`)

	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if got != "value" {
		t.Fatalf("kGetEnv = %q, want %q", got, "value")
	}
}

func TestKHostnameReturnsAString(t *testing.T) {
	value := evalJS(t, `kHostname()`)
	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Fatal("kHostname returned an empty string")
	}
}

func TestUnderscoreIsAvailable(t *testing.T) {
	value := evalJS(t, `_.map([1, 2, 3], function (n) { return n * 2 }).join(",")`)
	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if got != "2,4,6" {
		t.Fatalf("underscore result = %q, want %q", got, "2,4,6")
	}
}

// A command that outruns the exec budget is stopped rather than hanging the hook.
func TestKExecHonoursTheExecTimeout(t *testing.T) {
	cfg := defaultVMConfig()
	cfg.execTimeout = 100 * time.Millisecond
	vm := newVM(cfg)

	start := time.Now()
	_, err := vm.Run(`kExec("sleep", "10")`)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("kExec did not throw for a command that outran the timeout")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("kExec took %s, so the timeout did not stop the command", elapsed)
	}
}

func TestKExecReportsTruncatedOutput(t *testing.T) {
	cfg := defaultVMConfig()
	cfg.maxOutput = 64
	vm := newVM(cfg)

	value, err := vm.Run(`
		var out = kExec("sh", "-c", "printf 'x%.0s' $(seq 1 1000)")
		out.stdout.length + "|" + out.truncated`)
	if err != nil {
		t.Fatalf("running script: %v", err)
	}
	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if want := "64|true"; got != want {
		t.Fatalf("kExec truncation = %q, want %q", got, want)
	}
}
