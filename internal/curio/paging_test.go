package curio

import (
	"testing"
	"time"
)

// --- helpers ---

// judgmentCand builds an unverifiable (judgment-lane) candidate with a distinct
// state key.
func judgmentCand(stateHash string) Candidate {
	return Candidate{
		RuleID:      "bead_merged_not_landed",
		Fingerprint: stateHash,
		StateHash:   stateHash,
		Summary:     "judgment finding " + stateHash,
	}
}

// verifiedCand builds a verified-lane candidate whose Verify() returns `holds`.
func verifiedCand(stateHash string, holds bool) Candidate {
	c := Candidate{
		RuleID:      "dead_owner_admission",
		Fingerprint: stateHash,
		StateHash:   stateHash,
		Summary:     "leak " + stateHash,
	}
	c.verify = func() bool { return holds }
	return c
}

// --- ClassifyLane ---

func TestClassifyLane(t *testing.T) {
	if got := ClassifyLane(verifiedCand("v", true)); got != LaneVerified {
		t.Errorf("verifiable candidate => %v, want LaneVerified", got)
	}
	if got := ClassifyLane(judgmentCand("j")); got != LaneJudgment {
		t.Errorf("non-verifiable candidate => %v, want LaneJudgment", got)
	}
	frozen := verifiedCand("f", true)
	frozen.Frozen = true
	if got := ClassifyLane(frozen); got != LaneNone {
		t.Errorf("frozen candidate => %v, want LaneNone (suppressed)", got)
	}
}

// --- Verified lane: uncapped, coalesced, page-once, never trips breaker ---

func TestVerifiedLane_CoalescesHoldingFindingsIntoOnePage(t *testing.T) {
	e := NewPagingEngine()
	now := time.Now()
	cands := []Candidate{
		verifiedCand("leak-a", true),
		verifiedCand("leak-b", true),
		verifiedCand("leak-c", false), // refuted: dropped
	}
	acts := e.Decide(cands, now)
	if len(acts) != 1 {
		t.Fatalf("expected ONE coalesced verified page, got %d: %+v", len(acts), acts)
	}
	a := acts[0]
	if a.Kind != ActionVerifiedPage || a.Lane != LaneVerified {
		t.Fatalf("wrong action: %+v", a)
	}
	if a.Occurrences != 2 {
		t.Errorf("should coalesce the 2 holding findings, got Occurrences=%d", a.Occurrences)
	}
	if a.Severity != "critical" {
		t.Errorf("verified page should be critical, got %q", a.Severity)
	}
	if len(a.Proof) != 2 {
		t.Errorf("proof list should carry both confirmed findings, got %v", a.Proof)
	}
	if e.State() != BreakerArmed {
		t.Errorf("verified lane must NOT trip the judgment breaker, state=%v", e.State())
	}
}

func TestVerifiedLane_PagesOnceWhileStillHolding(t *testing.T) {
	e := NewPagingEngine()
	now := time.Now()
	c := verifiedCand("leak-a", true)

	first := e.Decide([]Candidate{c}, now)
	if len(first) != 1 {
		t.Fatalf("first cycle should page, got %+v", first)
	}
	// Same leak still holds next cycle — must NOT re-page.
	second := e.Decide([]Candidate{c}, now.Add(time.Minute))
	if len(second) != 0 {
		t.Errorf("a still-holding leak must page once, not every cycle: %+v", second)
	}
}

func TestVerifiedLane_RepagesAfterClearAndRecur(t *testing.T) {
	e := NewPagingEngine()
	now := time.Now()
	holding := verifiedCand("leak-a", true)

	if got := e.Decide([]Candidate{holding}, now); len(got) != 1 {
		t.Fatalf("first page expected, got %+v", got)
	}
	// Leak clears (no verified candidate present at all this cycle).
	if got := e.Decide(nil, now.Add(time.Minute)); len(got) != 0 {
		t.Fatalf("cleared cycle should not page, got %+v", got)
	}
	// Leak recurs — paged-memory was forgotten, so it pages again.
	if got := e.Decide([]Candidate{holding}, now.Add(2*time.Minute)); len(got) != 1 {
		t.Errorf("a recurring leak should page again after clearing, got %+v", got)
	}
}

func TestVerifiedLane_RefutedNeverPages(t *testing.T) {
	e := NewPagingEngine()
	got := e.Decide([]Candidate{verifiedCand("leak-a", false)}, time.Now())
	if len(got) != 0 {
		t.Errorf("a refuted verified finding must not page, got %+v", got)
	}
}

