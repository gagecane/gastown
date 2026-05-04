package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/telemetry"
)

// NewSession creates a new detached tmux session.
func (t *Tmux) NewSession(name, workDir string) error {
	if err := validateSessionName(name); err != nil {
		return err
	}
	args := []string{"new-session", "-d", "-s", name}
	if workDir != "" {
		args = append(args, "-c", workDir)
	}
	if _, err := t.run(args...); err != nil {
		return err
	}
	// tmux 3.3+ sets window-size=manual on detached sessions (no client present),
	// which locks the window at 80x24 even after a client attaches. Override to
	// "latest" so the window auto-resizes to the attaching client's terminal size.
	_, _ = t.run("set-option", "-wt", name, "window-size", "latest")
	return nil
}

// NewSessionWithCommand creates a new detached tmux session that immediately runs a command.
// Unlike NewSession + SendKeys, this avoids race conditions where the shell isn't ready
// or the command arrives before the shell prompt. The command runs directly as the
// initial process of the pane.
//
// Validates workDir (if non-empty) exists and is a directory. After creation, performs
// a brief health check to catch immediate command failures (binary not found, syntax
// errors, etc.) so callers get an error instead of a silently dead session.
// See: https://github.com/anthropics/gastown/issues/280
func (t *Tmux) NewSessionWithCommand(name, workDir, command string) error {
	if err := validateSessionName(name); err != nil {
		return err
	}
	if workDir != "" {
		info, err := os.Stat(workDir)
		if err != nil {
			return fmt.Errorf("invalid work directory %q: %w", workDir, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("work directory %q is not a directory", workDir)
		}
	}
	if err := validateCommandBinary(command); err != nil {
		return err
	}

	// Defense-in-depth: remove CLAUDECODE from the tmux server's global
	// environment so new sessions don't inherit it. Claude Code sets this
	// variable on startup and the tmux server inherits it if started from
	// within a Claude Code session. This causes nested-session detection
	// failures in all subsequently created sessions.
	_, _ = t.run("set-environment", "-g", "-u", "CLAUDECODE")

	// Two-step creation: create session with default shell first, configure
	// remain-on-exit, then replace the shell with the actual command. This
	// eliminates the race between command exit and health check setup.
	args := []string{"new-session", "-d", "-s", name}
	if workDir != "" {
		args = append(args, "-c", workDir)
	}
	if _, err := t.run(args...); err != nil {
		return err
	}
	// tmux 3.3+ sets window-size=manual on detached sessions (no client present),
	// which locks the window at 80x24 even after a client attaches. Override to
	// "latest" so the window auto-resizes to the attaching client's terminal size.
	_, _ = t.run("set-option", "-wt", name, "window-size", "latest")

	// Enable remain-on-exit BEFORE command runs so we can inspect exit status
	_, _ = t.run("set-option", "-t", name, "remain-on-exit", "on")

	// Replace the initial shell with the actual command.
	// On Windows (psmux), respawn-pane doesn't support passing a command
	// argument, so we use send-keys to type the command into the shell.
	if runtime.GOOS == "windows" {
		if _, err := t.run("send-keys", "-t", name, command, "Enter"); err != nil {
			_ = t.KillSession(name)
			return fmt.Errorf("failed to send command in session %q: %w", name, err)
		}
	} else {
		respawnArgs := []string{"respawn-pane", "-k", "-t", name}
		if workDir != "" {
			respawnArgs = append(respawnArgs, "-c", workDir)
		}
		respawnArgs = append(respawnArgs, command)
		if _, err := t.run(respawnArgs...); err != nil {
			_ = t.KillSession(name)
			return fmt.Errorf("failed to start command in session %q: %w", name, err)
		}
	}

	return t.checkSessionAfterCreate(name, command)
}

// NewSessionWithCommandAndEnv creates a new detached tmux session with environment
// variables set via -e flags. This ensures the initial shell process inherits the
// correct environment from the session, rather than inheriting from the tmux server
// or parent process. The -e flags set session-level environment before the shell
// starts, preventing stale env vars (e.g., GT_ROLE from a parent mayor session)
// from leaking into crew/polecat shells.
//
// The command should still use 'exec env' for WaitForCommand detection compatibility,
// but -e provides defense-in-depth for the initial shell environment.
// Requires tmux >= 3.2.
func (t *Tmux) NewSessionWithCommandAndEnv(name, workDir, command string, env map[string]string) error {
	if err := validateSessionName(name); err != nil {
		return err
	}

	// Kill stale same-named sessions on other sockets to prevent split-brain.
	// This is best-effort: failures are silently ignored.
	t.killSplitBrainSession(name)

	if workDir != "" {
		info, err := os.Stat(workDir)
		if err != nil {
			return fmt.Errorf("invalid work directory %q: %w", workDir, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("work directory %q is not a directory", workDir)
		}
	}
	if err := validateCommandBinary(command); err != nil {
		return err
	}

	// Two-step creation: create session with env vars and default shell, then
	// replace the shell with the actual command after configuring remain-on-exit.
	args := []string{"new-session", "-d", "-s", name}
	if workDir != "" {
		args = append(args, "-c", workDir)
	}
	// Add -e flags to set environment variables in the session before the shell starts.
	// Keys are sorted for deterministic behavior.
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, env[k]))
	}
	if _, err := t.run(args...); err != nil {
		return err
	}
	// tmux 3.3+ sets window-size=manual on detached sessions (no client present),
	// which locks the window at 80x24 even after a client attaches. Override to
	// "latest" so the window auto-resizes to the attaching client's terminal size.
	_, _ = t.run("set-option", "-wt", name, "window-size", "latest")

	// Enable remain-on-exit BEFORE command runs so we can inspect exit status
	_, _ = t.run("set-option", "-t", name, "remain-on-exit", "on")

	// Replace the initial shell with the actual command.
	if runtime.GOOS == "windows" {
		if _, err := t.run("send-keys", "-t", name, command, "Enter"); err != nil {
			_ = t.KillSession(name)
			return fmt.Errorf("failed to send command in session %q: %w", name, err)
		}
	} else {
		respawnArgs := []string{"respawn-pane", "-k", "-t", name}
		if workDir != "" {
			respawnArgs = append(respawnArgs, "-c", workDir)
		}
		respawnArgs = append(respawnArgs, command)
		if _, err := t.run(respawnArgs...); err != nil {
			_ = t.KillSession(name)
			return fmt.Errorf("failed to start command in session %q: %w", name, err)
		}
	}

	return t.checkSessionAfterCreate(name, command)
}

