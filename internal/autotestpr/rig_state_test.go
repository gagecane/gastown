// Tests for the per-rig auto-test-pr state bead model (Phase 0
// task 8, gu-l6xu).
package autotestpr

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRigStateBeadID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		rig  string
		want string
	}{
		{"gastown_upstream", "gastown_upstream-auto-test-state"},
		{"casc_crud", "casc_crud-auto-test-state"},
	}
	for _, tt := range tests {
		if got := RigStateBeadID(tt.rig); got != tt.want {
			t.Errorf("RigStateBeadID(%q) = %q; want %q", tt.rig, got, tt.want)
		}
	}
}

func TestDefaultRigState_SchemaAndIdleState(t *testing.T) {
	t.Parallel()

	s := DefaultRigState()
	if s.SchemaVersion != RigStateSchemaVersion {
		t.Errorf("SchemaVersion = %d; want %d", s.SchemaVersion, RigStateSchemaVersion)
	}
	if s.State != PerRigCycleStateIdle {
		t.Errorf("State = %q; want %q", s.State, PerRigCycleStateIdle)
	}
	if s.CurrentCycle != nil {
		t.Errorf("CurrentCycle = %+v; want nil", s.CurrentCycle)
	}
	if s.LastCycleAt != "" {
		t.Errorf("LastCycleAt = %q; want empty", s.LastCycleAt)
	}
	if s.LastCycleOutcome != "" {
		t.Errorf("LastCycleOutcome = %q; want empty", s.LastCycleOutcome)
	}
	if s.PausedUntil != "" {
		t.Errorf("PausedUntil = %q; want empty", s.PausedUntil)
	}
	if s.Incidents != nil {
		t.Errorf("Incidents = %v; want nil", s.Incidents)
	}
}

func TestDefaultRigState_MetadataDoesNotContainTransitionOrRejectionLog(t *testing.T) {
	t.Parallel()

	// Acceptance criterion: parent state bead's Issue.Metadata
	// post-cycle does NOT contain transition_log[] or rejection_log[] keys.
	s := DefaultRigState()
	raw, err := s.MarshalMetadata()
	if err != nil {
		t.Fatalf("MarshalMetadata: %v", err)
	}

	got := string(raw)
	if strings.Contains(got, "transition_log") {
		t.Errorf("RigState metadata contains 'transition_log': %s", got)
	}
	if strings.Contains(got, "rejection_log") {
		t.Errorf("RigState metadata contains 'rejection_log': %s", got)
	}
}

func TestRigState_MarshalRoundTrip(t *testing.T) {
	t.Parallel()

	original := RigState{
		SchemaVersion:    RigStateSchemaVersion,
		State:            PerRigCycleStateMRPending,
		LastCycleAt:      "2026-05-20T10:00:00Z",
		LastCycleOutcome: "merged",
		CurrentCycle: &CurrentCycle{
			CycleID:     "gu-cycle-abc",
			StartedAt:   "2026-05-21T12:00:00Z",
			PolecatBead: "gu-leg-xyz",
			MRBead:      "gu-mr-abc",
			Branch:      "auto-test/gastown_upstream/gu-cycle-abc",
		},
		PausedUntil: "",
		Incidents: []Incident{
			{
				At:    "2026-05-20T09:00:00Z",
				Actor: "mayor/",
				Kind:  IncidentRigPause,
				Rig:   "gastown_upstream",
			},
		},
	}

	raw, err := original.MarshalMetadata()
	if err != nil {
		t.Fatalf("MarshalMetadata: %v", err)
	}

	parsed, err := UnmarshalRigState(raw)
	if err != nil {
		t.Fatalf("UnmarshalRigState: %v", err)
	}

	if parsed.SchemaVersion != original.SchemaVersion {
		t.Errorf("SchemaVersion round-trip: %d vs %d", parsed.SchemaVersion, original.SchemaVersion)
	}
	if parsed.State != original.State {
		t.Errorf("State round-trip: %q vs %q", parsed.State, original.State)
	}
	if parsed.LastCycleAt != original.LastCycleAt {
		t.Errorf("LastCycleAt round-trip: %q vs %q", parsed.LastCycleAt, original.LastCycleAt)
	}
	if parsed.LastCycleOutcome != original.LastCycleOutcome {
		t.Errorf("LastCycleOutcome round-trip: %q vs %q", parsed.LastCycleOutcome, original.LastCycleOutcome)
	}
	if parsed.CurrentCycle == nil {
		t.Fatal("CurrentCycle is nil after round-trip")
	}
	if parsed.CurrentCycle.CycleID != original.CurrentCycle.CycleID {
		t.Errorf("CurrentCycle.CycleID round-trip: %q vs %q",
			parsed.CurrentCycle.CycleID, original.CurrentCycle.CycleID)
	}
	if parsed.CurrentCycle.Branch != original.CurrentCycle.Branch {
		t.Errorf("CurrentCycle.Branch round-trip: %q vs %q",
			parsed.CurrentCycle.Branch, original.CurrentCycle.Branch)
	}
	if len(parsed.Incidents) != 1 {
		t.Fatalf("Incidents len = %d; want 1", len(parsed.Incidents))
	}
	if parsed.Incidents[0].Kind != IncidentRigPause {
		t.Errorf("Incidents[0].Kind = %q; want %q", parsed.Incidents[0].Kind, IncidentRigPause)
	}
}

func TestUnmarshalRigState_EmptyAndNull(t *testing.T) {
	t.Parallel()

	// Empty bytes → zero value.
	s, err := UnmarshalRigState(nil)
	if err != nil {
		t.Fatalf("UnmarshalRigState(nil): %v", err)
	}
	if s.SchemaVersion != 0 {
		t.Errorf("nil → SchemaVersion = %d; want 0", s.SchemaVersion)
	}

	// "null" string → zero value.
	s, err = UnmarshalRigState(json.RawMessage("null"))
	if err != nil {
		t.Fatalf("UnmarshalRigState(null): %v", err)
	}
	if s.State != "" {
		t.Errorf("null → State = %q; want empty", s.State)
	}
}

func TestRigState_NullCurrentCycle(t *testing.T) {
	t.Parallel()

	s := DefaultRigState()
	raw, err := s.MarshalMetadata()
	if err != nil {
		t.Fatalf("MarshalMetadata: %v", err)
	}

	// Should serialize current_cycle as null per the design.
	if !strings.Contains(string(raw), `"current_cycle":null`) {
		t.Errorf("expected current_cycle:null in JSON, got: %s", raw)
	}
}
