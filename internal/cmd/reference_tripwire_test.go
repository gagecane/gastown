package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestIsNonDispatchableBead covers the dispatch-time filter (hq-9jeyo, ask 1):
// do-not-dispatch / pinned labels or issue_type=reference exclude a bead from
// dispatch; ordinary work is unaffected.
func TestIsNonDispatchableBead(t *testing.T) {
	cases := []struct {
		name string
		info beadStatusInfo
		want bool
	}{
		{"plain work", beadStatusInfo{Status: "open"}, false},
		{"unrelated label", beadStatusInfo{Status: "open", Labels: []string{"gt:rig", "p1"}}, false},
		{"do-not-dispatch", beadStatusInfo{Labels: []string{"do-not-dispatch"}}, true},
		{"pinned", beadStatusInfo{Labels: []string{"pinned"}}, true},
		{"reference type", beadStatusInfo{Type: "reference"}, true},
		{"reference type case-insensitive", beadStatusInfo{Type: "Reference"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNonDispatchableBead(tc.info); got != tc.want {
				t.Errorf("isNonDispatchableBead(%+v) = %v, want %v", tc.info, got, tc.want)
			}
		})
	}
}

// TestIsNonDispatchableIssue covers the gt done guard (hq-9jeyo, ask 2): the
// *beads.Issue form used to refuse closing a mis-hooked tripwire.
func TestIsNonDispatchableIssue(t *testing.T) {
	if isNonDispatchableIssue(nil) {
		t.Error("nil issue must not be flagged")
	}
	if isNonDispatchableIssue(&beads.Issue{Labels: []string{"p2"}}) {
		t.Error("ordinary work must not be flagged")
	}
	for _, lbl := range []string{"do-not-dispatch", "pinned"} {
		if !isNonDispatchableIssue(&beads.Issue{Labels: []string{lbl}}) {
			t.Errorf("label %q must flag the issue", lbl)
		}
	}
	if !isNonDispatchableIssue(&beads.Issue{Type: "reference"}) {
		t.Error("issue_type=reference must flag the issue")
	}
}

// TestIsReferenceTripwireBeadInfo covers the sling-ingestion guard (hq-9jeyo,
// ask 3): refusing to schedule prevents the sling-context + auto-convoy.
func TestIsReferenceTripwireBeadInfo(t *testing.T) {
	if isReferenceTripwireBeadInfo(nil) {
		t.Error("nil must not be flagged")
	}
	if isReferenceTripwireBeadInfo(&beadInfo{Status: "open", Labels: []string{"bug"}}) {
		t.Error("ordinary work must not be flagged")
	}
	if !isReferenceTripwireBeadInfo(&beadInfo{Labels: []string{"do-not-dispatch"}}) {
		t.Error("do-not-dispatch must be flagged")
	}
	if !isReferenceTripwireBeadInfo(&beadInfo{Labels: []string{"pinned"}}) {
		t.Error("pinned must be flagged")
	}
	if !isReferenceTripwireBeadInfo(&beadInfo{IssueType: "reference"}) {
		t.Error("issue_type=reference must be flagged")
	}
}
