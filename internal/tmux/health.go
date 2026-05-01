package tmux

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
)

// ZombieStatus describes the liveness state of a tmux agent session.
type ZombieStatus int

const (
	// SessionHealthy means the session exists and the agent process is alive.
	SessionHealthy ZombieStatus = iota
	// SessionDead means the tmux session does not exist.
	SessionDead
	// AgentDead means the tmux session exists but the agent process has died.
	AgentDead
	// AgentHung means the tmux session and agent process exist but there has
	// been no tmux activity for longer than the specified threshold.
	AgentHung
)

// String returns a human-readable label for the zombie status.
func (z ZombieStatus) String() string {
	switch z {
	case SessionHealthy:
		return "healthy"
	case SessionDead:
		return "session-dead"
	case AgentDead:
		return "agent-dead"
	case AgentHung:
		return "agent-hung"
	default:
		return "unknown"
	}
}

// IsZombie returns true if the status represents a zombie (any non-healthy state
// where the session exists but the agent is dead or hung).
func (z ZombieStatus) IsZombie() bool {
	return z == AgentDead || z == AgentHung
}

// CheckSessionHealth determines the health status of an agent session.
// It performs three levels of checking:
//  1. Session existence (tmux has-session)
//  2. Agent process liveness (IsAgentAlive — checks process tree)
//  3. Activity staleness (GetSessionActivity — checks tmux output timestamp)
//
// The maxInactivity parameter controls how long a session can be idle before
// being considered hung. Pass 0 to skip activity checking (only check process
// liveness). A reasonable default for production is 10-15 minutes.
//
// This is the preferred unified method for zombie detection across all agent types.
func (t *Tmux) CheckSessionHealth(session string, maxInactivity time.Duration) ZombieStatus {
	// Level 1: Does the tmux session exist?
	alive, err := t.HasSession(session)
	if err != nil || !alive {
		return SessionDead
	}

	// Level 2: Is the agent process running inside the session?
	if !t.IsAgentAlive(session) {
		return AgentDead
	}

	// Level 3: Has there been recent activity? (optional)
	if maxInactivity > 0 {
		lastActivity, err := t.GetSessionActivity(session)
		if err == nil && !lastActivity.IsZero() {
			if time.Since(lastActivity) > maxInactivity {
				return AgentHung
			}
		}
		// On error or zero time, skip activity check — don't false-positive
	}

	return SessionHealthy
}

// processMatchesNames checks if a process's binary name matches any of the given names.
// Uses ps to get the actual command name from the process's executable path.
// This handles cases where argv[0] is modified (e.g., Claude showing version "2.1.30").
func processMatchesNames(pid string, names []string) bool {
	if len(names) == 0 {
		return false
	}
	// Use ps to get the command name (COMM column gives the executable name)
	cmd := exec.Command("ps", "-p", pid, "-o", "comm=")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	// Get just the base name (in case it's a full path like /Users/.../claude)
	commPath := strings.TrimSpace(string(out))
	comm := filepath.Base(commPath)

	// Check if any name matches
	for _, name := range names {
		if comm == name {
			return true
		}
	}
	return false
}

// hasDescendantWithNames checks if a process has any descendant (child, grandchild, etc.)
// matching any of the given names. Recursively traverses the process tree up to maxDepth.
// Used when the pane command is a shell (bash, zsh, pwsh) that launched an agent.
func hasDescendantWithNames(pid string, names []string, depth int) bool {
	const maxDepth = 10 // Prevent infinite loops in case of circular references
	if len(names) == 0 || depth > maxDepth {
		return false
	}
	if runtime.GOOS == "windows" {
		return hasDescendantWithNamesWindows(pid, names, depth)
	}
	return hasDescendantWithNamesPosix(pid, names, depth)
}

