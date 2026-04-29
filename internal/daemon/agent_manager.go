package daemon

// Agent manager: lifecycle helpers for every tmux-hosted Claude session the
// daemon spawns or supervises — Deacon, Boot, Witnesses, Refineries, Mayor —
// plus the kill/ghost cleanup path for when a patrol is disabled.
//
// Each "ensure<Role>Running" is idempotent: it spawns the agent if it's not
// already running, otherwise no-ops. The "kill<Role>Sessions" helpers are
// the symmetric teardown for when a patrol is disabled.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/boot"
	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/mayor"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/witness"
)

// DeaconRole is the role name for the Deacon's handoff bead.
const DeaconRole = "deacon"

// getDeaconSessionName returns the Deacon session name for the daemon's town.
func (d *Daemon) getDeaconSessionName() string {
	return session.DeaconSessionName()
}

// ensureBootRunning spawns Boot to triage the Deacon.
// Boot is a fresh-each-tick watchdog that decides whether to start/wake/nudge
// the Deacon, centralizing the "when to wake" decision in an agent.
// In degraded mode (no tmux), falls back to mechanical checks.
// bootSpawnCooldown returns the config-driven boot spawn cooldown.
// Boot triage runs are expensive (AI reasoning); if one just ran, skip.
func (d *Daemon) bootSpawnCooldown() time.Duration {
	return d.loadOperationalConfig().GetDaemonConfig().BootSpawnCooldownD()
}

func (d *Daemon) ensureBootRunning() {
	// Cooldown gate: skip if Boot was spawned recently (fixes #2084)
	if !d.bootLastSpawned.IsZero() && time.Since(d.bootLastSpawned) < d.bootSpawnCooldown() {
		d.logger.Printf("Boot spawned %s ago, within cooldown (%s), skipping",
			time.Since(d.bootLastSpawned).Round(time.Second), d.bootSpawnCooldown())
		return
	}

	// Idle guard: skip if Deacon is healthy AND no beads are actively in flight.
	//
	// Boot's job is to triage a stuck or unresponsive Deacon and to flag stuck
	// in_progress/hooked work. If Deacon has written a fresh heartbeat and no
	// beads are in_progress or hooked, there is nothing to triage.
	//
	// We deliberately do NOT update bootLastSpawned on an idle skip: the cooldown
	// is about rate-limiting real spawns; the idle check should re-run every
	// heartbeat so Boot fires promptly when work actually appears.
	hb := deacon.ReadHeartbeat(d.config.TownRoot)
	if hb != nil && hb.IsFresh() && !d.hasActiveWork() {
		d.logger.Println("Boot spawn skipped: Deacon is healthy and no active work in flight")
		return
	}

	b := boot.New(d.config.TownRoot)

	// Check for degraded mode
	degraded := os.Getenv("GT_DEGRADED") == "true"
	if degraded || !d.tmux.IsAvailable() {
		// In degraded mode, run mechanical triage directly
		d.logger.Println("Degraded mode: running mechanical Boot triage")
		d.runDegradedBootTriage(b)
		return
	}

	// Idle check: run gt-idle-check to see if the system needs waking.
	// If idle (all rigs parked, no polecats, deacon alive), skip the expensive
	// Claude Boot session and use degraded mechanical triage instead.
	// This saves ~480 Claude sessions/day when Gas Town is not in active use.
	idleCheckBin := filepath.Join(d.config.TownRoot, "bin", "gt-idle-check")
	if _, err := os.Stat(idleCheckBin); err == nil {
		//nolint:gosec // G204: path is constructed from config
		cmd := exec.Command(idleCheckBin)
		cmd.Env = append(os.Environ(), fmt.Sprintf("PATH=%s:%s",
			filepath.Join(d.config.TownRoot, "bin"), os.Getenv("PATH")))
		if output, err := cmd.CombinedOutput(); err == nil {
			// Exit 0 = idle, use degraded triage (zero tokens)
			d.runDegradedBootTriage(b)
			return
		} else {
			// Exit 1 = needs waking, proceed to full Claude Boot
			d.logger.Printf("Idle check: waking — %s", strings.TrimSpace(string(output)))
		}
	}

	// Spawn Boot in a fresh tmux session
	d.logger.Println("Spawning Boot for triage...")
	if err := b.Spawn(""); err != nil {
		d.logger.Printf("Error spawning Boot: %v, falling back to direct Deacon check", err)
		// Fallback: ensure Deacon is running directly
		d.ensureDeaconRunning()
		return
	}

	d.bootLastSpawned = time.Now()
	d.logger.Println("Boot spawned successfully")
}

