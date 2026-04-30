package cmd

// Work discovery and work-context propagation for `gt prime`.
//
// findAgentWork / findAgentWorkOnce look up the agent's hooked (or in-progress)
// bead, with role-aware retry for polecats, crew, and dogs to handle the race
// where dispatch has written hook state but Dolt replicas haven't caught up.
//
// injectWorkContext / setTmuxWorkContext propagate GT_WORK_RIG/BEAD/MOL into
// the current process env and the tmux session env so OTel attribution and
// subprocesses (bd, mail, …) see the right work identity.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/telemetry"
)

// findAgentWork looks up hooked or in-progress beads assigned to this agent.
// Primary: reads hook_bead from the agent bead (same strategy as detectSessionState/gt hook).
// Fallback: queries by assignee for agents without an agent bead.
// For polecats and crew, retries up to 3 times with 2-second delays to handle
// the timing race where hook state hasn't propagated by the time gt prime runs.
// See: https://github.com/steveyegge/gastown/issues/1438
//
// Returns (nil, nil) if no work is found.
// Returns (nil, err) if all attempts failed due to database errors — the caller
// MUST distinguish this from "no work" to avoid silently closing beads. (GH#2638)
func findAgentWork(ctx RoleContext) (*beads.Issue, error) {
	agentID := getAgentIdentity(ctx)
	if agentID == "" {
		return nil, nil
	}

	// Polecats, crew, and dogs use a retry loop to handle the timing race
	// where the hook write (status=hooked + assignee) hasn't propagated to
	// new Dolt connections by the time gt prime runs on session startup.
	// Dogs are especially affected since dispatch is fire-and-forget. (GH#2748)
	// Uses exponential backoff: 500ms, 1s, 2s, 4s, 8s (total ~15.5s max).
	// See: https://github.com/steveyegge/gastown/issues/2389
	//
	// On compact/resume, the agent already has work context in memory.
	// A single attempt suffices — retries would add ~15s of latency to
	// compaction hooks, causing non-Claude runtimes to report hook failure.
	maxAttempts := 1
	if (ctx.Role == RolePolecat || ctx.Role == RoleCrew || ctx.Role == RoleDog) && !isCompactResume() {
		maxAttempts = 5
	}

	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			time.Sleep(backoff)
			backoff *= 2
		}

		result, err := findAgentWorkOnce(ctx, agentID)
		if result != nil {
			return result, nil
		}
		if err != nil {
			lastErr = err
		} else {
			// Successful query returned no work — not a DB error
			lastErr = nil
		}
	}

	return nil, lastErr
}

// findAgentWorkOnce performs a single attempt to find hooked work for an agent.
// Returns (nil, nil) when no work is found.
// Returns (nil, err) when the database query itself failed — the caller must
// not treat this as "no work assigned". (GH#2638)
func findAgentWorkOnce(ctx RoleContext, agentID string) (*beads.Issue, error) {
	// Use rig root for beads queries instead of ctx.WorkDir. Polecat worktrees
	// rely on .beads/redirect which can fail to resolve in edge cases, causing
	// polecats to miss hooked work and exit immediately. The rig root directory
	// always has the authoritative .beads/ database. (GH#2503)
	b := beads.New(rigBeadsRoot(ctx))

	// Agent bead's hook_bead field. NOTE: updateAgentHookBead was made a no-op
	// (see sling_helpers.go), so HookBead is typically empty. Kept for backward
	// compatibility with agent beads that still have hook_bead set.
	agentBeadID := buildAgentBeadID(agentID, ctx.Role, ctx.TownRoot)
	if agentBeadID != "" {
		agentBeadDir := beads.ResolveHookDir(ctx.TownRoot, agentBeadID, ctx.WorkDir)
		ab := beads.New(agentBeadDir)
		if agentBead, err := ab.Show(agentBeadID); err == nil && agentBead != nil && agentBead.HookBead != "" {
			hookBeadDir := beads.ResolveHookDir(ctx.TownRoot, agentBead.HookBead, ctx.WorkDir)
			hb := beads.New(hookBeadDir)
			if hookBead, err := hb.Show(agentBead.HookBead); err == nil && hookBead != nil &&
				(hookBead.Status == beads.StatusHooked || hookBead.Status == "in_progress") {
				return hookBead, nil
			}
		}
	}

	// Fallback: query by assignee
	hookedBeads, err := b.List(beads.ListOptions{
		Status:   beads.StatusHooked,
		Assignee: agentID,
		Priority: -1,
	})
	if err != nil {
		return nil, fmt.Errorf("querying hooked beads: %w", err)
	}

	// Fall back to in_progress beads (session interrupted before completion)
	if len(hookedBeads) == 0 {
		inProgressBeads, err := b.List(beads.ListOptions{
			Status:   "in_progress",
			Assignee: agentID,
			Priority: -1,
		})
		if err != nil {
			return nil, fmt.Errorf("querying in-progress beads: %w", err)
		}
		if len(inProgressBeads) > 0 {
			hookedBeads = inProgressBeads
		}
	}

	// Town-level fallback: rig-level agents (polecats, crew) may have hooked
	// HQ beads (hq-* prefix) stored in townRoot/.beads, not the rig's database.
	// Matches the fallback in molecule_status.go and unsling.go. (gt-dtq7)
	if len(hookedBeads) == 0 && !isTownLevelRole(agentID) && ctx.TownRoot != "" {
		townB := beads.New(filepath.Join(ctx.TownRoot, ".beads"))
		if townHooked, err := townB.List(beads.ListOptions{
			Status:   beads.StatusHooked,
			Assignee: agentID,
			Priority: -1,
		}); err == nil && len(townHooked) > 0 {
			hookedBeads = townHooked
		} else if townIP, err := townB.List(beads.ListOptions{
			Status:   "in_progress",
			Assignee: agentID,
			Priority: -1,
		}); err == nil && len(townIP) > 0 {
			hookedBeads = townIP
		}
		// Town-level fallback errors are non-fatal — rig-level query succeeded
	}

	if len(hookedBeads) == 0 {
		return nil, nil
	}
	return hookedBeads[0], nil
}

