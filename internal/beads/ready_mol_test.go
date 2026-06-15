package beads

import "testing"

// TestParseReadyMolOutput_Envelope verifies that the schema_version:1 envelope
// emitted by bd ready --mol --json (issues under .steps[].issue) is decoded
// correctly. Regression test for gs-nw6w.
func TestParseReadyMolOutput_Envelope(t *testing.T) {
	out := []byte(`{
		"molecule_id": "gs-wisp-wam",
		"molecule_title": "mol-polecat-work",
		"ready_steps": 2,
		"schema_version": 1,
		"steps": [
			{"issue": {"id": "gs-wisp-upp", "title": "Load context"}},
			{"issue": {"id": "gs-wisp-wam", "title": "mol-polecat-work"}}
		],
		"total_steps": 8
	}`)

	issues, err := parseReadyMolOutput(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}
	if issues[0].ID != "gs-wisp-upp" || issues[1].ID != "gs-wisp-wam" {
		t.Errorf("unexpected issue ids: %q, %q", issues[0].ID, issues[1].ID)
	}
}

// TestParseReadyMolOutput_LegacyArray verifies that the legacy flat-array form
// is still accepted, so gt tolerates bd version drift in either direction.
func TestParseReadyMolOutput_LegacyArray(t *testing.T) {
	out := []byte(`[{"id": "gs-wisp-upp", "title": "Load context"}]`)

	issues, err := parseReadyMolOutput(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "gs-wisp-upp" {
		t.Fatalf("expected 1 issue gs-wisp-upp, got %+v", issues)
	}
}

// TestParseReadyMolOutput_EmptyEnvelope verifies an envelope with no ready
// steps decodes to an empty slice, not an error.
func TestParseReadyMolOutput_EmptyEnvelope(t *testing.T) {
	out := []byte(`{"schema_version": 1, "steps": [], "ready_steps": 0}`)

	issues, err := parseReadyMolOutput(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("expected 0 issues, got %d", len(issues))
	}
}