// hasActiveWork returns true if any bead store has in_progress or hooked beads.
// These are the only states Boot can meaningfully act on: in_progress work may be
// stuck, and hooked work is waiting on a polecat that may have died.
//
// Returns true conservatively on error or when no stores are available, so the
// caller falls through to spawn Boot rather than suppressing it incorrectly.
func (d *Daemon) hasActiveWork() bool {
	if len(d.beadsStores) == 0 {
		// No stores open — cannot inspect; let Boot run to be safe.
		return true
	}

	ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
	defer cancel()

	for name, store := range d.beadsStores {
		for _, rawStatus := range []string{"in_progress"} {
			s := beadsdk.Status(rawStatus)
			filter := beadsdk.IssueFilter{Status: &s, Limit: 1}
			issues, err := store.SearchIssues(ctx, "", filter)
			if err != nil {
				d.logger.Printf("hasActiveWork: %s/%s query failed: %v — assuming work present",
					name, rawStatus, err)
				return true // conservative: don't suppress Boot on query failure
			}
			if len(issues) > 0 {
				return true
			}
		}
	}
	return false
}

// runDegradedBootTriage performs mechanical Boot logic without AI reasoning.
// This is for degraded mode when tmux is unavailable.
func (d *Daemon) runDegradedBootTriage(b *boot.Boot) {
	startTime := time.Now()
	status := &boot.Status{
		StartedAt: startTime,
	}

	// Simple check: is Deacon session alive?
	hasDeacon, err := d.tmux.HasSession(d.getDeaconSessionName())
	if err != nil {
		d.logger.Printf("Error checking Deacon session: %v", err)
		status.LastAction = "error"
		status.Error = err.Error()
	} else if !hasDeacon {
		d.logger.Println("Deacon not running, starting...")
		d.ensureDeaconRunning()
		status.LastAction = "start"
		status.Target = "deacon"
	} else {
		status.LastAction = "nothing"
	}

	status.CompletedAt = time.Now()

	if err := b.SaveStatus(status); err != nil {
		d.logger.Printf("Warning: failed to save Boot status: %v", err)
	}
}

// ensureDeaconRunning ensures the Deacon is running.
// Uses deacon.Manager for consistent startup behavior (WaitForShellReady, GUPP, etc.).
func (d *Daemon) ensureDeaconRunning() {
	const agentID = "deacon"

	// Check restart tracker for backoff/crash loop
	if d.restartTracker != nil {
		if d.restartTracker.IsInCrashLoop(agentID) {
			d.logger.Printf("Deacon is in crash loop, skipping restart (use 'gt daemon clear-backoff deacon' to reset)")
			return
		}
		if !d.restartTracker.CanRestart(agentID) {
			remaining := d.restartTracker.GetBackoffRemaining(agentID)
			d.logger.Printf("Deacon restart in backoff, %s remaining", remaining.Round(time.Second))
			return
		}
	}

	mgr := deacon.NewManager(d.config.TownRoot)

	if err := mgr.Start(""); err != nil {
		if err == deacon.ErrAlreadyRunning {
			// Deacon is running - record success to reset backoff
			if d.restartTracker != nil {
				d.restartTracker.RecordSuccess(agentID)
			}
			return
		}
		d.logger.Printf("Error starting Deacon: %v", err)
		return
	}

	// Record this restart attempt for backoff tracking
	if d.restartTracker != nil {
		d.restartTracker.RecordRestart(agentID)
		if err := d.restartTracker.Save(); err != nil {
			d.logger.Printf("Warning: failed to save restart state: %v", err)
		}
	}

	// Track when we started the Deacon to prevent race condition in checkDeaconHeartbeat.
	// The heartbeat file will still be stale until the Deacon runs a full patrol cycle.
	d.deaconLastStarted = time.Now()
	d.metrics.recordRestart(d.ctx, "deacon")
	telemetry.RecordDaemonRestart(d.ctx, "deacon")
	d.logger.Println("Deacon started successfully")
}

