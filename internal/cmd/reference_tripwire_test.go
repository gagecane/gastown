package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestIsNonDispatchableLabelSet covers the canonical predicate (gs-0cj) that
// every dispatch-path guard now shares, including the convoy-feed candidate
// filter that previously let a labeled tripwire (lb-rtjr.13) reach a polecat.
func TestIsNonDispatchableLabelSet(t *testing.T) {
	cases := []struct {
		name      string
		issueType string
		labels    []string
		want      bool
	}{
		{"plain work", "task", []string{"p1"}, false},
		{"do-not-dispatch", "task", []string{"do-not-dispatch"}, true},
		{"pinned", "bug", []string{"pinned"}, true},
		{"reference type", "reference", nil, true},
		{"reference case-insensitive", "Reference", nil, true},
		// The exact lb-rtjr.13 tripwire that bypassed the convoy-feed selection.
		{"lb-rtjr.13 triple", "reference", []string{"do-not-dispatch", "pinned", "reference"}, true},
		{"empty", "", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNonDispatchableLabelSet(tc.issueType, tc.labels); got != tc.want {
				t.Errorf("isNonDispatchableLabelSet(%q,%v) = %v, want %v", tc.issueType, tc.labels, got, tc.want)
			}
		})
	}
}

// TestConvoyFeedExcludesTripwire proves the gs-0cj fix at the data shape the
// convoy-feed filter actually uses: a trackedIssueInfo carrying the
// do-not-dispatch/pinned/reference markers is recognized as non-dispatchable
// (and thus skipped) while ordinary tracked work is not.
func TestConvoyFeedExcludesTripwire(t *testing.T) {
	tripwire := trackedIssueInfo{
		ID:        "lb-rtjr.13",
		Status:    "open",
		IssueType: "reference",
		Labels:    []string{"do-not-dispatch", "pinned", "reference"},
	}
	if !isNonDispatchableLabelSet(tripwire.IssueType, tripwire.Labels) {
		t.Error("lb-rtjr.13 tripwire must be excluded from convoy-feed candidates")
	}
	work := trackedIssueInfo{ID: "lb-100", Status: "open", IssueType: "task", Labels: []string{"p1"}}
	if isNonDispatchableLabelSet(work.IssueType, work.Labels) {
		t.Error("ordinary tracked work must remain a feed candidate")
	}
}

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

// Note: the pure isReferenceTripwireBeadInfo predicate now lives in
// internal/dispatch (gu-y5z8d); its unit coverage is
// dispatch.TestIsReferenceTripwireBeadInfo.
