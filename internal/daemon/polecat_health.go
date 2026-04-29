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
	"github.com/steveyegge/gastown/internal/session"
)

// This file contains per-polecat crash detection, mass-death aggregation, and
// the witness-notification path. These run on every heartbeat to spot
// polecats whose tmux session has died while they still have assigned work,
// which would otherwise leave beads stuck until the witness orphan scan.
// Idle/dead reaping and scheduler dispatch live in reaper.go and
// maintenance.go respectively.

// sessionDeath records a detected session death for mass death analysis.
type sessionDeath struct {
	sessionName string
	timestamp   time.Time
}

// Mass death detection parameters — fallback defaults used when
// TownSettings does not override them.
const (
	massDeathWindow    = 30 * time.Second // Time window to detect mass death
	massDeathThreshold = 3                // Number of deaths to trigger alert
)

// checkPolecatSessionHealth proactively validates polecat tmux sessions.
// This detects crashed polecats that:
// 1. Have work-on-hook (assigned work)
// 2. Report state=running/working in their agent bead
// 3. But the tmux session is actually dead
//
// When a crash is detected, the polecat is automatically restarted.
// This provides faster recovery than waiting for GUPP timeout or Witness detection.
func (d *Daemon) checkPolecatSessionHealth() {
	d.rigPool.runPerRig(d.ctx, d.getKnownRigs(), func(ctx context.Context, rigName string) error {
		d.checkRigPolecatHealth(rigName)
		return nil
	})
}

// checkRigPolecatHealth checks polecat session health for a specific rig.
func (d *Daemon) checkRigPolecatHealth(rigName string) {
	// Get polecat directories for this rig
	polecatsDir := filepath.Join(d.config.TownRoot, rigName, "polecats")
	polecats, err := listPolecatWorktrees(polecatsDir)
	if err != nil {
		return // No polecats directory - rig might not have polecats
	}

	for _, polecatName := range polecats {
		d.checkPolecatHealth(rigName, polecatName)
	}
}

func listPolecatWorktrees(polecatsDir string) ([]string, error) {
	entries, err := os.ReadDir(polecatsDir)
	if err != nil {
		return nil, err
	}

	polecats := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		polecats = append(polecats, name)
	}

	return polecats, nil
}