// deaconGracePeriod returns the config-driven deacon grace period.
// The Deacon needs time to initialize Claude, run SessionStart hooks, execute gt prime,
// run a patrol cycle, and write a fresh heartbeat. Default: 5 minutes.
func (d *Daemon) deaconGracePeriod() time.Duration {
	return d.loadOperationalConfig().GetDaemonConfig().DeaconGracePeriodD()
}

// checkDeaconHeartbeat checks if the Deacon is making progress.
// This is a belt-and-suspenders fallback in case Boot doesn't detect stuck states.
// Uses the heartbeat file that the Deacon updates on each patrol cycle.
//
// PATCH-005: Fixed grace period logic. Old logic skipped heartbeat check entirely
// during grace period, allowing stuck Deacons to go undetected. New logic:
// - Always read heartbeat first
// - Grace period only applies if heartbeat is from BEFORE we started Deacon
// - If heartbeat is from AFTER start but stale, Deacon is stuck
func (d *Daemon) checkDeaconHeartbeat() {
	// Respect crash-loop guard: if the restart tracker says Deacon is in a
	// crash loop, do not kill the session — the guard is deliberately holding
	// off restarts to break the cycle. (Fixes #2086)
	if d.restartTracker != nil && d.restartTracker.IsInCrashLoop("deacon") {
		d.logger.Printf("Deacon is in crash-loop state, skipping heartbeat kill check")
		return
	}

	// Always read heartbeat first (PATCH-005)
	hb := deacon.ReadHeartbeat(d.config.TownRoot)

	sessionName := d.getDeaconSessionName()

	// Check if we recently started a Deacon
	if !d.deaconLastStarted.IsZero() {
		timeSinceStart := time.Since(d.deaconLastStarted)

		if hb == nil {
			// No heartbeat file exists
			if timeSinceStart < d.deaconGracePeriod() {
				d.logger.Printf("Deacon started %s ago, awaiting first heartbeat...",
					timeSinceStart.Round(time.Second))
				return
			}
			// Grace period expired without any heartbeat - Deacon failed to start
			// Stuck-agent-dog: kill and restart
			d.logger.Printf("STUCK DEACON: started %s ago but hasn't written heartbeat (session: %s)",
				timeSinceStart.Round(time.Minute), sessionName)
			d.restartStuckDeacon(sessionName, fmt.Sprintf("no heartbeat after %s", timeSinceStart.Round(time.Minute)))
			return
		}

		// Heartbeat exists - check if it's from BEFORE we started this Deacon
		if hb.Timestamp.Before(d.deaconLastStarted) {
			// Heartbeat is stale (from before restart)
			if timeSinceStart < d.deaconGracePeriod() {
				d.logger.Printf("Deacon started %s ago, heartbeat is pre-restart, awaiting fresh heartbeat...",
					timeSinceStart.Round(time.Second))
				return
			}
			// Grace period expired but heartbeat still from before start
			// Stuck-agent-dog: kill and restart
			d.logger.Printf("STUCK DEACON: started %s ago but heartbeat still pre-restart (session: %s)",
				timeSinceStart.Round(time.Minute), sessionName)
			d.restartStuckDeacon(sessionName, fmt.Sprintf("heartbeat pre-restart after %s", timeSinceStart.Round(time.Minute)))
			return
		}

		// Heartbeat is from AFTER we started - Deacon has written at least one heartbeat
		// Fall through to normal staleness check
	}

	// No recent start tracking or Deacon has written fresh heartbeat - check normally
	if hb == nil {
		// No heartbeat file - Deacon hasn't started a cycle yet
		return
	}

	age := hb.Age()

	// If heartbeat is fresh (< 5 min), nothing to do
	if hb.IsFresh() {
		return
	}

	d.logger.Printf("Deacon heartbeat is stale (%s old), checking session...", age.Round(time.Minute))

	// Check if session exists
	hasSession, err := d.tmux.HasSession(sessionName)
	if err != nil {
		d.logger.Printf("Error checking Deacon session: %v", err)
		return
	}

	if !hasSession {
		// Session doesn't exist - ensureDeaconRunning already ran earlier
		// in heartbeat, so Deacon should be starting
		return
	}

	// Session exists but heartbeat is stale - Deacon may be stuck.
	// Two-tier response: nudge for stale (5-20 min), kill and restart
	// only for very stale (>= 20 min). Kill threshold must be > backoff-max
	// to avoid false positive kills during legitimate await-signal sleep.
	if hb.IsVeryStale() {
		// Stuck-agent-dog: kill and restart
		d.logger.Printf("STUCK DEACON: heartbeat stale for %s, session %s needs restart", age.Round(time.Minute), sessionName)
		d.restartStuckDeacon(sessionName, fmt.Sprintf("heartbeat stale for %s", age.Round(time.Minute)))
	} else {
		// Stale but not very stale (5-20 min) - nudge to wake up (unless idle).
		//
		// Idle guard: skip nudge if no beads are actively in flight.
		// This mirrors the Boot idle guard (ensureBootRunning). When the Deacon's
		// heartbeat has gone stale during an await-signal backoff sleep, sending a
		// nudge interrupts the exponential backoff for no reason — the Deacon will
		// wake naturally at its next timeout. Only nudge if work is actually in
		// flight (in_progress or hooked) that the Deacon may need to act on.
		// Conservative: on store errors hasActiveWork returns true, so nudge fires.
		// See also: runtime/runtime.go:99-101 — session-started nudge was removed
		// for the same reason (it interrupted the deacon's await-signal backoff).
		if !d.hasActiveWork() {
			d.logger.Println("Deacon nudge skipped: no active work in flight, await-signal will fire naturally")
			return
		}

		d.logger.Printf("Deacon stuck for %s - nudging session", age.Round(time.Minute))
		if err := d.tmux.NudgeSession(sessionName, "HEALTH_CHECK: heartbeat stale, respond to confirm responsiveness"); err != nil {
			d.logger.Printf("Error nudging stuck Deacon: %v", err)
		}
	}
}

