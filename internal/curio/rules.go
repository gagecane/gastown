package curio

import (
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/fingerprint"
	"github.com/steveyegge/gastown/internal/liveness"
)

// Rule is a pure content-detection predicate. It reads the normalized Input
// (all probes already resolved) and returns zero or more candidates. Rules MUST
// be deterministic and do no I/O — that is what lets the replay harness grade
// them as a CI gate.
type Rule interface {
	// ID is the stable rule identifier (used in candidate RuleID + fingerprint).
	ID() string
	// Eval returns candidates for the given input window.
	Eval(in Input) []Candidate
}

// isCurio reports whether a provenance actor is Curio itself. The loop-breaker
// (safety invariant 5) excludes only Curio's own records — sibling patrols stay
// visible to detection (eng-review decision 3: narrowed from "all patrols").
func isCurio(filedBy string) bool {
	return filedBy == CurioActor
}

// suppressed is the full Call 1(A) loop-breaker: a record is suppressed if it
// is Curio's OWN (FiledBy == "curio") OR a REACTION to a Curio-filed bead
// (CausalRoot ∈ Input.CurioBeads). Build 2a extends the original FiledBy-only
// check with the second, causal half so that once filing turns on, the churn a
// Curio bead provokes downstream cannot feed back as a fresh detection.
func (in Input) suppressed(filedBy string, p causalProvenance) bool {
	return isCurio(filedBy) || in.isCurioReaction(p)
}

// --- Rule (a): bead closed "merged" but commit not in main ancestry ---
// gu-kc3lo class. No rate/latency signature; a pure correctness fact.

type mergedNotLandedRule struct{}

func (mergedNotLandedRule) ID() string { return "bead_merged_not_landed" }

func (r mergedNotLandedRule) Eval(in Input) []Candidate {
	var out []Candidate
	for _, b := range in.Beads {
		if in.suppressed(b.FiledBy, b.causalProvenance) {
			continue
		}
		if b.CloseReason != "merged" {
			continue
		}
		// A bead claiming "merged" with no commit, or a commit absent from main
		// ancestry, is a merged-but-not-landed finding. Empty commit is
		// suspicious on its own (claims merged, no landed commit recorded).
		if b.Commit == "" || !b.CommitInMainAncestry {
			summary := fmt.Sprintf("bead %s closed 'merged' but commit %q not in main ancestry (rig %s)",
				b.ID, b.Commit, b.Rig)
			out = append(out, newCandidate(in.Window.ID, r.ID(), b.ID, b.Rig, "bead.close.merged", 1, summary))
		}
	}
	return out
}

// --- Rule (b): kill-signal in dog logs near a Dolt PID ---
// gc-wisp-2yc7 class. Single discrete event, no statistical precursor.

type killSignalNearDoltRule struct{}

func (killSignalNearDoltRule) ID() string { return "kill_signal_near_dolt" }

func (r killSignalNearDoltRule) Eval(in Input) []Candidate {
	var out []Candidate
	for i, l := range in.LogLines {
		if in.suppressed(l.FiledBy, l.causalProvenance) {
			continue
		}
		if !l.NearDoltPID {
			continue
		}
		// Target on source+index keeps distinct lines from the same source as
		// distinct candidates within a window (dedup across windows is by fp).
		target := fmt.Sprintf("%s#%d", l.Source, i)
		summary := fmt.Sprintf("kill/quit signal near Dolt PID in %s log: %q", l.Source, l.Text)
		out = append(out, newCandidate(in.Window.ID, r.ID(), target, "", "dog.log.kill_signal", 1, summary))
	}
	return out
}

// --- Rule (c): alarm/dispatch rate spike (content threshold rule) ---
// gu-70rg 327-flood class. A CONTENT rate rule with fixed per-series
// thresholds, NOT the L1 EWMA/MAD detector (out of scope). Rare-event series
// (dispatch.stuck_agent, escalation, sched_fail) are threshold-0: any non-zero
// count fires, matching the Phase 0 measured baselines.

// rateThresholds are the fixed per-series fire thresholds. A series fires when
// Observed > threshold. Rare-event series use 0 (any non-zero fires). Values
// from Phase 0 measured normal baselines (design doc Phase 0 Results).
var rateThresholds = map[string]int{
	"dispatch.stuck_agent": 0,
	"escalation":           0,
	"sched_fail":           0,
	// Bursty/normal-traffic series carry a high ceiling so only true floods
	// fire. sling/mail/bead normal maxima are ~120-235/day; done is burstier.
	"sling":      300,
	"done":       400,
	"mail":       300,
	"bead.open":  150,
	"bead.close": 150,
}

