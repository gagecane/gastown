package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/session"
)

// This file contains the daemon's reapers: the dead-polecat wisp reaper
// (resets assigned beads when their owning polecat has provably crashed)
// and the idle-polecat reaper (kills tmux sessions that survived gt done
// to free API slots). Both are invoked from heartbeat and are defensive
// by design — they require multiple converging signals before acting.


// reapDeadPolecatWisps resets in_progress/hooked beads assigned to polecats
// whose tmux sessions have been dead (with a stale heartbeat) for longer than
// the configured timeout. This complements checkPolecatSessionHealth, which
// detects crashes and notifies the witness but does NOT reset the stuck beads.
//
// Why this exists: when a polecat hard-crashes (OOM, tmux kill, machine reboot),
// its Stop hook never fires, so the bead it was working on stays in_progress
// forever. That triggers gt doctor patrol-not-stuck warnings on every run and
// requires manual `bd update --status=open` or `gt sling` to recover. The
// stuck-agent-dog plugin was supposed to handle this but its context-aware
// restart path does not reset beads. See gu-1x0j.
//
// The reap is conservative by design:
//   - Only runs for rigs with a polecats/ directory.
//   - Skips polecats whose directory is already gone (DetectOrphanedBeads
//     handles that case from the witness side).
//   - Requires BOTH a dead tmux session AND a stale heartbeat file — either
//     alone is insufficient evidence of a permanent crash (sessions briefly
//     disappear during rebuilds; heartbeats can go stale during long-running
//     commands).
//   - Requires the heartbeat file to actually exist. No heartbeat means we
//     can't prove liveness, so we defer to the witness orphan scan.
//   - Reverifies the session and heartbeat staleness immediately before reset
//     to narrow the TOCTOU window.
func (d *Daemon) reapDeadPolecatWisps() {
	opCfg := d.loadOperationalConfig().GetDaemonConfig()
	timeout := opCfg.DeadPolecatReapTimeoutD()
	if timeout <= 0 {
		// Explicitly disabled via config — treat <=0 as "off" to preserve the
		// escape hatch for operators who want to rely solely on DetectOrphanedBeads.
		return
	}

	d.rigPool.runPerRig(d.ctx, d.getKnownRigs(), func(ctx context.Context, rigName string) error {
		d.reapRigDeadPolecatWisps(rigName, timeout)
		return nil
	})
}

// reapRigDeadPolecatWisps scans a single rig for in_progress/hooked beads
// assigned to dead polecats and resets them.
func (d *Daemon) reapRigDeadPolecatWisps(rigName string, timeout time.Duration) {
	polecatsDir := filepath.Join(d.config.TownRoot, rigName, "polecats")
	if _, err := os.Stat(polecatsDir); err != nil {
		return // Rig has no polecats — nothing to reap.
	}

	polecatPrefix := rigName + "/polecats/"

	type beadInfo struct {
		ID       string `json:"id"`
		Assignee string `json:"assignee"`
		Status   string `json:"status"`
	}

	// List candidate beads in both hooked and in_progress states. The sling
	// flow leaves slung work as hooked; polecats flip to in_progress on claim.
	var candidates []beadInfo
	for _, status := range []string{"hooked", "in_progress"} {
		cmd := exec.Command(d.bdPath, "list", "--rig="+rigName, "--status="+status, "--json", "--limit=0") //nolint:gosec // G204: args are constructed internally
		setSysProcAttr(cmd)
		cmd.Dir = d.config.TownRoot
		cmd.Env = os.Environ()
		output, err := cmd.Output()
		if err != nil {
			d.logger.Printf("reap-dead-polecat-wisps: list %s for %s failed: %v", status, rigName, err)
			continue
		}
		if len(output) == 0 {
			continue
		}
		var batch []beadInfo
		if err := json.Unmarshal(output, &batch); err != nil {
			d.logger.Printf("reap-dead-polecat-wisps: parse %s for %s failed: %v", status, rigName, err)
			continue
		}
		for i := range batch {
			batch[i].Status = status
		}
		candidates = append(candidates, batch...)
	}

	if len(candidates) == 0 {
		return
	}

	for _, bead := range candidates {
		if bead.Assignee == "" || !strings.HasPrefix(bead.Assignee, polecatPrefix) {
			continue // Not assigned to a polecat in this rig.
		}
		polecatName := strings.TrimPrefix(bead.Assignee, polecatPrefix)
		if polecatName == "" || strings.Contains(polecatName, "/") {
			continue // Malformed assignee (nested path, etc.) — skip defensively.
		}

		d.maybeReapDeadPolecatBead(rigName, polecatName, bead.ID, bead.Status, polecatsDir, timeout)
	}
}

