package daemon

import (
	"context"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/telemetry"
)

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
