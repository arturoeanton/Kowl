package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
)

// evalJS runs source in a VM with the k* bindings installed and returns its value.
func evalJS(t *testing.T, source string) goja.Value {
	t.Helper()
	value, err := newVM(defaultVMConfig()).RunString(source)
	if err != nil {
		t.Fatalf("running script: %v\n%s", err, source)
	}
	return value
}

// evalJSError runs source expecting it to throw, and returns the error.
func evalJSError(t *testing.T, source string) error {
	t.Helper()
	_, err := newVM(defaultVMConfig()).RunString(source)
	if err == nil {
		t.Fatalf("script did not throw:\n%s", source)
	}
	return err
}

func TestKExecReturnsStructuredResult(t *testing.T) {
	value := evalJS(t, `
		var out = kExec("sh", "-c", "echo to-stdout; echo to-stderr 1>&2; exit 4")
		out.stdout.trim() + "|" + out.stderr.trim() + "|" + out.code + "|" + out.truncated`)

	got := value.String()
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
	got := value.String()
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

	got := value.String()
	if want := "one\ntwo\n"; got != want {
		t.Fatalf("file contents = %q, want %q", got, want)
	}
}

func TestKAppendFileCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.txt")

	value := evalJS(t, `
		kAppendFile("line\n", `+quote(path)+`)
		kFileToString(`+quote(path)+`)`)

	got := value.String()
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

	got := value.String()
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

	got := value.String()
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

	got := value.String()
	if got != "value" {
		t.Fatalf("kGetEnv = %q, want %q", got, "value")
	}
}

func TestKHostnameReturnsAString(t *testing.T) {
	value := evalJS(t, `kHostname()`)
	got := value.String()
	if got == "" {
		t.Fatal("kHostname returned an empty string")
	}
}

func TestUnderscoreIsAvailable(t *testing.T) {
	value := evalJS(t, `_.map([1, 2, 3], function (n) { return n * 2 }).join(",")`)
	got := value.String()
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
	_, err := vm.RunString(`kExec("sleep", "10")`)
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

	value, err := vm.RunString(`
		var out = kExec("sh", "-c", "printf 'x%.0s' $(seq 1 1000)")
		out.stdout.length + "|" + out.truncated`)
	if err != nil {
		t.Fatalf("running script: %v", err)
	}
	got := value.String()
	if want := "64|true"; got != want {
		t.Fatalf("kExec truncation = %q, want %q", got, want)
	}
}

func TestKCliPerformsARequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s %s", r.Method, r.URL.Path, r.Header.Get("Client"))
	}))
	defer server.Close()

	value := evalJS(t, `
		kCli.URL(`+quote(server.URL)+`)
		const req = kCli.Request()
		req.Path("/headers")
		req.SetHeader("Client", "kowl")
		const res = req.Send();
		`+"`${res.statusCode} ${res.String()}`")

	got := value.String()
	if want := "200 GET /headers kowl"; got != want {
		t.Fatalf("kCli response = %q, want %q", got, want)
	}
}

func TestKCliPostsAJSONBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s %s", r.Method, body)
	}))
	defer server.Close()

	value := evalJS(t, `
		kCli.URL(`+quote(server.URL)+`)
		const req = kCli.Request()
		req.Method("POST")
		req.Use(kBodyJSON({"foo": "bar"}))
		req.Send().String()`)

	got := value.String()
	if !strings.HasPrefix(got, "POST ") || !strings.Contains(got, `"foo"`) {
		t.Fatalf("kCli POST echoed %q, want the JSON body", got)
	}
}

// A server that never answers must not hold the hook past --http-timeout. Send throws
// on failure now, so the script has to catch it.
func TestKCliHonoursTheHTTPTimeout(t *testing.T) {
	block := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer server.Close()
	defer close(block)

	cfg := defaultVMConfig()
	cfg.httpTimeout = 150 * time.Millisecond
	vm := newVM(cfg)

	start := time.Now()
	value, err := vm.RunString(`
		kCli.URL(` + quote(server.URL) + `)
		let outcome = "no error"
		try { kCli.Request().Send() } catch (e) { outcome = "error" }
		outcome`)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("running script: %v", err)
	}

	if got := value.String(); got != "error" {
		t.Fatalf("Send() reported %q for a request that timed out, want an error", got)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Send() took %s, so --http-timeout did not apply", elapsed)
	}
}