// hasDescendantWithNamesPosix uses pgrep to find child processes on Unix systems.
func hasDescendantWithNamesPosix(pid string, names []string, depth int) bool {
	const maxDepth = 10
	if depth > maxDepth {
		return false
	}
	// Use pgrep to find child processes
	cmd := exec.Command("pgrep", "-P", pid, "-l")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	// Build a set of names for fast lookup
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}
	// Check if any child matches, or recursively check grandchildren
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "PID name" e.g., "29677 node"
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			childPid := parts[0]
			childName := parts[1]
			// Direct match
			if nameSet[childName] {
				return true
			}
			// Recursive check of descendants
			if hasDescendantWithNames(childPid, names, depth+1) {
				return true
			}
		}
	}
	return false
}

// FindSessionByWorkDir finds tmux sessions where the pane's current working directory
// matches or is under the target directory. Returns session names that match.
// If processNames is provided, only returns sessions that match those processes.
// If processNames is nil or empty, returns all sessions matching the directory.
func (t *Tmux) FindSessionByWorkDir(targetDir string, processNames []string) ([]string, error) {
	sessions, err := t.ListSessions()
	if err != nil {
		return nil, err
	}

	var matches []string
	for _, session := range sessions {
		if session == "" {
			continue
		}

		workDir, err := t.GetPaneWorkDir(session)
		if err != nil {
			continue // Skip sessions we can't query
		}

		// Check if workdir matches target (exact match or subdir)
		if workDir == targetDir || strings.HasPrefix(workDir, targetDir+"/") {
			if len(processNames) > 0 {
				if t.IsRuntimeRunning(session, processNames) {
					matches = append(matches, session)
				}
				continue
			}
			matches = append(matches, session)
		}
	}

	return matches, nil
}

// IsAgentRunning checks if an agent appears to be running in the session.
//
// If expectedPaneCommands is non-empty, the pane's current command must match one of them.
// If expectedPaneCommands is empty, any non-shell command counts as "agent running".
func (t *Tmux) IsAgentRunning(session string, expectedPaneCommands ...string) bool {
	cmd, err := t.GetPaneCommand(session)
	if err != nil {
		return false
	}

	if len(expectedPaneCommands) > 0 {
		for _, expected := range expectedPaneCommands {
			if expected != "" && cmd == expected {
				return true
			}
		}
		return false
	}

	// Fallback: any non-shell command counts as running.
	for _, shell := range constants.SupportedShells {
		if cmd == shell {
			return false
		}
	}
	return cmd != ""
}

// IsRuntimeRunning checks if a runtime appears to be running in the session.
//
// ZFC (gt-qmsx): Reads declared GT_PANE_ID from session environment first,
// then checks only that pane. Falls back to scanning all panes for legacy
// sessions without GT_PANE_ID.
func (t *Tmux) IsRuntimeRunning(session string, processNames []string) bool {
	if len(processNames) == 0 {
		return false
	}

	// ZFC: check declared pane identity set at session startup (gt-qmsx).
	if declaredPane, err := t.GetEnvironment(session, "GT_PANE_ID"); err == nil && declaredPane != "" {
		if t.checkTargetPaneForRuntime(session, declaredPane, processNames) {
			return true
		}
		// On Windows (psmux), pane IDs like %1 may not be supported by
		// display-message. Fall through to legacy path instead of returning false.
		if runtime.GOOS != "windows" {
			return false
		}
	}

	// Legacy fallback: check first window, then scan all panes.
	if t.checkPaneForRuntime(session, processNames) {
		return true
	}
	out, err := t.run("list-panes", "-s", "-t", session, "-F", "#{pane_current_command}\t#{pane_pid}")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			continue
		}
		cmd, pid := parts[0], parts[1]
		if t.matchesPaneRuntime(session, cmd, pid, processNames) {
			return true
		}
	}
	return false
}

