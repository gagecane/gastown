package upstreamsync

import (
	"encoding/json"
	"testing"
)

func TestSyncState_IsValid(t *testing.T) {
	tests := []struct {
		state SyncState
		valid bool
	}{
		{StateIdle, true},
		{StateChecking, true},
		{StateSyncing, true},
		{StateResolving, true},
		{StateGating, true},
		{StatePushing, true},
		{StateFailed, true},
		{StatePaused, true},
		{SyncState("bogus"), false},
		{SyncState(""), false},
	}

	for _, tt := range tests {
		if got := tt.state.IsValid(); got != tt.valid {
			t.Errorf("SyncState(%q).IsValid() = %v, want %v", tt.state, got, tt.valid)
		}
	}
}

func TestDefaultSyncStateMetadata(t *testing.T) {
	s := DefaultSyncStateMetadata("gastown_upstream", "upstream", "main", "main")

	if s.SchemaVersion != StateBeadSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", s.SchemaVersion, StateBeadSchemaVersion)
	}
	if s.Rig != "gastown_upstream" {
		t.Errorf("Rig = %q, want %q", s.Rig, "gastown_upstream")
	}
	if s.State != StateIdle {
		t.Errorf("State = %q, want %q", s.State, StateIdle)
	}
	if s.UpstreamRemote != "upstream" {
		t.Errorf("UpstreamRemote = %q, want %q", s.UpstreamRemote, "upstream")
	}
	if s.UpstreamBranch != "main" {
		t.Errorf("UpstreamBranch = %q, want %q", s.UpstreamBranch, "main")
	}
	if s.TargetBranch != "main" {
		t.Errorf("TargetBranch = %q, want %q", s.TargetBranch, "main")
	}
	if s.CurrentAttempt != nil {
		t.Errorf("CurrentAttempt should be nil for default state")
	}
	if s.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d, want 0", s.ConsecutiveFailures)
	}
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	original := SyncStateMetadata{
		SchemaVersion:       1,
		Rig:                 "gastown_upstream",
		State:               StateSyncing,
		UpstreamRemote:      "upstream",
		UpstreamBranch:      "main",
		TargetBranch:        "main",
		LastSyncAt:          "2026-05-25T14:00:00Z",
		LastSyncOutcome:     "success",
		LastSyncSHA:         "abc1234def5678",
		ConsecutiveFailures: 0,
		CurrentAttempt: &CurrentAttempt{
			ID:          "gu-sync-att-002",
			StartedAt:   "2026-05-25T21:00:00Z",
			UpstreamSHA: "def5678abc1234",
			PreSyncSHA:  "abc1234def5678",
			Strategy:    "merge",
			Actor:       "polecat/dust",
		},
		Attempts: []SyncAttempt{
			{
				ID:          "gu-sync-att-001",
				StartedAt:   "2026-05-25T14:00:00Z",
				CompletedAt: "2026-05-25T14:02:30Z",
				Outcome:     "success",
				UpstreamSHA: "abc1234def5678",
				PreSyncSHA:  "999888777666",
				PostSyncSHA: "abc1234def5678",
				Strategy:    "fast-forward",
				GateResults: map[string]GateResult{
					"build": GatePass,
					"test":  GatePass,
					"vet":   GatePass,
				},
				Actor: "polecat/guzzle",
			},
		},
	}

	raw, err := original.MarshalMetadata()
	if err != nil {
		t.Fatalf("MarshalMetadata: %v", err)
	}

	parsed, err := UnmarshalSyncState(raw)
	if err != nil {
		t.Fatalf("UnmarshalSyncState: %v", err)
	}

	if parsed.SchemaVersion != original.SchemaVersion {
		t.Errorf("SchemaVersion roundtrip: got %d, want %d", parsed.SchemaVersion, original.SchemaVersion)
	}
	if parsed.State != original.State {
		t.Errorf("State roundtrip: got %q, want %q", parsed.State, original.State)
	}
	if parsed.Rig != original.Rig {
		t.Errorf("Rig roundtrip: got %q, want %q", parsed.Rig, original.Rig)
	}
	if parsed.LastSyncAt != original.LastSyncAt {
		t.Errorf("LastSyncAt roundtrip: got %q, want %q", parsed.LastSyncAt, original.LastSyncAt)
	}
	if parsed.CurrentAttempt == nil {
		t.Fatal("CurrentAttempt should not be nil after roundtrip")
	}
	if parsed.CurrentAttempt.ID != original.CurrentAttempt.ID {
		t.Errorf("CurrentAttempt.ID roundtrip: got %q, want %q",
			parsed.CurrentAttempt.ID, original.CurrentAttempt.ID)
	}
	if len(parsed.Attempts) != 1 {
		t.Fatalf("Attempts length: got %d, want 1", len(parsed.Attempts))
	}
	if parsed.Attempts[0].Outcome != "success" {
		t.Errorf("Attempts[0].Outcome: got %q, want %q", parsed.Attempts[0].Outcome, "success")
	}
}

