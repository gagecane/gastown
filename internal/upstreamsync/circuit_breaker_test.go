package upstreamsync

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
)

func TestShouldTrip(t *testing.T) {
	cases := []struct {
		failures  int
		threshold int
		want      bool
	}{
		{0, 3, false},
		{1, 3, false},
		{2, 3, false},
		{3, 3, true}, // equality trips
		{4, 3, true},
		{0, 0, false}, // 0 threshold → use default 3
		{2, 0, false},
		{3, 0, true},
		{1, 1, true},
	}
	for _, tt := range cases {
		got := ShouldTrip(tt.failures, tt.threshold)
		if got != tt.want {
			t.Errorf("ShouldTrip(failures=%d, threshold=%d) = %v, want %v",
				tt.failures, tt.threshold, got, tt.want)
		}
	}
}

func TestShouldTrip_DefaultMatchesConfig(t *testing.T) {
	// The fall-through default must match the published config default
	// or operators will see different thresholds in the breaker vs. in
	// `gt upstream config`.
	if !ShouldTrip(config.DefaultUpstreamSyncMaxConsecutiveFailures, 0) {
		t.Errorf("default threshold (%d) should trip at exact match",
			config.DefaultUpstreamSyncMaxConsecutiveFailures)
	}
	if ShouldTrip(config.DefaultUpstreamSyncMaxConsecutiveFailures-1, 0) {
		t.Errorf("default threshold should NOT trip at one below")
	}
}

func TestCircuitBreakerReason_Stable(t *testing.T) {
	// Stable prefix is part of the audit contract — IsAutoPaused
	// depends on it. Don't change without updating IsAutoPaused.
	r := CircuitBreakerReason(3)
	if !strings.HasPrefix(r, "circuit-breaker:") {
		t.Errorf("CircuitBreakerReason(%d) = %q, missing stable prefix", 3, r)
	}
	if !strings.Contains(r, "3 consecutive failures") {
		t.Errorf("CircuitBreakerReason(%d) should mention failure count, got %q", 3, r)
	}
}

func TestIsAutoPaused(t *testing.T) {
	cases := []struct {
		name  string
		state SyncStateMetadata
		want  bool
	}{
		{
			name:  "idle never auto-paused",
			state: SyncStateMetadata{State: StateIdle},
			want:  false,
		},
		{
			name: "paused with circuit-breaker reason",
			state: SyncStateMetadata{
				State:       StatePaused,
				PauseReason: CircuitBreakerReason(3),
			},
			want: true,
		},
		{
			name: "paused with operator reason",
			state: SyncStateMetadata{
				State:       StatePaused,
				PauseReason: "investigating broken upstream test (by overseer)",
			},
			want: false,
		},
		{
			name: "paused with empty reason",
			state: SyncStateMetadata{
				State:       StatePaused,
				PauseReason: "",
			},
			want: false,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAutoPaused(tt.state); got != tt.want {
				t.Errorf("IsAutoPaused() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMostRecentTrip(t *testing.T) {
	state := SyncStateMetadata{
		ConsecutiveFailures: 3,
		Attempts: []SyncAttempt{
			{ID: "a1", Outcome: "success", CompletedAt: "2026-05-01T00:00:00Z"},
			{ID: "a2", Outcome: "gate-failure", CompletedAt: "2026-05-02T00:00:00Z"},
			{ID: "a3", Outcome: "conflict", CompletedAt: "2026-05-03T00:00:00Z"},
			{ID: "a4", Outcome: "push-failure", CompletedAt: "2026-05-04T00:00:00Z"},
		},
	}
	evt := MostRecentTrip(state, 3)
	if evt == nil {
		t.Fatal("MostRecentTrip returned nil for a state above threshold")
	}
	if evt.LastFailedAttemptID != "a4" {
		t.Errorf("LastFailedAttemptID = %q, want a4", evt.LastFailedAttemptID)
	}
	if evt.LastFailedOutcome != "push-failure" {
		t.Errorf("LastFailedOutcome = %q, want push-failure", evt.LastFailedOutcome)
	}
	if evt.Failures != 3 {
		t.Errorf("Failures = %d, want 3", evt.Failures)
	}
}

func TestMostRecentTrip_BelowThreshold(t *testing.T) {
	state := SyncStateMetadata{
		ConsecutiveFailures: 1,
		Attempts: []SyncAttempt{
			{ID: "a1", Outcome: "gate-failure", CompletedAt: "2026-05-01T00:00:00Z"},
		},
	}
	if MostRecentTrip(state, 3) != nil {
		t.Errorf("expected nil trip when failures (%d) below threshold (%d)",
			state.ConsecutiveFailures, 3)
	}
}

func TestMostRecentTrip_NoFailureHistory(t *testing.T) {
	// Counter says we should be tripped, but there's no failure record
	// in attempts (e.g., history truncated). Returns nil rather than
	// fabricate an event.
	state := SyncStateMetadata{
		ConsecutiveFailures: 5,
		Attempts: []SyncAttempt{
			{ID: "a1", Outcome: "success", CompletedAt: "2026-05-01T00:00:00Z"},
		},
	}
	if MostRecentTrip(state, 3) != nil {
		t.Errorf("expected nil when no failed attempts in history")
	}
}