// checkSessionAfterCreate verifies that a newly created session's command didn't
// fail immediately (binary not found, syntax error, etc.). Expects remain-on-exit
// to already be enabled on the session. Checks the exit status after a brief delay.
//
// Returns an error for ANY early exit within the 250ms startup window —
// including status=0 (clean exit). Callers of this helper spawn long-lived
// agent processes (kiro-cli, claude-code, shells); none should exit cleanly
// in 250ms. The only case where that happens in practice is a daemon-spawned
// dog whose agent dies on startup before the hook runs (gu-hq88, gt-ltnxs).
// Returning success on early clean exits caused the pane to be destroyed
// silently, and downstream VerifySurvived() then reported an opaque "died
// during startup" with no pane contents — the exact diagnostic gap gu-klwv
// was trying to close but could not, because by the time DiagnosticCapture
// ran the session was already gone.
//
// To preserve what little signal the dying pane produced, this helper now
// captures pane scrollback BEFORE destroying the session and surfaces it in
// the returned error. Callers that already wrap errors with extra context
// (session/lifecycle.go capturePaneDiagnostic) will see the tmux-level
// capture arrive via the wrapped error rather than an empty string.
func (t *Tmux) checkSessionAfterCreate(name, command string) error {
	// snapshotAndKill grabs pane scrollback before destroying the session so
	// the error message carries whatever the dying process wrote to stderr.
	// Bounded at checkPaneDiagBytes to keep error lines from blowing up daemon.log.
	snapshotAndKill := func() string {
		raw, _ := t.CapturePaneAll(name)
		trimmed := strings.TrimSpace(raw)
		if len(trimmed) > checkPaneDiagBytes {
			trimmed = "…" + trimmed[len(trimmed)-checkPaneDiagBytes:]
		}
		_ = t.KillSession(name)
		return trimmed
	}

	checkPaneDead := func() (bool, error) {
		paneDead, _ := t.run("display-message", "-p", "-t", name, "#{pane_dead}")
		if strings.TrimSpace(paneDead) != "1" {
			return false, nil
		}
		exitStatus, _ := t.run("display-message", "-p", "-t", name, "#{pane_dead_status}")
		status := strings.TrimSpace(exitStatus)
		if status == "" {
			status = "?"
		}
		diag := snapshotAndKill()
		if diag != "" {
			return true, fmt.Errorf("session %q: command exited early with status %s: %s\n--- pane output ---\n%s\n--- end pane output ---", name, status, command, diag)
		}
		return true, fmt.Errorf("session %q: command exited early with status %s: %s (pane produced no output)", name, status, command)
	}

	// First check at 50ms: catches fast failures on lightly-loaded runners.
	time.Sleep(50 * time.Millisecond)
	if dead, err := checkPaneDead(); dead {
		return err
	}

	// Second check at 250ms: catches exec failures on loaded CI runners where
	// process startup takes longer than 50ms. This is the fix for CI getting
	// false negatives on TestNewSessionWithCommand_ExecEnvBadBinary. Normal
	// long-lived sessions (Claude, shell) will still be alive here and return nil.
	time.Sleep(200 * time.Millisecond)
	if dead, err := checkPaneDead(); dead {
		return err
	}

	// Pane is alive — restore default (no need to keep dead sessions around)
	_, _ = t.run("set-option", "-t", name, "remain-on-exit", "off")
	return nil
}