// --- Judgment lane: 3-state breaker, sustained + burst ceilings ---

func TestJudgmentLane_BurstCeilingTripsClosed(t *testing.T) {
	e := NewPagingEngine()
	now := time.Now()
	// 3 distinct findings in one cycle => burst >= judgmentBurstCeiling (3).
	cands := []Candidate{judgmentCand("j1"), judgmentCand("j2"), judgmentCand("j3")}
	acts := e.Decide(cands, now)
	if len(acts) != 1 || acts[0].Kind != ActionJudgmentTrip {
		t.Fatalf("burst should trip the breaker exactly once, got %+v", acts)
	}
	if e.State() != BreakerTripped {
		t.Errorf("after trip, state should be Tripped (then latch Closed), got %v", e.State())
	}
	// 3 clusters is NOT > cascadeClusterThreshold(3), so severity is HIGH.
	if acts[0].Severity != "high" {
		t.Errorf("3 clusters => HIGH (misfire), got %q", acts[0].Severity)
	}
}

func TestJudgmentLane_SustainedCeilingTripsClosed(t *testing.T) {
	e := NewPagingEngine()
	base := time.Now()
	// Feed 6 occurrences spread across the 60m window but slow enough to stay
	// under the 3/5m burst ceiling, so only the SUSTAINED ceiling trips.
	var last []PageAction
	for i := 0; i < judgmentCeiling; i++ {
		ts := base.Add(time.Duration(i) * 10 * time.Minute)
		last = e.Decide([]Candidate{judgmentCand("j1")}, ts)
	}
	if len(last) != 1 || last[0].Kind != ActionJudgmentTrip {
		t.Fatalf("the 6th occurrence should trip the sustained ceiling, got %+v", last)
	}
	// Single cluster => HIGH.
	if last[0].Severity != "high" {
		t.Errorf("single cluster => HIGH, got %q", last[0].Severity)
	}
	if last[0].Occurrences < judgmentCeiling {
		t.Errorf("trip should report >= %d occurrences, got %d", judgmentCeiling, last[0].Occurrences)
	}
}

func TestJudgmentLane_CascadeGradesCritical(t *testing.T) {
	e := NewPagingEngine()
	now := time.Now()
	// 4 distinct clusters (> cascadeClusterThreshold 3) in one burst => CRITICAL.
	cands := []Candidate{
		judgmentCand("j1"), judgmentCand("j2"), judgmentCand("j3"), judgmentCand("j4"),
	}
	acts := e.Decide(cands, now)
	if len(acts) != 1 || acts[0].Kind != ActionJudgmentTrip {
		t.Fatalf("expected a single trip, got %+v", acts)
	}
	if acts[0].Severity != "critical" {
		t.Errorf("4 distinct clusters => CRITICAL (cascade), got %q", acts[0].Severity)
	}
	if acts[0].Clusters != 4 {
		t.Errorf("expected 4 clusters, got %d", acts[0].Clusters)
	}
}

func TestJudgmentLane_LatchesClosedAndBumps(t *testing.T) {
	e := NewPagingEngine()
	now := time.Now()
	// Trip via burst.
	trip := e.Decide([]Candidate{judgmentCand("j1"), judgmentCand("j2"), judgmentCand("j3")}, now)
	if len(trip) != 1 || trip[0].Kind != ActionJudgmentTrip {
		t.Fatalf("expected trip, got %+v", trip)
	}
	tripKey := trip[0].DedupKey

	// Next cycle with more judgment occurrences => BUMP, not a new trip, same key.
	bump := e.Decide([]Candidate{judgmentCand("j1")}, now.Add(time.Minute))
	if len(bump) != 1 || bump[0].Kind != ActionJudgmentBump {
		t.Fatalf("latched breaker should BUMP, got %+v", bump)
	}
	if bump[0].DedupKey != tripKey {
		t.Errorf("bump must target the same escalation: trip=%q bump=%q", tripKey, bump[0].DedupKey)
	}
	if e.State() != BreakerClosed {
		t.Errorf("breaker should be latched Closed, got %v", e.State())
	}
}