func TestUnmarshalSyncState_EmptyInput(t *testing.T) {
	tests := []struct {
		name  string
		input json.RawMessage
	}{
		{"nil", nil},
		{"empty", json.RawMessage{}},
		{"null string", json.RawMessage("null")},
		{"empty string", json.RawMessage("")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := UnmarshalSyncState(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if s.SchemaVersion != 0 {
				t.Errorf("expected zero-value state, got SchemaVersion=%d", s.SchemaVersion)
			}
		})
	}
}

func TestStateBeadID(t *testing.T) {
	tests := []struct {
		prefix string
		want   string
	}{
		{"gu", "gu-upstream-sync-state"},
		{"za", "za-upstream-sync-state"},
		{"x", "x-upstream-sync-state"},
	}

	for _, tt := range tests {
		got := StateBeadID(tt.prefix)
		if got != tt.want {
			t.Errorf("StateBeadID(%q) = %q, want %q", tt.prefix, got, tt.want)
		}
	}
}

func TestToStatusSummary(t *testing.T) {
	s := SyncStateMetadata{
		SchemaVersion:       1,
		Rig:                 "gastown_upstream",
		State:               StateIdle,
		UpstreamRemote:      "upstream",
		UpstreamBranch:      "main",
		TargetBranch:        "main",
		LastSyncAt:          "2026-05-25T14:00:00Z",
		LastSyncOutcome:     "success",
		ConsecutiveFailures: 0,
	}

	summary := s.ToStatusSummary()

	if summary.Rig != "gastown_upstream" {
		t.Errorf("Rig = %q, want %q", summary.Rig, "gastown_upstream")
	}
	if summary.State != "idle" {
		t.Errorf("State = %q, want %q", summary.State, "idle")
	}
	if summary.Paused {
		t.Error("Paused should be false for idle state")
	}
	if summary.UpstreamRemote != "upstream" {
		t.Errorf("UpstreamRemote = %q, want %q", summary.UpstreamRemote, "upstream")
	}
}

func TestToStatusSummary_Paused(t *testing.T) {
	s := SyncStateMetadata{
		SchemaVersion: 1,
		Rig:           "myrig",
		State:         StatePaused,
		PausedUntil:   "2026-06-01T00:00:00Z",
		PauseReason:   "investigating upstream test failure",
	}

	summary := s.ToStatusSummary()

	if !summary.Paused {
		t.Error("Paused should be true for paused state")
	}
	if summary.PauseReason != "investigating upstream test failure" {
		t.Errorf("PauseReason = %q, want expected reason", summary.PauseReason)
	}
	if summary.State != "paused" {
		t.Errorf("State = %q, want %q", summary.State, "paused")
	}
}

func TestFormatLastSync(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "never"},
		{"invalid-timestamp", "invalid-timestamp"},
	}

	for _, tt := range tests {
		got := FormatLastSync(tt.input)
		if got != tt.want {
			t.Errorf("FormatLastSync(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
