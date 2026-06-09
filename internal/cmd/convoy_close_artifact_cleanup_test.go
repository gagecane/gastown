package cmd

import (
	"testing"
)

// TestIsAutoCreatedConvoy verifies the description-marker detection used by
// `gt convoy close` to recognize auto-dispatched tracking artifacts that
// should be eligible for cleanup without --force (gu-hllyx).
func TestIsAutoCreatedConvoy(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want bool
	}{
		{
			name: "single-bead auto-convoy marker",
			desc: "Auto-created convoy tracking ta-1h5b\nMerge: mr",
			want: true,
		},
		{
			name: "batch auto-convoy marker",
			desc: "Auto-created convoy tracking 5 beads\nMerge: direct",
			want: true,
		},
		{
			name: "marker with trailing convoy fields appended",
			desc: "Auto-created convoy tracking gt-abc\nNotify: mayor/\nMerge: direct",
			want: true,
		},
		{
			name: "human-created convoy lacks the marker",
			desc: "Convoy tracking 3 issues\nOwner: mayor/\nMerge: mr",
			want: false,
		},
		{
			name: "empty description",
			desc: "",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAutoCreatedConvoy(tt.desc); got != tt.want {
				t.Errorf("isAutoCreatedConvoy(%q) = %v, want %v", tt.desc, got, tt.want)
			}
		})
	}
}

// TestIsNonWorkIssueType locks down the set of bead types that the close-
// without-force gate treats as non-work containers (gu-hllyx). Adding a new
// non-work type requires updating this test as well.
func TestIsNonWorkIssueType(t *testing.T) {
	nonWork := []string{"epic", "convoy", "molecule"}
	for _, typ := range nonWork {
		if !isNonWorkIssueType(typ) {
			t.Errorf("isNonWorkIssueType(%q) = false, want true", typ)
		}
	}

	work := []string{"task", "bug", "feature", "chore", "", "reference"}
	for _, typ := range work {
		if isNonWorkIssueType(typ) {
			t.Errorf("isNonWorkIssueType(%q) = true, want false", typ)
		}
	}
}

// TestAllNonWorkOpenIssues verifies the gate predicate that decides whether
// auto-convoy close can skip --force: every open tracked issue must be a
// non-work container, and an empty list does NOT enable the cleanup path
// (the caller has already short-circuited that case).
func TestAllNonWorkOpenIssues(t *testing.T) {
	tests := []struct {
		name string
		open []trackedIssueInfo
		want bool
	}{
		{
			name: "empty input returns false",
			open: nil,
			want: false,
		},
		{
			name: "single epic open",
			open: []trackedIssueInfo{{ID: "ta-1h5b", IssueType: "epic"}},
			want: true,
		},
		{
			name: "all non-work types",
			open: []trackedIssueInfo{
				{ID: "ta-1", IssueType: "epic"},
				{ID: "hq-cv-2", IssueType: "convoy"},
				{ID: "wisp-3", IssueType: "molecule"},
			},
			want: true,
		},
		{
			name: "single work bead blocks",
			open: []trackedIssueInfo{
				{ID: "ta-1", IssueType: "epic"},
				{ID: "gt-2", IssueType: "task"},
			},
			want: false,
		},
		{
			name: "unknown type counts as work (conservative)",
			open: []trackedIssueInfo{{ID: "x", IssueType: ""}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := allNonWorkOpenIssues(tt.open); got != tt.want {
				t.Errorf("allNonWorkOpenIssues(%+v) = %v, want %v", tt.open, got, tt.want)
			}
		})
	}
}

// TestSummarizeOpenIssueTypes verifies the human-readable summary used in the
// artifact-cleanup log line.
func TestSummarizeOpenIssueTypes(t *testing.T) {
	open := []trackedIssueInfo{
		{ID: "ta-1h5b", IssueType: "epic"},
		{ID: "hq-cv-abc", IssueType: "convoy"},
		{ID: "x", IssueType: ""},
	}
	got := summarizeOpenIssueTypes(open)
	want := "ta-1h5b:epic, hq-cv-abc:convoy, x:unknown"
	if got != want {
		t.Errorf("summarizeOpenIssueTypes() = %q, want %q", got, want)
	}
}

// TestArtifactCleanupGate covers the combined predicate that runConvoyClose
// uses to decide whether an open-tracked-issue close needs --force. This is
// the regression-prevention test for gu-hllyx: an auto-convoy tracking only
// an epic (legitimately still open) should NOT require --force, while a
// human-created convoy or one tracking real work should.
func TestArtifactCleanupGate(t *testing.T) {
	tests := []struct {
		name           string
		description    string
		openIssues     []trackedIssueInfo
		wantCleanupOk  bool // true if --force should be unnecessary
	}{
		{
			name:        "auto-convoy tracking only an epic — cleanup ok",
			description: "Auto-created convoy tracking ta-1h5b\nMerge: mr",
			openIssues: []trackedIssueInfo{
				{ID: "ta-1h5b", IssueType: "epic", Status: "open"},
			},
			wantCleanupOk: true,
		},
		{
			name:        "auto-convoy tracking a real task — force required",
			description: "Auto-created convoy tracking gt-abc\nMerge: mr",
			openIssues: []trackedIssueInfo{
				{ID: "gt-abc", IssueType: "task", Status: "in_progress"},
			},
			wantCleanupOk: false,
		},
		{
			name:        "human-created convoy with open epic — force still required",
			description: "Convoy tracking 1 issue\nOwner: mayor/\nMerge: mr",
			openIssues: []trackedIssueInfo{
				{ID: "ta-1h5b", IssueType: "epic", Status: "open"},
			},
			wantCleanupOk: false,
		},
		{
			name:        "auto-convoy with mixed open issues — force required",
			description: "Auto-created convoy tracking 3 beads\nMerge: direct",
			openIssues: []trackedIssueInfo{
				{ID: "ta-1", IssueType: "epic"},
				{ID: "gt-2", IssueType: "task"},
			},
			wantCleanupOk: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAutoCreatedConvoy(tt.description) && allNonWorkOpenIssues(tt.openIssues)
			if got != tt.wantCleanupOk {
				t.Errorf("artifact-cleanup gate = %v, want %v\n  desc: %q\n  open: %+v",
					got, tt.wantCleanupOk, tt.description, tt.openIssues)
			}
		})
	}
}

