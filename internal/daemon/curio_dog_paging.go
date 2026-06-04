package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/curio"
	"github.com/steveyegge/gastown/internal/util"
)

// Call 2 (gu-2coqj) SHADOW-MODE paging emitter + dead-man heartbeat.
//
// The PagingEngine decides; this file is the only place that touches the
// outside world for paging. The operator-mandated SAFETY GATE lives here:
//
//   - ALWAYS: record every action to the curio_shadow_page ledger and the
//     daemon log, and refresh the pull-based dead-man heartbeat.
//   - ONLY when CurioConfig.PageForReal is true: additionally raise a durable,
//     ACK-required Overseer-CRITICAL escalation. This is OFF by default and is
//     a deliberate second enablement after precision/cadence are proven on the
//     live shadow ledger.
//
// Design note (per bead scope): the real Overseer page is NOT gated by
// estop/thaw — a confirmed human-reach condition must reach a human even when
// the town is paused. The SHADOW gate (PageForReal) is the only thing standing
// between this code and an actual page, which is exactly why it defaults off.

// curioHeartbeatFile is the pull-based dead-man heartbeat path under
// <town>/.runtime. A watcher (or operator) reads it to confirm the Curio paging
// path is alive: a stale/absent heartbeat means Curio stopped evaluating and a
// real fault could now go unpaged. Refreshed every curio cycle.
const curioHeartbeatFile = "curio-paging-heartbeat.json"

// curioHeartbeat is the heartbeat payload written each cycle.
type curioHeartbeat struct {
	// LastCycleAt is when the paging engine last ran (RFC3339 UTC).
	LastCycleAt string `json:"last_cycle_at"`
	// WindowID labels the cycle.
	WindowID string `json:"window_id"`
	// Actions is how many page actions the engine emitted this cycle.
	Actions int `json:"actions"`
	// BreakerState is the judgment breaker's mode (armed/tripped/closed).
	BreakerState string `json:"breaker_state"`
	// ShadowMode is true while PageForReal is off (no real paging).
	ShadowMode bool `json:"shadow_mode"`
}

// emitCurioPages records the engine's decisions per the SHADOW-mode safety gate
// and refreshes the dead-man heartbeat. Best-effort throughout: a ledger or
// heartbeat I/O error is logged and never aborts the patrol cycle (a missed
// shadow row only delays precision measurement; the engine state is unaffected).
func (d *Daemon) emitCurioPages(windowID string, actions []PageAction) {
	pageForReal := d.curioPageForReal()

	// Always refresh the dead-man heartbeat — even on a zero-action cycle, so an
	// absent/stale heartbeat unambiguously means "Curio stopped evaluating."
	d.writeCurioHeartbeat(windowID, len(actions), pageForReal)

	if len(actions) == 0 {
		return
	}

	// Log every decision (shadow audit trail in daemon.log).
	for _, a := range actions {
		d.logger.Printf("curio: WOULD-PAGE [%s] lane=%s severity=%s occurrences=%d clusters=%d: %s",
			a.Kind, a.Lane, a.Severity, a.Occurrences, a.Clusters, a.Summary)
	}

	// Always write the shadow ledger (the live precision/cadence record).
	d.recordCurioShadowPages(windowID, actions, pageForReal)

	if !pageForReal {
		d.logger.Printf("curio: SHADOW MODE — %d action(s) logged + ledgered, NO Overseer page sent", len(actions))
		return
	}

	// Live paging (deliberate second enablement). Raise one Overseer-CRITICAL
	// escalation per action, deduped by the action's stable key.
	for _, a := range actions {
		d.pageOverseer(a)
	}
}

// PageAction is the daemon-side alias for curio.PageAction so daemon files read
// naturally without curio. qualifiers everywhere.
type PageAction = curio.PageAction

// curioPageForReal reports whether the live-paging gate is open. Defaults to
// false (shadow mode) whenever config is absent.
func (d *Daemon) curioPageForReal() bool {
	if d.patrolConfig != nil && d.patrolConfig.Patrols != nil && d.patrolConfig.Patrols.Curio != nil {
		return d.patrolConfig.Patrols.Curio.PageForReal
	}
	return false
}

// writeCurioHeartbeat refreshes the pull-based dead-man heartbeat. Best-effort.
func (d *Daemon) writeCurioHeartbeat(windowID string, actions int, pageForReal bool) {
	hb := curioHeartbeat{
		LastCycleAt:  time.Now().UTC().Format(time.RFC3339),
		WindowID:     windowID,
		Actions:      actions,
		ShadowMode:   !pageForReal,
		BreakerState: "armed",
	}
	if d.curioPaging != nil {
		hb.BreakerState = d.curioPaging.State().String()
	}

	data, err := json.Marshal(hb)
	if err != nil {
		d.logger.Printf("curio: heartbeat marshal failed: %v", err)
		return
	}
	path := filepath.Join(d.config.TownRoot, ".runtime", curioHeartbeatFile)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		d.logger.Printf("curio: heartbeat mkdir failed: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil { //nolint:gosec // G306: operational heartbeat, non-sensitive
		d.logger.Printf("curio: heartbeat write failed: %v", err)
	}
}

// recordCurioShadowPages writes the would-page actions to the HQ shadow ledger.
// Best-effort: gated on the shared Dolt breaker and never aborts the cycle.
func (d *Daemon) recordCurioShadowPages(windowID string, actions []PageAction, pageForReal bool) {
	if !d.doltBreaker.Allow() {
		d.logger.Printf("curio: dolt-degraded — skipping shadow-ledger write")
		return
	}
	store, err := curio.OpenStore("127.0.0.1", d.doltServerPort(), "hq")
	if err != nil {
		d.doltBreaker.Record(err)
		d.logger.Printf("curio: shadow-ledger open failed: %v", err)
		return
	}
	defer func() { _ = store.Close() }()

	n, err := store.RecordShadowPages(windowID, actions, pageForReal)
	d.doltBreaker.Record(err)
	if err != nil {
		d.logger.Printf("curio: shadow-ledger write failed: %v", err)
		return
	}
	d.logger.Printf("curio: shadow-ledger recorded %d action(s)", n)
}

// pageOverseer raises a durable, ACK-required Overseer escalation for one page
// action. Only reached when PageForReal is enabled. The escalation carries the
// action's stable dedup key so `gt escalate`'s close-aware dedup bumps the
// existing bead instead of creating duplicates on a re-fire. Best-effort:
// failure is logged (the engine has already latched, so a missed page surfaces
// as a stale heartbeat / shadow-ledger row for the operator).
//
// Overridable in tests via the package-level hook.
var pageOverseer = func(d *Daemon, a PageAction) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	reason := a.Summary
	cmd := exec.CommandContext(ctx, d.gtPath, "escalate", //nolint:gosec // G204: args constructed internally
		"--severity", a.Severity,
		"--source", "daemon:curio",
		"--dedup",
		"--signature", a.DedupKey,
		"--fingerprint", a.DedupKey,
		"--reason", reason,
		"curio: "+a.Summary)
	util.SetDetachedProcessGroup(cmd)
	if err := cmd.Run(); err != nil {
		d.logger.Printf("curio: Overseer page failed (key=%s): %v", a.DedupKey, err)
		return
	}
	d.logger.Printf("curio: PAGED Overseer [%s] severity=%s key=%s", a.Kind, a.Severity, a.DedupKey)
}

// pageOverseer is a thin method wrapper so the hook can be swapped in tests
// while callers use a stable method name.
func (d *Daemon) pageOverseer(a PageAction) { pageOverseer(d, a) }