// restartStuckDeacon kills a stuck Deacon session and respawns it.
// Uses RestartTracker for exponential backoff and crash-loop prevention.
// Notifies via gt-notify (zero token cost) if the notify script exists.
func (d *Daemon) restartStuckDeacon(sessionName, reason string) {
	const agentID = "deacon"

	// Check restart tracker before acting
	if d.restartTracker != nil {
		if d.restartTracker.IsInCrashLoop(agentID) {
			d.logger.Printf("Stuck-agent-dog: Deacon in crash loop, not restarting (use 'gt daemon clear-backoff deacon')")
			d.notifySlack("admin", "critical", fmt.Sprintf("Deacon crash loop detected — manual intervention required. Reason: %s", reason))
			return
		}
		if !d.restartTracker.CanRestart(agentID) {
			remaining := d.restartTracker.GetBackoffRemaining(agentID)
			d.logger.Printf("Stuck-agent-dog: Deacon restart in backoff, %s remaining", remaining.Round(time.Second))
			return
		}
	}

	// Kill the stuck session
	d.logger.Printf("Stuck-agent-dog: killing stuck Deacon session %s (reason: %s)", sessionName, reason)
	if err := d.tmux.KillSession(sessionName); err != nil {
		d.logger.Printf("Stuck-agent-dog: error killing session %s: %v", sessionName, err)
		// Continue — session may already be dead
	}

	// Brief pause for tmux cleanup
	time.Sleep(2 * time.Second)

	// Respawn via ensureDeaconRunning (which uses deacon.Manager)
	d.ensureDeaconRunning()

	// Verify it came back
	hasSession, err := d.tmux.HasSession(sessionName)
	if err != nil || !hasSession {
		d.logger.Printf("Stuck-agent-dog: FAILED to respawn Deacon after kill")
		d.notifySlack("admin", "critical", fmt.Sprintf("Deacon restart FAILED — session did not respawn. Reason: %s", reason))
		return
	}

	d.logger.Printf("Stuck-agent-dog: Deacon restarted successfully")
	d.notifySlack("admin", "high", fmt.Sprintf("Deacon was stuck (%s) — auto-restarted successfully", reason))
}

