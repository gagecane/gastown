package daemon

// Heartbeat cycle: the recovery-focused tick that the daemon runs on a
// fixed interval. Heartbeats are the safety net for dead sessions,
// GUPP violations, and orphaned work. Normal wake is handled by the
// feed curator (bd activity --follow) and dedicated tickers.

import (
	"time"

	"github.com/steveyegge/gastown/internal/estop"
	"github.com/steveyegge/gastown/internal/session"
)

// recoveryHeartbeatInterval returns the config-driven recovery heartbeat interval.
// Normal wake is handled by feed subscription (bd activity --follow).
// The daemon is a safety net for dead sessions, GUPP violations, and orphaned work.
// Default: 3 minutes — fast enough to detect stuck agents promptly.
func (d *Daemon) recoveryHeartbeatInterval() time.Duration {
	return d.loadOperationalConfig().GetDaemonConfig().RecoveryHeartbeatIntervalD()
}

// heartbeat performs one heartbeat cycle.
// The daemon is recovery-focused: it ensures agents are running and detects failures.
// Normal wake is handled by feed subscription (bd activity --follow).
// The daemon is the safety net for edge cases:
// - Dead sessions that need restart
// - Agents with work-on-hook not progressing (GUPP violation)
// - Orphaned work (assigned to dead agents)
func (d *Daemon) heartbeat(state *State) {
	// Skip heartbeat if shutdown is in progress.
	// This prevents the daemon from fighting shutdown by auto-restarting killed agents.
	// The shutdown.lock file is created by gt down before terminating sessions.
	if d.isShutdownInProgress() {
		d.logger.Println("Shutdown in progress, skipping heartbeat")
		return
	}

	// Skip agent management if E-stop is active.
	// The daemon stays alive (to maintain Dolt, etc.) but does NOT
	// restart any agents. This prevents fighting the E-stop by auto-spawning
	// sessions that were intentionally frozen.
	if estop.IsActive(d.config.TownRoot) {
		d.logger.Println("E-STOP active, skipping agent management")
		return
	}

	d.metrics.recordHeartbeat(d.ctx)
	d.logger.Println("Heartbeat starting (recovery-focused)")

	// Invalidate the per-tick rigs cache so this heartbeat re-reads from disk.
	// Within a tick the cache coalesces the ~10 getKnownRigs() call sites into
	// a single read; invalidating here ensures we pick up rigs.json changes
	// between ticks.
	d.invalidateKnownRigsCache()

	// 0a. Reload prefix registry so new/changed rigs get correct session names.
	// Without this, rigs added after daemon startup get the "gt" default prefix,
	// causing ghost sessions like gt-witness instead of ti-witness. (hq-ouz, hq-eqf, hq-3i4)
	if err := session.InitRegistry(d.config.TownRoot); err != nil {
		d.logger.Printf("Warning: failed to reload prefix registry: %v", err)
	}

	// 0b. Kill ghost sessions left over from stale registry (default "gt" prefix).
	d.killDefaultPrefixGhosts()

	// 0. Ensure Dolt server is running (if configured)
	// This must happen before beads operations that depend on Dolt.
	d.ensureDoltServerRunning()

	// 1. Ensure Deacon is running (restart if dead)
	// Check patrol config - can be disabled in mayor/daemon.json
	if d.isPatrolActive("deacon") {
		d.ensureDeaconRunning()
	} else {
		d.logger.Printf("Deacon patrol disabled in config, skipping")
		// Kill leftover deacon/boot sessions from before patrol was disabled.
		// Without this, a stale deacon keeps running its own patrol loop,
		// spawning witnesses and refineries despite daemon config. (hq-2mstj)
		d.killDeaconSessions()
	}

	// 2. Poke Boot for intelligent triage (stuck/nudge/interrupt)
	// Boot handles nuanced "is Deacon responsive" decisions
	// Only run if Deacon patrol is enabled
	if d.isPatrolActive("deacon") {
		d.ensureBootRunning()
	}

	// 3. Direct Deacon heartbeat check (belt-and-suspenders)
	// Boot may not detect all stuck states; this provides a fallback
	// Only run if Deacon patrol is enabled
	if d.isPatrolActive("deacon") {
		d.checkDeaconHeartbeat()
	}

	// 4. Ensure Witnesses are running for all rigs (restart if dead)
	// Check patrol config - can be disabled in mayor/daemon.json
	if d.isPatrolActive("witness") {
		d.ensureWitnessesRunning()
	} else {
		d.logger.Printf("Witness patrol disabled in config, skipping")
		// Kill leftover witness sessions from before patrol was disabled. (hq-2mstj)
		d.killWitnessSessions()
	}

	// 5. Ensure Refineries are running for all rigs (restart if dead)
	// Check patrol config - can be disabled in mayor/daemon.json
	// Pressure-gated: refineries consume API credits, defer when system is loaded.
	if d.isPatrolActive("refinery") {
		if p := d.checkPressure("refinery"); !p.OK {
			d.logger.Printf("Deferring refinery spawn: %s", p.Reason)
		} else {
			d.ensureRefineriesRunning()
		}
	} else {
		d.logger.Printf("Refinery patrol disabled in config, skipping")
		// Kill leftover refinery sessions from before patrol was disabled. (hq-2mstj)
		d.killRefinerySessions()
	}

	// 6. Ensure Mayor is running (restart if dead)
	d.ensureMayorRunning()

	// 6.5. Handle Dog lifecycle: cleanup stuck dogs and dispatch plugins
	// Pressure-gated: dog dispatch spawns new agent sessions.
	if d.isPatrolActive("handler") {
		if p := d.checkPressure("dog"); !p.OK {
			d.logger.Printf("Deferring dog dispatch: %s", p.Reason)
			// Still run cleanup phases (stuck/stale/idle) — only skip dispatch
			d.handleDogsCleanupOnly()
		} else {
			d.handleDogs()
		}
	} else {
		d.logger.Printf("Handler patrol disabled in config, skipping")
	}

	// 7. Process lifecycle requests
	d.processLifecycleRequests()

	// 9. (Removed) Stale agent check - violated "discover, don't track"

	// 10. Check for GUPP violations (agents with work-on-hook not progressing)
	d.checkGUPPViolations()

	// 11. Check for orphaned work (assigned to dead agents)
	d.checkOrphanedWork()

	// 12. Check polecat session health (proactive crash detection)
	// This validates tmux sessions are still alive for polecats with work-on-hook
	d.checkPolecatSessionHealth()

	// 12a. Reap stuck in_progress/hooked wisps belonging to dead polecats.
	// When a polecat hard-crashes (OOM, tmux kill), its Stop hook never fires
	// and any assigned in_progress/hooked beads stay forever, triggering
	// doctor patrol-not-stuck warnings. checkPolecatSessionHealth detects the
	// crash but only notifies the witness — it does not reset the beads.
	// This reaper bridges that gap with a conservative timeout. See gu-1x0j.
	d.reapDeadPolecatWisps()

	// 12b. Reap idle polecat sessions to prevent API slot burn.
	// Polecats transition to IDLE after gt done but sessions stay alive.
	// Kill sessions that have been idle longer than the configured threshold.
	d.reapIdlePolecats()

	// 13. Clean up orphaned claude subagent processes (memory leak prevention)
	// These are Task tool subagents that didn't clean up after completion.
	// This is a safety net - Deacon patrol also does this more frequently.
	d.cleanupOrphanedProcesses()

	// 13. Prune stale local polecat tracking branches across all rig clones.
	// When polecats push branches to origin, other clones create local tracking
	// branches via git fetch. After merge, remote branches are deleted but local
	// branches persist indefinitely. This cleans them up periodically.
	d.pruneStaleBranches()

	// 14. Dispatch scheduled work (capacity-controlled polecat dispatch).
	// Shells out to `gt scheduler run` to avoid circular import between daemon and cmd.
	// Pressure-gated: polecats are the primary resource consumers.
	if p := d.checkPressure("polecat"); !p.OK {
		d.logger.Printf("Deferring polecat dispatch: %s", p.Reason)
	} else {
		d.dispatchQueuedWork()
	}

	// 15. Rotate oversized Dolt logs (copytruncate for child process fds).
	// daemon.log uses lumberjack for automatic rotation; this handles Dolt server logs.
	d.rotateOversizedLogs()

	// 16. Scan hooked-mail counts for OTel gauges (gu-hhqk AC#5).
	// Heartbeat cadence (3 min) is appropriate: dead-letter threshold is
	// 30 min, so 3-min resolution is plenty and the queries are cheap.
	d.updateHookedBeadsMetrics()

	// Update state
	state.LastHeartbeat = time.Now()
	state.HeartbeatCount++
	if err := SaveState(d.config.TownRoot, state); err != nil {
		d.logger.Printf("Warning: failed to save state: %v", err)
	}

	d.logger.Printf("Heartbeat complete (#%d)", state.HeartbeatCount)
}

// rotateOversizedLogs checks Dolt server log files and rotates any that exceed
// the size threshold. Uses copytruncate which is safe for logs held open by
// child processes. Runs every heartbeat but is cheap (just stat calls).
func (d *Daemon) rotateOversizedLogs() {
	result := RotateLogs(d.config.TownRoot)
	for _, path := range result.Rotated {
		d.logger.Printf("log_rotation: rotated %s", path)
	}
	for _, err := range result.Errors {
		d.logger.Printf("log_rotation: error: %v", err)
	}
}
