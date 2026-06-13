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

// isCurioSeries reports whether a series name is one Curio itself emits (the
// CurioSeriesPrefix air-gap). It is the SINGLE definition of that predicate:
// the live rate rule (rateSpikeRule.Eval), the EWMA detector, and the
// Retrospect digest filter (selfReferential) all call this, so the air-gap is
// single-sourced (design-doc Q5: "reuses the EXACT predicates ... not
// re-implemented"). Do not inline strings.HasPrefix(s, CurioSeriesPrefix)
// anywhere — route every check through here.
func isCurioSeries(series string) bool {
	return strings.HasPrefix(series, CurioSeriesPrefix)
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
// thresholds, NOT the L1 EWMA/MAD detector. The fixed rule is the coarse
// hard-ceiling backstop; the EWMA detector (detector.go, k=3.0) is the
// statistical complement that catches subtler shifts. Both run side by side.

// rateThresholds are the calibrated per-series fire thresholds (gc-e2uvyr.3). A
// series fires when Observed > threshold. Values are set to p95 + margin from
// the gc-e2uvyr.2 live baseline (town .events.jsonl, each series' instrumented
// era, full UTC days). They are deliberate ceilings above normal heavy
// throughput so the rule only flags true floods — normal busy days stay quiet.
//
//	series       baseline min/med/p95/max   threshold   rationale
//	sling        34/180/266/310             350         > observed max (310) + margin
//	done         33/212/900/1183            1300        > observed max (1183); old 400 tripped on routine bursts
//	mail         27/181/709/839             900         > observed max (839); old 300 tripped on any busy day
//	escalation   2/60/120/120               150         > observed max (120); old 0 fired every active day
//	sched_fail   0/6/27/27                  30          > observed max (27); old 0 fired on any dispatch failure
//
// The previously threshold-0 noisy series (escalation, sched_fail) move to a
// ceiling here and lean on the EWMA detector for nuanced detection — both
// series are in detectorSeries (detector.go), so a sustained anomaly below the
// fixed ceiling is still caught statistically without the per-day false fires.
//
// dispatch.stuck_agent stays at 0 (any non-zero fires): it has NEVER been
// emitted across the entire corpus, so this is a deliberate floor that pages on
// the first-ever occurrence of a genuinely critical, never-before-seen event —
// not an unbaselined accident.
//
// bead.open / bead.close keep their prior ceilings: they are sourced from bead
// Dolt, not events.jsonl (see rateSeriesForEventType in collect_live.go), so the
// live rate collector never feeds them and the gc-e2uvyr.2 events baseline does
// not cover them. They are left unchanged to avoid an unbaselined retune.
//
// Defaults can be overridden per-series via daemon.json patrols.curio
// rate_thresholds without a rebuild (see DefaultRulesWithThresholds).
var rateThresholds = map[string]int{
	"dispatch.stuck_agent": 0,
	"escalation":           150,
	"sched_fail":           30,
	"sling":                350,
	"done":                 1300,
	"mail":                 900,
	"bead.open":            150,
	"bead.close":           150,
}

// DefaultRateThresholds returns a copy of the calibrated per-series thresholds.
// Callers (e.g. the daemon config layer) overlay operator overrides on top of
// this so an absent or partial config still falls back to safe calibrated
// defaults.
func DefaultRateThresholds() map[string]int {
	out := make(map[string]int, len(rateThresholds))
	for k, v := range rateThresholds {
		out[k] = v
	}
	return out
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
		if isCurioSeries(c.Series) {
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

// DefaultRules returns the Phase 1 content-rule set in a stable order, using the
// calibrated default rate thresholds.
func DefaultRules() []Rule {
	return DefaultRulesWithThresholds(rateThresholds)
}

// DefaultRulesWithThresholds returns the Phase 1 content-rule set with the
// rate-spike rule keyed on the supplied per-series thresholds. A nil or empty
// map falls back to the calibrated defaults, so a missing daemon.json config
// can never disable the rate ceiling. The caller owns the map; it is not
// mutated here.
func DefaultRulesWithThresholds(thresholds map[string]int) []Rule {
	if len(thresholds) == 0 {
		thresholds = rateThresholds
	}
	return []Rule{
		mergedNotLandedRule{},
		killSignalNearDoltRule{},
		rateSpikeRule{thresholds: thresholds},
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
