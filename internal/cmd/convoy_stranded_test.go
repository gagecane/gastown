package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsReadyIssue_BlockingAndStatus(t *testing.T) {
	tests := []struct {
		name string
		in   trackedIssueInfo
		want bool
	}{
		{
			name: "closed issue never ready",
			in: trackedIssueInfo{
				Status:  "closed",
				Blocked: false,
			},
			want: false,
		},
		{
			name: "blocked open issue not ready",
			in: trackedIssueInfo{
				Status:  "open",
				Blocked: true,
			},
			want: false,
		},
		{
			name: "open unassigned issue ready",
			in: trackedIssueInfo{
				Status:  "open",
				Blocked: false,
			},
			want: true,
		},
		{
			name: "non-open unassigned issue treated ready for recovery",
			in: trackedIssueInfo{
				Status:  "in_progress",
				Blocked: false,
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isReadyIssue(tc.in, nil)
			if got != tc.want {
				t.Fatalf("isReadyIssue() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRelayWriteConvoy verifies which convoys are treated as shared-branch
// relays for single-writer serialization (gs-9ct #2). Only a named base branch
// pushed by its legs (merge local/direct) contends; merge=mr legs each use
// their own polecat/* branch.
func TestRelayWriteConvoy(t *testing.T) {
	cases := []struct {
		name       string
		baseBranch string
		merge      string
		want       bool
	}{
		{"local relay", "proto/v3-build", "local", true},
		{"direct relay", "proto/v3-build", "direct", true},
		{"mr is not a relay-write", "proto/v3-build", "mr", false},
		{"no base is not a relay-write", "", "local", false},
		{"default (no base, no merge)", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := relayWriteConvoy(tc.baseBranch, tc.merge); got != tc.want {
				t.Errorf("relayWriteConvoy(%q,%q) = %v, want %v", tc.baseBranch, tc.merge, got, tc.want)
			}
		})
	}
}

// TestHasLiveRelayWriter_NoLiveSessions verifies the negative cases of the
// single-writer probe: an unassigned in-flight bead, an open/closed bead, and
// an empty set all report "no live writer" (so dispatch proceeds). The
// positive case requires a real tmux session and is exercised by integration
// coverage; the probe fails open (see hasLiveRelayWriter doc).
func TestHasLiveRelayWriter_NoLiveSessions(t *testing.T) {
	cases := []struct {
		name    string
		tracked []trackedIssueInfo
	}{
		{"empty", nil},
		{"in_progress but unassigned", []trackedIssueInfo{{Status: "in_progress", Assignee: ""}}},
		{"open with assignee is not a writer", []trackedIssueInfo{{Status: "open", Assignee: "gastown/polecats/goose"}}},
		{"closed with assignee is not a writer", []trackedIssueInfo{{Status: "closed", Assignee: "gastown/polecats/goose"}}},
		// Assignee whose tmux session does not exist in the test env ⇒ not live.
		{"in_progress with dead session", []trackedIssueInfo{{Status: "in_progress", Assignee: "gastown/polecats/ghost-xyz-nonexistent"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if hasLiveRelayWriter(tc.tracked) {
				t.Errorf("hasLiveRelayWriter(%+v) = true, want false", tc.tracked)
			}
		})
	}
}

func TestApplyFreshIssueDetails_SetsBlockedFlag(t *testing.T) {
	dep := trackedDependency{
		ID:     "gt-123",
		Status: "open",
	}
	details := &issueDetails{
		ID:             "gt-123",
		Status:         "open",
		BlockedByCount: 1,
	}

	applyFreshIssueDetails(&dep, details)

	if !dep.Blocked {
		t.Fatalf("applyFreshIssueDetails() should set Blocked=true when details are blocked")
	}
}

func TestIssueDetailsIsBlocked(t *testing.T) {
	tests := []struct {
		name string
		in   issueDetails
		want bool
	}{
		{
			name: "blocked_by_count marks blocked",
			in: issueDetails{
				BlockedByCount: 2,
			},
			want: true,
		},
		{
			name: "blocked_by list marks blocked",
			in: issueDetails{
				BlockedBy: []string{"gt-1"},
			},
			want: true,
		},
		{
			name: "open blocks dependency marks blocked",
			in: issueDetails{
				Dependencies: []issueDependency{
					{DependencyType: "blocks", Status: "open"},
				},
			},
			want: true,
		},
		{
			name: "closed blocks dependency does not mark blocked",
			in: issueDetails{
				Dependencies: []issueDependency{
					{DependencyType: "blocks", Status: "closed"},
				},
			},
			want: false,
		},
		{
			name: "non-blocking dependency does not mark blocked",
			in: issueDetails{
				Dependencies: []issueDependency{
					{DependencyType: "parent-child", Status: "open"},
				},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.IsBlocked()
			if got != tc.want {
				t.Fatalf("IsBlocked() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsSlingableBead(t *testing.T) {
	// Set up a fake town root with routes.jsonl
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	routesContent := `{"prefix": "gt-", "path": "gastown/mayor/rig"}
{"prefix": "bd-", "path": "beads/mayor/rig"}
{"prefix": "hq-", "path": "."}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		beadID string
		want   bool
	}{
		{"rig bead is slingable", "gt-wisp-abc", true},
		{"another rig bead is slingable", "bd-wisp-xyz", true},
		{"town-level bead not slingable", "hq-wisp-abc", false},
		{"town-level convoy not slingable", "hq-cv-kl6ns", false},
		{"unknown prefix not slingable", "zz-wisp-abc", false},
		{"no prefix assumes slingable", "nohyphen", true},
		{"empty ID assumes slingable", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isSlingableBead(townRoot, tc.beadID)
			if got != tc.want {
				t.Fatalf("isSlingableBead(%q) = %v, want %v", tc.beadID, got, tc.want)
			}
		})
	}
}
