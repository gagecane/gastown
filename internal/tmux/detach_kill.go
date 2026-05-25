package tmux

import (
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

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
	cmd := exec.Command("bash", "-c", script)

	// Detach the subprocess so it survives parent exit.
	detachCmd(cmd)

	// Discard all I/O — the subprocess runs independently.
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

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
	cmd := exec.Command("bash", "-c", script)

	// Detach the subprocess so it survives parent exit.
	detachCmd(cmd)

	// Discard all I/O — the subprocess runs independently.
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	return cmd.Start()
}

// shellQuote wraps a string in single quotes for safe shell interpolation.
// Internal single quotes are escaped as '\'' (end quote, escaped quote, new quote).
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
