package reaper

import (
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// fakeScrubBeads implements ActiveMRScrubBeads for tests. It tracks
// UpdateAgentActiveMR calls so we can assert the scrubber wrote (or did not
// write) clears for each agent bead.
type fakeScrubBeads struct {
	agents     map[string]*beads.Issue
	issues     map[string]*beads.Issue
	listErr    error
	updateErrs map[string]error
	updates    map[string]string // id -> new active_mr value
}

func newFakeScrubBeads() *fakeScrubBeads {
	return &fakeScrubBeads{
		agents:     map[string]*beads.Issue{},
		issues:     map[string]*beads.Issue{},
		updateErrs: map[string]error{},
		updates:    map[string]string{},
	}
}

func (f *fakeScrubBeads) ListAgentBeads() (map[string]*beads.Issue, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.agents, nil
}

func (f *fakeScrubBeads) Show(id string) (*beads.Issue, error) {
	// Match the real Beads.Show contract: missing → ErrNotFound.
	if issue, ok := f.issues[id]; ok {
		return issue, nil
	}
	if issue, ok := f.agents[id]; ok {
		return issue, nil
	}
	return nil, beads.ErrNotFound
}

func (f *fakeScrubBeads) UpdateAgentActiveMR(id string, activeMR string) error {
	if err := f.updateErrs[id]; err != nil {
		return err
	}
	f.updates[id] = activeMR
	// Reflect the change back into agents so subsequent reads see it.
	if issue, ok := f.agents[id]; ok && issue != nil {
		fields := beads.ParseAgentFields(issue.Description)
		if fields == nil {
			fields = &beads.AgentFields{}
		}
		fields.ActiveMR = activeMR
		issue.Description = beads.FormatAgentDescription(issue.Title, fields)
	}
	return nil
}

// makeAgentBead builds an agent bead Issue with the given active_mr,
// last_source_issue, and cleanup_status fields populated.
func makeAgentBead(id, activeMR, lastSource, cleanup string) *beads.Issue {
	fields := &beads.AgentFields{
		RoleType:        "polecat",
		AgentState:      "idle",
		ActiveMR:        activeMR,
		LastSourceIssue: lastSource,
		CleanupStatus:   cleanup,
	}
	return &beads.Issue{
		ID:          id,
		Title:       "agent " + id,
		Type:        "agent",
		Status:      "open",
		Description: beads.FormatAgentDescription("agent "+id, fields),
	}
}

func TestScrubStaleActiveMR_ClearsTerminalRefs(t *testing.T) {
	f := newFakeScrubBeads()
	// Polecat with active_mr pointing at a closed MR whose source bead is
	// also closed: classic gu-dhqm scenario where work landed via a sibling
	// MR but our active_mr was never cleared. Should be cleared.
	f.agents["polecat-1"] = makeAgentBead("polecat-1", "wisp-merged", "src-closed", "clean")
	f.issues["wisp-merged"] = &beads.Issue{ID: "wisp-merged", Status: "closed"}
	f.issues["src-closed"] = &beads.Issue{ID: "src-closed", Status: "closed"}

	result, err := ScrubStaleActiveMR(f, false)
	if err != nil {
		t.Fatalf("ScrubStaleActiveMR returned error: %v", err)
	}
	if result.Cleared != 1 {
		t.Fatalf("Cleared = %d, want 1 (entries=%v)", result.Cleared, result.ClearedEntries)
	}
	if got := f.updates["polecat-1"]; got != "" {
		t.Fatalf("UpdateAgentActiveMR(polecat-1, %q), want empty string clear", got)
	}
	if result.PreservedWIP != 0 || result.StillPending != 0 {
		t.Fatalf("expected zero preserved/pending, got %+v", result)
	}
}

func TestScrubStaleActiveMR_PreservesHumanWIP(t *testing.T) {
	// Three flavors of preserved cleanup_status — each must keep its
	// active_mr set even though the assessment would otherwise clear it
	// (gc-eysed). cacr-wisp-50q is one such polecat in production.
	cases := []string{"has_uncommitted", "has_stash", "has_unpushed"}
	for _, cleanup := range cases {
		t.Run(cleanup, func(t *testing.T) {
			f := newFakeScrubBeads()
			f.agents["wip-polecat"] = makeAgentBead("wip-polecat", "wisp-orphan", "src-closed", cleanup)
			f.issues["wisp-orphan"] = &beads.Issue{ID: "wisp-orphan", Status: "closed"}
			f.issues["src-closed"] = &beads.Issue{ID: "src-closed", Status: "closed"}

			result, err := ScrubStaleActiveMR(f, false)
			if err != nil {
				t.Fatalf("ScrubStaleActiveMR returned error: %v", err)
			}
			if result.Cleared != 0 {
				t.Fatalf("Cleared = %d, want 0 (WIP polecat must be preserved)", result.Cleared)
			}
			if result.PreservedWIP != 1 {
				t.Fatalf("PreservedWIP = %d, want 1", result.PreservedWIP)
			}
			if _, updated := f.updates["wip-polecat"]; updated {
				t.Fatalf("UpdateAgentActiveMR was called on a WIP polecat (gc-eysed violation)")
			}
		})
	}
}

func TestScrubStaleActiveMR_LeavesPendingMRsAlone(t *testing.T) {
	f := newFakeScrubBeads()
	// active_mr points at a still-open MR — the work hasn't merged yet, so
	// active_mr is doing its job and must stay.
	f.agents["live-polecat"] = makeAgentBead("live-polecat", "wisp-open", "src-open", "clean")
	f.issues["wisp-open"] = &beads.Issue{ID: "wisp-open", Status: "open"}
	f.issues["src-open"] = &beads.Issue{ID: "src-open", Status: "open"}

	result, err := ScrubStaleActiveMR(f, false)
	if err != nil {
		t.Fatalf("ScrubStaleActiveMR returned error: %v", err)
	}
	if result.Cleared != 0 {
		t.Fatalf("Cleared = %d, want 0 (open MR must be preserved)", result.Cleared)
	}
	if result.StillPending != 1 {
		t.Fatalf("StillPending = %d, want 1", result.StillPending)
	}
	if _, updated := f.updates["live-polecat"]; updated {
		t.Fatalf("UpdateAgentActiveMR was called on a live MR")
	}
}

func TestScrubStaleActiveMR_LeavesPendingWhenSourceStillOpen(t *testing.T) {
	f := newFakeScrubBeads()
	// MR is reaped (missing) but source issue is still open: AssessActiveMR
	// fails closed (Pending=true) because the source hasn't terminated. The
	// scrubber must respect that.
	f.agents["sibling-polecat"] = makeAgentBead("sibling-polecat", "wisp-reaped", "src-open", "clean")
	// wisp-reaped intentionally absent → ErrNotFound
	f.issues["src-open"] = &beads.Issue{ID: "src-open", Status: "open"}

	result, err := ScrubStaleActiveMR(f, false)
	if err != nil {
		t.Fatalf("ScrubStaleActiveMR returned error: %v", err)
	}
	if result.Cleared != 0 {
		t.Fatalf("Cleared = %d, want 0 (open source must keep active_mr)", result.Cleared)
	}
	if result.StillPending != 1 {
		t.Fatalf("StillPending = %d, want 1 (got %+v)", result.StillPending, result)
	}
}

func TestScrubStaleActiveMR_DryRunDoesNotMutate(t *testing.T) {
	f := newFakeScrubBeads()
	f.agents["polecat-dry"] = makeAgentBead("polecat-dry", "wisp-merged", "src-closed", "clean")
	f.issues["wisp-merged"] = &beads.Issue{ID: "wisp-merged", Status: "closed"}
	f.issues["src-closed"] = &beads.Issue{ID: "src-closed", Status: "closed"}

	result, err := ScrubStaleActiveMR(f, true)
	if err != nil {
		t.Fatalf("ScrubStaleActiveMR returned error: %v", err)
	}
	if !result.DryRun {
		t.Fatal("DryRun flag not propagated")
	}
	if result.Cleared != 1 {
		t.Fatalf("Cleared = %d, want 1 (dry run still counts)", result.Cleared)
	}
	if len(f.updates) != 0 {
		t.Fatalf("dry run wrote updates: %v", f.updates)
	}
}

func TestScrubStaleActiveMR_TolerantOfPerBeadFailures(t *testing.T) {
	f := newFakeScrubBeads()
	// One clearable bead that fails to update; one clearable that succeeds.
	f.agents["bead-bad"] = makeAgentBead("bead-bad", "wisp-merged-1", "src-closed", "clean")
	f.agents["bead-good"] = makeAgentBead("bead-good", "wisp-merged-2", "src-closed", "clean")
	f.issues["wisp-merged-1"] = &beads.Issue{ID: "wisp-merged-1", Status: "closed"}
	f.issues["wisp-merged-2"] = &beads.Issue{ID: "wisp-merged-2", Status: "closed"}
	f.issues["src-closed"] = &beads.Issue{ID: "src-closed", Status: "closed"}
	f.updateErrs["bead-bad"] = errors.New("dolt explosion")

	result, err := ScrubStaleActiveMR(f, false)
	if err != nil {
		t.Fatalf("ScrubStaleActiveMR returned error: %v", err)
	}
	if result.Cleared != 1 {
		t.Fatalf("Cleared = %d, want 1 (one succeeded)", result.Cleared)
	}
	if len(result.Anomalies) != 1 {
		t.Fatalf("Anomalies = %d, want 1 (one failed); got %v", len(result.Anomalies), result.Anomalies)
	}
	if result.Anomalies[0].Type != "active_mr_clear_failed" {
		t.Fatalf("Anomaly type = %q, want active_mr_clear_failed", result.Anomalies[0].Type)
	}
}

func TestScrubStaleActiveMR_ListErrorPropagates(t *testing.T) {
	f := newFakeScrubBeads()
	f.listErr = errors.New("dolt down")
	if _, err := ScrubStaleActiveMR(f, false); err == nil {
		t.Fatal("expected error when ListAgentBeads fails")
	}
}

func TestScrubStaleActiveMR_NilClient(t *testing.T) {
	if _, err := ScrubStaleActiveMR(nil, false); err == nil {
		t.Fatal("expected error for nil client")
	}
}

func TestScrubStaleActiveMR_SkipsBeadsWithoutActiveMR(t *testing.T) {
	f := newFakeScrubBeads()
	f.agents["no-mr"] = makeAgentBead("no-mr", "", "", "clean")
	f.agents["mr-set"] = makeAgentBead("mr-set", "wisp-x", "src-closed", "clean")
	f.issues["wisp-x"] = &beads.Issue{ID: "wisp-x", Status: "closed"}
	f.issues["src-closed"] = &beads.Issue{ID: "src-closed", Status: "closed"}

	result, err := ScrubStaleActiveMR(f, false)
	if err != nil {
		t.Fatalf("ScrubStaleActiveMR: %v", err)
	}
	if result.Scanned != 2 {
		t.Fatalf("Scanned = %d, want 2", result.Scanned)
	}
	if result.HadActiveMR != 1 {
		t.Fatalf("HadActiveMR = %d, want 1", result.HadActiveMR)
	}
	if result.Cleared != 1 {
		t.Fatalf("Cleared = %d, want 1", result.Cleared)
	}
}

func TestIsPolecatPreservingHumanWIP(t *testing.T) {
	cases := []struct {
		cleanup string
		want    bool
	}{
		{"", false},
		{"clean", false},
		{"unknown", false},
		{"has_uncommitted", true},
		{"has_stash", true},
		{"has_unpushed", true},
	}
	for _, tc := range cases {
		got := isPolecatPreservingHumanWIP(&beads.AgentFields{CleanupStatus: tc.cleanup})
		if got != tc.want {
			t.Errorf("isPolecatPreservingHumanWIP(cleanup=%q) = %v, want %v", tc.cleanup, got, tc.want)
		}
	}
	if isPolecatPreservingHumanWIP(nil) {
		t.Error("isPolecatPreservingHumanWIP(nil) = true, want false")
	}
}

func TestAgentSourceIssueHint(t *testing.T) {
	cases := []struct {
		name string
		in   *beads.AgentFields
		want string
	}{
		{"nil fields", nil, ""},
		{"empty fields", &beads.AgentFields{}, ""},
		{"prefers last_source_issue", &beads.AgentFields{LastSourceIssue: "src-1", HookBead: "hook-1"}, "src-1"},
		{"falls back to hook_bead", &beads.AgentFields{HookBead: "hook-2"}, "hook-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentSourceIssueHint(tc.in); got != tc.want {
				t.Fatalf("agentSourceIssueHint = %q, want %q", got, tc.want)
			}
		})
	}
}