type rateSpikeRule struct {
	thresholds map[string]int
}

func (rateSpikeRule) ID() string { return "alarm_rate_spike" }

func (r rateSpikeRule) Eval(in Input) []Candidate {
	var out []Candidate
	for _, c := range in.EventCounts {
		if in.suppressed(c.FiledBy, c.causalProvenance) {
			continue
		}
		// Call 1(A) air-gap: never rate-detect Curio's own telemetry series,
		// regardless of which actor the events were attributed to.
		if strings.HasPrefix(c.Series, CurioSeriesPrefix) {
			continue
		}
		threshold, known := r.thresholds[c.Series]
		if !known {
			// Unknown series: no configured threshold, do not fire (avoids
			// noise from series we haven't baselined).
			continue
		}
		if c.Observed > threshold {
			summary := fmt.Sprintf("series %q rate %d exceeds threshold %d", c.Series, c.Observed, threshold)
			cand := newCandidate(in.Window.ID, rateSpikeRule{}.ID(), c.Series, "", c.Series, c.Observed, summary)
			out = append(out, cand)
		}
	}
	return out
}

// --- Rule (d): dead-owner polecat-admission reservation ---
// Discovered in the wild (design addendum, gu-t6jqq class). A reservation whose
// owning PID is dead leaks capacity. No rate/latency signature.

type deadOwnerAdmissionRule struct{}

func (deadOwnerAdmissionRule) ID() string { return "dead_owner_admission" }

func (r deadOwnerAdmissionRule) Eval(in Input) []Candidate {
	var out []Candidate
	for _, a := range in.Admissions {
		if in.suppressed(a.FiledBy, a.causalProvenance) {
			continue
		}
		if a.OwnerAlive {
			continue
		}
		summary := fmt.Sprintf("admission reservation %s owned by dead PID %d leaking capacity (rig %s)",
			a.ID, a.PID, a.Rig)
		cand := newCandidate(in.Window.ID, r.ID(), a.ID, a.Rig, "polecat.admission.dead_owner", 1, summary)

		// Call 1(B) state-hash damper: the actionable STATE is "this rig has
		// leaked capacity", not "this PID-keyed reservation file exists". The
		// scheduler rewrites reservation files across boot/deacon cycles, so the
		// same leak flaps through a series of distinct reservation IDs/owners.
		// Keying StateHash on the rig (the stable dimension) collapses that flap
		// to ONE candidate. When the rig is unknown we fall back to the
		// per-reservation fingerprint (default) — never over-collapse unkeyed
		// reservations into a single bucket.
		if a.Rig != "" {
			cand.StateHash = fingerprint.Of(r.ID(), "rig", a.Rig)
		}

		// Call 3 freeze-class fast path: dead_owner is the rule firing in
		// production, and its truth is a cheap, deterministic syscall — so it
		// rides the LaneVerified path. Attach a Verify() thunk that re-probes
		// PID liveness; the finding STILL HOLDS iff the owner is still dead. Eval
		// only constructs the thunk (pure); the live emitter (Call 2, 2b) calls
		// it. Capturing the PID by value keeps the thunk free of loop-var aliasing.
		pid := a.PID
		cand.verify = func() bool { return !liveness.PIDAlive(pid) }

		out = append(out, cand)
	}
	return out
}

// DefaultRules returns the Phase 1 content-rule set in a stable order.
func DefaultRules() []Rule {
	return []Rule{
		mergedNotLandedRule{},
		killSignalNearDoltRule{},
		rateSpikeRule{thresholds: rateThresholds},
		deadOwnerAdmissionRule{},
	}
}

// Evaluate runs all rules over the input and returns deduplicated candidates,
// sorted deterministically by fingerprint for stable output.
//
// Dedup is by StateHash, not Fingerprint (Call 1(B) state-hash damper): two
// candidates that describe the same DISTINCT STATE — even via different
// fingerprints, like a leak flapping across reservation IDs within one rig —
// collapse to one. For rules that don't set a coarser StateHash, StateHash ==
// Fingerprint, so this is identical to the prior fingerprint-dedup behavior.
// First-writer-wins within the rule order, so output stays deterministic.
func Evaluate(rules []Rule, in Input) []Candidate {
	seen := make(map[string]bool)
	var out []Candidate
	for _, rule := range rules {
		for _, c := range rule.Eval(in) {
			key := c.StateHash
			if key == "" {
				key = c.Fingerprint
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fingerprint < out[j].Fingerprint })
	return out
}