// checkPaneDiagBytes bounds the pane scrollback surfaced in early-exit errors
// so daemon.log lines stay readable. Matches the diagPaneCaptureBytes budget
// used by session/lifecycle.go capturePaneDiagnostic for consistency.
const checkPaneDiagBytes = 2048

// EnsureSessionFresh ensures a session is available and healthy.
// If the session exists but is a zombie (Claude not running), it kills the session first.
// This prevents "session already exists" errors when trying to restart dead agents.
//
// A session is considered a zombie if:
// - The tmux session exists
// - But Claude (node process) is not running in it
//
// Uses create-first approach to avoid TOCTOU race conditions in multi-agent
// environments where another agent could create the same session between a
// check and create call.
//
// Returns nil if session was created successfully or already exists with a running agent.
func (t *Tmux) EnsureSessionFresh(name, workDir string) error {
	if err := validateSessionName(name); err != nil {
		return err
	}

	// Try to create the session first (atomic — avoids check-then-create race)
	err := t.NewSession(name, workDir)
	if err == nil {
		return nil // Created successfully
	}
	if !errors.Is(err, ErrSessionExists) {
		return fmt.Errorf("creating session: %w", err)
	}

	// Session already exists — check if it's a zombie
	if t.IsAgentRunning(name) {
		// Session is healthy (agent running) — nothing to do
		return nil
	}

	// Zombie session: tmux alive but agent dead
	// Kill it so we can create a fresh one
	// Use KillSessionWithProcesses to ensure all descendant processes are killed
	if err := t.KillSessionWithProcesses(name); err != nil {
		return fmt.Errorf("killing zombie session: %w", err)
	}

	// Create fresh session (handle race: another agent may have created it
	// between our kill and this create — that's fine, treat as success)
	err = t.NewSession(name, workDir)
	if errors.Is(err, ErrSessionExists) {
		return nil
	}
	return err
}