// Sanity: the entries returned in a successful scrub include the MR status
// and resolved source issue so operators can correlate cleared refs with
// the merged sibling.
func TestScrubStaleActiveMR_ClearedEntriesHaveContext(t *testing.T) {
	f := newFakeScrubBeads()
	f.agents["polecat-x"] = makeAgentBead("polecat-x", "wisp-y", "src-z", "clean")
	f.issues["wisp-y"] = &beads.Issue{ID: "wisp-y", Status: "closed"}
	f.issues["src-z"] = &beads.Issue{ID: "src-z", Status: "closed"}

	result, err := ScrubStaleActiveMR(f, false)
	if err != nil {
		t.Fatalf("ScrubStaleActiveMR: %v", err)
	}
	if len(result.ClearedEntries) != 1 {
		t.Fatalf("ClearedEntries len = %d, want 1", len(result.ClearedEntries))
	}
	got := result.ClearedEntries[0]
	want := ScrubActiveMREntry{
		AgentBeadID: "polecat-x",
		ActiveMR:    "wisp-y",
		MRStatus:    "closed",
		SourceIssue: "src-z",
	}
	if got != want {
		t.Fatalf("ClearedEntries[0] = %+v, want %+v", got, want)
	}
}

// Compile-time guard: keep ActiveMRScrubBeads narrow enough that we can
// fmt.Stringer it for free if a future debug print needs it.
var _ = func() string { return fmt.Sprintf("%T", (*fakeScrubBeads)(nil)) }
