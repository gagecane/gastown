package tmux

import (
	"fmt"
	"runtime"
	"strings"
)

// GetPaneCommand returns the current command running in a pane.
// Returns "bash", "zsh", "claude", "node", etc.
func (t *Tmux) GetPaneCommand(session string) (string, error) {
	// Use display-message targeting the first window explicitly (:^) to avoid
	// returning the active pane's command when a non-agent window is focused.
	// Agent processes always run in the first window; without explicit targeting,
	// a user-created window or split pane (running a shell) could cause health
	// checks to falsely report the agent as dead.
	out, err := t.run("display-message", "-t", session+":^", "-p", "#{pane_current_command}")
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(out)
	if result == "" {
		return "", fmt.Errorf("empty command for session %s (session may not exist)", session)
	}
	return result, nil
}

// FindAgentPane finds the pane running an agent process within a session.
// In multi-window/multi-pane sessions, send-keys -t <session> targets the
// active/focused pane, which may not be the agent pane. This method returns
// the pane ID (e.g., "%5") of the one running the agent.
//
// ZFC (gt-qmsx): Reads declared GT_PANE_ID from session environment first.
// Falls back to scanning all panes for legacy sessions without GT_PANE_ID.
//
// Returns ("", nil) if the session has only one pane (no disambiguation needed),
// or if no agent pane can be identified (caller should fall back to session targeting).
func (t *Tmux) FindAgentPane(session string) (string, error) {
	// ZFC: read declared pane identity set at session startup (gt-qmsx).
	// This replaces process-tree inference for sessions that record GT_PANE_ID.
	if declaredPane, err := t.GetEnvironment(session, "GT_PANE_ID"); err == nil && declaredPane != "" {
		// Verify the pane still exists in tmux (it may have been killed/respawned).
		if _, verifyErr := t.run("display-message", "-t", declaredPane, "-p", "#{pane_id}"); verifyErr == nil {
			return declaredPane, nil
		}
		// Declared pane is gone — fall through to scan.
	}

	// Fallback: scan all panes for legacy sessions without GT_PANE_ID.
	return t.findAgentPaneByScan(session)
}

// findAgentPaneByScan enumerates all panes across all windows (-s) and returns
// the pane ID of the one running the agent. This is the legacy path for sessions
// that predate GT_PANE_ID (gt-qmsx).
func (t *Tmux) findAgentPaneByScan(session string) (string, error) {
	out, err := t.run("list-panes", "-s", "-t", session, "-F", "#{pane_id}\t#{pane_current_command}\t#{pane_pid}")
	if err != nil {
		return "", err
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) <= 1 {
		// Single pane - no disambiguation needed
		return "", nil
	}

	// Get agent process names from session environment
	processNames := t.resolveSessionProcessNames(session)

	// Check each pane for agent process
	for _, line := range lines {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		paneID := parts[0]
		paneCmd := parts[1]
		panePID := parts[2]

		if t.matchesPaneRuntime(session, paneCmd, panePID, processNames) {
			return paneID, nil
		}
	}

	// No agent pane found
	return "", nil
}

// GetPaneID returns the pane identifier for a session's first pane.
// Returns a pane ID like "%0" that can be used with RespawnPane.
// Targets pane 0 explicitly to be consistent with GetPaneCommand,
// GetPanePID, and GetPaneWorkDir.
func (t *Tmux) GetPaneID(session string) (string, error) {
	out, err := t.run("display-message", "-t", session+":0.0", "-p", "#{pane_id}")
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(out)
	if result == "" {
		return "", fmt.Errorf("no panes found in session %s", session)
	}
	return result, nil
}

// GetPaneWorkDir returns the current working directory of a pane.
// Targets pane 0 explicitly to avoid returning the active pane's
// working directory in multi-pane sessions.
func (t *Tmux) GetPaneWorkDir(session string) (string, error) {
	out, err := t.run("display-message", "-t", session+":0.0", "-p", "#{pane_current_path}")
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(out)
	if result == "" {
		return "", fmt.Errorf("empty working directory for session %s (session may not exist)", session)
	}
	return result, nil
}

