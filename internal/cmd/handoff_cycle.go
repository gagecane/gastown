package cmd

// handoff_cycle.go — PreCompact-hook driven handoff modes.
//
// These entry points complement the primary `runHandoff` flow in handoff.go:
//
//   - runHandoffAuto:  state save only, no session cycling
//   - runHandoffCycle: full session respawn with --continue context carry-over
//
// Split out of handoff.go (gu-a1q) to keep each file focused.

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// runHandoffAuto saves state without cycling the session.
// Used by the PreCompact hook to preserve context before compaction.
// No tmux required — just collects state, sends handoff mail, and writes marker.
func runHandoffAuto() error {
	// Build subject
	subject := handoffSubject
	if subject == "" {
		reason := handoffReason
		if reason == "" {
			reason = "auto"
		}
		subject = fmt.Sprintf("🤝 HANDOFF: %s", reason)
	}

	// Auto-collect state if no explicit message
	message := handoffMessage
	if message == "" {
		message = collectHandoffState()
	}

	if handoffDryRun {
		fmt.Printf("[auto-handoff] Would send mail: subject=%q\n", subject)
		fmt.Printf("[auto-handoff] Would write handoff marker\n")
		return nil
	}

	// Close any in-progress molecule steps before state save (gt-e26g).
	cleanupMoleculeOnHandoff()

	// Send handoff mail to self
	beadID, err := sendHandoffMail(subject, message)
	if err != nil {
		// Non-fatal — log and continue
		fmt.Fprintf(os.Stderr, "auto-handoff: could not send mail: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "auto-handoff: saved state to %s\n", beadID)
	}

	// Write handoff marker so post-compact prime knows it's post-handoff
	if cwd, err := os.Getwd(); err == nil {
		runtimeDir := filepath.Join(cwd, constants.DirRuntime)
		_ = os.MkdirAll(runtimeDir, 0755)
		markerPath := filepath.Join(runtimeDir, constants.FileHandoffMarker)
		sessionName := "auto-handoff"
		if tmux.IsInsideTmux() {
			if name, err := getCurrentTmuxSession(); err == nil {
				sessionName = name
			}
		}
		_ = os.WriteFile(markerPath, []byte(sessionName), 0644)
	}

	// Log handoff event
	if townRoot, err := workspace.FindFromCwd(); err == nil && townRoot != "" {
		agent := detectSender()
		if agent == "" || agent == "overseer" {
			agent = "unknown"
		}
		_ = events.LogFeed(events.TypeHandoff, agent, events.HandoffPayload(subject, false))
	}

	return nil
}

// runHandoffCycle performs a full session cycle — save state AND respawn.
// This is the PreCompact-triggered session succession mechanism (gt-op78).
//
// Unlike --auto (state only) or normal handoff (polecat→gt-done redirect),
// --cycle always does a full respawn regardless of role. This enables
// crew workers (and polecats) to get a fresh context window when the
// current one fills up.
//
// The flow:
//  1. Auto-collect state (inbox, ready beads, hooked work)
//  2. Send handoff mail to self (auto-hooked for successor)
//  3. Write handoff marker (prevents handoff loop)
//  4. Respawn the tmux pane with a fresh Claude instance
//
// The successor session starts via SessionStart hook (gt prime --hook),
// finds the hooked work, and continues from where we left off.
func runHandoffCycle() error {
	// Build subject
	subject := handoffSubject
	if subject == "" {
		reason := handoffReason
		if reason == "" {
			reason = "context-cycle"
		}
		subject = fmt.Sprintf("🤝 HANDOFF: %s", reason)
	}

	// Auto-collect state if no explicit message
	message := handoffMessage
	if message == "" {
		message = collectHandoffState()
	}

	// Must be in tmux to respawn
	if !tmux.IsInsideTmux() {
		// Fall back to auto mode (save state only) if not in tmux
		fmt.Fprintf(os.Stderr, "handoff --cycle: not in tmux, falling back to state-save only\n")
		handoffMessage = message
		handoffSubject = subject
		return runHandoffAuto()
	}

	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		fmt.Fprintf(os.Stderr, "handoff --cycle: TMUX_PANE not set, falling back to state-save only\n")
		handoffMessage = message
		handoffSubject = subject
		return runHandoffAuto()
	}

	currentSession, err := getCurrentTmuxSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "handoff --cycle: could not get session: %v, falling back to state-save only\n", err)
		handoffMessage = message
		handoffSubject = subject
		return runHandoffAuto()
	}

	// Use the caller's socket for pane operations (same as runHandoff).
	callerSocket := tmux.SocketFromEnv()
	t := tmux.NewTmuxWithSocket(callerSocket)

	if handoffDryRun {
		fmt.Printf("[cycle] Would send handoff mail: subject=%q\n", subject)
		fmt.Printf("[cycle] Would write handoff marker\n")
		fmt.Printf("[cycle] Would execute: tmux clear-history -t %s\n", pane)
		fmt.Printf("[cycle] Would execute: tmux respawn-pane -k -t %s <restart-cmd>\n", pane)
		return nil
	}

	// Close any in-progress molecule steps before cycling (gt-e26g).
	cleanupMoleculeOnHandoff()

	// Send handoff mail to self (auto-hooked for successor).
	// Fatal on failure — same rationale as runHandoff: silent failure causes
	// the next session to find an empty hook and lose all context.
	beadID, err := sendHandoffMail(subject, message)
	if err != nil {
		agent := sessionToGTRole(currentSession)
		if agent == "" {
			agent = currentSession
		}
		if townRoot, trErr := workspace.FindFromCwd(); trErr == nil && townRoot != "" {
			_ = LogHandoffNoPersist(townRoot, agent, subject, err)
		}
		fmt.Fprintf(os.Stderr, "The session was NOT respawned. Fix the issue and retry.\n")
		return fmt.Errorf("handoff --cycle: mail failed to persist: %w", err)
	}
	fmt.Fprintf(os.Stderr, "handoff --cycle: saved state to %s\n", beadID)

	// Write handoff marker so post-cycle prime knows it's post-handoff.
	// Format: "session_id\nreason" — the reason enables isCompactResume()
	// to detect compaction-triggered cycles and use a lighter continuation
	// directive instead of full re-initialization. (GH#1965)
	if cwd, err := os.Getwd(); err == nil {
		runtimeDir := filepath.Join(cwd, constants.DirRuntime)
		_ = os.MkdirAll(runtimeDir, 0755)
		markerPath := filepath.Join(runtimeDir, constants.FileHandoffMarker)
		markerContent := currentSession
		if handoffReason != "" {
			markerContent += "\n" + handoffReason
		}
		_ = os.WriteFile(markerPath, []byte(markerContent), 0644)
	}

	// Record handoff time for cooldown enforcement (gt-058d).
	recordHandoffTime()

	// Log cycle event AFTER persistence succeeds.
	if townRoot, err := workspace.FindFromCwd(); err == nil && townRoot != "" {
		agent := sessionToGTRole(currentSession)
		if agent == "" {
			agent = currentSession
		}
		_ = LogHandoff(townRoot, agent, subject)
		_ = events.LogFeed(events.TypeHandoff, agent, events.HandoffPayload(subject, true))
	}

	// Build restart command with --continue so the new session resumes
	// the previous conversation (preserves context across compaction cycles).
	restartCmd, err := buildRestartCommandWithOpts(currentSession, buildRestartCommandOpts{
		ContinueSession: true,
		ContinuePrompt:  "Context compacted. Continue your previous task.",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "handoff --cycle: could not build restart command: %v\n", err)
		return err
	}

	fmt.Fprintf(os.Stderr, "handoff --cycle: cycling session %s\n", currentSession)

	// Set remain-on-exit so the pane survives process death during handoff
	if err := t.SetRemainOnExit(pane, true); err != nil {
		style.PrintWarning("could not set remain-on-exit: %v", err)
	}

	// Clear scrollback history before respawn
	if err := t.ClearHistory(pane); err != nil {
		style.PrintWarning("could not clear history: %v", err)
	}

	// Check if pane's working directory exists (may have been deleted)
	paneWorkDir, _ := t.GetPaneWorkDir(currentSession)
	if paneWorkDir != "" {
		if _, err := os.Stat(paneWorkDir); err != nil {
			if townRoot := detectTownRootFromCwd(); townRoot != "" {
				return t.RespawnPaneWithWorkDir(pane, townRoot, restartCmd)
			}
		}
	}

	// Respawn pane — this atomically kills current process and starts fresh
	return t.RespawnPane(pane, restartCmd)
}