// checkTargetPaneForRuntime checks if a specific pane (by ID, e.g., "%5") is
// running a matching process. Used by the ZFC path when GT_PANE_ID is declared.
func (t *Tmux) checkTargetPaneForRuntime(session, paneID string, processNames []string) bool {
	cmd, err := t.run("display-message", "-t", paneID, "-p", "#{pane_current_command}")
	if err != nil {
		return false // pane doesn't exist
	}
	pid, _ := t.run("display-message", "-t", paneID, "-p", "#{pane_pid}")
	return t.matchesPaneRuntime(session, strings.TrimSpace(cmd), strings.TrimSpace(pid), processNames)
}

// checkPaneForRuntime checks if the first window's pane is running a matching process.
func (t *Tmux) checkPaneForRuntime(session string, processNames []string) bool {
	cmd, err := t.GetPaneCommand(session)
	if err != nil {
		return false
	}
	pid, _ := t.GetPanePID(session)
	return t.matchesPaneRuntime(session, cmd, pid, processNames)
}

// cursorAgentSessionDeclaresCursor reports whether tmux session env identifies the Cursor
// runtime (preset "cursor"). Used to disambiguate the generic process name "agent" (Cursor's
// install script symlinks `agent` to the same binary as cursor-agent) from unrelated binaries
// named `agent`. The most reliable signal is GT_AGENT=cursor together with GT_PROCESS_NAMES
// set at session startup (see internal/session/lifecycle.go).
func cursorAgentSessionDeclaresCursor(t *Tmux, session string) bool {
	if t == nil || session == "" {
		return false
	}
	agent, err := t.GetEnvironment(session, "GT_AGENT")
	if err == nil && agent == string(config.AgentCursor) {
		return true
	}
	if names, err := t.GetEnvironment(session, "GT_PROCESS_NAMES"); err == nil && names != "" {
		for _, n := range strings.Split(names, ",") {
			if strings.TrimSpace(n) == "cursor-agent" {
				return true
			}
		}
	}
	return false
}

func withoutProcessName(names []string, drop string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != drop {
			out = append(out, n)
		}
	}
	return out
}

// processNamesForSession returns process names for pane matching, dropping the ambiguous
// name "agent" unless the session declares the Cursor runtime.
func processNamesForSession(t *Tmux, session string, processNames []string) []string {
	if len(processNames) == 0 {
		return processNames
	}
	if cursorAgentSessionDeclaresCursor(t, session) {
		return processNames
	}
	return withoutProcessName(processNames, "agent")
}

// matchesPaneRuntime checks if a pane with the given command and PID is running a matching process.
func (t *Tmux) matchesPaneRuntime(session, cmd, pid string, processNames []string) bool {
	names := processNamesForSession(t, session, processNames)
	if len(names) == 0 {
		return false
	}
	// Direct command match
	for _, name := range names {
		if cmd == name {
			return true
		}
	}
	if pid == "" {
		return false
	}
	// If pane command is a shell, check descendants
	for _, shell := range constants.SupportedShells {
		if cmd == shell {
			return hasDescendantWithNames(pid, names, 0)
		}
	}
	// Unrecognized command: check if process itself matches (version-as-argv[0])
	if processMatchesNames(pid, names) {
		return true
	}
	// Finally check descendants as fallback
	return hasDescendantWithNames(pid, names, 0)
}

// IsAgentAlive checks if an agent is running in the session using agent-agnostic detection.
// It reads GT_PROCESS_NAMES from the session environment for accurate process detection,
// falling back to GT_AGENT-based lookup for legacy sessions.
// This is the preferred method for zombie detection across all agent types.
func (t *Tmux) IsAgentAlive(session string) bool {
	return t.IsRuntimeRunning(session, t.resolveSessionProcessNames(session))
}

