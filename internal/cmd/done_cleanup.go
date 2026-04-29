package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/townlog"
)

// This file holds polecat-lifecycle cleanup helpers for `gt done`:
// actor-type detection, wisp purging, and the deprecated self-nuke / self-kill
// paths retained for explicit termination scenarios (the active path is the
// persistent-polecat IDLE transition inside runDone).

// isPolecatActor checks if a BD_ACTOR value represents a polecat.
// Polecat actors have format: rigname/polecats/polecatname
// Non-polecat actors have formats like: gastown/crew/name, rigname/witness, etc.
func isPolecatActor(actor string) bool {
	parts := strings.Split(actor, "/")
	return len(parts) >= 2 && parts[1] == "polecats"
}

// purgeClosedEphemeralBeads removes closed ephemeral beads (wisps) that accumulated
// during this and prior sessions. Polecat/witness sessions create mol-polecat-work
// steps, mol-witness-patrol cycles, etc. as wisps. These get closed during normal
// operation but are never deleted, accumulating hundreds of rows that pollute
// bd ready/list output. (hq-6161m)
//
// Best-effort: errors are logged but don't block gt done completion.
func purgeClosedEphemeralBeads(bd *beads.Beads) {
	out, err := bd.Run("purge", "--force", "--quiet")
	if err != nil {
		// Non-fatal: purge failure shouldn't block session completion
		fmt.Fprintf(os.Stderr, "Warning: wisp purge failed: %v\n", err)
		return
	}
	// bd purge --force --quiet outputs the count of purged beads
	outStr := strings.TrimSpace(string(out))
	if outStr != "" && outStr != "0" {
		fmt.Fprintf(os.Stderr, "Purged closed ephemeral beads: %s\n", outStr)
	}
}

// selfNukePolecat deletes this polecat's worktree.
// DEPRECATED (gt-4ac): No longer called from gt done. Polecats now go idle
// instead of self-nuking. Kept for explicit nuke scenarios.
// This is safe because:
// 1. Work has been pushed to origin (verified below)
// 2. We're about to exit anyway
// 3. Unix allows deleting directories while processes run in them
func selfNukePolecat(roleInfo RoleInfo, _ string) error {
	if roleInfo.Role != RolePolecat || roleInfo.Polecat == "" || roleInfo.Rig == "" {
		return fmt.Errorf("not a polecat: role=%s, polecat=%s, rig=%s", roleInfo.Role, roleInfo.Polecat, roleInfo.Rig)
	}

	// Get polecat manager using existing helper
	mgr, _, err := getPolecatManager(roleInfo.Rig)
	if err != nil {
		return fmt.Errorf("getting polecat manager: %w", err)
	}

	// Verify branch actually exists on a remote before nuking local copy.
	// If push didn't land (no remote, auth failure, etc.), preserve worktree
	// so Witness/Refinery can still access the branch.
	clonePath := mgr.ClonePath(roleInfo.Polecat)
	polecatGit := git.NewGit(clonePath)
	remotes, err := polecatGit.Remotes()
	if err != nil || len(remotes) == 0 {
		return fmt.Errorf("no git remotes configured — preserving worktree to prevent data loss")
	}
	branchName, err := polecatGit.CurrentBranch()
	if err != nil {
		return fmt.Errorf("cannot determine current branch — preserving worktree: %w", err)
	}
	pushed := false
	for _, remote := range remotes {
		exists, err := polecatGit.RemoteBranchExists(remote, branchName)
		if err == nil && exists {
			pushed = true
			break
		}
	}
	if !pushed {
		return fmt.Errorf("branch %s not found on any remote — preserving worktree", branchName)
	}

	// Use nuclear=true since we verified the branch is pushed
	// selfNuke=true because polecat is deleting its own worktree from inside it
	if err := mgr.RemoveWithOptions(roleInfo.Polecat, true, true, true); err != nil {
		return fmt.Errorf("removing worktree: %w", err)
	}

	return nil
}

// selfKillSession terminates the polecat's own tmux session after logging the event.
// DEPRECATED (gt-hdf8): No longer called from gt done. Polecats now transition to
// IDLE with session preserved instead of self-killing. Kept for explicit kill scenarios
// (e.g., Witness-directed termination).
//
// The polecat determines its session from environment variables:
// - GT_RIG: the rig name
// - GT_POLECAT: the polecat name
// Session name format: gt-<rig>-<polecat>
func selfKillSession(townRoot string, roleInfo RoleInfo) error {
	// Get session info from environment (set at session startup)
	rigName := os.Getenv("GT_RIG")
	polecatName := os.Getenv("GT_POLECAT")

	// Fall back to roleInfo if env vars not set (shouldn't happen but be safe)
	if rigName == "" {
		rigName = roleInfo.Rig
	}
	if polecatName == "" {
		polecatName = roleInfo.Polecat
	}

	if rigName == "" || polecatName == "" {
		return fmt.Errorf("cannot determine session: rig=%q, polecat=%q", rigName, polecatName)
	}

	sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)
	agentID := fmt.Sprintf("%s/polecats/%s", rigName, polecatName)

	// Log to townlog (human-readable audit log)
	if townRoot != "" {
		logger := townlog.NewLogger(townRoot)
		_ = logger.Log(townlog.EventKill, agentID, "self-clean: done means idle")
	}

	// Log to events (JSON audit log with structured payload)
	_ = events.LogFeed(events.TypeSessionDeath, agentID,
		events.SessionDeathPayload(sessionName, agentID, "self-clean: done means idle", "gt done"))

	// Kill our own tmux session with proper process cleanup
	// This will terminate Claude and all child processes, completing the self-cleaning cycle.
	// We use KillSessionWithProcessesExcluding to ensure no orphaned processes are left behind,
	// while excluding our own PID to avoid killing ourselves before cleanup completes.
	// The tmux kill-session at the end will terminate us along with the session.
	t := tmux.NewTmux()
	myPID := strconv.Itoa(os.Getpid())
	if err := t.KillSessionWithProcessesExcluding(sessionName, []string{myPID}); err != nil {
		return fmt.Errorf("killing session %s: %w", sessionName, err)
	}

	return nil
}
