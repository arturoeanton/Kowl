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

func TestKCliPerformsARequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s %s", r.Method, r.URL.Path, r.Header.Get("Client"))
	}))
	defer server.Close()

	value := evalJS(t, `
		kCli.URL(`+quote(server.URL)+`)
		var req = kCli.Request()
		req.Path("/headers")
		req.SetHeader("Client", "kowl")
		var res = req.Send()
		res[0].StatusCode + " " + res[0].String()`)

	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
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
		var req = kCli.Request()
		req.Method("POST")
		req.Use(kBodyJSON({"foo": "bar"}))
		var res = req.Send()
		res[0].String()`)

	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "POST ") || !strings.Contains(got, `"foo"`) {
		t.Fatalf("kCli POST echoed %q, want the JSON body", got)
	}
}

// A server that never answers must not hold the hook past --http-timeout.
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
	value, err := vm.Run(`
		kCli.URL(` + quote(server.URL) + `)
		var res = kCli.Request().Send()
		res[1] === null ? "no error" : "error"`)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("running script: %v", err)
	}

	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if got != "error" {
		t.Fatalf("Send() reported %q for a request that timed out, want an error", got)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Send() took %s, so --http-timeout did not apply", elapsed)
	}
}

// evalJSWithLogger runs source in a VM whose script output is captured.
func evalJSWithLogger(t *testing.T, level Level, source string) string {
	t.Helper()
	logs := &safeBuffer{}
	cfg := defaultVMConfig()
	cfg.logger = NewLogger(logs, level)
	if _, err := newVM(cfg).Run(source); err != nil {
		t.Fatalf("running script: %v\n%s", err, source)
	}
	return logs.String()
}

// otto's own console writes straight to stdout, which bypasses --log-level and leaves
// script output on a different stream from Kowl's.
func TestConsoleLogGoesThroughTheLogger(t *testing.T) {
	got := evalJSWithLogger(t, LevelInfo, `console.log("hello", "world")`)

	if !strings.Contains(got, "info") || !strings.Contains(got, "hello world") {
		t.Fatalf("console.log produced %q, want an info line with both arguments", got)
	}
}

func TestConsoleLevelsMapToLogLevels(t *testing.T) {
	tests := []struct{ call, level string }{
		{`console.debug("m")`, "debug"},
		{`console.log("m")`, "info"},
		{`console.info("m")`, "info"},
		{`console.warn("m")`, "warn"},
		{`console.error("m")`, "error"},
		{`kDebug("m")`, "debug"},
		{`kLog("m")`, "info"},
		{`kWarn("m")`, "warn"},
		{`kError("m")`, "error"},
	}
	for _, tt := range tests {
		t.Run(tt.call, func(t *testing.T) {
			got := evalJSWithLogger(t, LevelDebug, tt.call)
			if !strings.Contains(got, tt.level+" ") {
				t.Fatalf("%s logged %q, want level %s", tt.call, got, tt.level)
			}
		})
	}
}

// --log-level now governs script output too.
func TestScriptLoggingRespectsTheLogLevel(t *testing.T) {
	got := evalJSWithLogger(t, LevelError, `
		console.debug("dropped-debug")
		console.log("dropped-info")
		console.error("kept-error")`)

	for _, unwanted := range []string{"dropped-debug", "dropped-info"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("an error-level logger kept %q:\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "kept-error") {
		t.Fatalf("an error-level logger dropped the error:\n%s", got)
	}
}

// A logged value containing a percent sign must not be treated as a format string.
func TestScriptLoggingDoesNotInterpretFormatVerbs(t *testing.T) {
	got := evalJSWithLogger(t, LevelInfo, `console.log("100% done %s %d")`)

	if strings.Contains(got, "%!") {
		t.Fatalf("the message was treated as a format string: %q", got)
	}
	if !strings.Contains(got, "100% done %s %d") {
		t.Fatalf("logged %q, want the message verbatim", got)
	}
}

// Objects should print as their contents, not as "[object Object]".
func TestScriptLoggingRendersObjects(t *testing.T) {
	got := evalJSWithLogger(t, LevelInfo, `console.log({name: "kowl"})`)

	if strings.Contains(got, "[object Object]") {
		t.Fatalf("an object logged as %q", got)
	}
	if !strings.Contains(got, "kowl") {
		t.Fatalf("logged %q, want the object's contents", got)
	}
}

func TestScriptLoggingHandlesNoArguments(t *testing.T) {
	got := evalJSWithLogger(t, LevelInfo, `console.log()`)
	if !strings.Contains(got, "info") {
		t.Fatalf("console.log() produced %q, want an empty info line", got)
	}
}

func TestKFileExistsReturnsABoolean(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "there.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	value := evalJS(t, `
		kFileExists(`+quote(file)+`) + "|" + kFileExists(`+quote(filepath.Join(dir, "missing"))+`)`)

	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if want := "true|false"; got != want {
		t.Fatalf("kFileExists = %q, want %q", got, want)
	}
}

func TestKStatReturnsAPlainObject(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(file, []byte("12345"), 0o640); err != nil {
		t.Fatal(err)
	}

	value := evalJS(t, `
		var s = kStat(`+quote(file)+`);
		var fields = [s.name, s.dir, s.size, s.mode, s.isDir, s.modTime === "" ? "no-time" : "has-time"];
		fields.join("|")`)

	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{"data.txt", dir, "5", "0640", "false", "has-time"}, "|")
	if got != want {
		t.Fatalf("kStat = %q, want %q", got, want)
	}
}

func TestKStatThrowsOnAMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.txt")
	err := evalJSError(t, `kStat(`+quote(missing)+`)`)
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error %q does not name the path", err)
	}
}

func TestKListDirReturnsAnArrayOfObjects(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("xx"), 0o644); err != nil {
		t.Fatal(err)
	}

	value := evalJS(t, `
		var entries = kListDir(`+quote(dir)+`)
		entries.length + "|" + entries[0].name + "|" + entries[0].size + "|" +
			entries[0].isDir + "|" + entries[1].name + "|" + entries[1].isDir`)

	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if want := "2|a.txt|2|false|sub|true"; got != want {
		t.Fatalf("kListDir = %q, want %q", got, want)
	}
}

// A script must be able to iterate the result with plain JavaScript.
func TestKListDirResultIsIterable(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	value := evalJS(t, `
		var names = []
		var entries = kListDir(`+quote(dir)+`)
		for (var i = 0; i < entries.length; i++) { names.push(entries[i].name) }
		names.join(",")`)

	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if got != "a.txt,b.txt" {
		t.Fatalf("iterated %q, want %q", got, "a.txt,b.txt")
	}
}

func TestKGlobReturnsMatches(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.log", "b.log", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	value := evalJS(t, `kGlob(`+quote(filepath.Join(dir, "*.log"))+`).length`)
	got, err := value.ToInteger()
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("kGlob matched %d paths, want 2", got)
	}
}

func TestKMkdirAllAndKRemoveAll(t *testing.T) {
	nested := filepath.Join(t.TempDir(), "a", "b", "c")

	value := evalJS(t, `
		kMkdirAll(`+quote(nested)+`)
		var created = kFileExists(`+quote(nested)+`)
		kRemoveAll(`+quote(filepath.Dir(filepath.Dir(nested)))+`)
		created + "|" + kFileExists(`+quote(nested)+`)`)

	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if want := "true|false"; got != want {
		t.Fatalf("kMkdirAll/kRemoveAll = %q, want %q", got, want)
	}
}

func TestKCopyFileAndKMoveFile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	copied := filepath.Join(dir, "copied.txt")
	moved := filepath.Join(dir, "moved.txt")
	if err := os.WriteFile(source, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	value := evalJS(t, `
		kCopyFile(`+quote(source)+`, `+quote(copied)+`);
		kMoveFile(`+quote(copied)+`, `+quote(moved)+`);
		var state = [
			kFileExists(`+quote(source)+`),
			kFileExists(`+quote(copied)+`),
			kFileToString(`+quote(moved)+`)
		];
		state.join("|")`)

	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if want := "true|false|payload"; got != want {
		t.Fatalf("copy then move = %q, want %q", got, want)
	}
}

func TestKCopyFileThrowsOnAMissingSource(t *testing.T) {
	dir := t.TempDir()
	evalJSError(t, `kCopyFile(`+quote(filepath.Join(dir, "missing"))+`, `+quote(filepath.Join(dir, "copy"))+`)`)
}

func TestKExecAcceptsAWorkingDirectory(t *testing.T) {
	dir := t.TempDir()

	value := evalJS(t, `kExec("pwd", {dir: `+quote(dir)+`}).stdout`)
	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(strings.TrimSpace(got))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != want {
		t.Fatalf("kExec ran in %q, want %q", resolved, want)
	}
}

func TestKExecAcceptsEnvAndStdin(t *testing.T) {
	value := evalJS(t, `
		var out = kExec("sh", "-c", "read line; echo $GREETING $line", {
			env: {GREETING: "hola"},
			stdin: "mundo\n"
		});
		out.stdout.trim()`)

	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if got != "hola mundo" {
		t.Fatalf("kExec produced %q, want %q", got, "hola mundo")
	}
}

// The options object is only recognised in the last position, so a string argument that
// happens to be last is still an argument.
func TestKExecTreatsTrailingStringsAsArguments(t *testing.T) {
	value := evalJS(t, `kExec("echo", "dir", "env", "stdin").stdout.trim()`)

	got, err := value.ToString()
	if err != nil {
		t.Fatal(err)
	}
	if got != "dir env stdin" {
		t.Fatalf("kExec produced %q, want the three arguments", got)
	}
}

func TestKExecRejectsAnUnknownOption(t *testing.T) {
	err := evalJSError(t, `kExec("echo", "hi", {shell: true})`)
	if !strings.Contains(err.Error(), "shell") {
		t.Fatalf("error %q does not name the unknown option", err)
	}
}

func TestKExecRejectsANonObjectEnv(t *testing.T) {
	evalJSError(t, `kExec("echo", "hi", {env: "LANG=C"})`)
}