// rigBeadsRoot returns the directory to use for beads queries.
// For rig-level agents (polecats, crew, witness, refinery), returns the rig
// root (e.g., ~/gt/myrig/) which has the authoritative .beads/ database.
// For town-level agents, returns ctx.WorkDir unchanged.
//
// This avoids relying on .beads/redirect in polecat worktrees, which can
// fail to resolve and cause polecats to see no hooked work. (GH#2503)
func rigBeadsRoot(ctx RoleContext) string {
	if ctx.Rig != "" && ctx.TownRoot != "" {
		return filepath.Join(ctx.TownRoot, ctx.Rig)
	}
	return ctx.WorkDir
}

// injectWorkContext extracts the current work context (rig, bead, molecule) from the
// hooked bead and persists it in two places so all subsequent subprocesses carry it:
//
//  1. Current process env (GT_WORK_RIG/BEAD/MOL via os.Setenv) — inherited by bd, mail,
//     and any other subprocess spawned from this gt prime invocation (e.g. bd prime).
//
//  2. Tmux session env (via tmux set-environment) — inherited by future processes
//     spawned in the session after a handoff or compaction (e.g. new Claude Code instance).
//
// These values are then read by telemetry.RecordPrime (defer in runPrime) and by
// telemetry.buildGTResourceAttrs which injects them into OTEL_RESOURCE_ATTRIBUTES for
// bd subprocesses launched from the Go SDK.
//
// When hookedBead is nil (no work on hook), the vars are cleared so stale context
// from a previous prime cycle does not leak into the current one.
// No-op in dry-run mode.
func injectWorkContext(ctx RoleContext, hookedBead *beads.Issue) {
	if primeDryRun || !telemetry.IsActive() {
		return
	}
	workRig := ""
	workBead := ""
	workMol := ""
	if hookedBead != nil {
		workRig = ctx.Rig
		workBead = hookedBead.ID
		if attachment := beads.ParseAttachmentFields(hookedBead); attachment != nil {
			workMol = attachment.AttachedMolecule
		}
	}
	_ = os.Setenv("GT_WORK_RIG", workRig)
	_ = os.Setenv("GT_WORK_BEAD", workBead)
	_ = os.Setenv("GT_WORK_MOL", workMol)
	setTmuxWorkContext(workRig, workBead, workMol)
}

// setTmuxWorkContext writes GT_WORK_RIG, GT_WORK_BEAD, GT_WORK_MOL into the current
// tmux session environment. Future processes spawned in the session (e.g. a new
// Claude Code instance after handoff/compaction) will inherit these values automatically.
// Empty values unset the variable in the session env to prevent stale context leaking
// across prime cycles. No-op when not running inside a tmux session.
func setTmuxWorkContext(workRig, workBead, workMol string) {
	if os.Getenv("TMUX") == "" {
		return
	}
	out, err := exec.Command("tmux", "display-message", "-p", "#{session_name}").Output()
	if err != nil {
		return
	}
	session := strings.TrimSpace(string(out))
	if session == "" {
		return
	}
	setOrUnset := func(key, value string) {
		if value != "" {
			_ = exec.Command("tmux", "set-environment", "-t", session, key, value).Run()
		} else {
			_ = exec.Command("tmux", "set-environment", "-u", "-t", session, key).Run()
		}
	}
	setOrUnset("GT_WORK_RIG", workRig)
	setOrUnset("GT_WORK_BEAD", workBead)
	setOrUnset("GT_WORK_MOL", workMol)
}
