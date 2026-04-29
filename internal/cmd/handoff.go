package cmd

// handoff.go — primary entry point for the `gt handoff` command.
//
// This file defines the cobra command, its flags, and `runHandoff` (the
// self-handoff flow). PreCompact-hook modes and helpers live alongside:
//
//   - handoff_cycle.go:   runHandoffAuto and runHandoffCycle entry points
//   - handoff_session.go: session/role/path resolution and remote respawn
//   - handoff_restart.go: restart command + environment variable building
//   - handoff_state.go:   mail, state collection, cleanup, cooldown
//
// All files share the cmd package, so no public API changed in the split (gu-a1q).

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

var handoffCmd = &cobra.Command{
	Use:         "handoff [bead-or-role]",
	GroupID:     GroupWork,
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Hand off to a fresh session, work continues from hook",
	Long: `End watch. Hand off to a fresh agent session.

This is the canonical way to end any agent session. It handles all roles:

  - Mayor, Crew, Witness, Refinery, Deacon: Respawns with fresh Claude instance
  - Polecats: Calls 'gt done --status DEFERRED' (Witness handles lifecycle)

When run without arguments, hands off the current session.
When given a bead ID (gt-xxx, hq-xxx), hooks that work first, then restarts.
When given a role name, hands off that role's session (and switches to it).

Examples:
  gt handoff                          # Hand off current session
  gt handoff gt-abc                   # Hook bead, then restart
  gt handoff gt-abc -s "Fix it"       # Hook with context, then restart
  gt handoff -s "Context" -m "Notes"  # Hand off with custom message
  gt handoff -c                       # Collect state into handoff message
  gt handoff crew                     # Hand off crew session
  gt handoff mayor                    # Hand off mayor session

The --collect (-c) flag gathers current state (hooked work, inbox, ready beads,
in-progress items) and includes it in the handoff mail. This provides context
for the next session without manual summarization.

The --cycle flag triggers automatic session cycling (used by PreCompact hooks).
Unlike --auto (state only) or normal handoff (polecat→gt-done redirect), --cycle
always does a full respawn regardless of role. This enables crew workers and
polecats to get a fresh context window when the current one fills up.

Any molecule on the hook will be auto-continued by the new session.
The SessionStart hook runs 'gt prime' to restore context.`,
	RunE: runHandoff,
}

var (
	handoffWatch      bool
	handoffDryRun     bool
	handoffSubject    string
	handoffMessage    string
	handoffCollect    bool
	handoffStdin      bool
	handoffAuto       bool
	handoffCycle      bool
	handoffReason     string
	handoffNoGitCheck bool
	handoffYes        bool
)

func init() {
	handoffCmd.Flags().BoolVarP(&handoffWatch, "watch", "w", true, "Switch to new session (for remote handoff)")
	handoffCmd.Flags().BoolVarP(&handoffDryRun, "dry-run", "n", false, "Show what would be done without executing")
	handoffCmd.Flags().StringVarP(&handoffSubject, "subject", "s", "", "Subject for handoff mail (optional)")
	handoffCmd.Flags().StringVarP(&handoffMessage, "message", "m", "", "Message body for handoff mail (optional)")
	handoffCmd.Flags().BoolVarP(&handoffCollect, "collect", "c", false, "Auto-collect state (status, inbox, beads) into handoff message")
	handoffCmd.Flags().BoolVar(&handoffStdin, "stdin", false, "Read message body from stdin (avoids shell quoting issues)")
	handoffCmd.Flags().BoolVar(&handoffAuto, "auto", false, "Save state only, no session cycling (for PreCompact hooks)")
	handoffCmd.Flags().BoolVar(&handoffCycle, "cycle", false, "Auto-cycle session (for PreCompact hooks that want full session replacement)")
	handoffCmd.Flags().StringVar(&handoffReason, "reason", "", "Reason for handoff (e.g., 'compaction', 'idle')")
	handoffCmd.Flags().BoolVar(&handoffNoGitCheck, "no-git-check", false, "Skip git workspace cleanliness check")
	handoffCmd.Flags().BoolVarP(&handoffYes, "yes", "y", false, "Skip confirmation prompt (for automation and scripting)")
	rootCmd.AddCommand(handoffCmd)
}

