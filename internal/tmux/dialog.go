package tmux

import (
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
)

// AcceptStartupDialogs dismisses startup dialogs that can block automated
// sessions. Currently handles (in order):
//  1. Workspace trust dialog (Claude "Quick safety check", Codex "Do you trust the contents of this directory?")
//  2. Bypass permissions warning ("Bypass Permissions mode") — requires Down+Enter
//
// Call this after starting the agent and waiting for it to initialize (WaitForCommand),
// but before sending any prompts. Idempotent: safe to call on sessions without dialogs.
func (t *Tmux) AcceptStartupDialogs(session string) error {
	if err := t.AcceptWorkspaceTrustDialog(session); err != nil {
		return fmt.Errorf("workspace trust dialog: %w", err)
	}
	if err := t.AcceptBypassPermissionsWarning(session); err != nil {
		return fmt.Errorf("bypass permissions warning: %w", err)
	}
	return nil
}

// AcceptWorkspaceTrustDialog dismisses workspace trust dialogs for supported
// agents. Claude shows "Quick safety check"; Codex shows
// "Do you trust the contents of this directory?". In both cases the safe
// continue option is pre-selected, so Enter accepts the dialog.
//
// Uses a polling loop instead of a single check to handle the race condition where
// the agent hasn't rendered the dialog yet when we first check. Exits early if the
// agent prompt appears (indicating no dialog will be shown).
func (t *Tmux) AcceptWorkspaceTrustDialog(session string) error {
	deadline := time.Now().Add(constants.DialogPollTimeout)
	for time.Now().Before(deadline) {
		content, err := t.CapturePane(session, 30)
		if err != nil {
			time.Sleep(constants.DialogPollInterval)
			continue
		}

		// Look for characteristic trust dialog text before prompt detection.
		// Codex trust screens include a leading ">" banner line, so prompt
		// detection alone would exit too early.
		if containsWorkspaceTrustDialog(content) {
			// Dialog found — accept it (option 1 is pre-selected, just press Enter)
			if _, err := t.run("send-keys", "-t", session, "Enter"); err != nil {
				return err
			}
			// Wait for dialog to dismiss before proceeding
			time.Sleep(500 * time.Millisecond)
			return nil
		}

		// Early exit: if agent prompt or shell prompt is visible, no trust dialog will appear.
		// Claude prompt is ">", shell prompts are "$", "%", "#".
		// Also exit if bypass permissions dialog is next (handled by AcceptBypassPermissionsWarning).
		if containsPromptIndicator(content) || strings.Contains(content, "Bypass Permissions mode") {
			return nil
		}

		time.Sleep(constants.DialogPollInterval)
	}

	// Timeout — no dialog detected, safe to proceed
	return nil
}

func containsWorkspaceTrustDialog(content string) bool {
	return strings.Contains(content, "trust this folder") ||
		strings.Contains(content, "Quick safety check") ||
		strings.Contains(content, "Do you trust the contents of this directory?")
}

// promptSuffixes are strings that indicate a shell or agent prompt is visible.
// Claude prompt ends with ">", shell prompts end with "$", "%", "#", or "❯".
var promptSuffixes = []string{">", "$", "%", "#", "❯"}

// containsPromptIndicator checks if pane content contains a prompt indicator
// that signals a shell or agent is ready (no dialog blocking it).
func containsPromptIndicator(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		for _, suffix := range promptSuffixes {
			if strings.HasSuffix(trimmed, suffix) {
				return true
			}
		}
	}
	return false
}

// AcceptBypassPermissionsWarning dismisses the Claude Code bypass permissions warning dialog.
// When Claude starts with --dangerously-skip-permissions, it shows a warning dialog that
// requires pressing Down arrow to select "Yes, I accept" and then Enter to confirm.
// This function checks if the warning is present before sending keys to avoid interfering
// with sessions that don't show the warning (e.g., already accepted or different config).
//
// Uses a polling loop instead of a single check to handle the race condition where
// Claude hasn't rendered the dialog yet when we first check. Exits early if the
// agent prompt appears (indicating no dialog will be shown).
//
// Call this after starting Claude and waiting for it to initialize (WaitForCommand),
// but before sending any prompts.
func (t *Tmux) AcceptBypassPermissionsWarning(session string) error {
	deadline := time.Now().Add(constants.DialogPollTimeout)
	for time.Now().Before(deadline) {
		content, err := t.CapturePane(session, 30)
		if err != nil {
			time.Sleep(constants.DialogPollInterval)
			continue
		}

		// Look for the characteristic warning text
		if strings.Contains(content, "Bypass Permissions mode") {
			// Dialog found — press Down to select "Yes, I accept" then Enter
			if _, err := t.run("send-keys", "-t", session, "Down"); err != nil {
				return err
			}
			time.Sleep(200 * time.Millisecond)
			if _, err := t.run("send-keys", "-t", session, "Enter"); err != nil {
				return err
			}
			return nil
		}

		// Early exit: if agent prompt or shell prompt is visible, no dialog will appear
		if containsPromptIndicator(content) {
			return nil
		}

		time.Sleep(constants.DialogPollInterval)
	}

	// Timeout — no dialog detected, safe to proceed
	return nil
}

// DismissStartupDialogsBlind sends the key sequences needed to dismiss all
// known Claude Code startup dialogs without screen-scraping pane content.
// This avoids coupling to third-party TUI strings that can change with any update.
//
// The sequence handles (in order):
//  1. Workspace trust dialog — Enter (option 1 "Yes, I trust this folder" is pre-selected)
//  2. Bypass permissions warning — Down+Enter (select "Yes, I accept" then confirm)
//
// Safe to call on sessions where no dialog is showing: Enter sends a blank input
// to an idle Claude prompt (harmless for a stalled session), and Down+Enter either
// does nothing or sends another blank input.
//
// This is intended for remediation of stalled sessions detected via structured
// signals (session age + activity). For startup-time dialog handling where
// precision matters, use AcceptStartupDialogs instead.
func (t *Tmux) DismissStartupDialogsBlind(session string) error {
	// Step 1: Send Enter to dismiss trust dialog (if present)
	if _, err := t.run("send-keys", "-t", session, "Enter"); err != nil {
		return fmt.Errorf("sending Enter for trust dialog: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Step 2: Send Down+Enter to dismiss bypass permissions dialog (if present)
	if _, err := t.run("send-keys", "-t", session, "Down"); err != nil {
		return fmt.Errorf("sending Down for bypass dialog: %w", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := t.run("send-keys", "-t", session, "Enter"); err != nil {
		return fmt.Errorf("sending Enter for bypass dialog: %w", err)
	}

	return nil
}