// notifySlack sends a notification via gt-notify (zero token cost).
// Channel: "admin" or "status". Priority: "critical", "high", "info", "success".
// Silently fails if gt-notify is not found — notification is best-effort.
func (d *Daemon) notifySlack(channel, priority, message string) {
	notifyBin := filepath.Join(d.config.TownRoot, "bin", "gt-notify")
	if _, err := os.Stat(notifyBin); err != nil {
		d.logger.Printf("Stuck-agent-dog: gt-notify not found at %s, skipping notification", notifyBin)
		return
	}

	//nolint:gosec // G204: args are constructed internally
	cmd := exec.Command(notifyBin, "--channel", channel, "--priority", priority, message)
	cmd.Env = append(os.Environ(), fmt.Sprintf("PATH=%s:%s", filepath.Join(d.config.TownRoot, "bin"), os.Getenv("PATH")))
	if output, err := cmd.CombinedOutput(); err != nil {
		d.logger.Printf("Stuck-agent-dog: gt-notify failed: %v (output: %s)", err, string(output))
	}
}

// ensureWitnessesRunning ensures witnesses are running for configured rigs.
// Called on each heartbeat to maintain witness patrol loops.
// Respects the rigs filter in daemon.json patrol config.
func (d *Daemon) ensureWitnessesRunning() {
	rigs := d.getPatrolRigs("witness")
	d.rigPool.runPerRig(d.ctx, rigs, func(ctx context.Context, rigName string) error {
		d.ensureWitnessRunning(rigName)
		return nil
	})
}

// hasPendingEvents checks if there are pending .event files in the given channel directory.
// Used to gate agent spawning: don't burn API credits starting a Claude session when
// there's nothing to process. The agent's await-event handles the actual consumption.
func (d *Daemon) hasPendingEvents(channel string) bool {
	eventDir := filepath.Join(d.config.TownRoot, "events", channel)
	entries, err := os.ReadDir(eventDir)
	if err != nil {
		return false // Directory doesn't exist or unreadable = no pending events
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".event") {
			return true
		}
	}
	return false
}

