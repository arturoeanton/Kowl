package js

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExists(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "there.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if !Exists(file) {
		t.Fatal("Exists said a file that is there is not")
	}
	if !Exists(dir) {
		t.Fatal("Exists said a directory that is there is not")
	}
	if Exists(filepath.Join(dir, "missing.txt")) {
		t.Fatal("Exists said a missing file is there")
	}
}

// A dangling symlink is a path that exists, even though what it points at does not.
func TestExistsReportsADanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "nowhere"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if !Exists(link) {
		t.Fatal("Exists said a dangling symlink is not there")
	}
}

func TestStatDescribesAFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(file, []byte("12345"), 0o640); err != nil {
		t.Fatal(err)
	}

	stat, err := Stat(file)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Name != "data.txt" || stat.Dir != dir || stat.Path != file {
		t.Fatalf("Stat = %+v, want the file's own name, dir and path", stat)
	}
	if stat.Size != 5 {
		t.Fatalf("Size = %d, want 5", stat.Size)
	}
	if stat.Mode != "0640" {
		t.Fatalf("Mode = %q, want %q", stat.Mode, "0640")
	}
	if stat.IsDir {
		t.Fatal("IsDir is true for a regular file")
	}
	if stat.ModTime.IsZero() {
		t.Fatal("ModTime is zero")
	}
}

func TestStatDescribesADirectory(t *testing.T) {
	stat, err := Stat(t.TempDir())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !stat.IsDir {
		t.Fatal("IsDir is false for a directory")
	}
}

func TestStatReportsAMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.txt")
	if _, err := Stat(missing); err == nil {
		t.Fatal("Stat returned nil error for a missing path")
	}
}

func TestListDirReturnsSortedEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"c.txt", "a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("xx"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := ListDir(dir)
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}

	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	if got, want := strings.Join(names, ","), "a.txt,b.txt,c.txt,sub"; got != want {
		t.Fatalf("ListDir = %q, want %q", got, want)
	}
	for _, entry := range entries {
		if entry.Path != filepath.Join(dir, entry.Name) {
			t.Fatalf("entry %q has path %q", entry.Name, entry.Path)
		}
		if entry.Name == "sub" && !entry.IsDir {
			t.Fatal("the subdirectory is not reported as one")
		}
		if entry.Name == "a.txt" && entry.Size != 2 {
			t.Fatalf("a.txt has size %d, want 2", entry.Size)
		}
	}
}

func TestListDirOnAnEmptyDirectory(t *testing.T) {
	entries, err := ListDir(t.TempDir())
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("ListDir = %v, want no entries", entries)
	}
}

func TestListDirReportsAMissingDirectory(t *testing.T) {
	if _, err := ListDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("ListDir returned nil error for a missing directory")
	}
}

