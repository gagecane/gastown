package tmux

import (
	"fmt"
	"strings"
)

// SetPaneDiedHook sets a pane-died hook on a session to detect crashes.
// When the pane exits, tmux runs the hook command with exit status info.
// The agentID is used to identify the agent in crash logs (e.g., "gastown/Toast").
func (t *Tmux) SetPaneDiedHook(session, agentID string) error {
	if err := validateSessionName(session); err != nil {
		return err
	}
	// Sanitize agentID to prevent shell injection (session already validated by regex)
	agentID = strings.ReplaceAll(agentID, "'", "'\\''")
	session = strings.ReplaceAll(session, "'", "'\\''") // safe after validation, but keep for consistency

	// Hook command logs the crash with exit status
	// #{pane_dead_status} is the exit code of the process that died
	// We run gt log crash which records to the town log
	hookCmd := fmt.Sprintf(`run-shell "gt log crash --agent '%s' --session '%s' --exit-code #{pane_dead_status}"`,
		agentID, session)

	// Set the hook on this specific session
	_, err := t.run("set-hook", "-t", session, "pane-died", hookCmd)
	return err
}

// SetAutoRespawnHook configures a session to automatically respawn when the pane dies.
// This is used for persistent agents like Deacon that should never exit.
// PATCH-010: Fixes Deacon crash loop by respawning at tmux level.
//
// The hook:
// 1. Waits 3 seconds (debounce rapid crashes)
// 2. Checks if pane is still dead (daemon may have already restarted it)
// 3. Respawns the pane with its original command
// 4. Re-enables remain-on-exit (respawn-pane resets it to off!)
//
// The hook uses run-shell -b (background) to prevent output from leaking to
// the user's active tmux pane, and includes || true to suppress error display.
//
// Requires remain-on-exit to be set first (called automatically by this function).
func (t *Tmux) SetAutoRespawnHook(session string) error {
	if err := validateSessionName(session); err != nil {
		return err
	}
	// First, enable remain-on-exit so the pane stays after process exit
	if err := t.SetRemainOnExit(session, true); err != nil {
		return fmt.Errorf("setting remain-on-exit: %w", err)
	}

	// Sanitize session name for shell safety
	safeSession := strings.ReplaceAll(session, "'", "'\\''")

	// Build the tmux command prefix, including socket flag when configured.
	// When a socket is configured, the embedded tmux commands MUST include
	// the -L flag. run-shell spawns a subprocess that runs bare `tmux` which
	// would otherwise connect to the default server instead of the town socket.
	tmuxCmd := "tmux"
	if t.socketName != "" {
		tmuxCmd = fmt.Sprintf("tmux -L %s", t.socketName)
	}

	hookCmd := buildAutoRespawnHookCmd(tmuxCmd, safeSession)

	// Set the hook on this specific session.
	// Note: this OVERWRITES any existing pane-died hook (e.g., SetPaneDiedHook).
	// tmux only allows one hook per event per session.
	_, err := t.run("set-hook", "-t", session, "pane-died", hookCmd)
	if err != nil {
		return fmt.Errorf("setting pane-died hook: %w", err)
	}

	return nil
}

// buildAutoRespawnHookCmd builds the pane-died hook command string for auto-respawn.
// The tmuxCmd parameter is the tmux binary invocation (e.g., "tmux -L gt" or "tmux").
// The session parameter is the already-sanitized session name.
//
// The command has three safety measures:
//
//  1. run-shell -b: Runs in background so output/errors never leak to the
//     user's active tmux pane. Without -b, run-shell displays failures
//     (like "'...' returned 1") on the attached client's current pane,
//     which can take over an unrelated session the user is viewing.
//
//  2. Dead-pane guard: Checks #{pane_dead} before respawning. The daemon's
//     heartbeat may have already restarted the session during the 3-second
//     sleep window. Without this guard, the hook blindly runs respawn-pane -k
//     which kills the daemon's freshly-started agent.
//
//  3. || true: Ensures the overall command always exits 0, suppressing any
//     error display from tmux even if the session was killed entirely.
func buildAutoRespawnHookCmd(tmuxCmd, session string) string {
	// The shell pipeline:
	//   sleep 3                              -- debounce rapid crashes
	//   list-panes ... #{pane_dead} | grep   -- guard: only proceed if pane is still dead
	//   respawn-pane -k                      -- restart with original command
	//   set-option remain-on-exit on         -- re-enable (respawn-pane resets it to off!)
	//   || true                              -- suppress errors unconditionally
	//
	// IMPORTANT: run-shell expands format variables (#{...}) at hook fire time,
	// not at shell execution time. We need the pane_dead check to run 3 seconds
	// AFTER the pane dies (to detect if the daemon already restarted it).
	// Using ##{pane_dead} escapes the first expansion (## -> #), so the shell
	// receives #{pane_dead} and passes it to the nested `tmux list-panes` call
	// which evaluates it at query time -- giving us the CURRENT pane state.
	return fmt.Sprintf(
		`run-shell -b "sleep 3 && %s list-panes -t '%s' -F '##{pane_dead}' 2>/dev/null | grep -q 1 && %s respawn-pane -k -t '%s' && %s set-option -t '%s' remain-on-exit on || true"`,
		tmuxCmd, session, tmuxCmd, session, tmuxCmd, session)
}
