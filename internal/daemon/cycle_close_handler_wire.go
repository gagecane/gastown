// Wiring for the Mayor cycle-close handler (Phase 0 task 3c, gu-xrxm6).
//
// This file bridges the daemon package's MRCycleCloseEvent/Handler types
// with the autotestpr.CycleCloseHandler. The daemon dog dispatches events
// using daemon.MRCycleCloseEvent; the handler lives in internal/autotestpr
// and uses its own (identical) type to avoid import cycles.
//
// The wiring is called once during daemon startup (in Run(), after beads
// stores are opened) to install the real handler. Before this call, events
// dispatch to the noopMRCycleCloseHandler which logs and drops.
package daemon

import (
	"context"
	"os"
	"os/exec"
	"time"

	"github.com/steveyegge/gastown/internal/autotestpr"
	"github.com/steveyegge/gastown/internal/beads"
)

// initMRCycleCloseHandler creates and registers the Mayor cycle-close
// handler. Called from Run() after beads stores are initialized.
//
// The handler requires:
//   - A beads.Beads wrapper pointed at the town root (for town-state
//     bead mutations and bug-bead creation).
//   - The daemon's logger for structured output.
//   - A nudge function that shells out to `gt nudge overseer` for
//     circuit-breaker trip notifications.
//
// If the mr_cycle_close patrol is not active, this is a no-op (the
// handler stays nil and events go to noopMRCycleCloseHandler).
func (d *Daemon) initMRCycleCloseHandler() {
	if !d.isPatrolActive("mr_cycle_close") {
		return
	}

	// Use the in-process beadsdk.Storage (hq store) when available.
	// This satisfies the acceptance criterion: "Attachment-bead writes go
	// through in-process beadsdk.Storage (no bd subprocess fan-out)."
	var b *beads.Beads
	if hqStore := d.beadsStores["hq"]; hqStore != nil {
		b = beads.NewWithStore(d.config.TownRoot, hqStore)
	} else {
		b = beads.New(d.config.TownRoot)
		d.logger.Printf("cycle-close-handler: hq store not available, falling back to bd subprocess")
	}

	handler := &autotestpr.CycleCloseHandler{
		Beads: b,
		NudgeOverseer: func(msg string) {
			d.nudgeOverseer(msg)
		},
		Now:  time.Now,
		Logf: d.logger.Printf,
	}

	// Register the bridge function that converts daemon.MRCycleCloseEvent
	// to autotestpr.MRCycleCloseEvent and calls the handler.
	d.SetMRCycleCloseHandler(func(ev MRCycleCloseEvent) {
		handler.HandleEvent(autotestpr.MRCycleCloseEvent{
			MRID:        ev.MRID,
			TargetRig:   ev.TargetRig,
			CloseReason: ev.CloseReason,
			Body:        ev.Body,
		})
	})

	d.logger.Printf("Mayor cycle-close handler registered (Phase 0 task 3c)")
}

// nudgeOverseer shells out to `gt nudge overseer` with the given message.
// Best-effort: errors are logged but do not propagate. The nudge is
// ephemeral (zero Dolt overhead) — it only needs to wake the overseer
// to evaluate the circuit-breaker state.
func (d *Daemon) nudgeOverseer(msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.gtPath, "nudge", "overseer", msg) //nolint:gosec // G204: args from internal
	cmd.Dir = d.config.TownRoot
	cmd.Env = os.Environ()
	setSysProcAttr(cmd)

	if err := cmd.Run(); err != nil {
		d.logger.Printf("cycle-close-handler: failed to nudge overseer: %v", err)
	}
}
