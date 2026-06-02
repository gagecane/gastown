package autotestpr

import (
	"testing"
	"time"
)

var rankerNow = time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)

// TestRankCandidates_HighestScoreWins verifies that the candidate with
// the highest (churn × uncovered_branches) is ranked first.
func TestRankCandidates_HighestScoreWins(t *testing.T) {
	t.Parallel()

	cands := []TargetCandidate{
		{Path: "a.go", Churn: 2, UncoveredBranches: []UncoveredBranch{{Line: 1}, {Line: 2}}}, // score 4
		{Path: "b.go", Churn: 5, UncoveredBranches: []UncoveredBranch{{Line: 1}, {Line: 2}, {Line: 3}}}, // score 15
		{Path: "c.go", Churn: 1, UncoveredBranches: []UncoveredBranch{{Line: 1}}}, // score 1
	}

	got := RankCandidates(cands, nil, rankerNow)
	if len(got) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(got))
	}
	if got[0].Path != "b.go" {
		t.Errorf("expected highest-score b.go first, got %q", got[0].Path)
	}
	if got[2].Path != "c.go" {
		t.Errorf("expected lowest-score c.go last, got %q", got[2].Path)
	}
}

// TestRankCandidates_RejectionCooldownExcludes verifies that a candidate
// whose path is under an active rejection cooldown is dropped, while the
// same path past its cooldown is retained.
func TestRankCandidates_RejectionCooldownExcludes(t *testing.T) {
	t.Parallel()

	cands := []TargetCandidate{
		{Path: "cooled.go", Churn: 10, UncoveredBranches: []UncoveredBranch{{Line: 1}, {Line: 2}}}, // highest score
		{Path: "open.go", Churn: 1, UncoveredBranches: []UncoveredBranch{{Line: 1}}},
	}

	rejections := []RejectionRecord{
		{File: "cooled.go", CooldownUntil: rankerNow.Add(24 * time.Hour)}, // still cooling
	}

	got := RankCandidates(cands, rejections, rankerNow)
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate after cooldown filter, got %d: %+v", len(got), got)
	}
	if got[0].Path != "open.go" {
		t.Errorf("expected cooled.go excluded, got %q", got[0].Path)
	}
}

// TestRankCandidates_ExpiredCooldownIncluded verifies a rejection whose
// cooldown has elapsed no longer excludes the file.
func TestRankCandidates_ExpiredCooldownIncluded(t *testing.T) {
	t.Parallel()

	cands := []TargetCandidate{
		{Path: "expired.go", Churn: 3, UncoveredBranches: []UncoveredBranch{{Line: 1}}},
	}
	rejections := []RejectionRecord{
		{File: "expired.go", CooldownUntil: rankerNow.Add(-1 * time.Hour)}, // past
	}

	got := RankCandidates(cands, rejections, rankerNow)
	if len(got) != 1 {
		t.Fatalf("expected expired-cooldown file included, got %d", len(got))
	}
}

// TestRankCandidates_DropsZeroScore verifies candidates with no
// uncovered branches (score 0) are dropped.
func TestRankCandidates_DropsZeroScore(t *testing.T) {
	t.Parallel()

	cands := []TargetCandidate{
		{Path: "fully-covered.go", Churn: 100, UncoveredBranches: nil},
		{Path: "real.go", Churn: 1, UncoveredBranches: []UncoveredBranch{{Line: 5}}},
	}
	got := RankCandidates(cands, nil, rankerNow)
	if len(got) != 1 || got[0].Path != "real.go" {
		t.Fatalf("expected only real.go, got %+v", got)
	}
}

// TestRankCandidates_TieBreakByPath verifies equal-score candidates are
// ordered deterministically by path.
func TestRankCandidates_TieBreakByPath(t *testing.T) {
	t.Parallel()

	cands := []TargetCandidate{
		{Path: "zebra.go", Churn: 2, UncoveredBranches: []UncoveredBranch{{Line: 1}}}, // score 2
		{Path: "apple.go", Churn: 2, UncoveredBranches: []UncoveredBranch{{Line: 1}}}, // score 2
	}
	got := RankCandidates(cands, nil, rankerNow)
	if got[0].Path != "apple.go" || got[1].Path != "zebra.go" {
		t.Errorf("expected apple.go before zebra.go on tie, got %q, %q", got[0].Path, got[1].Path)
	}
}

// TestOrderUncoveredByChurnProximity verifies uncovered branches are
// ordered by ascending line-distance to the nearest churn range (NG5).
func TestOrderUncoveredByChurnProximity(t *testing.T) {
	t.Parallel()

	branches := []UncoveredBranch{
		{Line: 100, Kind: "legacy"}, // far from churn
		{Line: 42, Kind: "inside"},  // inside churn range
		{Line: 55, Kind: "near"},    // 5 lines below churn end
	}
	// Recent churn touched lines 40-50.
	churn := []ChurnRange{{Start: 40, End: 50}}

	got := OrderUncoveredByChurnProximity(branches, churn)
	wantOrder := []string{"inside", "near", "legacy"}
	for i, w := range wantOrder {
		if got[i].Kind != w {
			t.Errorf("position %d: expected %q, got %q (full: %+v)", i, w, got[i].Kind, got)
		}
	}
}

// TestOrderUncoveredByChurnProximity_NoChurnFallsBackToLineOrder
// verifies that with no churn ranges, branches sort by ascending line.
func TestOrderUncoveredByChurnProximity_NoChurnFallsBackToLineOrder(t *testing.T) {
	t.Parallel()

	branches := []UncoveredBranch{
		{Line: 90}, {Line: 10}, {Line: 50},
	}
	got := OrderUncoveredByChurnProximity(branches, nil)
	if got[0].Line != 10 || got[1].Line != 50 || got[2].Line != 90 {
		t.Errorf("expected ascending line order, got %+v", got)
	}
}

// TestOrderUncoveredByChurnProximity_DoesNotMutateInput verifies the
// input slice is not reordered.
func TestOrderUncoveredByChurnProximity_DoesNotMutateInput(t *testing.T) {
	t.Parallel()

	branches := []UncoveredBranch{{Line: 90}, {Line: 10}}
	_ = OrderUncoveredByChurnProximity(branches, nil)
	if branches[0].Line != 90 {
		t.Errorf("input slice was mutated: %+v", branches)
	}
}