// checkPolecatHealth checks a single polecat's session health.
// If the polecat has work-on-hook but the tmux session is dead, it's restarted.
func (d *Daemon) checkPolecatHealth(rigName, polecatName string) {
	// Build the expected tmux session name
	sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)

	// Check if tmux session exists
	sessionAlive, err := d.tmux.HasSession(sessionName)
	if err != nil {
		d.logger.Printf("Error checking session %s: %v", sessionName, err)
		return
	}

	if sessionAlive {
		// Session is alive - nothing to do
		return
	}

	// Session is dead. Check if the polecat has work-on-hook.
	prefix := beads.GetPrefixForRig(d.config.TownRoot, rigName)
	agentBeadID := beads.PolecatBeadIDWithPrefix(prefix, rigName, polecatName)
	info, err := d.getAgentBeadInfo(agentBeadID)
	if err != nil {
		// Agent bead doesn't exist or error - polecat might not be registered
		return
	}

	// Check if polecat has hooked work
	if info.HookBead == "" {
		// No hooked work - this polecat is orphaned (should have self-nuked).
		// Self-cleaning model: polecats nuke themselves on completion.
		// An orphan with a dead session doesn't need restart - it needs cleanup.
		// Let the Witness handle orphan detection/cleanup during patrol.
		return
	}

	// Terminal state guard: skip polecats in intentional shutdown states.
	// agent_state='done' means normal completion; agent_state='nuked' means forced shutdown.
	// Their sessions being dead is expected, not a crash. Without this check,
	// the dead session + open hook_bead combination can fire false CRASHED_POLECAT
	// alerts during the race window before the hook_bead is closed.
	// This check is pure in-memory (info.State is already populated), so it runs before
	// the more expensive isBeadClosed subprocess call.
	agentState := beads.AgentState(info.State)
	if agentState == beads.AgentStateDone || agentState == beads.AgentStateNuked {
		d.logger.Printf("Skipping crash detection for %s/%s: agent_state=%s (intentional shutdown, not a crash)",
			rigName, polecatName, info.State)
		return
	}

	// Stale hook guard: skip polecats whose hook_bead is already closed.
	// When a polecat completes work normally (gt done), the hook_bead gets closed
	// but may not be cleared from the agent bead before the session stops.
	// Without this check, every heartbeat cycle fires a false CRASHED_POLECAT alert
	// for the dead session + non-empty hook_bead combination.
	if d.isBeadClosed(info.HookBead) {
		d.logger.Printf("Skipping crash detection for %s/%s: hook_bead %s is already closed (work completed normally)",
			rigName, polecatName, info.HookBead)
		return
	}

	// Spawning guard: skip polecats being actively started by gt sling.
	// agent_state='spawning' means the polecat bead was created (with hook_bead
	// set atomically) but the tmux session hasn't been launched yet. Restarting
	// here would create a second Claude process alongside the one gt sling is
	// about to start, causing the double-spawn bug (issue #1752).
	//
	// Time-bound: only skip if the bead was updated recently (within 5 minutes).
	// If gt sling crashed during spawn, the polecat would be stuck in 'spawning'
	// indefinitely. The Witness patrol also catches spawning-as-zombie, but a
	// time-bound here makes the daemon self-sufficient for this edge case.
	if beads.AgentState(info.State) == beads.AgentStateSpawning {
		if updatedAt, err := time.Parse(time.RFC3339, info.LastUpdate); err == nil {
			if time.Since(updatedAt) < 5*time.Minute {
				d.logger.Printf("Skipping restart for %s/%s: agent_state=spawning (gt sling in progress, updated %s ago)",
					rigName, polecatName, time.Since(updatedAt).Round(time.Second))
				return
			}
			d.logger.Printf("Spawning guard expired for %s/%s: agent_state=spawning but last updated %s ago (>5m), proceeding with crash detection",
				rigName, polecatName, time.Since(updatedAt).Round(time.Second))
		} else {
			// Can't parse timestamp — be safe, skip restart during spawning
			d.logger.Printf("Skipping restart for %s/%s: agent_state=spawning (gt sling in progress, unparseable updated_at)",
				rigName, polecatName)
			return
		}
	}

	// TOCTOU guard: re-verify session is still dead before restarting.
	// Between the initial check and now, the session may have been restarted
	// by another heartbeat cycle, witness, or the polecat itself.
	sessionRevived, err := d.tmux.HasSession(sessionName)
	if err == nil && sessionRevived {
		return // Session came back - no restart needed
	}

	// Polecat has work but session is dead - this is a crash!
	d.logger.Printf("CRASH DETECTED: polecat %s/%s has hook_bead=%s but session %s is dead",
		rigName, polecatName, info.HookBead, sessionName)

	// Track this death for mass death detection
	d.recordSessionDeath(sessionName)

	// Emit session_death event for audit trail / feed visibility
	_ = events.LogFeed(events.TypeSessionDeath, sessionName,
		events.SessionDeathPayload(sessionName, rigName+"/polecats/"+polecatName, "crash detected by daemon health check", "daemon"))

	// Notify witness — stuck-agent-dog plugin handles context-aware restart
	d.notifyWitnessOfCrashedPolecat(rigName, polecatName, info.HookBead)
}

// recordSessionDeath records a session death and checks for mass death pattern.
func (d *Daemon) recordSessionDeath(sessionName string) {
	d.deathsMu.Lock()
	defer d.deathsMu.Unlock()

	now := time.Now()

	// Add this death
	d.recentDeaths = append(d.recentDeaths, sessionDeath{
		sessionName: sessionName,
		timestamp:   now,
	})

	// Prune deaths outside the window
	cutoff := now.Add(-massDeathWindow)
	var recent []sessionDeath
	for _, death := range d.recentDeaths {
		if death.timestamp.After(cutoff) {
			recent = append(recent, death)
		}
	}
	d.recentDeaths = recent

	// Check for mass death
	if len(d.recentDeaths) >= massDeathThreshold {
		d.emitMassDeathEvent()
	}
}

