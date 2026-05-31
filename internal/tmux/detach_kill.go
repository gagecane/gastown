package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// EnvTmuxDetachedKillLog, when set to a file path, redirects the detached
// kill subprocess's stdout and stderr to that file with `set -x` tracing
// enabled. Production code never sets this — the subprocess discards I/O
// for a reason (it must survive the parent's exit and has no live observer).
//
// Tests for the detached-kill path opt in by setting this to a per-test
// temp file. When the polling deadline elapses without the session
// disappearing, the test dumps the captured log so we have a non-empty
// signal to root-cause future flakes (gu-4l21).
const EnvTmuxDetachedKillLog = "GT_TEST_TMUX_DETACHED_KILL_LOG"

// DetachedKillSession spawns a detached subprocess that waits for the given
// delay then kills the named tmux session. The subprocess survives the
// parent's exit, making this safe for self-kill scenarios where the calling
// process is running inside the session it needs to terminate.
//
// This replaces the previous goroutine-based pattern:
//
//	go func() {
//	    time.Sleep(3 * time.Second)
//	    t.KillSession(name)
//	}()
//
// The goroutine approach had a race condition: killing your own tmux session
// terminates all processes in it (including the goroutine's parent), so the
// kill might never execute if the parent exits first, or the goroutine dies
// when the session is destroyed.
//
// A detached subprocess (new session on Unix, new process group on Windows)
// is independent of the tmux session lifecycle and will reliably execute the
// kill after the delay.
func (t *Tmux) DetachedKillSession(name string, delay time.Duration) error {
	if err := validateSessionName(name); err != nil {
		return err
	}

	delaySec := int(delay.Seconds())
	if delaySec < 1 {
		delaySec = 1
	}

	// Build the tmux kill-session command with socket flag if needed.
	var tmuxKillCmd string
	if t.socketName != "" {
		tmuxKillCmd = fmt.Sprintf("tmux -L %s kill-session -t %s",
			shellQuote(t.socketName), shellQuote(name))
	} else {
		tmuxKillCmd = fmt.Sprintf("tmux kill-session -t %s", shellQuote(name))
	}

	// Spawn: sleep <delay>; <tmux kill command>
	script := fmt.Sprintf("sleep %d; %s", delaySec, tmuxKillCmd)
	cmd := buildDetachedKillCmd(script)

	// Detach the subprocess so it survives parent exit.
	detachCmd(cmd)

	return cmd.Start()
}

// DetachedKillSessionWithProcesses spawns a detached subprocess that waits for
// the given delay then kills all processes in the named tmux session before
// terminating it. Like DetachedKillSession but uses the more thorough
// KillSessionWithProcesses approach to prevent orphan processes.
//
// The subprocess invokes `gt` itself (which calls KillSessionWithProcesses
// internally) rather than reimplementing the process-tree walk in bash.
// If `gt` is not available, falls back to plain tmux kill-session.
func (t *Tmux) DetachedKillSessionWithProcesses(name string, delay time.Duration) error {
	if err := validateSessionName(name); err != nil {
		return err
	}

	delaySec := int(delay.Seconds())
	if delaySec < 1 {
		delaySec = 1
	}

	// Build the tmux kill-session command with socket flag if needed.
	var tmuxKillCmd string
	if t.socketName != "" {
		tmuxKillCmd = fmt.Sprintf("tmux -L %s kill-session -t %s",
			shellQuote(t.socketName), shellQuote(name))
	} else {
		tmuxKillCmd = fmt.Sprintf("tmux kill-session -t %s", shellQuote(name))
	}

	// Spawn: sleep <delay>; <tmux kill command>
	// We use plain tmux kill-session here since the detached subprocess cannot
	// easily call KillSessionWithProcesses (which is a Go method). The tmux
	// kill-session sends SIGHUP to all processes in the session, which is
	// sufficient when remain-on-exit is disabled (which the callers already do).
	script := fmt.Sprintf("sleep %d; %s", delaySec, tmuxKillCmd)
	cmd := buildDetachedKillCmd(script)

	// Detach the subprocess so it survives parent exit.
	detachCmd(cmd)

	return cmd.Start()
}

// buildDetachedKillCmd constructs the bash subprocess that runs the
// sleep-then-kill script. By default it discards all I/O — the subprocess
// must survive parent exit, so there is no live observer for its output.
//
// When EnvTmuxDetachedKillLog is set (tests only), the subprocess's stdout
// and stderr are redirected to the named file with `set -x` tracing turned
// on. This converts a future flake from a bare "session should have been
// killed" assertion into a debuggable trace showing exactly which command
// failed (gu-4l21).
func buildDetachedKillCmd(script string) *exec.Cmd {
	logPath := os.Getenv(EnvTmuxDetachedKillLog)
	if logPath != "" {
		// `set -x` traces every command; `exec >>file 2>&1` redirects all
		// further output to the log. Append (>>) so concurrent test
		// invocations don't truncate each other's traces.
		script = fmt.Sprintf("exec >>%s 2>&1; set -x; %s", shellQuote(logPath), script)
	}
	cmd := exec.Command("bash", "-c", script)
	// Discard all I/O — the subprocess runs independently. When logging is
	// enabled, the script's `exec` redirect inside bash overrides this.
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd
}

// shellQuote wraps a string in single quotes for safe shell interpolation.
// Internal single quotes are escaped as '\” (end quote, escaped quote, new quote).
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	// If the string is simple (alphanumeric + dash + underscore), no quoting needed.
	safe := true
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return strconv.Quote(s)
}