// EnsureSessionFreshWithCommand is like EnsureSessionFresh but creates the
// session with a command as the pane's initial process via NewSessionWithCommand.
// This eliminates the race condition in the EnsureSessionFresh + SendKeys pattern
// where the shell may not be ready to receive keystrokes, resulting in empty
// windows. The command runs as the pane's initial process — no shell involved.
//
// If an existing session has a healthy agent, returns ErrSessionRunning.
func (t *Tmux) EnsureSessionFreshWithCommand(name, workDir, command string) error {
	if err := validateSessionName(name); err != nil {
		return err
	}

	// Check if session exists
	running, err := t.HasSession(name)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if running {
		if t.IsAgentRunning(name) {
			// Session is healthy — don't replace it
			return ErrSessionRunning
		}
		// Zombie session: tmux alive but agent dead — kill it
		if err := t.KillSessionWithProcesses(name); err != nil {
			return fmt.Errorf("killing zombie session: %w", err)
		}
	}

	// Create session with command as the initial process
	return t.NewSessionWithCommand(name, workDir, command)
}

// EnsureSessionFreshWithCommandAndEnv is like EnsureSessionFreshWithCommand but
// also seeds the session's environment via tmux -e flags. The -e flags set
// session-level env BEFORE the shell starts, so the initial pane (and any
// subprocesses the agent spawns, e.g. bd) inherit it. SetEnvironment after
// creation only affects newly spawned panes — not the running pane's
// subprocess tree (gt-neycp).
//
// If an existing session has a healthy agent, returns ErrSessionRunning.
func (t *Tmux) EnsureSessionFreshWithCommandAndEnv(name, workDir, command string, env map[string]string) error {
	if err := validateSessionName(name); err != nil {
		return err
	}

	running, err := t.HasSession(name)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if running {
		if t.IsAgentRunning(name) {
			return ErrSessionRunning
		}
		if err := t.KillSessionWithProcesses(name); err != nil {
			return fmt.Errorf("killing zombie session: %w", err)
		}
	}

	return t.NewSessionWithCommandAndEnv(name, workDir, command, env)
}

// KillSession terminates a tmux session. Idempotent: returns nil if the
// session is already gone or there is no tmux server.
func (t *Tmux) KillSession(name string) (retErr error) {
	defer func() { telemetry.RecordSessionStop(context.Background(), name, retErr) }()
	_, retErr = t.run("kill-session", "-t", name)
	if errors.Is(retErr, ErrSessionNotFound) || errors.Is(retErr, ErrNoServer) {
		retErr = nil
	}
	return retErr
}

// processKillGracePeriod is how long to wait after SIGTERM before sending SIGKILL.
// 2 seconds gives processes time to clean up gracefully. The previous 100ms was too short
// and caused Claude processes to become orphans when they couldn't shut down in time.
const processKillGracePeriod = 2 * time.Second

