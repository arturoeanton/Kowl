package js

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// FileStat describes a path on disk.
type FileStat struct {
	Path    string
	Name    string
	Dir     string
	Size    int64
	Mode    string
	ModTime time.Time
	IsDir   bool
}

// DirEntry is one item inside a directory.
type DirEntry struct {
	Name  string
	Path  string
	Size  int64
	IsDir bool
}

// Exists reports whether a path is there. Anything that is not a "does not exist" error
// still counts as present: the path is real, it just cannot be read from here.
func Exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil || !os.IsNotExist(err)
}

// Stat describes one path.
func Stat(path string) (FileStat, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileStat{}, fmt.Errorf("stat %s: %w", path, err)
	}
	return FileStat{
		Path:    path,
		Name:    info.Name(),
		Dir:     filepath.Dir(path),
		Size:    info.Size(),
		Mode:    fmt.Sprintf("%04o", info.Mode().Perm()),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}, nil
}

// ListDir returns the entries of a directory, sorted by name.
func ListDir(path string) ([]DirEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", path, err)
	}

	listed := make([]DirEntry, 0, len(entries))
	for _, entry := range entries {
		item := DirEntry{
			Name:  entry.Name(),
			Path:  filepath.Join(path, entry.Name()),
			IsDir: entry.IsDir(),
		}
		// A dangling symlink still belongs in the listing; it just has no size.
		if info, err := entry.Info(); err == nil {
			item.Size = info.Size()
		}
		listed = append(listed, item)
	}
	sort.Slice(listed, func(i, j int) bool { return listed[i].Name < listed[j].Name })
	return listed, nil
}

// Glob returns the paths matching a pattern, sorted.
func Glob(pattern string) ([]string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("bad pattern %q: %w", pattern, err)
	}
	sort.Strings(matches)
	if matches == nil {
		// An empty result reads better as an empty list than as null.
		matches = []string{}
	}
	return matches, nil
}

// MkdirAll creates a directory and every parent it needs.
func MkdirAll(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	return nil
}

// RemoveAll deletes a path and, if it is a directory, everything below it. Unlike
// RemoveFile it does not complain when the path is already gone.
func RemoveAll(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	return nil
}

// CopyFile copies a regular file, preserving its permissions. The destination is
// replaced if it exists; its directory must already be there.
func CopyFile(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("copying %s: %w", source, err)
	}
	if info.IsDir() {
		return fmt.Errorf("copying %s: is a directory", source)
	}

	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("copying %s: %w", source, err)
	}
	defer in.Close()

	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("copying to %s: %w", destination, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copying to %s: %w", destination, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", destination, err)
	}
	return nil
}

// MoveFile renames a file, falling back to a copy when the two paths are on different
// filesystems and rename cannot cross the boundary.
func MoveFile(source, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	}
	if err := CopyFile(source, destination); err != nil {
		return fmt.Errorf("moving %s: %w", source, err)
	}
	if err := os.Remove(source); err != nil {
		return fmt.Errorf("moving %s: %w", source, err)
	}
	return nil
}