// ensureWitnessRunning ensures the witness for a specific rig is running.
// Discover, don't track: uses Manager.Start() which checks tmux directly (gt-zecmc).
func (d *Daemon) ensureWitnessRunning(rigName string) {
	// Check rig operational state before auto-starting
	if operational, reason := d.isRigOperational(rigName); !operational {
		d.logger.Printf("Skipping witness auto-start for %s: %s", rigName, reason)
		// Kill leftover witness session if rig is not operational (docked/parked).
		// Without this, sessions started before the rig was docked survive until
		// the next explicit 'gt rig dock' command. (hq-snx61)
		name := session.WitnessSessionName(session.PrefixFor(rigName))
		if exists, _ := d.tmux.HasSession(name); exists {
			d.logger.Printf("Killing leftover witness %s (rig %s)", name, reason)
			if err := d.tmux.KillSessionWithProcesses(name); err != nil {
				d.logger.Printf("Error killing leftover witness %s: %v", name, err)
			}
		}
		return
	}

	// Manager.Start() handles: zombie detection, session creation, env vars, theming,
	// startup readiness waits, and crucially - startup/propulsion nudges (GUPP).
	// It returns ErrAlreadyRunning if Claude is already running in tmux.
	r := &rig.Rig{
		Name: rigName,
		Path: filepath.Join(d.config.TownRoot, rigName),
	}
	mgr := witness.NewManager(r)

	// NOTE: Hung session detection removed for witnesses (serial killer bug).
	// Idle witnesses legitimately produce no tmux output while waiting for work.
	// The deacon's patrol health-scan step handles stuck detection with proper
	// context (checks for active work before declaring something stuck).
	// See: daemon.log "is hung (no activity for 30m0s), killing for restart"

	if err := mgr.Start(false, "", nil); err != nil {
		if err == witness.ErrAlreadyRunning {
			// Already running - this is the expected case
			d.logger.Printf("Witness for %s already running, skipping spawn", rigName)
			return
		}
		d.logger.Printf("Error starting witness for %s: %v", rigName, err)
		return
	}

	d.metrics.recordRestart(d.ctx, "witness")
	telemetry.RecordDaemonRestart(d.ctx, "witness-"+rigName)
	d.logger.Printf("Witness session for %s started successfully", rigName)
}

// ensureRefineriesRunning ensures refineries are running for configured rigs.
// Called on each heartbeat to maintain refinery merge queue processing.
// Respects the rigs filter in daemon.json patrol config.
func (d *Daemon) ensureRefineriesRunning() {
	rigs := d.getPatrolRigs("refinery")
	d.rigPool.runPerRig(d.ctx, rigs, func(ctx context.Context, rigName string) error {
		d.ensureRefineryRunning(rigName)
		return nil
	})
}

// ensureRefineryRunning ensures the refinery for a specific rig is running.
// Discover, don't track: uses Manager.Start() which checks tmux directly (gt-zecmc).
func (d *Daemon) ensureRefineryRunning(rigName string) {
	// Check rig operational state before auto-starting
	if operational, reason := d.isRigOperational(rigName); !operational {
		d.logger.Printf("Skipping refinery auto-start for %s: %s", rigName, reason)
		// Kill leftover refinery session if rig is not operational (docked/parked).
		// Without this, sessions started before the rig was docked survive until
		// the next explicit 'gt rig dock' command. (hq-snx61)
		name := session.RefinerySessionName(session.PrefixFor(rigName))
		if exists, _ := d.tmux.HasSession(name); exists {
			d.logger.Printf("Killing leftover refinery %s (rig %s)", name, reason)
			if err := d.tmux.KillSessionWithProcesses(name); err != nil {
				d.logger.Printf("Error killing leftover refinery %s: %v", name, err)
			}
		}
		return
	}

	// Event gate: don't spawn a new Claude session when there's nothing to process.
	// If a refinery session is already running, Start() returns ErrAlreadyRunning (cheap).
	// But spawning a NEW session with an empty queue burns API credits for nothing.
	// The refinery formula uses await-event internally, so it will wake when events appear.
	if !d.hasPendingEvents("refinery") {
		// Check if session already exists before skipping — let running sessions continue
		r := &rig.Rig{
			Name: rigName,
			Path: filepath.Join(d.config.TownRoot, rigName),
		}
		mgr := refinery.NewManager(r)
		if running, _ := mgr.IsRunning(); !running {
			d.logger.Printf("No pending refinery events and no session running for %s, skipping spawn", rigName)
			return
		}
	}

	// Manager.Start() handles: zombie detection, session creation, env vars, theming,
	// WaitForClaudeReady, and crucially - startup/propulsion nudges (GUPP).
	// It returns ErrAlreadyRunning if Claude is already running in tmux.
	r := &rig.Rig{
		Name: rigName,
		Path: filepath.Join(d.config.TownRoot, rigName),
	}
	mgr := refinery.NewManager(r)

	// NOTE: Hung session detection removed for refineries (serial killer bug).
	// Idle refineries legitimately produce no tmux output while waiting for MRs.
	// The deacon's patrol health-scan step handles stuck detection with proper
	// context (checks for active work before declaring something stuck).
	// See: daemon.log "is hung (no activity for 30m0s), killing for restart"

	if err := mgr.Start(false, ""); err != nil {
		if err == refinery.ErrAlreadyRunning {
			// Already running - this is the expected case when fix is working
			d.logger.Printf("Refinery for %s already running, skipping spawn", rigName)
			return
		}
		d.logger.Printf("Error starting refinery for %s: %v", rigName, err)
		return
	}

	d.metrics.recordRestart(d.ctx, "refinery")
	telemetry.RecordDaemonRestart(d.ctx, "refinery-"+rigName)
	d.logger.Printf("Refinery session for %s started successfully", rigName)
}

