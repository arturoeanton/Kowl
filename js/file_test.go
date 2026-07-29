package js

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileThenReadFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.txt")

	if err := WriteFile("hello\n", path); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got != "hello\n" {
		t.Fatalf("ReadFile = %q, want %q", got, "hello\n")
	}
}

func TestWriteFileReplacesExistingContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.txt")

	if err := WriteFile("first", path); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := WriteFile("second", path); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got != "second" {
		t.Fatalf("ReadFile = %q, want %q", got, "second")
	}
}

// ReadFile used to return the error text in place of the contents, so a script that
// ignored the status code could write the error message back into the file.
func TestReadFileReportsMissingFileWithoutInventingContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.txt")

	got, err := ReadFile(path)
	if err == nil {
		t.Fatal("ReadFile returned nil error for a missing file")
	}
	if got != "" {
		t.Fatalf("ReadFile returned %q as contents alongside an error", got)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error %q does not name the file", err)
	}
}

// AppendFile omitted O_CREATE, so appending to a file that did not exist failed.
func TestAppendFileCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.txt")

	if err := AppendFile("line one\n", path); err != nil {
		t.Fatalf("AppendFile on a missing file: %v", err)
	}
	got, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got != "line one\n" {
		t.Fatalf("ReadFile = %q, want %q", got, "line one\n")
	}
}

func TestAppendFileAppendsRatherThanReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.txt")

	for _, line := range []string{"one\n", "two\n", "three\n"} {
		if err := AppendFile(line, path); err != nil {
			t.Fatalf("AppendFile(%q): %v", line, err)
		}
	}
	got, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if want := "one\ntwo\nthree\n"; got != want {
		t.Fatalf("ReadFile = %q, want %q", got, want)
	}
}

func TestRemoveFileDeletesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.txt")
	if err := WriteFile("x", path); err != nil {
		t.Fatal(err)
	}

	if err := RemoveFile(path); err != nil {
		t.Fatalf("RemoveFile: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still exists after RemoveFile (stat error: %v)", err)
	}
}

func TestRemoveFileReportsMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.txt")

	if err := RemoveFile(path); err == nil {
		t.Fatal("RemoveFile returned nil error for a missing file")
	}
}

func TestWriteFileReportsUnwritablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "data.txt")

	if err := WriteFile("x", path); err == nil {
		t.Fatal("WriteFile returned nil error for a path whose directory does not exist")
	}
}