func TestJudgmentLane_ClosedQuietCycleNoAction(t *testing.T) {
	e := NewPagingEngine()
	now := time.Now()
	e.Decide([]Candidate{judgmentCand("j1"), judgmentCand("j2"), judgmentCand("j3")}, now)
	// A quiet cycle (no judgment occurrences) while latched => no action.
	got := e.Decide(nil, now.Add(time.Minute))
	if len(got) != 0 {
		t.Errorf("latched breaker on a quiet cycle should emit nothing, got %+v", got)
	}
	if e.State() != BreakerClosed {
		t.Errorf("state should remain Closed, got %v", e.State())
	}
}

func TestJudgmentLane_BelowCeilingDoesNotTrip(t *testing.T) {
	e := NewPagingEngine()
	now := time.Now()
	// 2 occurrences: below both burst (3) and sustained (6) ceilings.
	got := e.Decide([]Candidate{judgmentCand("j1"), judgmentCand("j2")}, now)
	if len(got) != 0 {
		t.Errorf("below ceiling must not trip, got %+v", got)
	}
	if e.State() != BreakerArmed {
		t.Errorf("breaker should stay Armed, got %v", e.State())
	}
}

func TestJudgmentLane_ManualResetRequired(t *testing.T) {
	e := NewPagingEngine()
	now := time.Now()
	e.Decide([]Candidate{judgmentCand("j1"), judgmentCand("j2"), judgmentCand("j3")}, now)
	if e.State() != BreakerTripped {
		t.Fatalf("precondition: should be tripped, got %v", e.State())
	}
	// No automatic recovery: even far in the future, the breaker stays latched
	// until Reset (it latches Closed on the next Decide).
	e.Decide(nil, now.Add(2*time.Hour))
	if e.State() != BreakerClosed {
		t.Fatalf("should latch Closed, got %v", e.State())
	}
	e.Reset()
	if e.State() != BreakerArmed {
		t.Errorf("manual Reset should re-arm the breaker, got %v", e.State())
	}
	// After reset, occurrences history is cleared, so a fresh sub-ceiling cycle
	// does not immediately re-trip.
	if got := e.Decide([]Candidate{judgmentCand("j1")}, now.Add(2*time.Hour)); len(got) != 0 {
		t.Errorf("after reset, a single occurrence must not trip, got %+v", got)
	}
}

// --- Frozen findings never reach either lane ---

func TestFrozenFindingsSuppressedFromBothLanes(t *testing.T) {
	e := NewPagingEngine()
	now := time.Now()
	fv := verifiedCand("leak", true)
	fv.Frozen = true
	fj1, fj2, fj3 := judgmentCand("j1"), judgmentCand("j2"), judgmentCand("j3")
	fj1.Frozen, fj2.Frozen, fj3.Frozen = true, true, true

	got := e.Decide([]Candidate{fv, fj1, fj2, fj3}, now)
	if len(got) != 0 {
		t.Errorf("frozen findings must not page on either lane, got %+v", got)
	}
	if e.State() != BreakerArmed {
		t.Errorf("frozen judgment findings must not feed the breaker, state=%v", e.State())
	}
}

// --- Mixed cycle: both lanes act, verified precedes judgment ---

func TestMixedCycle_BothLanesActDeterministicOrder(t *testing.T) {
	e := NewPagingEngine()
	now := time.Now()
	cands := []Candidate{
		verifiedCand("leak-a", true),
		judgmentCand("j1"), judgmentCand("j2"), judgmentCand("j3"),
	}
	acts := e.Decide(cands, now)
	if len(acts) != 2 {
		t.Fatalf("expected verified page + judgment trip, got %+v", acts)
	}
	if acts[0].Lane != LaneVerified || acts[1].Lane != LaneJudgment {
		t.Errorf("verified action must precede judgment action, got %v then %v", acts[0].Lane, acts[1].Lane)
	}
}

// --- Occurrence window pruning ---

func TestJudgmentLane_OldOccurrencesPruned(t *testing.T) {
	e := NewPagingEngine()
	base := time.Now()
	// One occurrence far in the past, then 2 fresh ones: the old one is outside
	// the 60m sustained window and must not count toward the ceiling.
	e.Decide([]Candidate{judgmentCand("old")}, base)
	got := e.Decide([]Candidate{judgmentCand("j1"), judgmentCand("j2")}, base.Add(90*time.Minute))
	if len(got) != 0 {
		t.Errorf("pruned-out old occurrence should not push us over the ceiling, got %+v", got)
	}
}