// maybeReapDeadPolecatBead resets a single bead if the owning polecat is
// provably dead (session gone + heartbeat stale) and the polecat directory
// still exists (so this is a crashed session, not a deleted polecat).
func (d *Daemon) maybeReapDeadPolecatBead(rigName, polecatName, beadID, status, polecatsDir string, timeout time.Duration) {
	polecatDir := filepath.Join(polecatsDir, polecatName)
	if _, err := os.Stat(polecatDir); err != nil {
		// Directory is gone — DetectOrphanedBeads (witness) handles this case
		// and knows how to distinguish truly orphaned beads from rename races.
		return
	}

	sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)

	alive, err := d.tmux.HasSession(sessionName)
	if err != nil {
		// Transient tmux error — err on the side of not reaping.
		return
	}
	if alive {
		return
	}

	// Heartbeat must exist AND be stale by at least `timeout`. Missing heartbeat
	// is not proof of death: it might just mean the polecat never touched one
	// (e.g. fresh install before heartbeat rollout), in which case we defer to
	// the witness orphan scanner instead of guessing.
	hb := polecat.ReadSessionHeartbeat(d.config.TownRoot, sessionName)
	if hb == nil {
		return
	}
	staleFor := time.Since(hb.Timestamp)
	if staleFor < timeout {
		return
	}

	// TOCTOU guard: re-check session + heartbeat immediately before reset.
	// A polecat could have been respawned between the initial checks and here.
	if alive2, err := d.tmux.HasSession(sessionName); err != nil || alive2 {
		return
	}
	hb2 := polecat.ReadSessionHeartbeat(d.config.TownRoot, sessionName)
	if hb2 == nil || time.Since(hb2.Timestamp) < timeout {
		return
	}

	// Reset bead to open with cleared assignee so the scheduler/sling flow
	// can re-dispatch it. We use bd update directly rather than routing
	// through the witness RECOVERED_BEAD pathway because:
	//   - The witness spawn-count ledger is intended for same-polecat respawn
	//     loops; a crashed polecat + fresh dispatch is a different failure mode.
	//   - The daemon already has authority to run bd update (see updateAgentHookBead).
	//   - Keeping the reset local avoids extra mail traffic and permanent Dolt
	//     commits on every heartbeat cycle.
	cmd := exec.Command(d.bdPath, "update", beadID, "--rig="+rigName, "--status=open", "--assignee=") //nolint:gosec // G204: args are constructed internally
	setSysProcAttr(cmd)
	cmd.Dir = d.config.TownRoot
	cmd.Env = append(os.Environ(), "BD_ACTOR=daemon")
	if output, err := cmd.CombinedOutput(); err != nil {
		d.logger.Printf("reap-dead-polecat-wisps: failed to reset %s (rig=%s polecat=%s): %v: %s",
			beadID, rigName, polecatName, err, strings.TrimSpace(string(output)))
		return
	}

	d.logger.Printf("reap-dead-polecat-wisps: reset %s (rig=%s polecat=%s prev_status=%s session=%s heartbeat_stale=%v threshold=%v)",
		beadID, rigName, polecatName, status, sessionName, staleFor.Truncate(time.Second), timeout)

	// Emit a session-death event so the activity feed and audit log capture the reap.
	_ = events.LogFeed(events.TypeSessionDeath, fmt.Sprintf("%s/%s", rigName, polecatName),
		events.SessionDeathPayload(sessionName, fmt.Sprintf("%s/polecats/%s", rigName, polecatName),
			fmt.Sprintf("dead-polecat-wisp-reap: bead=%s prev_status=%s heartbeat_stale=%v (threshold=%v)",
				beadID, status, staleFor.Truncate(time.Second), timeout),
			"daemon"))
}

// reapIdlePolecats kills polecat tmux sessions that have been idle too long.
// The persistent polecat model (gt-4ac) keeps sessions alive after gt done for reuse,
// but idle sessions consume API slots (Claude Code process stays alive at 0% CPU).
// This reaper checks heartbeat state and kills sessions idle longer than the threshold.
func (d *Daemon) reapIdlePolecats() {
	opCfg := d.loadOperationalConfig().GetDaemonConfig()
	idleTimeout := opCfg.PolecatIdleSessionTimeoutD()

	d.rigPool.runPerRig(d.ctx, d.getKnownRigs(), func(ctx context.Context, rigName string) error {
		d.reapRigIdlePolecats(rigName, idleTimeout)
		return nil
	})
}

// reapRigIdlePolecats checks all polecats in a rig and kills idle sessions.
func (d *Daemon) reapRigIdlePolecats(rigName string, timeout time.Duration) {
	polecatsDir := filepath.Join(d.config.TownRoot, rigName, "polecats")
	polecats, err := listPolecatWorktrees(polecatsDir)
	if err != nil {
		return // No polecats directory
	}

	for _, polecatName := range polecats {
		d.reapIdlePolecat(rigName, polecatName, timeout)
	}
}

