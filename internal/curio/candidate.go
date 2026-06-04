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

	// StateHash is the Call 1(B) state-hash damper key: the dedup identity of
	// the finding's DISTINCT STATE, deliberately INDEPENDENT of volatile
	// dimensions (e.g. which transient owner currently holds a leak). When a
	// target flaps between states that share the same actionable condition
	// (the boot<->deacon flap), every flap maps to one StateHash and Evaluate
	// collapses them to a single candidate. Defaults to Fingerprint, so a rule
	// that does not set a coarser state key behaves exactly as before.
	StateHash string `json:"state_hash"`

	// ReactionCount and Frozen are the Call 1(C) reaction-count backstop output,
	// populated cross-cycle by a ReactionTracker (not by Eval — Eval is pure).
	// ReactionCount is how many times this (rule,target) has flipped presence in
	// the tracker's recent window; Frozen is set once it exceeds the freeze
	// threshold. Call 2 (gu-2coqj) consumes Frozen to suppress a churning
	// finding from the paging lane. Both are runtime-only: they are NOT
	// persisted in build 2a (the store schema is unchanged).
	ReactionCount int  `json:"-"`
	Frozen        bool `json:"-"`

	// verify is the Call 3 freeze-class fast-path thunk. A rule that implements
	// the LaneVerified path (currently dead_owner_admission) sets it at emit
	// time to a cheap, deterministic syscall re-probe (Verify() via
	// internal/liveness). It is constructed in Eval but NEVER CALLED there, so
	// Eval stays pure and replay-gradeable; only the live emitter (Call 2, 2b)
	// invokes Verify(). Unexported so encoding/json ignores it.
	verify func() bool
}

// Verifiable reports whether this candidate carries a syscall-level verifier
// (the LaneVerified fast path). A candidate without one is judgment-lane.
func (c Candidate) Verifiable() bool { return c.verify != nil }

// Verify re-probes the candidate's underlying condition live and returns true
// if the finding STILL HOLDS. It is the only impure path in the candidate
// layer and is never exercised during replay. A non-Verifiable candidate
// returns false (it cannot self-confirm — it belongs in the judgment lane).
func (c Candidate) Verify() bool {
	if c.verify == nil {
		return false
	}
	return c.verify()
}

// newCandidate builds a candidate, computing its dedup fingerprint from
// (ruleID, target) via the collision-free fingerprint family. StateHash
// defaults to the fingerprint (each distinct (rule,target) is its own state).
func newCandidate(windowID, ruleID, target, rig, series string, observed int, summary string) Candidate {
	fp := fingerprint.Of(ruleID, target)
	return Candidate{
		WindowID:    windowID,
		Series:      series,
		Observed:    observed,
		Hypothesis:  nil,
		RuleID:      ruleID,
		Target:      target,
		Rig:         rig,
		Fingerprint: fp,
		StateHash:   fp,
		Summary:     summary,
	}
}