// resolveSessionProcessNames returns the process names to check for a session.
// Prefers GT_PROCESS_NAMES (set at startup, handles custom agents that shadow
// built-in presets). Falls back to GT_AGENT-based lookup for legacy sessions.
func (t *Tmux) resolveSessionProcessNames(session string) []string {
	// Prefer explicit process names set at startup (handles custom agents correctly)
	if names, err := t.GetEnvironment(session, "GT_PROCESS_NAMES"); err == nil && names != "" {
		return strings.Split(names, ",")
	}
	// Fallback: resolve from agent name (built-in presets only)
	agentName, _ := t.GetEnvironment(session, "GT_AGENT")
	return config.GetProcessNames(agentName) // Returns Claude defaults if empty
}

// WaitForCommand polls until the pane is NOT running one of the excluded commands.
// Useful for waiting until a shell has started a new process (e.g., claude).
// Returns nil when a non-excluded command is detected, or error on timeout.
//
// ZFC fallback: when the pane command IS a shell (e.g., bash), checks for the
// GT_AGENT_READY env var set by the agent's SessionStart hook (gt prime --hook).
// This handles agents wrapped in shell scripts (e.g., c2claude wrapping
// claude-original) where exec env does not replace the shell as the pane
// foreground process. Replaces process-tree probing (IsAgentAlive) per gt-sk5u.
func (t *Tmux) WaitForCommand(session string, excludeCommands []string, timeout time.Duration) error {
	// ZFC: Clear agent-ready sentinel to prevent stale values from previous
	// agent runs. The agent's SessionStart hook (gt prime --hook) sets this
	// to "1" once the agent is running. Unsetting here ensures we only detect
	// the NEW agent, not a leftover from a previous run.
	_, _ = t.run("set-environment", "-u", "-t", session, EnvAgentReady)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd, err := t.GetPaneCommand(session)
		if err != nil {
			time.Sleep(constants.PollInterval)
			continue
		}
		// Check if current command is NOT in the exclude list
		excluded := false
		for _, exc := range excludeCommands {
			if cmd == exc {
				excluded = true
				break
			}
		}
		if !excluded {
			return nil
		}
		// ZFC fallback: check if the agent signaled readiness via its startup
		// hook. This replaces process-tree descendant probing (IsAgentAlive)
		// for wrapped agents where pane_current_command remains a shell.
		if ready, err := t.GetEnvironment(session, EnvAgentReady); err == nil && ready == "1" {
			return nil
		}
		time.Sleep(constants.PollInterval)
	}
	return fmt.Errorf("timeout waiting for command (still running excluded command)")
}

// WaitForShellReady polls until the pane is running a shell command, and that
// shell has remained the pane's current command for at least two consecutive
// polls (i.e., is stable). Useful for waiting until a process has exited and
// returned to shell.
//
// The stability check matters because during shell startup, .zshrc/.bashrc and
// similar rc files can briefly fork short-lived subprocesses (e.g., `sh` via
// `brew shellenv`, `mise activate`, or login profile chains). Without stability,
// WaitForShellReady can return while the pane momentarily shows one shell
// (e.g., `sh`) only to transition to a different shell (e.g., `zsh`) a few ms
// later. Callers that then cache the "current shell" at ready time end up with
// a stale value.
func (t *Tmux) WaitForShellReady(session string, timeout time.Duration) error {
	shells := constants.SupportedShells
	isShell := func(cmd string) bool {
		for _, s := range shells {
			if cmd == s {
				return true
			}
		}
		return false
	}
	deadline := time.Now().Add(timeout)
	var lastShell string
	for time.Now().Before(deadline) {
		cmd, err := t.GetPaneCommand(session)
		if err != nil {
			lastShell = ""
			time.Sleep(constants.PollInterval)
			continue
		}
		if isShell(cmd) {
			// Require the same shell to be observed in two consecutive polls
			// before declaring ready. This filters out transient shells (e.g.,
			// `sh` briefly spawned by zsh init) that would otherwise cause
			// callers to cache a stale pane command.
			if lastShell == cmd {
				return nil
			}
			lastShell = cmd
		} else {
			lastShell = ""
		}
		time.Sleep(constants.PollInterval)
	}
	return fmt.Errorf("timeout waiting for shell")
}

