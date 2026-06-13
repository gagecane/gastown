// curio_dog_reconcile.go is the B0b (gu-wg7i5) CONSUMER half of curio_ledger
// population: the post-close reconciler + outcome classifier.
//
// The daemon's existing bead-close event stream — ConvoyManager.pollStore in
// convoy_manager.go, the same close-event path the convoy/refinery post-merge
// flow already rides — is the ONE hook this extends. There is no separate
// close-event infra: every genuine close (after that path's per-cycle +
// cross-cycle dedup) fires an onBeadClose callback, and this reconciler is that
// callback. On each close it asks: is this bead in curio_ledger? If so, classify
// the close reason into an outcome and stamp (outcome, resolved_at). A close for
// a bead NOT in the ledger is a no-op.
//
// The orchestration (reconcileCurioLedgerClose) is a free function over two
// small interfaces so it is unit-testable with fakes — no live Dolt, no live
// convoy poller. The daemon wires the concrete store + bead reader at the
// callback site.
package daemon

import (
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/curio"
)

// ledgerReconciler is the consumer-side write surface the reconciler needs:
// membership check + outcome write. *curio.Store satisfies it; tests fake it.
type ledgerReconciler interface {
	// LedgerHasBead reports whether a ledger row exists for beadID (the
	// not-in-ledger no-op guard).
	LedgerHasBead(beadID string) (bool, error)
	// SetLedgerOutcome stamps (outcome, resolved_at) on the bead's row and
	// reports whether a row was updated.
	SetLedgerOutcome(beadID, outcome string) (updated bool, err error)
}

// *curio.Store is the production ledgerReconciler; assert it at compile time so
// a store-method signature drift breaks the build here, not at runtime.
var _ ledgerReconciler = (*curio.Store)(nil)

// closeSignalReader resolves a closed bead's classification signals (close
// reason + labels). The daemon's beads wrapper is the production impl; tests
// supply a fake so the reconciler runs without a bead store.
type closeSignalReader interface {
	// CloseSignals returns the close reason and labels for beadID.
	CloseSignals(beadID string) (closeReason string, labels []string, err error)
}

// reconcileCurioLedgerClose is the B0b reconciliation orchestration for a single
// bead close. It:
//
//  1. checks ledger membership — a close for a bead NOT in curio_ledger is a
//     no-op (returns reconciled=false, no error), so the common case (most
//     closes are not Curio beads) costs one indexed lookup and nothing else;
//  2. reads the bead's close reason + labels;
//  3. classifies them into an outcome (ambiguous → 'unknown', never 'fixed';
//     structured curio-outcome:<code> label preferred over free-text heuristic);
//  4. stamps (outcome, resolved_at) on the ledger row.
//
// It returns (reconciled, outcome, err): reconciled is true only when a ledger
// row was actually updated. Errors are returned for the caller to log; the
// reconciler is best-effort and one failed close must not wedge the close
// stream.
func reconcileCurioLedgerClose(beadID string, ledger ledgerReconciler, reader closeSignalReader, logf func(string, ...interface{})) (bool, string, error) {
	inLedger, err := ledger.LedgerHasBead(beadID)
	if err != nil {
		return false, "", err
	}
	if !inLedger {
		// Not a Curio-filed bead — the overwhelmingly common case. No-op.
		return false, "", nil
	}

	closeReason, labels, err := reader.CloseSignals(beadID)
	if err != nil {
		return false, "", err
	}

	outcome := curio.ClassifyOutcome(closeReason, labels)

	updated, err := ledger.SetLedgerOutcome(beadID, outcome)
	if err != nil {
		return false, outcome, err
	}
	if updated {
		logf("curio: reconciled ledger close bead=%s outcome=%s (reason=%q)", beadID, outcome, closeReason)
	}
	return updated, outcome, nil
}

// daemonCloseSignalReader is the production closeSignalReader: it reads a closed
// bead's close reason + labels via the town beads wrapper (routes to the owning
// rig's Dolt for rig-prefixed IDs).
type daemonCloseSignalReader struct {
	d *Daemon
}

// CloseSignals returns the close reason + labels for beadID.
func (r daemonCloseSignalReader) CloseSignals(beadID string) (string, []string, error) {
	b := beads.New(r.d.config.TownRoot)
	issue, err := b.Show(beadID)
	if err != nil {
		return "", nil, err
	}
	if issue == nil {
		return "", nil, nil
	}
	return issue.CloseReason, issue.Labels, nil
}

// onCurioBeadClose is the close-event callback the daemon hands to the convoy
// manager (the existing bead-close event stream). It is best-effort and gated
// off the same circuit breaker as the patrol: a Dolt outage skips reconciliation
// rather than wedging the close stream. It does NOT gate on the FileTunedRules
// filing knob — the ledger may hold rows filed during an earlier ON window, and
// those closes must still reconcile even if filing is currently OFF.
func (d *Daemon) onCurioBeadClose(beadID string) {
	if beadID == "" {
		return
	}
	if !d.doltBreaker.Allow() {
		// Dolt degraded — skip silently. A missed reconcile is recovered the
		// next time the bead's row is queried by the precision lane (the row
		// persists; only outcome/resolved_at are momentarily blank).
		return
	}

	store, err := curio.OpenStore("127.0.0.1", d.doltServerPort(), "hq")
	if err != nil {
		d.doltBreaker.Record(err)
		d.logger.Printf("curio: reconcile: failed to open HQ store for bead=%s: %v", beadID, err)
		return
	}
	defer func() { _ = store.Close() }()

	_, _, err = reconcileCurioLedgerClose(beadID, store, daemonCloseSignalReader{d: d}, d.logger.Printf)
	d.doltBreaker.Record(err)
	if err != nil {
		d.logger.Printf("curio: reconcile: bead=%s failed (best-effort): %v", beadID, err)
	}
}