// KillSessionWithProcesses explicitly kills all processes in a session before terminating it.
// This prevents orphan processes that survive tmux kill-session due to SIGHUP being ignored.
//
// Process:
// 1. Get the pane's main process PID and its process group ID (PGID)
// 2. Kill the entire process group (catches reparented processes that stayed in the group)
// 3. Find all descendant processes recursively (catches any stragglers)
// 4. Send SIGTERM/SIGKILL to descendants
// 5. Kill the pane process itself
// 6. Kill the tmux session
//
// The process group kill is critical because:
// - pgrep -P only finds direct children (PPID matching)
// - Processes that reparent to init (PID 1) are missed by pgrep
// - But they typically stay in the same process group unless they call setsid()
//
// This ensures Claude processes and all their children are properly terminated.
func (t *Tmux) KillSessionWithProcesses(name string) error {
	// Disarm auto-respawn BEFORE killing anything. The pane-died hook would
	// otherwise respawn the process 3 seconds after we kill it, creating a
	// zombie that fights every kill attempt.
	_ = t.SetRemainOnExit(name, false)
	_, _ = t.run("set-hook", "-t", name, "-u", "pane-died")

	// Get the pane PID
	pid, err := t.GetPanePID(name)
	if err != nil {
		// Session might not exist or server may have already gone away.
		killErr := t.KillSession(name)
		if killErr == nil || errors.Is(killErr, ErrSessionNotFound) || errors.Is(killErr, ErrNoServer) {
			return nil
		}
		return killErr
	}

	if pid != "" {
		// Walk the process tree for all descendants (catches processes that
		// called setsid() and created their own process groups)
		descendants := getAllDescendants(pid)

		// Build known PID set for group membership verification
		knownPIDs := make(map[string]bool, len(descendants)+1)
		knownPIDs[pid] = true
		for _, d := range descendants {
			knownPIDs[d] = true
		}

		// Find reparented processes from our process group. Instead of killing
		// the entire group blindly with syscall.Kill(-pgid, ...) — which could
		// hit unrelated processes sharing the same PGID — we enumerate group
		// members and only include those reparented to init (PPID == 1), which
		// indicates they were likely children in our tree that outlived their parent.
		pgid := getProcessGroupID(pid)
		if pgid != "" && pgid != "0" && pgid != "1" {
			reparented := collectReparentedGroupMembers(pgid, knownPIDs)
			descendants = append(descendants, reparented...)
		}

		// Send SIGTERM to all descendants (deepest first to avoid orphaning)
		for _, dpid := range descendants {
			_ = exec.Command("kill", "-TERM", dpid).Run()
		}

		// Wait for graceful shutdown (2s gives processes time to clean up)
		time.Sleep(processKillGracePeriod)

		// Send SIGKILL to any remaining descendants
		for _, dpid := range descendants {
			_ = exec.Command("kill", "-KILL", dpid).Run()
		}

		// Kill the pane process itself (may have called setsid() and detached)
		_ = exec.Command("kill", "-TERM", pid).Run()
		time.Sleep(processKillGracePeriod)
		_ = exec.Command("kill", "-KILL", pid).Run()
	}

	// Kill the tmux session
	// Ignore missing/dead-server errors - killing the pane process may have
	// already caused tmux to destroy the session automatically.
	err = t.KillSession(name)
	if errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrNoServer) {
		return nil
	}
	return err
}

// KillSessionWithProcessesExcluding is like KillSessionWithProcesses but excludes
// specified PIDs from being killed. This is essential for self-kill scenarios where
// the calling process (e.g., gt done) is running inside the session it's terminating.
// Without exclusion, the caller would be killed before completing the cleanup.
func (t *Tmux) KillSessionWithProcessesExcluding(name string, excludePIDs []string) error {
	// Disarm auto-respawn BEFORE killing anything (same as KillSessionWithProcesses).
	_ = t.SetRemainOnExit(name, false)
	_, _ = t.run("set-hook", "-t", name, "-u", "pane-died")

	// Build exclusion set for O(1) lookup
	exclude := make(map[string]bool)
	for _, pid := range excludePIDs {
		exclude[pid] = true
	}

	// Get the pane PID
	pid, err := t.GetPanePID(name)
	if err != nil {
		// Session might not exist or server may have already gone away.
		killErr := t.KillSession(name)
		if killErr == nil || errors.Is(killErr, ErrSessionNotFound) || errors.Is(killErr, ErrNoServer) {
			return nil
		}
		return killErr
	}

	if pid != "" {
		// Get the process group ID
		pgid := getProcessGroupID(pid)

		// Collect all PIDs to kill (from multiple sources)
		toKill := make(map[string]bool)

		// 1. Get all descendant PIDs recursively (catches processes that called setsid())
		descendants := getAllDescendants(pid)

		// Build known PID set for group membership verification
		knownPIDs := make(map[string]bool, len(descendants)+1)
		knownPIDs[pid] = true
		for _, dpid := range descendants {
			if !exclude[dpid] {
				toKill[dpid] = true
			}
			knownPIDs[dpid] = true
		}

		// 2. Get verified process group members (only reparented-to-init processes).
		// Instead of adding ALL group members — which could include unrelated
		// processes sharing the same PGID — we only add those that were reparented
		// to init (PPID == 1), indicating they were likely children in our tree.
		if pgid != "" && pgid != "0" && pgid != "1" {
			for _, member := range collectReparentedGroupMembers(pgid, knownPIDs) {
				if !exclude[member] {
					toKill[member] = true
				}
			}
		}

		// Convert to slice for iteration
		var killList []string
		for p := range toKill {
			killList = append(killList, p)
		}

		// Send SIGTERM to all non-excluded processes
		for _, dpid := range killList {
			_ = exec.Command("kill", "-TERM", dpid).Run()
		}

		// Wait for graceful shutdown (2s gives processes time to clean up)
		time.Sleep(processKillGracePeriod)

		// Send SIGKILL to any remaining non-excluded processes
		for _, dpid := range killList {
			_ = exec.Command("kill", "-KILL", dpid).Run()
		}

		// Kill the pane process itself (may have called setsid() and detached)
		// Only if not excluded
		if !exclude[pid] {
			_ = exec.Command("kill", "-TERM", pid).Run()
			time.Sleep(processKillGracePeriod)
			_ = exec.Command("kill", "-KILL", pid).Run()
		}
	}

	// Kill the tmux session - this will terminate the excluded process too.
	// Ignore missing/dead-server errors - if we killed all non-excluded
	// processes, tmux may have already destroyed the session automatically.
	err = t.KillSession(name)
	if errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrNoServer) {
		return nil
	}
	return err
}