func TestGlobReturnsSortedMatches(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"b.log", "a.log", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	matches, err := Glob(filepath.Join(dir, "*.log"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	want := []string{filepath.Join(dir, "a.log"), filepath.Join(dir, "b.log")}
	if strings.Join(matches, ",") != strings.Join(want, ",") {
		t.Fatalf("Glob = %v, want %v", matches, want)
	}
}

// Nothing matching is an empty list, not a null a script has to guard against.
func TestGlobReturnsAnEmptyListWhenNothingMatches(t *testing.T) {
	matches, err := Glob(filepath.Join(t.TempDir(), "*.nope"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if matches == nil {
		t.Fatal("Glob returned nil rather than an empty list")
	}
	if len(matches) != 0 {
		t.Fatalf("Glob = %v, want no matches", matches)
	}
}

func TestGlobRejectsABrokenPattern(t *testing.T) {
	if _, err := Glob("[unterminated"); err == nil {
		t.Fatal("Glob returned nil error for an unterminated character class")
	}
}

func TestMkdirAllCreatesEveryParent(t *testing.T) {
	nested := filepath.Join(t.TempDir(), "a", "b", "c")

	if err := MkdirAll(nested); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	stat, err := Stat(nested)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !stat.IsDir {
		t.Fatal("MkdirAll did not create a directory")
	}
}

func TestMkdirAllIsIdempotent(t *testing.T) {
	nested := filepath.Join(t.TempDir(), "a", "b")
	if err := MkdirAll(nested); err != nil {
		t.Fatal(err)
	}
	if err := MkdirAll(nested); err != nil {
		t.Fatalf("MkdirAll on an existing directory: %v", err)
	}
}

func TestRemoveAllDeletesATree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tree")
	if err := MkdirAll(filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile("x", filepath.Join(root, "sub", "file.txt")); err != nil {
		t.Fatal(err)
	}

	if err := RemoveAll(root); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if Exists(root) {
		t.Fatal("the tree is still there after RemoveAll")
	}
}

// RemoveAll is the forgiving one; RemoveFile is the one that reports a missing path.
func TestRemoveAllAcceptsAMissingPath(t *testing.T) {
	if err := RemoveAll(filepath.Join(t.TempDir(), "never-existed")); err != nil {
		t.Fatalf("RemoveAll on a missing path: %v", err)
	}
}

func TestCopyFileCopiesContentsAndMode(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	destination := filepath.Join(dir, "destination.txt")
	if err := os.WriteFile(source, []byte("payload"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := CopyFile(source, destination); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}

	got, err := ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got != "payload" {
		t.Fatalf("copy contains %q, want %q", got, "payload")
	}
	stat, err := Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode != "0640" {
		t.Fatalf("copy has mode %q, want %q", stat.Mode, "0640")
	}
	if !Exists(source) {
		t.Fatal("CopyFile removed the source")
	}
}

func TestCopyFileReplacesAnExistingDestination(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	destination := filepath.Join(dir, "destination.txt")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("much longer old content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CopyFile(source, destination); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}
	got, err := ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got != "new" {
		t.Fatalf("destination contains %q, want it truncated to %q", got, "new")
	}
}

func TestCopyFileRefusesADirectory(t *testing.T) {
	dir := t.TempDir()
	if err := CopyFile(dir, filepath.Join(dir, "copy")); err == nil {
		t.Fatal("CopyFile returned nil error for a directory source")
	}
}

func TestCopyFileReportsAMissingSource(t *testing.T) {
	dir := t.TempDir()
	if err := CopyFile(filepath.Join(dir, "missing"), filepath.Join(dir, "copy")); err == nil {
		t.Fatal("CopyFile returned nil error for a missing source")
	}
}

func TestMoveFileMovesAndRemovesTheSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	destination := filepath.Join(dir, "moved.txt")
	if err := os.WriteFile(source, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := MoveFile(source, destination); err != nil {
		t.Fatalf("MoveFile: %v", err)
	}
	if Exists(source) {
		t.Fatal("the source is still there after MoveFile")
	}
	got, err := ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got != "payload" {
		t.Fatalf("moved file contains %q, want %q", got, "payload")
	}
}

func TestMoveFileReportsAMissingSource(t *testing.T) {
	dir := t.TempDir()
	if err := MoveFile(filepath.Join(dir, "missing"), filepath.Join(dir, "moved")); err == nil {
		t.Fatal("MoveFile returned nil error for a missing source")
	}
}

// rename cannot cross a filesystem boundary, which is ordinary in a container where the
// source is on a tmpfs and the destination is not. MoveFile copies instead.
func TestMoveFileFallsBackToCopyingWhenRenameFails(t *testing.T) {
	original := renameFile
	t.Cleanup(func() { renameFile = original })
	renameFile = func(string, string) error { return errors.New("invalid cross-device link") }

	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	destination := filepath.Join(dir, "moved.txt")
	if err := os.WriteFile(source, []byte("payload"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := MoveFile(source, destination); err != nil {
		t.Fatalf("MoveFile: %v", err)
	}

	if Exists(source) {
		t.Fatal("the source survived a move that fell back to copying")
	}
	got, err := ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got != "payload" {
		t.Fatalf("moved file contains %q, want %q", got, "payload")
	}
	stat, err := Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode != "0640" {
		t.Fatalf("the fallback lost the permissions: mode %q, want %q", stat.Mode, "0640")
	}
}

// When the copy cannot work either, the source must be left where it is rather than
// removed after a half-finished move.
func TestMoveFileKeepsTheSourceWhenTheFallbackFails(t *testing.T) {
	original := renameFile
	t.Cleanup(func() { renameFile = original })
	renameFile = func(string, string) error { return errors.New("invalid cross-device link") }

	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(source, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := MoveFile(source, filepath.Join(dir, "no-such-dir", "moved.txt"))
	if err == nil {
		t.Fatal("MoveFile returned nil for a destination it cannot write")
	}
	if !Exists(source) {
		t.Fatal("the source was removed even though the move failed")
	}
}

func TestAppendFileReportsAPathItCannotOpen(t *testing.T) {
	// A directory can be opened but not written to.
	if err := AppendFile("x", t.TempDir()); err == nil {
		t.Fatal("AppendFile returned nil for a directory")
	}
}

func TestMkdirAllReportsAParentThatIsAFile(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "in-the-way")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := MkdirAll(filepath.Join(blocker, "child")); err == nil {
		t.Fatal("MkdirAll returned nil for a path whose parent is a file")
	}
}

func TestCopyFileReportsADestinationItCannotWrite(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(source, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CopyFile(source, filepath.Join(dir, "no-such-dir", "copy.txt")); err == nil {
		t.Fatal("CopyFile returned nil for a destination directory that does not exist")
	}
}
