package cmd

import (
	"strings"
	"testing"
)

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

// TestSynthesisOutputPersistenceDirective verifies the synthesis-side Output
// Persistence directive (gu-drftd recurrence): the synthesis polecat must
// persist its full synthesized document to the synthesis bead's notes so it is
// readable regardless of which worktree the polecat ran in. When a concrete
// bead ID is known (the upfront formula path) the bd command references it;
// otherwise (the manual/trigger path that parses the ID from `bd create`) it
// self-references.
func TestSynthesisOutputPersistenceDirective(t *testing.T) {
	withID := synthesisOutputPersistenceDirective("gt-syn-xyz")
	if !strings.Contains(withID, "Output Persistence (REQUIRED)") {
		t.Errorf("directive missing required header: %q", withID)
	}
	if !strings.Contains(withID, "bd update gt-syn-xyz --notes") {
		t.Errorf("directive should reference the concrete bead ID: %q", withID)
	}

	withoutID := synthesisOutputPersistenceDirective("")
	if !strings.Contains(withoutID, "bd update <this-bead-id> --notes") {
		t.Errorf("directive should self-reference when bead ID unknown: %q", withoutID)
	}
}