func runHandoff(cmd *cobra.Command, args []string) error {
	// Handle --stdin: read message body from stdin (avoids shell quoting issues)
	if handoffStdin {
		if handoffMessage != "" {
			return fmt.Errorf("cannot use --stdin with --message/-m")
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		handoffMessage = strings.TrimRight(string(data), "\n")
	}

	// --auto mode: save state only, no session cycling.
	// Used by PreCompact hook to preserve state before compaction.
	// Note: auto-mode exits here, before the git-status warning check below.
	// This is intentional — auto-handoffs are triggered by hooks and should not
	// spam warnings. The --no-git-check flag has no effect in auto mode.
	if handoffAuto {
		return runHandoffAuto()
	}

	// --cycle mode: full session cycling, triggered by PreCompact hook.
	// Unlike --auto (state only), this replaces the current session with a fresh one.
	// Unlike normal handoff, this skips the polecat→gt-done redirect because
	// cycling preserves work state (the hook stays attached).
	//
	// Flow: collect state → send handoff mail → respawn pane (fresh Claude instance)
	// The successor session picks up hooked work via SessionStart hook (gt prime --hook).
	if handoffCycle {
		return runHandoffCycle()
	}

	// Check if we're a polecat - polecats use gt done instead.
	// Check GT_ROLE first: coordinators (mayor, witness, etc.) may have a stale
	// GT_POLECAT in their environment from spawning polecats. Only block if the
	// parsed role is actually polecat (handles compound forms like
	// "gastown/polecats/Toast"). If GT_ROLE is unset, fall back to GT_POLECAT.
	isPolecat := false
	polecatName := ""
	if role := os.Getenv("GT_ROLE"); role != "" {
		parsedRole, _, name := parseRoleString(role)
		if parsedRole == RolePolecat {
			isPolecat = true
			polecatName = name
			// Bare "polecat" role yields empty name; fall back to GT_POLECAT.
			if polecatName == "" {
				polecatName = os.Getenv("GT_POLECAT")
			}
		}
	} else if name := os.Getenv("GT_POLECAT"); name != "" {
		isPolecat = true
		polecatName = name
	}
	if isPolecat {
		fmt.Printf("%s Polecat detected (%s) - using gt done for handoff\n",
			style.Bold.Render("🐾"), polecatName)
		// Polecats don't respawn themselves - Witness handles lifecycle
		// Call gt done with DEFERRED status to preserve work state
		doneCmd := exec.Command("gt", "done", "--status", "DEFERRED")
		doneCmd.Stdout = os.Stdout
		doneCmd.Stderr = os.Stderr
		return doneCmd.Run()
	}

	// Prompt for confirmation unless --yes/-y was passed or stdin is not a TTY.
	// Only interactive (human) sessions get prompted; agent automation proceeds
	// without blocking on stdin (gas-6z0).
	if !handoffYes && !handoffDryRun && term.IsTerminal(int(os.Stdin.Fd())) {
		if !promptYesNo("Ready to hand off? This will restart the session.") {
			fmt.Println("Handoff canceled.")
			return nil
		}
	}

	// Enforce minimum handoff cooldown to prevent tight restart loops (gt-058d).
	// When a patrol agent (e.g., witness) completes quickly on idle rigs,
	// it can hand off immediately and the daemon respawns, creating a crash loop.
	enforceHandoffCooldown()

	// If --collect flag is set, auto-collect state into the message
	if handoffCollect {
		collected := collectHandoffState()
		if handoffMessage == "" {
			handoffMessage = collected
		} else {
			handoffMessage = handoffMessage + "\n\n---\n" + collected
		}
		if handoffSubject == "" {
			handoffSubject = "Session handoff with context"
		}
	}

	// Use a socket-aware Tmux for pane operations. The calling process may be
	// on a different tmux server than the town socket (e.g., default socket).
	// For self-handoff, pane operations (clear-history, respawn-pane) must target
	// the caller's own server. SocketFromEnv() reads $TMUX to find the right one.
	callerSocket := tmux.SocketFromEnv()
	t := tmux.NewTmuxWithSocket(callerSocket)
	// Town-socket Tmux for session-level queries (getSessionPane, etc.)
	townTmux := tmux.NewTmux()
	_ = townTmux // used later for remote handoff

	// Verify we're in tmux
	if !tmux.IsInsideTmux() {
		return fmt.Errorf("not running in tmux - cannot hand off")
	}

	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return fmt.Errorf("TMUX_PANE not set - cannot hand off")
	}

	// Get current session name from GT_ROLE (preferred) or tmux display-message.
	currentSession, err := getCurrentTmuxSession()
	if err != nil {
		return fmt.Errorf("getting session name: %w", err)
	}

	// Warn if workspace has uncommitted or unpushed work (wa-7967c).
	// Note: this checks the caller's cwd, not the target session's workdir.
	// For remote handoff (gt handoff <role>), the warning reflects the caller's
	// workspace state. Checking the target session's workdir would require tmux
	// pane introspection and is deferred to a future enhancement.
	if !handoffNoGitCheck {
		warnHandoffGitStatus()
	}

	// Determine target session and check for bead hook
	targetSession := currentSession
	if len(args) > 0 {
		arg := args[0]

		// Check if arg is a bead ID (gt-xxx, hq-xxx, bd-xxx, etc.)
		if looksLikeBeadID(arg) {
			// Hook the bead first
			if err := hookBeadForHandoff(arg); err != nil {
				return fmt.Errorf("hooking bead: %w", err)
			}
			// Update subject if not set
			if handoffSubject == "" {
				handoffSubject = fmt.Sprintf("🪝 HOOKED: %s", arg)
			}
		} else {
			// User specified a role to hand off
			targetSession, err = resolveRoleToSession(arg)
			if err != nil {
				return fmt.Errorf("resolving role: %w", err)
			}
		}
	}

	// Build the restart command
	restartCmd, err := buildRestartCommand(targetSession)
	if err != nil {
		return err
	}

	// If handing off a different session, we need to find its pane and respawn there.
	// Remote sessions live on the town socket, so use townTmux for their operations.
	if targetSession != currentSession {
		// Update tmux session env before respawn (not during dry-run — see below)
		updateSessionEnvForHandoff(townTmux, targetSession, "")
		return handoffRemoteSession(townTmux, targetSession, restartCmd)
	}

	// Close any in-progress molecule steps before cycling (gt-e26g).
	// Without this, patrol agents that handoff mid-cycle leak orphaned wisps.
	cleanupMoleculeOnHandoff()

	// Handing off ourselves - print feedback then respawn
	fmt.Printf("%s Handing off %s...\n", style.Bold.Render("🤝"), currentSession)

	// Resolve agent identity once for both success and failure paths.
	agent := sessionToGTRole(currentSession)
	if agent == "" {
		agent = currentSession
	}

	// Dry run mode - show what would happen (BEFORE any side effects)
	if handoffDryRun {
		if handoffSubject != "" || handoffMessage != "" {
			fmt.Printf("Would send handoff mail: subject=%q (auto-hooked)\n", handoffSubject)
		}
		fmt.Printf("Would execute: tmux clear-history -t %s\n", pane)
		fmt.Printf("Would execute: tmux respawn-pane -k -t %s %s\n", pane, restartCmd)
		return nil
	}

	// Update tmux session environment for liveness detection.
	// IsAgentAlive reads GT_PROCESS_NAMES via tmux show-environment (session env),
	// not from shell exports. The restart command sets shell exports for the child
	// process, but we must also update the session env so liveness checks work.
	// Placed after the dry-run guard to avoid mutating session state during dry-run.
	updateSessionEnvForHandoff(t, currentSession, "")

	// Send handoff mail to self (defaults applied inside sendHandoffMail).
	// The mail is auto-hooked so the next session picks it up.
	// CRITICAL: Mail must persist to Dolt BEFORE logging to town.log.
	// If Dolt is down, we must NOT log a false handoff to town.log.
	beadID, err := sendHandoffMail(handoffSubject, handoffMessage)
	if err != nil {
		// Handoff persistence failure is fatal — do not silently continue.
		// A silent failure causes the next session to find an empty hook,
		// losing all handoff context.
		if townRoot, trErr := workspace.FindFromCwd(); trErr == nil && townRoot != "" {
			_ = LogHandoffNoPersist(townRoot, agent, handoffSubject, err)
		}
		fmt.Fprintf(os.Stderr, "The session was NOT respawned. Fix the issue and retry 'gt handoff'.\n")
		return fmt.Errorf("handoff mail failed to persist (Dolt may be down): %w", err)
	}
	fmt.Printf("%s Sent handoff mail %s (auto-hooked)\n", style.Bold.Render("📬"), beadID)

	// Log handoff event AFTER Dolt persistence succeeds.
	// Previously this logged BEFORE sendHandoffMail, causing false entries
	// in town.log when Dolt was down.
	if townRoot, err := workspace.FindFromCwd(); err == nil && townRoot != "" {
		_ = LogHandoff(townRoot, agent, handoffSubject)
		_ = events.LogFeed(events.TypeHandoff, agent, events.HandoffPayload(handoffSubject, true))
	}

	// NOTE: reportAgentState("stopped") removed (gt-zecmc)
	// Agent liveness is observable from tmux - no need to record it in bead.
	// "Discover, don't track" principle: reality is truth, state is derived.

	// Clear scrollback history before respawn (resets copy-mode from [0/N] to [0/0])
	if err := t.ClearHistory(pane); err != nil {
		// Non-fatal - continue with respawn even if clear fails
		style.PrintWarning("could not clear history: %v", err)
	}

	// Write handoff marker for successor detection (prevents handoff loop bug).
	// The marker is cleared by gt prime after it outputs the warning.
	// This tells the new session "you're post-handoff, don't re-run /handoff"
	if cwd, err := os.Getwd(); err == nil {
		runtimeDir := filepath.Join(cwd, constants.DirRuntime)
		_ = os.MkdirAll(runtimeDir, 0755)
		markerPath := filepath.Join(runtimeDir, constants.FileHandoffMarker)
		_ = os.WriteFile(markerPath, []byte(currentSession), 0644)
	}

	// Record handoff time for cooldown enforcement (gt-058d).
	recordHandoffTime()

	// Set remain-on-exit so the pane survives process death during handoff.
	// Without this, killing processes causes tmux to destroy the pane before
	// we can respawn it. This is essential for tmux session reuse.
	if err := t.SetRemainOnExit(pane, true); err != nil {
		style.PrintWarning("could not set remain-on-exit: %v", err)
	}

	// NOTE: For self-handoff, we do NOT call KillPaneProcesses here.
	// That would kill the gt handoff process itself before it can call RespawnPane,
	// leaving the pane dead with no respawn. RespawnPane's -k flag handles killing
	// atomically - tmux kills the old process and spawns the new one together.
	// See: https://github.com/steveyegge/gastown/issues/859 (pane is dead bug)
	//
	// For orphan prevention, we rely on respawn-pane -k which sends SIGHUP/SIGTERM.
	// If orphans still occur, the solution is to adjust the restart command to
	// kill orphans at startup, not to kill ourselves before respawning.

	// Check if pane's working directory exists (may have been deleted)
	paneWorkDir, _ := t.GetPaneWorkDir(currentSession)
	if paneWorkDir != "" {
		if _, err := os.Stat(paneWorkDir); err != nil {
			if townRoot := detectTownRootFromCwd(); townRoot != "" {
				style.PrintWarning("pane working directory deleted, using town root")
				return t.RespawnPaneWithWorkDir(pane, townRoot, restartCmd)
			}
		}
	}

	// Use respawn-pane -k to atomically kill current process and start new one
	// Note: respawn-pane automatically resets remain-on-exit to off
	return t.RespawnPane(pane, restartCmd)
}
