package tmux

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/steveyegge/gastown/internal/constants"
)

// DisplayMessage shows a message in the tmux status line.
// This is non-disruptive - it doesn't interrupt the session's input.
// Duration is specified in milliseconds.
func (t *Tmux) DisplayMessage(session, message string, durationMs int) error {
	// Set display time temporarily, show message, then restore
	// Use -d flag for duration in tmux 2.9+
	_, err := t.run("display-message", "-t", session, "-d", fmt.Sprintf("%d", durationMs), message)
	return err
}

// DisplayMessageDefault shows a message with default duration (5 seconds).
func (t *Tmux) DisplayMessageDefault(session, message string) error {
	return t.DisplayMessage(session, message, constants.DefaultDisplayMs)
}

// SendNotificationBanner sends a visible notification banner to a tmux session.
// This interrupts the terminal to ensure the notification is seen.
// Uses echo to print a boxed banner with the notification details.
func (t *Tmux) SendNotificationBanner(session, from, subject string) error {
	// Sanitize inputs to prevent output manipulation
	from = strings.ReplaceAll(from, "\n", " ")
	from = strings.ReplaceAll(from, "\r", " ")
	subject = strings.ReplaceAll(subject, "\n", " ")
	subject = strings.ReplaceAll(subject, "\r", " ")

	// Build the banner text
	banner := fmt.Sprintf(`echo '
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
📬 NEW MAIL from %s
Subject: %s
Run: gt mail inbox
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
'`, from, subject)

	return t.SendKeys(session, banner)
}

// ApplyTheme sets the status bar style for a session.
func (t *Tmux) ApplyTheme(session string, theme Theme) error {
	_, err := t.run("set-option", "-t", session, "status-style", theme.Style())
	return err
}

// ClearTheme removes Gas Town tmux styling from a session.
func (t *Tmux) ClearTheme(session string) error {
	if _, err := t.run("set-option", "-t", session, "-u", "status-style"); err != nil {
		return err
	}
	_, err := t.run("set-window-option", "-t", session, "-u", "window-style")
	return err
}

// ApplyWindowStyle sets or resets the window background (window-style).
// If ws is nil, resets to terminal defaults. If non-nil, applies the colors.
func (t *Tmux) ApplyWindowStyle(session string, ws *WindowStyle) error {
	style := "bg=default,fg=default"
	if ws != nil {
		style = ws.Style()
	}
	_, err := t.run("set-option", "-t", session, "window-style", style)
	return err
}

// roleIcons maps role names to display icons for the status bar.
// Uses centralized emojis from constants package.
// Includes legacy keys ("coordinator", "health-check") for backwards compatibility.
var roleIcons = map[string]string{
	// Standard role names (from constants)
	constants.RoleMayor:    constants.EmojiMayor,
	constants.RoleDeacon:   constants.EmojiDeacon,
	constants.RoleWitness:  constants.EmojiWitness,
	constants.RoleRefinery: constants.EmojiRefinery,
	constants.RoleCrew:     constants.EmojiCrew,
	constants.RolePolecat:  constants.EmojiPolecat,
	// Legacy names (for backwards compatibility)
	"coordinator":  constants.EmojiMayor,
	"health-check": constants.EmojiDeacon,
}

// SetStatusFormat configures the left side of the status bar.
// Shows compact identity: icon + minimal context
func (t *Tmux) SetStatusFormat(session, rig, worker, role string) error {
	// Get icon for role (empty string if not found)
	icon := roleIcons[role]

	// Compact format - icon already identifies role
	// Mayor: 🎩 Mayor
	// Crew:  👷 gastown/crew/max (full path)
	// Polecat: 😺 gastown/Toast
	var left string
	if rig == "" {
		// Town-level agent (Mayor, Deacon) - keep as-is
		left = fmt.Sprintf("%s %s ", icon, worker)
	} else {
		// Rig agents - use session name (already in prefix format: gt-crew-gus)
		left = fmt.Sprintf("%s %s ", icon, session)
	}

	if _, err := t.run("set-option", "-t", session, "status-left-length", "25"); err != nil {
		return err
	}
	_, err := t.run("set-option", "-t", session, "status-left", left)
	return err
}

// SetDynamicStatus configures the right side with dynamic content.
// Uses a shell command that tmux calls periodically to get current status.
func (t *Tmux) SetDynamicStatus(session string) error {
	if err := validateSessionName(session); err != nil {
		return err
	}

	// tmux calls this command every status-interval seconds
	// gt status-line reads env vars and mail to build the status
	//
	// On Windows, tmux #() spawns a visible cmd.exe + conhost.exe window on
	// every invocation, causing rapid screen flashing. Fall back to a static
	// status until psmux supports CREATE_NO_WINDOW for #() commands.
	var right string
	if runtime.GOOS == "windows" {
		right = `%H:%M`
	} else {
		right = fmt.Sprintf(`#(gt status-line --session=%s 2>/dev/null) %%H:%%M`, session)
	}

	if _, err := t.run("set-option", "-t", session, "status-right-length", "80"); err != nil {
		return err
	}
	// Set faster refresh for more responsive status
	if _, err := t.run("set-option", "-t", session, "status-interval", "5"); err != nil {
		return err
	}
	_, err := t.run("set-option", "-t", session, "status-right", right)
	return err
}