// HasSession checks if a session exists (exact match).
// Uses "=" prefix for exact matching, preventing prefix matches
// (e.g., "gt-deacon-boot" won't match when checking for "gt-deacon").
func (t *Tmux) HasSession(name string) (bool, error) {
	// psmux (Windows tmux alternative) doesn't support the "=" exact-match
	// prefix for session targets. Use the bare name on Windows.
	target := "=" + name
	if runtime.GOOS == "windows" {
		target = name
	}
	_, err := t.run("has-session", "-t", target)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrNoServer) {
			return false, nil
		}
		// psmux (Windows) returns exit code 1 with empty stderr, bypassing
		// wrapError's string matching. Fall back to treating any error as
		// "not found" on Windows only.
		if runtime.GOOS == "windows" {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ListSessions returns all session names.
func (t *Tmux) ListSessions() ([]string, error) {
	out, err := t.run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		if errors.Is(err, ErrNoServer) {
			return nil, nil // No server = no sessions
		}
		return nil, err
	}

	if out == "" {
		return nil, nil
	}

	var sessions []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// psmux ignores -F format and returns "name: N windows (created ...)"
		// Extract just the session name before the colon.
		if idx := strings.Index(line, ": "); idx > 0 {
			line = line[:idx]
		}
		sessions = append(sessions, line)
	}
	return sessions, nil
}

// SessionSet provides O(1) session existence checks by caching session names.
// Use this when you need to check multiple sessions to avoid N+1 subprocess calls.
type SessionSet struct {
	sessions map[string]struct{}
}

// NewSessionSet creates a SessionSet from a list of session names.
// This is useful for testing or when session names are known from another source.
func NewSessionSet(names []string) *SessionSet {
	set := &SessionSet{
		sessions: make(map[string]struct{}, len(names)),
	}
	for _, name := range names {
		set.sessions[name] = struct{}{}
	}
	return set
}

// GetSessionSet returns a SessionSet containing all current sessions.
// Call this once at the start of an operation, then use Has() for O(1) checks.
// This replaces multiple HasSession() calls with a single ListSessions() call.
//
// Builds the map directly from tmux output to avoid intermediate slice allocation.
func (t *Tmux) GetSessionSet() (*SessionSet, error) {
	out, err := t.run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		if errors.Is(err, ErrNoServer) {
			return &SessionSet{sessions: make(map[string]struct{})}, nil
		}
		return nil, err
	}

	// Count newlines to pre-size map (avoids rehashing during insertion)
	count := strings.Count(out, "\n") + 1
	set := &SessionSet{
		sessions: make(map[string]struct{}, count),
	}

	// Parse directly without intermediate slice allocation
	for len(out) > 0 {
		idx := strings.IndexByte(out, '\n')
		var line string
		if idx >= 0 {
			line = out[:idx]
			out = out[idx+1:]
		} else {
			line = out
			out = ""
		}
		if line != "" {
			set.sessions[line] = struct{}{}
		}
	}
	return set, nil
}

