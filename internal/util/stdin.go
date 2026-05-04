// ABOUTME: Timeout-safe stdin helpers for agent-reachable prompt callsites.
// ABOUTME: Fixes the pty-hang class documented in gt-ube24.

package util

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrStdinTimeout is returned when no data arrives on stdin before the timeout.
// Callers MUST treat this as "non-interactive: use defaults", not as a hard failure.
var ErrStdinTimeout = errors.New("stdin read timed out")

// DefaultStdinTimeout is the default wait before falling through to defaults.
// 2s matches typical agent response latency while still feeling snappy for
// humans who fat-finger a command without `-y`. Override via GT_STDIN_TIMEOUT_MS.
const DefaultStdinTimeout = 2 * time.Second

// AgentRoleEnv is the env var that signals "running inside an agent session".
// When set (non-empty), timeout-safe helpers short-circuit to the default
// answer immediately — no wait, no prompt shown.
const AgentRoleEnv = "GT_ROLE"

// StdinTimeoutEnv overrides DefaultStdinTimeout. Value is in milliseconds.
const StdinTimeoutEnv = "GT_STDIN_TIMEOUT_MS"

// stdinReader is the stdin source used by the helpers. Tests override this to
// inject a pipe / fake reader without touching the real os.Stdin.
var stdinReader io.Reader = os.Stdin

// stdoutWriter is where PromptYesNoWithTimeout writes the prompt text.
// Tests override this to capture output.
var stdoutWriter io.Writer = os.Stdout

// isAgentContext reports whether the current process is running inside an
// agent session (GT_ROLE set to a non-empty value).
//
// See gt-ube24: under LLM runtimes, term.IsTerminal(os.Stdin) returns true
// because the pty is real — but no human is there to type. GT_ROLE is the
// only reliable out-of-band signal we have.
func isAgentContext() bool {
	return os.Getenv(AgentRoleEnv) != ""
}

// resolveTimeout returns the effective timeout. Honors GT_STDIN_TIMEOUT_MS if
// set to a parseable integer, otherwise returns the caller-supplied value.
// A zero or negative env value disables the timeout (blocks forever), but
// that's only useful for debugging — agents should never set it that way.
func resolveTimeout(caller time.Duration) time.Duration {
	if raw := os.Getenv(StdinTimeoutEnv); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return caller
}

// ReadStdinLineWithTimeout reads a single line from stdin. If no data arrives
// within timeout, returns ("", ErrStdinTimeout). The trailing newline is
// stripped from the returned string.
//
// Under LLM runtimes (kiro-cli, etc.), os.Stdin often looks like an interactive
// pty but no human is there to type. A blocking ReadString hangs the entire
// agent session. This helper fires a goroutine to do the read and races it
// against a timer — on timeout, the caller falls through to non-interactive
// defaults.
//
// The goroutine leaks when the timeout wins (it's still blocked on ReadString
// and we have no portable way to interrupt it). This is acceptable because
// callers are short-lived CLI commands that exit shortly after the read, at
// which point the process teardown reaps the goroutine.
//
// See gt-ube24 for the full pty-hang audit.
func ReadStdinLineWithTimeout(timeout time.Duration) (string, error) {
	timeout = resolveTimeout(timeout)

	type readResult struct {
		line string
		err  error
	}
	ch := make(chan readResult, 1)

	// Snapshot stdinReader into a local before launching the goroutine.
	// When the timeout wins, the goroutine may still be blocked on
	// ReadString; tests restore stdinReader via t.Cleanup, which would
	// otherwise race with the goroutine's read of the package-level var.
	// The goroutine outliving the caller is documented (see godoc above);
	// the package-level var access is not — and the race detector rightly
	// flags it. Fix: the goroutine only touches its own local.
	src := stdinReader

	go func() {
		reader := bufio.NewReader(src)
		line, err := reader.ReadString('\n')
		ch <- readResult{line: line, err: err}
	}()

	// A zero/negative timeout means "block forever". Honor that for the
	// GT_STDIN_TIMEOUT_MS=0 escape hatch, though production code should
	// always pass a positive value.
	if timeout <= 0 {
		r := <-ch
		return strings.TrimRight(r.line, "\r\n"), r.err
	}

	select {
	case r := <-ch:
		// Treat EOF-with-no-data the same as timeout so callers can't be
		// tricked into "user pressed enter with empty line" when the pipe
		// just closed.
		if r.err != nil && r.line == "" {
			return "", r.err
		}
		return strings.TrimRight(r.line, "\r\n"), nil
	case <-time.After(timeout):
		return "", ErrStdinTimeout
	}
}

// PromptYesNoWithTimeout prints question to stdout and reads one line with
// timeout. On timeout or EOF, returns defaultAnswer. Answers starting with
// 'y' (case-insensitive) are true, everything else is false.
//
// If running inside an agent context (GT_ROLE set), short-circuits to
// defaultAnswer immediately — no wait, no prompt shown. This keeps
// interactive humans' UX unchanged while making agent context fail-closed
// on the "did they forget -y" axis.
func PromptYesNoWithTimeout(question string, defaultAnswer bool, timeout time.Duration) bool {
	// Agent context: skip the prompt entirely. No wait, no output.
	if isAgentContext() {
		return defaultAnswer
	}

	suffix := "[y/N]"
	if defaultAnswer {
		suffix = "[Y/n]"
	}
	fmt.Fprintf(stdoutWriter, "%s %s: ", question, suffix)

	line, err := ReadStdinLineWithTimeout(timeout)
	if err != nil {
		// Timeout or EOF: fall through to the default. Print a newline so
		// the cursor doesn't sit on the prompt line in human runs that
		// happen to time out (rare — humans usually type or Ctrl-C).
		fmt.Fprintln(stdoutWriter)
		return defaultAnswer
	}

	answer := strings.ToLower(strings.TrimSpace(line))
	if answer == "" {
		return defaultAnswer
	}
	return answer == "y" || answer == "yes"
}
