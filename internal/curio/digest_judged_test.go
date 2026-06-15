package curio

import (
	"strings"
	"testing"
)

// TestExcludeJudged_DropsJudgedKeepsUnresolved proves gu-1whne lever B: a
// candidate whose fingerprint has a judged ledger outcome is dropped, while a
// candidate with no judged row (genuinely unresolved) survives.
func TestExcludeJudged_DropsJudgedKeepsUnresolved(t *testing.T) {
	resolved := newCandidate("w1", "alarm_rate_spike", "sling", "", "sling", 450, "RESOLVED-LONG-AGO")
	unresolved := newCandidate("w1", "kill_signal_near_dolt", "deacon#0", "", "dog.log.kill_signal", 1, "STILL-OPEN")

	judged := map[string]struct{}{resolved.Fingerprint: {}}

	got := ExcludeJudged([]Candidate{resolved, unresolved}, judged)

	if len(got) != 1 {
		t.Fatalf("ExcludeJudged kept %d candidates, want 1\ngot: %+v", len(got), got)
	}
	if got[0].Fingerprint != unresolved.Fingerprint {
		t.Errorf("surviving candidate = %+v, want the unresolved kill_signal finding", got[0])
	}
}

// TestExcludeJudged_EmptyInputsAndSet covers the boundary cases: nil input, and
// an empty judged set (nothing dropped).
func TestExcludeJudged_EmptyInputsAndSet(t *testing.T) {
	if got := ExcludeJudged(nil, map[string]struct{}{"x": {}}); len(got) != 0 {
		t.Errorf("nil input → %d candidates, want 0", len(got))
	}
	cands := []Candidate{
		newCandidate("w1", "r", "t", "", "s", 1, "keep me"),
	}
	if got := ExcludeJudged(cands, nil); len(got) != 1 {
		t.Errorf("empty judged set should drop nothing; got %d, want 1", len(got))
	}
	if got := ExcludeJudged(cands, map[string]struct{}{}); len(got) != 1 {
		t.Errorf("empty judged set should drop nothing; got %d, want 1", len(got))
	}
}

// TestRenderedDigestNeverShowsJudged wires the filter the way the caller does
// (ExcludeJudged before RenderDigest) and asserts the rendered artifact — the
// thing the agent actually reads — carries no already-judged cluster.
func TestRenderedDigestNeverShowsJudged(t *testing.T) {
	resolved := newCandidate("w1", "alarm_rate_spike", "sling", "", "sling", 450, "RESOLVED-MARKER")
	open := newCandidate("w1", "kill_signal_near_dolt", "deacon#0", "", "dog.log.kill_signal", 1, "OPEN-MARKER")

	judged := map[string]struct{}{resolved.Fingerprint: {}}
	digest := RenderDigest(fixedCutoff, ExcludeJudged([]Candidate{resolved, open}, judged), nil)

	if strings.Contains(digest, "RESOLVED-MARKER") {
		t.Errorf("rendered digest leaked already-judged candidate:\n%s", digest)
	}
	if !strings.Contains(digest, "OPEN-MARKER") {
		t.Errorf("genuinely-unresolved finding missing from digest:\n%s", digest)
	}
}

// TestJudgedOutcomeInList_SingleSourced asserts the SQL IN-list helper renders
// exactly the judged set (and excludes the unknown/unreconciled states), so the
// precision aggregate and the stale-candidate exclusion cannot drift apart.
func TestJudgedOutcomeInList_SingleSourced(t *testing.T) {
	list := judgedOutcomeInList()
	for _, want := range []string{OutcomeFixed, OutcomeFalsePositive, OutcomeDuplicate, OutcomeDeferred} {
		if !strings.Contains(list, "'"+want+"'") {
			t.Errorf("judgedOutcomeInList() = %q, missing %q", list, want)
		}
	}
	if strings.Contains(list, "'"+OutcomeUnknown+"'") {
		t.Errorf("judgedOutcomeInList() = %q must NOT include the unknown outcome", list)
	}
}