func TestKSleepAcceptsADurationString(t *testing.T) {
	start := time.Now()
	evalJS(t, `kSleep("120ms")`)
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("kSleep(\"120ms\") returned after %s", elapsed)
	}
}

func TestKSleepAcceptsMilliseconds(t *testing.T) {
	start := time.Now()
	evalJS(t, `kSleep(120)`)
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("kSleep(120) returned after %s", elapsed)
	}
}

// A wait longer than the hook has left would be interrupted part way through, which is
// worse than never starting it.
func TestKSleepRefusesToOutlastTheHookTimeout(t *testing.T) {
	cfg := defaultVMConfig()
	cfg.hookTimeout = 100 * time.Millisecond
	vm := newVM(cfg)

	start := time.Now()
	_, err := vm.RunString(`kSleep("30s")`)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("kSleep accepted a wait longer than the hook timeout")
	}
	if !strings.Contains(err.Error(), "hook timeout") {
		t.Fatalf("error %q does not explain the limit", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("kSleep waited %s before refusing", elapsed)
	}
}

func TestKSleepRejectsBadArguments(t *testing.T) {
	tests := []struct{ name, call string }{
		{"missing", `kSleep()`},
		{"negative", `kSleep(-5)`},
		{"unparseable", `kSleep("soon")`},
		{"no unit", `kSleep("500")`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evalJSError(t, tt.call)
		})
	}
}

func TestKSleepAcceptsZero(t *testing.T) {
	evalJS(t, `kSleep(0)`)
}

// --- what the engine itself provides -----------------------------------------------

// otto was ES5 only. Scripts can now be written in the language people actually use.
func TestModernJavaScriptIsAvailable(t *testing.T) {
	tests := []struct{ name, source, want string }{
		{"let and const", `let a = 1; const b = 2; String(a + b)`, "3"},
		{"arrow functions", `[1, 2, 3].map(n => n * 2).join(",")`, "2,4,6"},
		{"template literals", "const n = 'kowl'; `hello ${n}`", "hello kowl"},
		{"destructuring", `const {a, b} = {a: "x", b: "y"}; a + b`, "xy"},
		{"spread", `const xs = [1, 2]; [...xs, 3].join(",")`, "1,2,3"},
		{"default arguments", `function f(x = "fallback") { return x }; f()`, "fallback"},
		{"Map", `const m = new Map([["k", "v"]]); m.get("k")`, "v"},
		{"Set", `String(new Set([1, 1, 2]).size)`, "2"},
		{"Object.assign", `Object.assign({}, {a: 1}, {b: 2}).b + ""`, "2"},
		{"for...of", `let out = ""; for (const c of "abc") { out += c }; out`, "abc"},
		{"classes", `class T { hello() { return "hi" } }; new T().hello()`, "hi"},
		{"JSON", `JSON.stringify({a: [1, 2]})`, `{"a":[1,2]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalJS(t, tt.source).String(); got != tt.want {
				t.Fatalf("%s = %q, want %q", tt.source, got, tt.want)
			}
		})
	}
}

// Go struct fields reach JavaScript in lower camel case; methods keep their Go names.
// The HTTP client is a Go object exposed directly, and uncapitalising its methods would
// have turned URL into uRL.
func TestFieldNamesAreMappedButMethodNamesAreNot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.txt")
	if err := os.WriteFile(path, []byte("xx"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := evalJS(t, `
		const s = kStat(`+quote(path)+`);
		[s.name, s.size, s.isDir, typeof s.Name, typeof kCli.URL].join("|")`).String()

	if want := "data.txt|2|false|undefined|function"; got != want {
		t.Fatalf("mapping = %q, want %q", got, want)
	}
}

// A Go helper returning (T, error) reaches JavaScript as one returning T that throws, so
// there is no error code to ignore.
func TestGoErrorsArriveAsExceptionsNotValues(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.txt")

	got := evalJS(t, `
		let outcome = "no throw";
		try { kFileToString(`+quote(missing)+`) } catch (e) { outcome = typeof e.message }
		outcome`).String()

	if got != "string" {
		t.Fatalf("a failing helper produced %q, want a thrown error with a message", got)
	}
}