// ensureMayorRunning ensures the Mayor is running.
// Uses mayor.Manager for consistent startup behavior.
// If the tmux session exists but the agent is dead (zombie), the daemon
// stops the zombie session and starts a fresh one.
func (d *Daemon) ensureMayorRunning() {
	mgr := mayor.NewManager(d.config.TownRoot)

	if err := mgr.Start(""); err != nil {
		if err == mayor.ErrAlreadyRunning {
			// Session exists — verify agent is actually alive.
			// During handoffs the agent is briefly undetectable, so we
			// only restart if the session has been a zombie for multiple
			// consecutive patrol cycles (debounce).
			if !d.isMayorAgentAlive(mgr) {
				d.mayorZombieCount++
				if d.mayorZombieCount >= 3 {
					d.logger.Printf("Mayor zombie detected (%d cycles), restarting", d.mayorZombieCount)
					if stopErr := mgr.Stop(); stopErr != nil && stopErr != mayor.ErrNotRunning {
						d.logger.Printf("Error stopping zombie Mayor: %v", stopErr)
						return
					}
					d.mayorZombieCount = 0
					if startErr := mgr.Start(""); startErr != nil {
						d.logger.Printf("Error restarting Mayor after zombie cleanup: %v", startErr)
						return
					}
					d.logger.Println("Mayor restarted after zombie cleanup")
				} else {
					d.logger.Printf("Mayor agent not detected (cycle %d/3), waiting before restart", d.mayorZombieCount)
				}
			} else {
				d.mayorZombieCount = 0
			}
			return
		}
		d.logger.Printf("Error starting Mayor: %v", err)
		return
	}

	d.mayorZombieCount = 0
	d.logger.Println("Mayor started successfully")
}

// isMayorAgentAlive checks if the Mayor's agent process is running in tmux.
func (d *Daemon) isMayorAgentAlive(mgr *mayor.Manager) bool {
	t := tmux.NewTmux()
	return t.IsAgentAlive(mgr.SessionName())
}

// killDeaconSessions kills leftover deacon and boot tmux sessions.
// Called when the deacon patrol is disabled to prevent stale deacons from
// running their own patrol loops and spawning agents. (hq-2mstj)
func (d *Daemon) killDeaconSessions() {
	for _, name := range []string{session.DeaconSessionName(), session.BootSessionName()} {
		exists, _ := d.tmux.HasSession(name)
		if exists {
			d.logger.Printf("Killing leftover %s session (patrol disabled)", name)
			if err := d.tmux.KillSessionWithProcesses(name); err != nil {
				d.logger.Printf("Error killing %s session: %v", name, err)
			}
		}
	}
}

// killWitnessSessions kills leftover witness tmux sessions for all rigs.
// Called when the witness patrol is disabled. (hq-2mstj)
func (d *Daemon) killWitnessSessions() {
	d.rigPool.runPerRig(d.ctx, d.getKnownRigs(), func(ctx context.Context, rigName string) error {
		name := session.WitnessSessionName(session.PrefixFor(rigName))
		exists, _ := d.tmux.HasSession(name)
		if exists {
			d.logger.Printf("Killing leftover %s session (patrol disabled)", name)
			if err := d.tmux.KillSessionWithProcesses(name); err != nil {
				d.logger.Printf("Error killing %s session: %v", name, err)
			}
		}
		return nil
	})
}

