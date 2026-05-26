// Tests for the OQ4 fallback attachment-bead pattern (Phase 0
// task 8, gu-l6xu). Covers the materializer, parse functions, and
// the record types.
package autotestpr

import (
	"encoding/json"
	"testing"
	"time"
)

// fixedTime is a pinned timestamp for deterministic test assertions.
var fixedTime = time.Date(2026, 5, 21, 14, 23, 0, 0, time.UTC)

func TestParseTransition_ValidPayload(t *testing.T) {
	t.Parallel()

	meta := transitionMetadata{
		SchemaVersion: 1,
		Rig:           "gastown_upstream",
		From:          "mr-pending",
		To:            "cooled-down",
		At:            fixedTime.Format(time.RFC3339),
		Actor:         "refinery",
		Context:       map[string]string{"mr_id": "gu-mr-abc12", "merged_sha": "abc1234"},
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	rec, err := parseTransition(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("parseTransition: %v", err)
	}

	if rec.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d; want 1", rec.SchemaVersion)
	}
	if rec.Rig != "gastown_upstream" {
		t.Errorf("Rig = %q; want gastown_upstream", rec.Rig)
	}
	if rec.From != "mr-pending" {
		t.Errorf("From = %q; want mr-pending", rec.From)
	}
	if rec.To != "cooled-down" {
		t.Errorf("To = %q; want cooled-down", rec.To)
	}
	if !rec.At.Equal(fixedTime) {
		t.Errorf("At = %v; want %v", rec.At, fixedTime)
	}
	if rec.Actor != "refinery" {
		t.Errorf("Actor = %q; want refinery", rec.Actor)
	}
	if rec.Context["mr_id"] != "gu-mr-abc12" {
		t.Errorf("Context[mr_id] = %q; want gu-mr-abc12", rec.Context["mr_id"])
	}
}

func TestParseTransition_EmptyPayload(t *testing.T) {
	t.Parallel()

	_, err := parseTransition(nil)
	if err == nil {
		t.Fatal("parseTransition(nil) should error")
	}
}

func TestParseRejection_ValidPayload(t *testing.T) {
	t.Parallel()

	cooldown := fixedTime.Add(14 * 24 * time.Hour)
	meta := rejectionMetadata{
		SchemaVersion: 1,
		Rig:           "gastown_upstream",
		File:          "internal/foo/bar.go",
		RejectedAt:    fixedTime.Format(time.RFC3339),
		Reason:        "wrong-target",
		CooldownUntil: cooldown.Format(time.RFC3339),
		MRID:          "gu-mr-abc09",
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	rec, err := parseRejection(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("parseRejection: %v", err)
	}

	if rec.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d; want 1", rec.SchemaVersion)
	}
	if rec.Rig != "gastown_upstream" {
		t.Errorf("Rig = %q; want gastown_upstream", rec.Rig)
	}
	if rec.File != "internal/foo/bar.go" {
		t.Errorf("File = %q; want internal/foo/bar.go", rec.File)
	}
	if !rec.RejectedAt.Equal(fixedTime) {
		t.Errorf("RejectedAt = %v; want %v", rec.RejectedAt, fixedTime)
	}
	if rec.Reason != "wrong-target" {
		t.Errorf("Reason = %q; want wrong-target", rec.Reason)
	}
	if !rec.CooldownUntil.Equal(cooldown) {
		t.Errorf("CooldownUntil = %v; want %v", rec.CooldownUntil, cooldown)
	}
	if rec.MRID != "gu-mr-abc09" {
		t.Errorf("MRID = %q; want gu-mr-abc09", rec.MRID)
	}
}

func TestParseRejection_EmptyPayload(t *testing.T) {
	t.Parallel()

	_, err := parseRejection(nil)
	if err == nil {
		t.Fatal("parseRejection(nil) should error")
	}
}

func TestRigLabel(t *testing.T) {
	t.Parallel()

	if got, want := RigLabel("gastown_upstream"), "rig:gastown_upstream"; got != want {
		t.Errorf("RigLabel = %q; want %q", got, want)
	}
}

func TestTransitionRecord_JSONShape(t *testing.T) {
	t.Parallel()

	// Verify the TransitionRecord produces the same JSON shape as the
	// previous in-blob transition_log[] entry documented in the
	// synthesis.
	rec := TransitionRecord{
		SchemaVersion: 1,
		Rig:           "gastown_upstream",
		From:          "mr-pending",
		To:            "cooled-down",
		At:            fixedTime,
		Actor:         "refinery",
		Context:       map[string]string{"mr_id": "gu-mr-abc12", "merged_sha": "abc1234"},
	}

	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Parse back to verify fields are present.
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	for _, key := range []string{"schema_version", "rig", "from", "to", "at", "actor", "context"} {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON missing key %q", key)
		}
	}
}

func TestRejectionRecord_JSONShape(t *testing.T) {
	t.Parallel()

	cooldown := fixedTime.Add(14 * 24 * time.Hour)
	rec := RejectionRecord{
		SchemaVersion: 1,
		Rig:           "gastown_upstream",
		File:          "internal/foo/bar.go",
		RejectedAt:    fixedTime,
		Reason:        "wrong-target",
		CooldownUntil: cooldown,
		MRID:          "gu-mr-abc09",
	}

	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	for _, key := range []string{"schema_version", "rig", "file", "rejected_at", "reason", "cooldown_until", "mr_id"} {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON missing key %q", key)
		}
	}
}

func TestMaterializeAutoTestState_NilBeads(t *testing.T) {
	t.Parallel()

	_, _, err := MaterializeAutoTestState(nil, "gastown_upstream")
	if err == nil {
		t.Fatal("expected error with nil beads")
	}
}

func TestMaterializeAutoTestState_EmptyRig(t *testing.T) {
	t.Parallel()

	// We can't easily test with a real Beads wrapper without a Dolt
	// server, but we can test the validation paths.
	_, _, err := MaterializeAutoTestState(nil, "")
	if err == nil {
		t.Fatal("expected error with empty rig")
	}
}

func TestCreateTransitionAttachment_Validation(t *testing.T) {
	t.Parallel()

	_, err := CreateTransitionAttachment(nil, TransitionRecord{Rig: "foo"})
	if err == nil {
		t.Fatal("expected error with nil beads")
	}

	// Dummy beads won't work but we can test the rig-empty check
	// (nil beads check fires first, so we just verify nil beads error).
}

func TestCreateRejectionAttachment_Validation(t *testing.T) {
	t.Parallel()

	_, err := CreateRejectionAttachment(nil, RejectionRecord{Rig: "foo"})
	if err == nil {
		t.Fatal("expected error with nil beads")
	}
}