// emitMassDeathEvent logs a mass death event when multiple sessions die in a short window.
func (d *Daemon) emitMassDeathEvent() {
	// Collect session names
	var sessions []string
	for _, death := range d.recentDeaths {
		sessions = append(sessions, death.sessionName)
	}

	count := len(sessions)
	window := massDeathWindow.String()

	d.logger.Printf("MASS DEATH DETECTED: %d sessions died in %s: %v", count, window, sessions)

	// Emit feed event
	_ = events.LogFeed(events.TypeMassDeath, "daemon",
		events.MassDeathPayload(count, window, sessions, ""))

	// Clear the deaths to avoid repeated alerts
	d.recentDeaths = nil
}

// isBeadClosed checks if a bead's status is "closed" by querying bd show --json.
// Returns true if the bead exists and has status "closed", false otherwise.
// On any error (bead not found, bd failure), returns false to err on the side
// of crash detection rather than silently suppressing alerts.
func (d *Daemon) isBeadClosed(beadID string) bool {
	cmd := exec.Command(d.bdPath, "show", beadID, "--json") //nolint:gosec // G204: args are constructed internally
	setSysProcAttr(cmd)
	cmd.Dir = d.config.TownRoot
	cmd.Env = os.Environ()

	output, err := cmd.Output()
	if err != nil {
		return false
	}

	var issues []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(output, &issues); err != nil || len(issues) == 0 {
		return false
	}

	return issues[0].Status == "closed"
}

// hasAssignedOpenWork checks if any work bead is assigned to the given polecat
// with a non-terminal status (hooked, in_progress, or open). This is the
// authoritative source of polecat work — the sling code sets status=hooked +
// assignee on the work bead, but no longer maintains the agent bead's hook_bead
// field (updateAgentHookBead is a no-op). Without this fallback, the idle reaper
// kills working polecats whose agent bead hook_bead is stale.
func (d *Daemon) hasAssignedOpenWork(rigName, assignee string) bool {
	for _, status := range []string{"hooked", "in_progress", "open"} {
		cmd := exec.Command(d.bdPath, "list", "--rig="+rigName, "--assignee="+assignee, "--status="+status, "--json") //nolint:gosec // G204: args are constructed internally
		cmd.Dir = d.config.TownRoot
		cmd.Env = os.Environ()
		output, err := cmd.Output()
		if err != nil {
			continue
		}
		var issues []json.RawMessage
		if json.Unmarshal(output, &issues) == nil && len(issues) > 0 {
			return true
		}
	}
	return false
}

// notifyWitnessOfCrashedPolecat notifies the witness when a polecat crash is detected.
// The stuck-agent-dog plugin handles context-aware restart decisions.
func (d *Daemon) notifyWitnessOfCrashedPolecat(rigName, polecatName, hookBead string) {
	witnessAddr := rigName + "/witness"
	subject := fmt.Sprintf("CRASHED_POLECAT: %s/%s detected", rigName, polecatName)
	body := fmt.Sprintf(`Polecat %s crash detected (session dead, work on hook).

hook_bead: %s

Restart deferred to stuck-agent-dog plugin for context-aware recovery.`,
		polecatName, hookBead)

	cmd := exec.Command(d.gtPath, "mail", "send", witnessAddr, "-s", subject, "-m", body) //nolint:gosec // G204: args are constructed internally
	setSysProcAttr(cmd)
	cmd.Dir = d.config.TownRoot
	cmd.Env = append(os.Environ(), "BD_ACTOR=daemon") // Identify as daemon, not overseer
	if err := cmd.Run(); err != nil {
		d.logger.Printf("Warning: failed to notify witness of crashed polecat: %v", err)
	}
}