// WaitForRuntimeReady polls until the runtime's prompt indicator appears in the pane.
// Runtime is ready when we see the configured prompt prefix at the start of a line.
//
// IMPORTANT: Bootstrap vs Steady-State Observation
//
// This function uses regex to detect runtime prompts - a ZFC violation.
// ZFC (Zero False Commands) principle: AI should observe AI, not regex.
//
// Bootstrap (acceptable):
//
//	During cold startup when no AI agent is running, the daemon uses this
//	function to get the Deacon online. Regex is acceptable here.
//
// Steady-State (use AI observation instead):
//
//	Once any AI agent is running, observation should be AI-to-AI:
//	- Deacon monitoring polecats → use patrol formula + AI analysis
//	- Deacon restarting → Mayor watches via 'gt peek'
//	- Mayor restarting → Deacon watches via 'gt peek'

// matchesPromptPrefix reports whether a captured pane line matches the
// configured ready-prompt prefix. It normalizes non-breaking spaces
// (U+00A0) to regular spaces before matching, because Claude Code uses
// NBSP after its ❯ prompt character while the default ReadyPromptPrefix
// uses a regular space. See https://github.com/steveyegge/gastown/issues/1387.
func matchesPromptPrefix(line, readyPromptPrefix string) bool {
	if readyPromptPrefix == "" {
		return false
	}
	trimmed := strings.TrimSpace(line)
	// Normalize NBSP (U+00A0) → regular space so that prompt matching
	// works regardless of which whitespace character the agent uses.
	trimmed = strings.ReplaceAll(trimmed, "\u00a0", " ")
	normalizedPrefix := strings.ReplaceAll(readyPromptPrefix, "\u00a0", " ")
	prefix := strings.TrimSpace(normalizedPrefix)
	return strings.HasPrefix(trimmed, normalizedPrefix) || (prefix != "" && trimmed == prefix)
}

func hasBusyIndicator(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	return strings.Contains(trimmed, "esc to interrupt") || strings.Contains(trimmed, "esc to cancel")
}

func readyPromptPrefixForSession(t *Tmux, session string) string {
	promptPrefix := DefaultReadyPromptPrefix
	agentName, err := t.GetEnvironment(session, "GT_AGENT")
	if err != nil || agentName == "" {
		return promptPrefix
	}
	preset := config.GetAgentPresetByName(agentName)
	if preset == nil || preset.ReadyPromptPrefix == "" {
		return promptPrefix
	}
	return preset.ReadyPromptPrefix
}