// killRefinerySessions kills leftover refinery tmux sessions for all rigs.
// Called when the refinery patrol is disabled. (hq-2mstj)
func (d *Daemon) killRefinerySessions() {
	d.rigPool.runPerRig(d.ctx, d.getKnownRigs(), func(ctx context.Context, rigName string) error {
		name := session.RefinerySessionName(session.PrefixFor(rigName))
		exists, _ := d.tmux.HasSession(name)
		if exists {
			d.logger.Printf("Killing leftover %s session (patrol disabled)", name)
			if err := d.tmux.KillSessionWithProcesses(name); err != nil {
				d.logger.Printf("Error killing %s session: %v", name, err)
			}
		}
		return nil
	})
}

// killDefaultPrefixGhosts kills tmux sessions that use the default "gt" prefix
// for roles that should use a rig-specific prefix. These ghost sessions appear
// when the daemon starts before a rig is registered or when the registry was
// stale. After a registry reload, any "gt-witness", "gt-refinery", or "gt-*"
// sessions that correspond to rigs with their own prefix are stale duplicates.
// Fix for: hq-ouz, hq-eqf, hq-3i4.
func (d *Daemon) killDefaultPrefixGhosts() {
	reg := session.DefaultRegistry()
	allRigs := reg.AllRigs() // rigName → shortPrefix
	if len(allRigs) == 0 {
		return
	}

	// Check if any rig actually has "gt" as its registered prefix.
	// If so, gt-witness is legitimate for that rig — don't kill it.
	gtIsLegitimate := false
	for _, prefix := range allRigs {
		if prefix == session.DefaultPrefix {
			gtIsLegitimate = true
			break
		}
	}
	if gtIsLegitimate {
		return
	}

	// Kill ghost sessions using the default "gt" prefix for patrol roles.
	for _, role := range []string{"witness", "refinery"} {
		ghostName := fmt.Sprintf("%s-%s", session.DefaultPrefix, role)
		exists, _ := d.tmux.HasSession(ghostName)
		if exists {
			d.logger.Printf("Killing ghost session %s (default prefix, stale registry artifact)", ghostName)
			if err := d.tmux.KillSessionWithProcesses(ghostName); err != nil {
				d.logger.Printf("Error killing ghost session %s: %v", ghostName, err)
			}
		}
	}

	// Also check for ghost polecat sessions: gt-<polecatName> where the polecat
	// actually belongs to a rig with a different prefix.
	for _, rigName := range d.getKnownRigs() {
		rigPrefix := session.PrefixFor(rigName)
		if rigPrefix == session.DefaultPrefix {
			continue // This rig uses "gt" — its sessions are fine
		}
		rigPath := filepath.Join(d.config.TownRoot, rigName, "polecats")
		entries, err := os.ReadDir(rigPath)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			polecatName := entry.Name()
			ghostName := fmt.Sprintf("%s-%s", session.DefaultPrefix, polecatName)
			exists, _ := d.tmux.HasSession(ghostName)
			if exists {
				// Verify the correct session isn't also running (avoid killing legit sessions)
				correctName := session.PolecatSessionName(rigPrefix, polecatName)
				correctExists, _ := d.tmux.HasSession(correctName)
				if !correctExists {
					// Ghost is the only session — it might be doing real work.
					// Log but don't kill; the registry reload will prevent new ghosts.
					d.logger.Printf("Ghost polecat session %s found (should be %s), not killing (may have active work)", ghostName, correctName)
				} else {
					// Both exist — ghost is definitely a duplicate, kill it.
					d.logger.Printf("Killing duplicate ghost polecat session %s (correct session %s exists)", ghostName, correctName)
					if err := d.tmux.KillSessionWithProcesses(ghostName); err != nil {
						d.logger.Printf("Error killing ghost session %s: %v", ghostName, err)
					}
				}
			}
		}
	}
}
