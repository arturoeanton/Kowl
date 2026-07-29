package js

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// ExecResult is the outcome of a command that ran to completion.
type ExecResult struct {
	Stdout string
	Stderr string
	// ExitCode is the process exit status. It is non-zero for a command that ran and
	// failed, which is not reported as an error: the caller decides whether it matters,
	// and Stdout and Stderr are preserved either way.
	ExitCode int
	// Truncated is true when output exceeded the limit and was cut short.
	Truncated bool
}

// Exec runs name with arg and returns its output. An error is returned only when the
// command could not be run at all, or when ctx expired before it finished.
func Exec(ctx context.Context, limit int, name string, arg ...string) (ExecResult, error) {
	cmd := exec.CommandContext(ctx, name, arg...)
	stdout := &limitedBuffer{limit: limit}
	stderr := &limitedBuffer{limit: limit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	result := ExecResult{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		Truncated: stdout.truncated || stderr.truncated,
	}

	switch {
	case err == nil:
		return result, nil
	case ctx.Err() != nil:
		return result, fmt.Errorf("running %s: %w", name, ctx.Err())
	default:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// The command ran and reported failure. That is a result, not an error.
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, fmt.Errorf("running %s: %w", name, err)
	}
}

// limitedBuffer collects output up to limit bytes and then discards the rest, so a
// runaway command cannot exhaust memory.
type limitedBuffer struct {
	limit     int
	data      []byte
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - len(b.data); room > 0 {
		if len(p) <= room {
			b.data = append(b.data, p...)
		} else {
			b.data = append(b.data, p[:room]...)
			b.truncated = true
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	// Report a full write so the command is never blocked by our limit.
	return len(p), nil
}

func (b *limitedBuffer) String() string { return string(b.data) }