// GetPanePID returns the PID of the pane's main process.
// When target is a session name, explicitly targets the first window (:^) to avoid
// returning the active pane's PID when a non-agent window is focused. When target is
// a pane ID (e.g., "%5"), uses it directly.
func (t *Tmux) GetPanePID(target string) (string, error) {
	tmuxTarget := target
	if !strings.HasPrefix(target, "%") {
		tmuxTarget = target + ":^"
	}
	out, err := t.run("display-message", "-t", tmuxTarget, "-p", "#{pane_pid}")
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(out)
	if result == "" {
		return "", fmt.Errorf("empty PID for target %s (session may not exist)", target)
	}
	return result, nil
}

// CapturePane captures the visible content of a pane.
func (t *Tmux) CapturePane(session string, lines int) (string, error) {
	return t.run("capture-pane", "-p", "-t", session, "-S", fmt.Sprintf("-%d", lines))
}

// CapturePaneAll captures all scrollback history.
func (t *Tmux) CapturePaneAll(session string) (string, error) {
	return t.run("capture-pane", "-p", "-t", session, "-S", "-")
}

// CapturePaneLines captures the last N lines of a pane as a slice.
func (t *Tmux) CapturePaneLines(session string, lines int) ([]string, error) {
	out, err := t.CapturePane(session, lines)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// RespawnPane kills all processes in a pane and starts a new command.
// This is used for "hot reload" of agent sessions - instantly restart in place.
// The pane parameter should be a pane ID (e.g., "%0") or session:window.pane format.
func (t *Tmux) RespawnPane(pane, command string) error {
	if runtime.GOOS == "windows" {
		// psmux: respawn-pane -k kills the process, then send-keys types the command.
		if _, err := t.run("respawn-pane", "-k", "-t", pane); err != nil {
			return err
		}
		_, err := t.run("send-keys", "-t", pane, command, "Enter")
		return err
	}
	_, err := t.run("respawn-pane", "-k", "-t", pane, command)
	return err
}

// RespawnPaneWithWorkDir kills all processes in a pane and starts a new command
// in the specified working directory. Use this when the pane's current working
// directory may have been deleted.
func (t *Tmux) RespawnPaneWithWorkDir(pane, workDir, command string) error {
	if runtime.GOOS == "windows" {
		if _, err := t.run("respawn-pane", "-k", "-t", pane); err != nil {
			return err
		}
		// Change directory first if needed, then run command
		if workDir != "" {
			cdCmd := fmt.Sprintf("Set-Location %s; %s", psQuoteValue(workDir), command)
			_, err := t.run("send-keys", "-t", pane, cdCmd, "Enter")
			return err
		}
		_, err := t.run("send-keys", "-t", pane, command, "Enter")
		return err
	}
	args := []string{"respawn-pane", "-k", "-t", pane}
	if workDir != "" {
		args = append(args, "-c", workDir)
	}
	args = append(args, command)
	_, err := t.run(args...)
	return err
}

// psQuoteValue quotes a value for PowerShell single-quoted strings.
func psQuoteValue(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ClearHistory clears the scrollback history buffer for a pane.
// This resets copy-mode display from [0/N] to [0/0].
// The pane parameter should be a pane ID (e.g., "%0") or session:window.pane format.
func (t *Tmux) ClearHistory(pane string) error {
	_, err := t.run("clear-history", "-t", pane)
	return err
}

// SetRemainOnExit controls whether a pane stays around after its process exits.
// When on, the pane remains with "[Exited]" status, allowing respawn-pane to restart it.
// When off (default), the pane is destroyed when its process exits.
// This is essential for handoff: set on before killing processes, so respawn-pane works.
func (t *Tmux) SetRemainOnExit(pane string, on bool) error {
	value := "on"
	if !on {
		value = "off"
	}
	_, err := t.run("set-option", "-t", pane, "remain-on-exit", value)
	return err
}
