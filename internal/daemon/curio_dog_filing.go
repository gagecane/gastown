// curio_dog_filing.go is the B0a (gu-czx5e) PRODUCER half of curio_ledger
// population: the candidate→bead filing path for the tuned rules.
//
// When the FileTunedRules gate is on, the curio patrol files a bead for each
// fresh tuned-rule candidate and, at file-time, writes the curio_ledger row
// (bead_id, fingerprint, rule_id, outcome=”) the P3 precision lane reasons
// over. The post-close reconciler + outcome classifier that later fill outcome
// and resolved_at are B0b (gu-wg7i5), NOT this bead.
//
// The orchestration (fileTunedCandidates) is a free function over two small
// interfaces so the filing-row insert is unit-testable with fakes — no live
// Dolt or bead store required. The daemon wires the concrete store + bead
// filer at the call site in runCurio.
package daemon

import (
	"github.com/steveyegge/gastown/internal/curio"
)

// tunedRuleIDs is the allowlist of rules whose candidates B0a files. The lane
// tunes alarm_rate_spike's per-series thresholds (curio.DefaultRateThresholds),
// so it is the only rule whose precision the ledger needs to measure today.
// Other rules stay candidates-only — keeping the filing blast radius minimal
// and the air-gap surface small. Adding a rule here is a deliberate scope
// decision, not an accident of iteration.
var tunedRuleIDs = map[string]bool{
	"alarm_rate_spike": true,
}

// isTunedRule reports whether a candidate's rule is in the file-enabled
// allowlist.
func isTunedRule(ruleID string) bool { return tunedRuleIDs[ruleID] }

// candidateLedger is the file-once + file-time write surface fileTunedCandidates
// needs. *curio.Store satisfies it; tests supply a fake.
type candidateLedger interface {
	// FingerprintFiled reports whether a ledger row already exists for the
	// fingerprint (the file-once dedup check).
	FingerprintFiled(fingerprint string) (bool, error)
	// InsertLedgerRow writes the file-time ledger row for a freshly filed bead.
	InsertLedgerRow(beadID, fingerprint, ruleID string) error
}

// *curio.Store is the production candidateLedger; assert it at compile time so a
// store-method signature drift breaks the build here, not at runtime.
var _ candidateLedger = (*curio.Store)(nil)

// candidateFiler files a bead for a tuned-rule candidate and returns the new
// bead's ID. The daemon's beadFiler (curio_dog_beads.go) is the production
// implementation; tests supply a fake.
type candidateFiler interface {
	FileCurioBead(c curio.Candidate) (beadID string, err error)
}

// fileTunedCandidates is the B0a filing orchestration. For each candidate whose
// rule is tuned (file-enabled), it:
//
//  1. skips the candidate if its fingerprint is already in the ledger
//     (file-once: a finding is filed at most once, and the ledger row persists
//     through bead close, so we never re-file);
//  2. files the bead via the filer;
//  3. writes the file-time ledger row (bead_id, fingerprint, rule_id) with
//     outcome=” so the precision lane can later reconcile it.
//
// It returns the count of newly filed beads. A per-candidate error is logged
// and skipped (best-effort: one bad candidate must not abort the cycle or
// suppress the rest), and the first encountered error is returned for
// observability after all candidates are processed. The file-then-ledger order
// matters: if the ledger insert fails after a successful file, the next cycle's
// FingerprintFiled returns false and we would re-file — so InsertLedgerRow uses
// INSERT IGNORE on bead_id and the filer is expected to dedup on its own
// fingerprint label, bounding duplicates to at most one stray bead per
// persistent ledger-write outage rather than one per cycle.
func fileTunedCandidates(cands []curio.Candidate, filer candidateFiler, ledger candidateLedger, logf func(string, ...interface{})) (int, error) {
	filed := 0
	var firstErr error
	noteErr := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}

	for _, c := range cands {
		if !isTunedRule(c.RuleID) {
			continue
		}

		already, err := ledger.FingerprintFiled(c.Fingerprint)
		if err != nil {
			logf("curio: filing: ledger lookup failed for fp=%s rule=%s: %v", c.Fingerprint, c.RuleID, err)
			noteErr(err)
			continue
		}
		if already {
			logf("curio: filing: fp=%s rule=%s already filed — skipping (file-once)", c.Fingerprint, c.RuleID)
			continue
		}

		beadID, err := filer.FileCurioBead(c)
		if err != nil {
			logf("curio: filing: file bead failed for fp=%s rule=%s: %v", c.Fingerprint, c.RuleID, err)
			noteErr(err)
			continue
		}

		if err := ledger.InsertLedgerRow(beadID, c.Fingerprint, c.RuleID); err != nil {
			logf("curio: filing: ledger insert failed for bead=%s fp=%s rule=%s: %v", beadID, c.Fingerprint, c.RuleID, err)
			noteErr(err)
			continue
		}

		filed++
		logf("curio: filing: filed bead=%s fp=%s rule=%s (ledger row written, outcome='')", beadID, c.Fingerprint, c.RuleID)
	}

	return filed, firstErr
}

// curioFileTunedRules reports whether the B0a candidate→bead filing gate is
// open. Defaults to false (candidates-only) whenever config is absent — the
// Patrol's prior behavior.
func (d *Daemon) curioFileTunedRules() bool {
	if d.patrolConfig != nil && d.patrolConfig.Patrols != nil && d.patrolConfig.Patrols.Curio != nil {
		return d.patrolConfig.Patrols.Curio.FileTunedRules
	}
	return false
}

// daemonCandidateFiler is the production candidateFiler: it files a curio bead
// at the town root via the beads CLI/store path. alarm_rate_spike candidates
// carry no rig (the rate series is town-global), so the bead lands in HQ rather
// than a rig tree.
type daemonCandidateFiler struct {
	d *Daemon
}

// FileCurioBead files a bead for a tuned-rule candidate and returns its ID.
func (f daemonCandidateFiler) FileCurioBead(c curio.Candidate) (string, error) {
	return f.d.fileCurioBead(c)
}