func (t *Tmux) WaitForRuntimeReady(session string, rc *config.RuntimeConfig, timeout time.Duration) error {
	if rc == nil || rc.Tmux == nil {
		return nil
	}

	if rc.Tmux.ReadyPromptPrefix == "" {
		if rc.Tmux.ReadyDelayMs <= 0 {
			return nil
		}
		// Fallback to fixed delay when prompt detection is unavailable.
		delay := time.Duration(rc.Tmux.ReadyDelayMs) * time.Millisecond
		if delay > timeout {
			delay = timeout
		}
		time.Sleep(delay)
		return nil
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Capture last few lines of the pane
		lines, err := t.CapturePaneLines(session, 10)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		// Look for runtime prompt indicator at start of line
		for _, line := range lines {
			if matchesPromptPrefix(line, rc.Tmux.ReadyPromptPrefix) {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for runtime prompt")
}

// DefaultReadyPromptPrefix is the Claude Code prompt prefix used for idle detection.
// Claude Code uses ❯ (U+276F) as the prompt character.
const DefaultReadyPromptPrefix = "❯ "

// WaitForIdle polls until the agent appears to be at an idle prompt.
// Unlike WaitForRuntimeReady (which is for bootstrap), this is for steady-state
// idle detection — used to avoid interrupting agents mid-work.
//
// Returns nil if the agent becomes idle within the timeout.
// Returns an error if the timeout expires while the agent is still busy.
func (t *Tmux) WaitForIdle(session string, timeout time.Duration) error {
	promptPrefix := readyPromptPrefixForSession(t, session)
	prefix := strings.TrimSpace(promptPrefix)

	// Require 2 consecutive idle polls to filter out transient states.
	// During inter-tool-call gaps (~500ms), the prompt may briefly appear
	// in the pane buffer while Claude Code is still actively working.
	// Two polls 200ms apart (400ms window) confirms genuine idle state.
	consecutiveIdle := 0
	const requiredConsecutive = 2

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		lines, err := t.CapturePaneLines(session, 5)
		if err != nil {
			// Distinguish terminal errors from transient ones.
			// Session not found or no server means the session is gone —
			// no point in polling further.
			if errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrNoServer) {
				return err
			}
			consecutiveIdle = 0
			time.Sleep(200 * time.Millisecond)
			continue
		}

		// Busy indicator check: if "esc to interrupt" is visible anywhere in
		// the recent pane output, the agent is actively working — NOT idle,
		// regardless of whether the prompt prefix is also visible.
		statusBarBusy := false
		for _, line := range lines {
			if hasBusyIndicator(line) {
				statusBarBusy = true
				break
			}
		}
		if statusBarBusy {
			consecutiveIdle = 0
			time.Sleep(200 * time.Millisecond)
			continue
		}

		// Scan all captured lines for the prompt prefix.
		// Claude Code renders a status bar below the prompt line,
		// so the prompt may not be the last non-empty line.
		promptFound := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if matchesPromptPrefix(trimmed, promptPrefix) || (prefix != "" && trimmed == prefix) {
				promptFound = true
				break
			}
		}

		if promptFound {
			consecutiveIdle++
			if consecutiveIdle >= requiredConsecutive {
				return nil
			}
		} else {
			consecutiveIdle = 0
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ErrIdleTimeout
}

// IsAtPrompt checks if the agent is currently at an idle prompt (non-blocking).
// Returns true if the pane shows the ReadyPromptPrefix, indicating the agent is
// idle and ready for input. Used by startup nudge verification to detect whether
// a nudge was lost (agent returned to prompt without processing it).
func (t *Tmux) IsAtPrompt(session string, rc *config.RuntimeConfig) bool {
	promptPrefix := DefaultReadyPromptPrefix
	if rc != nil && rc.Tmux != nil && rc.Tmux.ReadyPromptPrefix != "" {
		promptPrefix = rc.Tmux.ReadyPromptPrefix
	}

	lines, err := t.CapturePaneLines(session, 10)
	if err != nil {
		return false
	}

	for _, line := range lines {
		if matchesPromptPrefix(line, promptPrefix) {
			return true
		}
	}
	return false
}

// IsIdle checks whether a session is currently at the idle input prompt (❯)
// with no active work in progress.
// Returns true if idle, false if the agent is busy or the check fails.
// This is a point-in-time snapshot, not a poll.
//
// Detection strategy: check the Claude Code status bar (bottom line of the
// pane starting with ⏵⏵). When the agent is actively working, the status
// bar contains "esc to interrupt". When idle, it does not.
func (t *Tmux) IsIdle(session string) bool {
	lines, err := t.CapturePaneLines(session, 5)
	if err != nil {
		return false
	}

	for _, line := range lines {
		if hasBusyIndicator(line) {
			return false
		}
	}

	promptPrefix := readyPromptPrefixForSession(t, session)
	for _, line := range lines {
		if matchesPromptPrefix(line, promptPrefix) {
			return true
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "⏵⏵") || strings.Contains(trimmed, "\u23F5\u23F5") {
			return true
		}
	}
	return false
}