// ConfigureGasTownSession applies Gas Town status configuration to a session.
// A nil theme disables tmux styling while still applying status/bindings.
//
// Window background is controlled by theme.Window:
//   - non-nil: apply Window's colors as the window background
//   - nil: reset window background to terminal defaults (disabled)
func (t *Tmux) ConfigureGasTownSession(session string, theme *Theme, rig, worker, role string) error {
	if theme != nil {
		if err := t.ApplyTheme(session, *theme); err != nil {
			return fmt.Errorf("applying theme: %w", err)
		}
		if err := t.ApplyWindowStyle(session, theme.Window); err != nil {
			return fmt.Errorf("applying window style: %w", err)
		}
	} else {
		if err := t.ClearTheme(session); err != nil {
			return fmt.Errorf("clearing theme: %w", err)
		}
	}
	if err := t.SetStatusFormat(session, rig, worker, role); err != nil {
		return fmt.Errorf("setting status format: %w", err)
	}
	if err := t.SetDynamicStatus(session); err != nil {
		return fmt.Errorf("setting dynamic status: %w", err)
	}
	if err := t.SetMailClickBinding(session); err != nil {
		return fmt.Errorf("setting mail click binding: %w", err)
	}
	if err := t.SetFeedBinding(session); err != nil {
		return fmt.Errorf("setting feed binding: %w", err)
	}
	if err := t.SetAgentsBinding(session); err != nil {
		return fmt.Errorf("setting agents binding: %w", err)
	}
	if err := t.SetRigMenuBinding(session); err != nil {
		return fmt.Errorf("setting rig menu binding: %w", err)
	}
	if err := t.SetCycleBindings(session); err != nil {
		return fmt.Errorf("setting cycle bindings: %w", err)
	}
	if err := t.EnableMouseMode(session); err != nil {
		return fmt.Errorf("enabling mouse mode: %w", err)
	}
	return nil
}

// EnableMouseMode enables mouse support and clipboard integration for a tmux session.
// This allows clicking to select panes/windows, scrolling with mouse wheel,
// and dragging to resize panes. Hold Shift for native terminal text selection.
// Also enables clipboard integration so copied text goes to system clipboard.
//
// Respects the user's global mouse preference: if the global setting is "off",
// mouse is not forced on for the session, so prefix+m toggles work correctly.
func (t *Tmux) EnableMouseMode(session string) error {
	// Check global mouse setting — respect user toggle (prefix+m)
	out, err := t.run("show-options", "-gv", "mouse")
	if err == nil && strings.TrimSpace(out) == "off" {
		// User has globally disabled mouse; don't override per-session
		return nil
	}
	if _, err := t.run("set-option", "-t", session, "mouse", "on"); err != nil {
		return err
	}
	// Enable clipboard integration with terminal (OSC 52)
	// This allows copying text to system clipboard when selecting with mouse
	_, err = t.run("set-option", "-t", session, "set-clipboard", "on")
	return err
}

// IsInsideTmux checks if the current process is running inside a tmux session.
// This is detected by the presence of the TMUX environment variable.
func IsInsideTmux() bool {
	return os.Getenv("TMUX") != ""
}

// SetMailClickBinding configures left-click on status-right to show mail preview.
// This creates a popup showing the first unread message when clicking the mail icon area.
//
// The binding is conditional: it only activates in Gas Town sessions (those matching
// a registered rig prefix or "hq-"). In non-GT sessions, the user's original
// MouseDown1StatusRight binding (if any) is preserved.
// See: https://github.com/steveyegge/gastown/issues/1548
func (t *Tmux) SetMailClickBinding(session string) error {
	// Skip if already configured — preserves user's original fallback from first call
	if t.isGTBinding("root", "MouseDown1StatusRight") {
		return nil
	}
	ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", sessionPrefixPattern())
	fallback := t.getKeyBinding("root", "MouseDown1StatusRight")
	if fallback == "" {
		// No prior binding — do nothing in non-GT sessions
		fallback = ":"
	}
	_, err := t.run("bind-key", "-T", "root", "MouseDown1StatusRight",
		"if-shell", ifShell,
		"display-popup -E -w 60 -h 15 'gt mail peek || echo No unread mail'",
		fallback)
	return err
}
