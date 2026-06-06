// curio_dog_patrol.go wires Curio's deterministic rules onto the dog health
// surface (gu-fcwx8.4, "Patrol hardening — Calls 1-3").
//
// The doctor_dog patrol tick calls runCurioPatrolCheck, which collects live
// admission state, runs the deterministic rules, and applies Call 3
// verification. Verified findings are logged and surfaced in the daemon's
// curio heartbeat as a fast-path addendum, letting agents observe confirmed
// findings between the 15m main curio ticks.
//
// This is NOT a new patrol ticker — it rides the existing curio tick but
// exposes a public RunDeterministicCheck for the dog health surface to call
// independently (e.g., from doctor_dog or a standalone gt dog health-check).
package daemon

import (
	"fmt"
	"time"

	"github.com/steveyegge/gastown/internal/curio"
)

// CurioPatrolCheckResult is the daemon-level result of running the
// deterministic curio rules on the dog health surface. It wraps the curio
// package's PatrolResult with daemon-specific context.
type CurioPatrolCheckResult struct {
	// Result is the raw curio patrol output.
	Result curio.PatrolResult `json:"result"`
	// NeedsAttention is true when at least one verified finding exists.
	NeedsAttention bool `json:"needs_attention"`
	// Recommendation is a human-readable summary for the health report.
	Recommendation string `json:"recommendation,omitempty"`
}

// runCurioPatrolCheck executes the deterministic curio rules as a dog health
// check. It collects only the admission state (the only data source with a
// verified-lane rule today), runs EvalDeterministicChecks, and returns the
// result. The merged-bead and kill-signal rules are judgment-lane (no verify
// thunk) so they pass through but are filtered by the Call 3 gate inside
// EvalDeterministicChecks.
//
// This is intentionally lightweight: no Dolt access (admissions are on the
// filesystem), no cross-cycle state, no paging. It can safely run at the
// doctor_dog's 5m cadence without impacting Dolt or the scheduler.
func (d *Daemon) runCurioPatrolCheck() CurioPatrolCheckResult {
	if !d.isPatrolActive("curio") {
		return CurioPatrolCheckResult{}
	}

	now := time.Now().UTC()
	windowID := fmt.Sprintf("dog-patrol/%s", now.Format(time.RFC3339))

	// Collect only filesystem-backed sources (admissions). The merged-bead
	// rule (a) requires Dolt and is judgment-lane anyway; the rate rule (c)
	// and kill-signal rule (b) are also judgment-lane. Including them in the
	// Input is safe (they'll produce candidates but EvalDeterministicChecks
	// filters to verified-only), but we skip the expensive Dolt/git sources
	// to keep this fast.
	in, err := curio.CollectInput(d.config.TownRoot, windowID)
	if err != nil {
		d.logger.Printf("curio-patrol-check: collect failed: %v", err)
		return CurioPatrolCheckResult{
			Recommendation: fmt.Sprintf("curio patrol check failed: %v", err),
		}
	}

	// Wire the Call 1(A) air-gap curio beads set (same as the main patrol).
	in.CurioBeads = d.collectCurioFiledBeads()

	result := curio.EvalDeterministicChecks(in)

	check := CurioPatrolCheckResult{
		Result:         result,
		NeedsAttention: len(result.Findings) > 0,
	}

	if check.NeedsAttention {
		check.Recommendation = curio.FormatFindingSummary(result)
		d.logger.Printf("curio-patrol-check: %s", check.Recommendation)
	} else {
		d.logger.Printf("curio-patrol-check: %d rules, %d candidates, 0 verified — clean",
			result.RulesRun, result.CandidatesFound)
	}

	return check
}

// RunDeterministicCheck is the public entry point for the dog health surface.
// It runs curio's deterministic rules with Call 1-3 hardening and returns
// findings suitable for inclusion in DogHealthResult.Recommendation or
// structured health reports.
//
// Callers (dog health-check CLI, doctor_dog, deacon patrol) get a fast,
// stateless, Dolt-free assessment of whether proven deterministic failures
// (currently: dead-owner admission leaks) are present. The main curio patrol
// continues to run the full rule set + EWMA + paging on its own 15m cadence.
func (d *Daemon) RunDeterministicCheck() CurioPatrolCheckResult {
	return d.runCurioPatrolCheck()
}
