// Package js holds the helpers exposed to Kowl scripts. Every function reports
// failures as a Go error; the binding layer turns those into JavaScript exceptions.
package js

import (
	"fmt"
	"os"
)

// ReadFile returns the whole contents of filename.
func ReadFile(filename string) (string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", filename, err)
	}
	return string(data), nil
}

// WriteFile replaces the contents of filename with value, creating it if needed.
func WriteFile(value, filename string) error {
	if err := os.WriteFile(filename, []byte(value), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", filename, err)
	}
	return nil
}

// AppendFile appends value to filename, creating it if it does not exist.
func AppendFile(value, filename string) error {
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("opening %s for append: %w", filename, err)
	}
	if _, err := f.WriteString(value); err != nil {
		f.Close()
		return fmt.Errorf("appending to %s: %w", filename, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", filename, err)
	}
	return nil
}

// RemoveFile deletes filename.
func RemoveFile(filename string) error {
	if err := os.Remove(filename); err != nil {
		return fmt.Errorf("removing %s: %w", filename, err)
	}
	return nil
}