// Has returns true if the session exists in the set.
// This is an O(1) lookup - no subprocess is spawned.
func (s *SessionSet) Has(name string) bool {
	if s == nil {
		return false
	}
	_, ok := s.sessions[name]
	return ok
}

// Names returns all session names in the set.
func (s *SessionSet) Names() []string {
	if s == nil || len(s.sessions) == 0 {
		return nil
	}
	names := make([]string, 0, len(s.sessions))
	for name := range s.sessions {
		names = append(names, name)
	}
	return names
}

// ListSessionIDs returns a map of session name to session ID.
// Session IDs are in the format "$N" where N is a number.
func (t *Tmux) ListSessionIDs() (map[string]string, error) {
	out, err := t.run("list-sessions", "-F", "#{session_name}:#{session_id}")
	if err != nil {
		if errors.Is(err, ErrNoServer) {
			return nil, nil // No server = no sessions
		}
		return nil, err
	}

	if out == "" {
		return nil, nil
	}

	result := make(map[string]string)
	skipped := 0
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		// Parse "name:$id" format
		idx := strings.Index(line, ":")
		if idx > 0 && idx < len(line)-1 {
			name := line[:idx]
			id := line[idx+1:]
			result[name] = id
		} else {
			skipped++
		}
	}
	// Note: skipped lines are silently ignored for backward compatibility
	_ = skipped
	return result, nil
}

// GetSessionActivity returns the last activity time for a session.
// This is updated whenever there's any activity in the session (input/output).
func (t *Tmux) GetSessionActivity(session string) (time.Time, error) {
	out, err := t.run("display-message", "-t", session, "-p", "#{session_activity}")
	if err != nil {
		return time.Time{}, err
	}

	timestamp, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing session activity: %w", err)
	}
	return time.Unix(timestamp, 0), nil
}

// AttachSession attaches to an existing session.
// Note: This replaces the current process with tmux attach.
func (t *Tmux) AttachSession(session string) error {
	_, err := t.run("attach-session", "-t", session)
	return err
}

// SelectWindow selects a window by index.
func (t *Tmux) SelectWindow(session string, index int) error {
	_, err := t.run("select-window", "-t", fmt.Sprintf("%s:%d", session, index))
	return err
}

// ResolveCurrentSession returns the session name for the tmux pane that is an
// ancestor of the calling process. Works even when $TMUX and $TMUX_PANE are
// not in the process environment (e.g., Claude Code hook subprocesses).
//
// Walks up the process parent chain and matches against tmux pane PIDs on
// the configured socket.
func (t *Tmux) ResolveCurrentSession() (string, error) {
	out, err := t.run("list-panes", "-a", "-F", "#{pane_pid} #{session_name}")
	if err != nil {
		return "", fmt.Errorf("listing panes: %w", err)
	}

	paneSessions := make(map[int]string)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		pid, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		paneSessions[pid] = parts[1]
	}

	// Walk up from our PID to PID 1, checking each against pane PIDs
	pid := os.Getpid()
	for pid > 1 {
		if name, ok := paneSessions[pid]; ok {
			return name, nil
		}
		ppid, err := parentPID(pid)
		if err != nil || ppid == pid {
			break
		}
		pid = ppid
	}

	return "", fmt.Errorf("no tmux pane ancestor found for pid %d", os.Getpid())
}

