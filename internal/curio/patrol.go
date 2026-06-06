// Package curio patrol surface: exposes deterministic rules as lightweight
// health checks consumable by the dog health system (gu-fcwx8.4).
//
// "Folding onto the dog surface" means the deacon's dogs can run curio's
// proven-deterministic checks on THEIR patrol cadence (typically 5m via
// doctor_dog) rather than waiting for the curio patrol's 15m tick. Only rules
// whose findings carry a Call 3 verify() thunk (the freeze-class fast path)
// are surfaced here — they are true by construction (a syscall re-confirms
// them), so they skip the judgment lane entirely and do not need the
// cross-cycle paging engine state.
//
// Safety: all Call 1-3 mechanisms are applied:
//   - Call 1(A) air-gap: suppressed() filters curio's own records
//   - Call 1(B) state-hash damper: Evaluate deduplicates by StateHash
//   - Call 3 freeze-class: only Verifiable() candidates that re-confirm
//     via Verify() are emitted as PatrolFindings
//
// Call 1(C) (reaction-count backstop) and Call 2 (paging engine) are NOT
// needed here: the dog surface produces findings for agent consumption, not
// human paging. The paging engine's cross-cycle state lives on the main curio
// patrol and is unaffected.
package curio

import (
	"fmt"
	"time"
)

// PatrolFinding is a single verified finding from the deterministic rule set,
// suitable for consumption by the dog health surface. Every PatrolFinding has
// been re-confirmed via its Call 3 verify() thunk — it is true by construction
// at the moment of emission.
type PatrolFinding struct {
	// RuleID identifies which rule produced the finding.
	RuleID string `json:"rule_id"`
	// Target is the entity (bead ID, reservation ID, rig) the finding is about.
	Target string `json:"target"`
	// Rig is the owning rig, when known.
	Rig string `json:"rig,omitempty"`
	// Summary is a human-readable one-liner.
	Summary string `json:"summary"`
	// Fingerprint is the stable dedup key.
	Fingerprint string `json:"fingerprint"`
	// StateHash is the Call 1(B) coarse state key (may equal Fingerprint).
	StateHash string `json:"state_hash"`
	// VerifiedAt is when the Verify() thunk confirmed the finding.
	VerifiedAt time.Time `json:"verified_at"`
}

// PatrolResult is the output of EvalDeterministicChecks — the dog surface
// payload. It carries only verified findings (Call 3 fast-path) plus metadata
// for observability.
type PatrolResult struct {
	// WindowID labels the check cycle.
	WindowID string `json:"window_id"`
	// EvaluatedAt is when the check ran.
	EvaluatedAt time.Time `json:"evaluated_at"`
	// RulesRun is how many deterministic rules were evaluated.
	RulesRun int `json:"rules_run"`
	// CandidatesFound is the total candidates before Call 3 filtering.
	CandidatesFound int `json:"candidates_found"`
	// Findings are the Call 3-verified results (verified = still holding).
	Findings []PatrolFinding `json:"findings"`
}

// EvalDeterministicChecks runs only the deterministic content rules over the
// provided Input, applies Call 1(A)+1(B) dedup via Evaluate, then filters to
// only Verifiable candidates whose Verify() thunk returns true. This is the
// "dog surface" entry point: fast, stateless, and independently callable.
//
// It does NOT touch the EWMA detector (requires cross-cycle state), the
// reaction tracker (Call 1C, cross-cycle), or the paging engine (Call 2,
// cross-cycle). Those stay on the main curio patrol's 15m cadence.
func EvalDeterministicChecks(in Input) PatrolResult {
	now := time.Now().UTC()
	rules := DefaultRules()

	// Call 1(A) + Call 1(B): Evaluate applies both the loop-breaker
	// (suppressed()) and the state-hash damper (dedup by StateHash).
	cands := Evaluate(rules, in)

	result := PatrolResult{
		WindowID:        in.Window.ID,
		EvaluatedAt:     now,
		RulesRun:        len(rules),
		CandidatesFound: len(cands),
	}

	// Call 3: only emit findings that carry a verify() thunk AND whose
	// underlying condition still holds at this instant. Non-verifiable
	// candidates (judgment-lane) are excluded from the dog surface — they
	// need the paging engine's cross-cycle state to be correctly classified.
	for _, c := range cands {
		if !c.Verifiable() {
			continue
		}
		if !c.Verify() {
			// The condition resolved between evaluation and verification.
			// This is the correct outcome (the leak was reaped) — not a finding.
			continue
		}
		result.Findings = append(result.Findings, PatrolFinding{
			RuleID:      c.RuleID,
			Target:      c.Target,
			Rig:         c.Rig,
			Summary:     c.Summary,
			Fingerprint: c.Fingerprint,
			StateHash:   c.StateHash,
			VerifiedAt:  now,
		})
	}

	return result
}

// FormatFindingSummary returns a concise multi-line summary of patrol findings
// for log output or dog health recommendations.
func FormatFindingSummary(r PatrolResult) string {
	if len(r.Findings) == 0 {
		return fmt.Sprintf("curio: %d rules, %d candidates, 0 verified findings",
			r.RulesRun, r.CandidatesFound)
	}
	summary := fmt.Sprintf("curio: %d rules, %d candidates, %d VERIFIED findings:",
		r.RulesRun, r.CandidatesFound, len(r.Findings))
	for _, f := range r.Findings {
		summary += fmt.Sprintf("\n  [%s] %s", f.RuleID, f.Summary)
	}
	return summary
}
