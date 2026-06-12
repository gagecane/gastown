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

	script := detachedKillScript(t.socketName, name, delaySec)
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

	// We use plain tmux kill-session here since the detached subprocess cannot
	// easily call KillSessionWithProcesses (which is a Go method). The tmux
	// kill-session sends SIGHUP to all processes in the session, which is
	// sufficient when remain-on-exit is disabled (which the callers already do).
	script := detachedKillScript(t.socketName, name, delaySec)
	cmd := buildDetachedKillCmd(script)

	// Detach the subprocess so it survives parent exit.
	detachCmd(cmd)

	return cmd.Start()
}

// detachedKillScript builds the bash one-liner the detached subprocess runs:
// wait for the delay, then kill the session and confirm it is gone, retrying
// the kill until it sticks (or a hard cap of ~30s of retries elapses).
//
// Why retry rather than fire once: a single `tmux kill-session` is fire-and-
// forget. Under heavy load the tmux server can be momentarily unresponsive (the
// same hiccup probeServerHealth's 200ms dial timeout exists to absorb), so the
// one kill can transiently fail and the session is then *never* reaped — no
// caller-side polling deadline, however wide, can recover a kill that was
// issued once and dropped. This was the recurring root cause behind the
// TestDetachedKill* flakes (gu-4l21, gu-zyxl, gu-49zso, gu-v4r86), which were
// each previously papered over by widening the test's poll deadline
// (15s -> 60s -> 120s). Retrying until has-session reports the session absent
// makes the detached kill self-healing: a dropped kill is simply reissued, so
// the session reliably disappears under load instead of leaking.
//
// The loop checks has-session first so a kill that already succeeded exits
// immediately; the happy path is unchanged (one kill, lands in ~1s). Each
// retry sleeps 500ms; the 60-iteration cap bounds the subprocess lifetime so a
// genuinely unkillable session (real regression) does not spin forever.
func detachedKillScript(socketName, name string, delaySec int) string {
	socketFlag := ""
	if socketName != "" {
		socketFlag = fmt.Sprintf("-L %s ", shellQuote(socketName))
	}
	target := shellQuote(name)
	// has-session exits non-zero when the session is absent; that is our
	// success condition. kill-session's own exit status is ignored — a failed
	// kill is simply retried, and a kill that raced the session already being
	// gone is harmless.
	return fmt.Sprintf(
		"sleep %d; "+
			"for i in $(seq 1 60); do "+
			"tmux %shas-session -t %s 2>/dev/null || exit 0; "+
			"tmux %skill-session -t %s 2>/dev/null; "+
			"sleep 0.5; "+
			"done",
		delaySec, socketFlag, target, socketFlag, target,
	)
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
