package curio

import "github.com/steveyegge/gastown/internal/fingerprint"

// Candidate is a single Curio finding. It is a CANDIDATE row, never a bead:
// Phase 1 emits candidates to the curio_candidate sidecar table only. Filing is
// off until precision is measured (Phase 2).
//
// The {Window, Series, Observed, EWMA, Deviation, Hypothesis} fields match the
// row shape mandated by the design doc. Hypothesis is always nil in Phase 1
// (the LLM hypothesizer is Phase 2). RuleID, Target, and Fingerprint support
// dedup and later rig-routing.
type Candidate struct {
	// WindowID is the window this candidate was found in.
	WindowID string `json:"window"`
	// Series is the descriptive series/signal name for the finding.
	Series string `json:"series"`
	// Observed is the observed quantity that triggered the rule (rule-specific:
	// an event count for the rate rule, 1 for boolean content rules).
	Observed int `json:"observed"`
	// EWMA and Deviation are descriptive only in Phase 1 — the statistical L1
	// detector is out of scope. Content rules leave them zero.
	EWMA      float64 `json:"ewma"`
	Deviation float64 `json:"deviation"`
	// Hypothesis is always nil in Phase 1 (Phase 2 LLM hypothesizer fills it).
	Hypothesis *string `json:"hypothesis"`

	// RuleID is the rule that produced this candidate.
	RuleID string `json:"rule_id"`
	// Target is the entity the finding is about (bead ID, rig, reservation ID).
	// Used for dedup and later rig-routing via beads.GetRigDirForName.
	Target string `json:"target"`
	// Rig is the owning rig for the finding, when known (for later filing).
	Rig string `json:"rig,omitempty"`
	// Fingerprint dedups candidates: fingerprint.Of(RuleID, Target).
	Fingerprint string `json:"fingerprint"`
	// Summary is a short human-readable description of the finding.
	Summary string `json:"summary"`
}

// newCandidate builds a candidate, computing its dedup fingerprint from
// (ruleID, target) via the collision-free fingerprint family.
func newCandidate(windowID, ruleID, target, rig, series string, observed int, summary string) Candidate {
	return Candidate{
		WindowID:    windowID,
		Series:      series,
		Observed:    observed,
		Hypothesis:  nil,
		RuleID:      ruleID,
		Target:      target,
		Rig:         rig,
		Fingerprint: fingerprint.Of(ruleID, target),
		Summary:     summary,
	}
}
