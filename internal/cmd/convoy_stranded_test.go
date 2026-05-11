package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// TestIsDeferUntilInFuture covers the defer_until parser used by isReadyIssue
// to hide deferred beads from stranded-scan dispatch (gu-vty0). We accept a
// couple of layouts because Dolt's JSON serialization has historically
// alternated between "2006-01-02 15:04:05" and RFC3339.
func TestIsDeferUntilInFuture(t *testing.T) {
	future := time.Now().Add(24 * time.Hour)
	past := time.Now().Add(-24 * time.Hour)

	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "empty string is not deferred", raw: "", want: false},
		{name: "whitespace only is not deferred", raw: "   ", want: false},
		{name: "malformed string is not deferred", raw: "not-a-date", want: false},
		{name: "rfc3339 future", raw: future.UTC().Format(time.RFC3339), want: true},
		{name: "rfc3339 past", raw: past.UTC().Format(time.RFC3339), want: false},
		{name: "mysql-style future", raw: future.UTC().Format("2006-01-02 15:04:05"), want: true},
		{name: "mysql-style past", raw: past.UTC().Format("2006-01-02 15:04:05"), want: false},
		{name: "rfc3339nano future", raw: future.UTC().Format(time.RFC3339Nano), want: true},
		{name: "t-separated no-zone future", raw: future.UTC().Format("2006-01-02T15:04:05"), want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDeferUntilInFuture(tc.raw); got != tc.want {
				t.Fatalf("isDeferUntilInFuture(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestIsReadyIssue_RespectsDeferUntil verifies that the stranded-scan
// readiness check hides beads whose defer_until sits in the future, and that
// it continues to recover beads whose defer_until has elapsed. This is the
// core guard against the gu-vty0 spawn-storm: a polecat that exits with
// --status DEFERRED sets defer_until = now + 24h on its hooked bead, and the
// next convoy scan must not rehook that bead until the window expires.
func TestIsReadyIssue_RespectsDeferUntil(t *testing.T) {
	tests := []struct {
		name string
		in   trackedIssueInfo
		want bool
	}{
		{
			name: "open+deferred-future hides bead",
			in: trackedIssueInfo{
				Status:     "open",
				DeferUntil: time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
			},
			want: false,
		},
		{
			name: "in_progress+deferred-future hides bead",
			in: trackedIssueInfo{
				Status:     "in_progress",
				DeferUntil: time.Now().Add(6 * time.Hour).UTC().Format(time.RFC3339),
			},
			want: false,
		},
		{
			name: "hooked+deferred-future hides bead",
			in: trackedIssueInfo{
				Status:     "hooked",
				DeferUntil: time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339),
			},
			want: false,
		},
		{
			name: "deferred-in-past does not hide (window expired)",
			in: trackedIssueInfo{
				Status:     "open",
				DeferUntil: time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
			},
			want: true,
		},
		{
			name: "malformed defer_until falls back to normal readiness",
			in: trackedIssueInfo{
				Status:     "open",
				DeferUntil: "not-a-date",
			},
			want: true,
		},
		{
			name: "empty defer_until treated as not deferred",
			in: trackedIssueInfo{
				Status:     "open",
				DeferUntil: "",
			},
			want: true,
		},
		{
			name: "deferred trumps blocker check short-circuit",
			in: trackedIssueInfo{
				Status:     "open",
				Blocked:    false,
				DeferUntil: time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isReadyIssue(tc.in, nil); got != tc.want {
				t.Fatalf("isReadyIssue(defer_until=%q, status=%q) = %v, want %v",
					tc.in.DeferUntil, tc.in.Status, got, tc.want)
			}
		})
	}
}

// TestApplyFreshIssueDetails_PropagatesDeferUntil covers the plumbing from
// issueDetails -> trackedDependency so isReadyIssue actually sees defer_until
// populated from bd show output. Without this, the stranded scan would still
// pick up deferred beads despite the convoy-level guard.
func TestApplyFreshIssueDetails_PropagatesDeferUntil(t *testing.T) {
	dep := trackedDependency{ID: "gt-123", Status: "open"}
	details := &issueDetails{
		ID:         "gt-123",
		Status:     "in_progress",
		DeferUntil: "2030-01-01T00:00:00Z",
	}

	applyFreshIssueDetails(&dep, details)

	if dep.DeferUntil != details.DeferUntil {
		t.Fatalf("DeferUntil not propagated: dep=%q details=%q", dep.DeferUntil, details.DeferUntil)
	}
}

// TestIssueDetailsJSON_DeferUntilUnmarshal ensures bd show's defer_until field
// flows end-to-end: JSON -> issueDetailsJSON -> issueDetails. A regression
// here silently re-enables the spawn storm.
func TestIssueDetailsJSON_DeferUntilUnmarshal(t *testing.T) {
	raw := `{"id":"gt-vty","status":"in_progress","defer_until":"2026-08-04T18:51:20Z"}`
	var parsed issueDetailsJSON
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.DeferUntil != "2026-08-04T18:51:20Z" {
		t.Fatalf("DeferUntil in JSON = %q, want 2026-08-04T18:51:20Z", parsed.DeferUntil)
	}
	details := parsed.toIssueDetails()
	if details.DeferUntil != parsed.DeferUntil {
		t.Fatalf("DeferUntil lost in toIssueDetails(): got %q, want %q", details.DeferUntil, parsed.DeferUntil)
	}
}