// reapIdlePolecat checks a single polecat and kills it if idle too long.
// A polecat is considered idle if:
//   - Heartbeat state is "exiting" or "idle" and timestamp exceeds threshold, OR
//   - Heartbeat state is "working" but timestamp is stale AND the polecat has no
//     hooked work (agent_state=idle in beads). This catches polecats that completed
//     gt done — persistentPreRun resets heartbeat to "working" on every gt sub-command,
//     so after gt done finishes the heartbeat shows "working" with a stale timestamp.
func (d *Daemon) reapIdlePolecat(rigName, polecatName string, timeout time.Duration) {
	sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)

	// Only check sessions that are actually alive
	alive, err := d.tmux.HasSession(sessionName)
	if err != nil || !alive {
		return
	}

	// Read heartbeat to check state and idle duration
	hb := polecat.ReadSessionHeartbeat(d.config.TownRoot, sessionName)
	if hb == nil {
		return // No heartbeat file — can't determine state
	}

	staleDuration := time.Since(hb.Timestamp)
	if staleDuration < timeout {
		return // Heartbeat is fresh — polecat is active
	}

	state := hb.EffectiveState()

	// Explicitly idle or exiting — safe to reap
	if state == polecat.HeartbeatIdle || state == polecat.HeartbeatExiting {
		d.killIdlePolecat(rigName, polecatName, sessionName, staleDuration, timeout, string(state))
		return
	}

	// Heartbeat says "working" but is stale — check if polecat actually has hooked work.
	// If agent_state=idle in beads and no hook_bead, the polecat finished gt done
	// and is sitting idle (heartbeat wasn't updated to "idle" because persistentPreRun
	// resets to "working" on every gt sub-command during gt done).
	if state == polecat.HeartbeatWorking {
		prefix := beads.GetPrefixForRig(d.config.TownRoot, rigName)
		agentBeadID := beads.PolecatBeadIDWithPrefix(prefix, rigName, polecatName)
		info, err := d.getAgentBeadInfo(agentBeadID)
		if err != nil {
			// Agent bead lookup failed — polecat has no provable work.
			// If heartbeat is stale enough (2x timeout), reap anyway to prevent
			// indefinite API burn when bead infrastructure is degraded.
			// But first check if the agent is actually running (GH#3342).
			if staleDuration >= timeout*2 && !d.tmux.IsAgentRunning(sessionName) {
				d.killIdlePolecat(rigName, polecatName, sessionName, staleDuration, timeout, "working-bead-lookup-failed")
			}
			return
		}

		// If polecat has hooked work that is still open, it might be stuck (not idle).
		// Don't reap — let checkPolecatSessionHealth handle stuck polecats.
		// But if the hook_bead is closed, the work is done and this is just an idle
		// polecat with a stale hook reference — safe to reap.
		if info.HookBead != "" && !d.isBeadClosed(info.HookBead) {
			return
		}

		// Fallback: agent bead hook_bead may be stale (updateAgentHookBead is a
		// no-op since the sling code declared work bead assignee as authoritative).
		// Before killing, check if any work bead is assigned to this polecat with
		// a non-terminal status. This prevents the reaper from killing polecats
		// whose agent bead hook_bead points to a closed bead from a previous swarm
		// while the polecat is actively working on a newly-slung bead.
		assignee := fmt.Sprintf("%s/polecats/%s", rigName, polecatName)
		if d.hasAssignedOpenWork(rigName, assignee) {
			return
		}

		// No hooked work + stale heartbeat — but check if the agent process
		// is still actively running before reaping. A failed gt sling rollback
		// can clear the hook while the agent is still working (GH#3342).
		if d.tmux.IsAgentRunning(sessionName) {
			return
		}
		d.killIdlePolecat(rigName, polecatName, sessionName, staleDuration, timeout, "working-no-hook")
	}
}

// killIdlePolecat terminates an idle polecat session and cleans up.
func (d *Daemon) killIdlePolecat(rigName, polecatName, sessionName string, idleDuration, timeout time.Duration, reason string) {
	d.logger.Printf("Reaping idle polecat %s/%s (state=%s, idle %v, threshold %v)",
		rigName, polecatName, reason, idleDuration.Truncate(time.Second), timeout)

	// Kill the tmux session (and all descendant processes)
	if err := d.tmux.KillSessionWithProcesses(sessionName); err != nil {
		d.logger.Printf("Warning: failed to kill idle polecat session %s: %v", sessionName, err)
		return
	}

	// Clean up heartbeat file
	polecat.RemoveSessionHeartbeat(d.config.TownRoot, sessionName)

	d.logger.Printf("Reaped idle polecat %s/%s — session killed, API slot freed", rigName, polecatName)

	// Emit feed event so the activity feed shows the reap
	_ = events.LogFeed(events.TypeSessionDeath, fmt.Sprintf("%s/%s", rigName, polecatName),
		events.SessionDeathPayload(sessionName, fmt.Sprintf("%s/polecats/%s", rigName, polecatName),
			fmt.Sprintf("idle-reap: %s, idle %v (threshold %v)", reason, idleDuration.Truncate(time.Second), timeout),
			"daemon"))
}
