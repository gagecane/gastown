package cmd

import "testing"

// TestIssueDetailsJSON_NotesRoundTrip verifies that a leg bead's notes flow
// through the bd-show JSON mapping into issueDetails. Synthesis relies on this
// to read each leg's persisted report from the rig DB regardless of which
// dir/worktree the leg polecat ran in (gu-drftd) — without the Notes field,
// dimensions that completed via `gt done --status DEFERRED` (writing to the
// shared rig dir) would be invisible to synthesis.
func TestIssueDetailsJSON_NotesRoundTrip(t *testing.T) {
	js := issueDetailsJSON{
		ID:     "gt-leg-abc",
		Title:  "Requirements Completeness",
		Status: "closed",
		Notes:  "# Review: requirements\n\n## Findings\nMissing success criteria.",
	}

	d := js.toIssueDetails()
	if d == nil {
		t.Fatal("toIssueDetails returned nil")
	}
	if d.Notes != js.Notes {
		t.Errorf("Notes = %q, want %q", d.Notes, js.Notes)
	}
}
