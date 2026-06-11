package util

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// FirstLine returns the first non-empty line from s, trimmed of whitespace.
// Used to extract the meaningful error message from subprocess stderr, which
// often includes multi-line cobra usage text after the actual error.
func FirstLine(s string) string {
	for _, line := range strings.SplitN(s, "\n", -1) {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return strings.TrimSpace(s)
}

// ExecWithOutput runs a command in the specified directory and returns stdout.
// If the command fails, stderr content is included in the error message.
func ExecWithOutput(workDir, cmd string, args ...string) (string, error) {
	c := exec.Command(cmd, args...) //nolint:gosec // G204: callers validate args
	c.Dir = workDir
	SetDetachedProcessGroup(c) // suppress console window flash on Windows

	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr

	if err := c.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("%s", errMsg)
		}
		return "", err
	}

	return strings.TrimSpace(stdout.String()), nil
}

// ExecRun runs a command in the specified directory.
// If the command fails, stderr content is included in the error message.
func ExecRun(workDir, cmd string, args ...string) error {
	c := exec.Command(cmd, args...) //nolint:gosec // G204: callers validate args
	c.Dir = workDir
	SetDetachedProcessGroup(c) // suppress console window flash on Windows

	var stderr bytes.Buffer
	c.Stderr = &stderr

	if err := c.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}
		return err
	}

	return nil
}

// ExecRunContext is ExecRun bounded by a context. When the context's deadline
// fires (or it is canceled), the command's entire process group is killed —
// SetProcessGroup installs a SIGKILL-the-group cancel hook — so a child that
// blocks indefinitely (e.g. a `gt polecat nuke` wedged on a contended flock,
// gu-odhqc) cannot hang the caller. Returning a context-deadline error lets the
// caller distinguish a timeout from a normal nuke refusal.
func ExecRunContext(ctx context.Context, workDir, cmd string, args ...string) error {
	c := exec.CommandContext(ctx, cmd, args...) //nolint:gosec // G204: callers validate args
	c.Dir = workDir
	SetProcessGroup(c) // group-kill the whole tree on context cancel/timeout

	var stderr bytes.Buffer
	c.Stderr = &stderr

	if err := c.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%s %s: %w", cmd, strings.Join(args, " "), ctxErr)
		}
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}
		return err
	}

	return nil
}
