package util

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
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

// ErrExecTimeout is returned by ExecRunWithTimeout when the command did not
// finish before the deadline. Callers can errors.Is against it to distinguish
// a hung subprocess from a command that ran and exited non-zero.
var ErrExecTimeout = errors.New("command timed out")

// ExecRunWithTimeout runs a command in workDir bounded by a wall-clock timeout.
// Unlike ExecRun, the command runs in its own process group with a kill-on-
// cancel hook (SetProcessGroup), so when the deadline fires the ENTIRE process
// tree is SIGKILLed — not just the direct child. This prevents a hung command
// (and any orphaned grandchildren it forked) from blocking the caller forever.
//
// A hung `gt polecat nuke` was wedging the witness patrol scan indefinitely
// (futex_wait_queue, >4.5m observed) because the old ExecRun path had no
// timeout and no group-kill (gu-odhqc). On timeout this returns an error that
// wraps ErrExecTimeout.
func ExecRunWithTimeout(timeout time.Duration, workDir, cmd string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	c := exec.CommandContext(ctx, cmd, args...) //nolint:gosec // G204: callers validate args
	c.Dir = workDir
	SetProcessGroup(c) // own process group + SIGKILL whole tree on cancel

	var stderr bytes.Buffer
	c.Stderr = &stderr

	if err := c.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%q: %w after %v", cmd, ErrExecTimeout, timeout)
		}
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}
		return err
	}

	return nil
}