// parentPID returns the parent PID of the given process.
func parentPID(pid int) (int, error) {
	data, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

// RenameSession renames a session.
func (t *Tmux) RenameSession(oldName, newName string) error {
	if err := validateSessionName(newName); err != nil {
		return err
	}
	_, err := t.run("rename-session", "-t", oldName, newName)
	return err
}

// SessionInfo contains information about a tmux session.
type SessionInfo struct {
	Name         string
	Windows      int
	Created      string
	Attached     bool
	Activity     string // Last activity time
	LastAttached string // Last time the session was attached
}

// GetSessionInfo returns detailed information about a session.
func (t *Tmux) GetSessionInfo(name string) (*SessionInfo, error) {
	format := "#{session_name}|#{session_windows}|#{session_created}|#{session_attached}|#{session_activity}|#{session_last_attached}"
	out, err := t.run("list-sessions", "-F", format, "-f", fmt.Sprintf("#{==:#{session_name},%s}", name))
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, ErrSessionNotFound
	}

	parts := strings.Split(out, "|")
	if len(parts) < 4 {
		return nil, fmt.Errorf("unexpected session info format: %s", out)
	}

	windows := 0
	_, _ = fmt.Sscanf(parts[1], "%d", &windows) // non-fatal: defaults to 0 on parse error

	// Convert unix timestamp to formatted string for consumers.
	created := parts[2]
	var createdUnix int64
	if _, err := fmt.Sscanf(created, "%d", &createdUnix); err == nil && createdUnix > 0 {
		created = time.Unix(createdUnix, 0).Format("2006-01-02 15:04:05")
	}

	info := &SessionInfo{
		Name:     parts[0],
		Windows:  windows,
		Created:  created,
		Attached: parts[3] == "1",
	}

	// Activity and last attached are optional (may not be present in older tmux)
	if len(parts) > 4 {
		info.Activity = parts[4]
	}
	if len(parts) > 5 {
		info.LastAttached = parts[5]
	}

	return info, nil
}

// GetSessionCreatedUnix returns the Unix timestamp when a session was created.
// Returns 0 if the session doesn't exist or can't be queried.
func (t *Tmux) GetSessionCreatedUnix(session string) (int64, error) {
	out, err := t.run("display-message", "-t", session, "-p", "#{session_created}")
	if err != nil {
		return 0, err
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing session_created %q: %w", out, err)
	}
	return ts, nil
}

// CurrentSessionName returns the tmux session name for the current process.
// Uses TMUX_PANE to target the caller's actual pane, avoiding tmux picking
// a random session when multiple sessions exist. Returns empty string if not in tmux.
func CurrentSessionName() string {
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return ""
	}
	out, err := BuildCommand("display-message", "-t", pane, "-p", "#{session_name}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// CleanupOrphanedSessions scans for zombie Gas Town sessions and kills them.
// A zombie session is one where tmux is alive but the Claude process has died.
// This runs at `gt start` time to prevent session name conflicts and resource accumulation.
//
// The isGTSession predicate identifies Gas Town sessions (e.g. session.IsKnownSession).
// It is passed as a parameter to avoid a circular import from tmux → session.
//
// Returns:
//   - cleaned: number of zombie sessions that were killed
//   - err: error if session listing failed (individual kill errors are logged but not returned)
func (t *Tmux) CleanupOrphanedSessions(isGTSession func(string) bool) (cleaned int, err error) {
	sessions, err := t.ListSessions()
	if err != nil {
		return 0, fmt.Errorf("listing sessions: %w", err)
	}

	for _, sess := range sessions {
		// Only process Gas Town sessions
		if !isGTSession(sess) {
			continue
		}

		// Check if the session is a zombie (tmux alive, agent dead)
		if !t.IsAgentAlive(sess) {
			// Kill the zombie session
			if killErr := t.KillSessionWithProcesses(sess); killErr != nil {
				// Log but continue - other sessions may still need cleanup
				fmt.Printf("  warning: failed to kill orphaned session %s: %v\n", sess, killErr)
				continue
			}
			cleaned++
		}
	}

	return cleaned, nil
}
