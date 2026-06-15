package upstreamsync

import (
	"testing"
)

// TestClearStaleFailureState_FailedRig is the regression guard for
// gu-jngrh: a rig wedged in StateFailed with a non-zero failure counter
// (from earlier failed attempts) must self-heal back to idle when the
// in-sync no-op path observes it is 0-behind. Before the fix there was no
// path to clear this — `resume` no-ops on a non-paused rig and `sync`
// short-circuits before recording an attempt.
func TestClearStaleFailureState_FailedRig(t *testing.T) {
	b, prefix := newProvisionTestBeads(t)
	if _, err := EnsureStateBead(b, prefix, "gastown_upstream", nil); err != nil {
		t.Fatalf("EnsureStateBead: %v", err)
	}

	// Wedge the rig: failed with 2 consecutive failures.
	if err := MutateSyncState(b, prefix, func(s *SyncStateMetadata) error {
		s.State = StateFailed
		s.ConsecutiveFailures = 2
		return nil
	}); err != nil {
		t.Fatalf("setup MutateSyncState: %v", err)
	}

	cleared, err := ClearStaleFailureState(b, prefix)
	if err != nil {
		t.Fatalf("ClearStaleFailureState: %v", err)
	}
	if !cleared {
		t.Fatal("expected cleared=true for a wedged failed rig")
	}

	state, err := LoadSyncState(b, prefix)
	if err != nil {
		t.Fatalf("LoadSyncState: %v", err)
	}
	if state.State != StateIdle {
		t.Errorf("state = %q, want %q", state.State, StateIdle)
	}
	if state.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d, want 0", state.ConsecutiveFailures)
	}
	if state.CurrentAttempt != nil {
		t.Errorf("CurrentAttempt = %+v, want nil", state.CurrentAttempt)
	}
}

// TestClearStaleFailureState_IdleNoFailures confirms the helper is a no-op
// on a healthy idle rig — no spurious writes, cleared=false.
func TestClearStaleFailureState_IdleNoFailures(t *testing.T) {
	b, prefix := newProvisionTestBeads(t)
	if _, err := EnsureStateBead(b, prefix, "gastown_upstream", nil); err != nil {
		t.Fatalf("EnsureStateBead: %v", err)
	}

	cleared, err := ClearStaleFailureState(b, prefix)
	if err != nil {
		t.Fatalf("ClearStaleFailureState: %v", err)
	}
	if cleared {
		t.Error("expected cleared=false for a healthy idle rig")
	}
}

// TestClearStaleFailureState_IdleWithStaleCounter covers the defensive
// edge where the rig is idle but a non-zero counter lingers: the counter
// is reset in place without a (rejected) idle→idle transition.
func TestClearStaleFailureState_IdleWithStaleCounter(t *testing.T) {
	b, prefix := newProvisionTestBeads(t)
	if _, err := EnsureStateBead(b, prefix, "gastown_upstream", nil); err != nil {
		t.Fatalf("EnsureStateBead: %v", err)
	}

	if err := MutateSyncState(b, prefix, func(s *SyncStateMetadata) error {
		s.State = StateIdle
		s.ConsecutiveFailures = 3
		return nil
	}); err != nil {
		t.Fatalf("setup MutateSyncState: %v", err)
	}

	cleared, err := ClearStaleFailureState(b, prefix)
	if err != nil {
		t.Fatalf("ClearStaleFailureState: %v", err)
	}
	if !cleared {
		t.Fatal("expected cleared=true for an idle rig with stale counter")
	}

	state, err := LoadSyncState(b, prefix)
	if err != nil {
		t.Fatalf("LoadSyncState: %v", err)
	}
	if state.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d, want 0", state.ConsecutiveFailures)
	}
}

// TestClearStaleFailureState_PausedUntouched confirms a paused rig is left
// alone — the explicit resume path owns clearing a pause.
func TestClearStaleFailureState_PausedUntouched(t *testing.T) {
	b, prefix := newProvisionTestBeads(t)
	if _, err := EnsureStateBead(b, prefix, "gastown_upstream", nil); err != nil {
		t.Fatalf("EnsureStateBead: %v", err)
	}

	if err := MutateSyncState(b, prefix, func(s *SyncStateMetadata) error {
		s.State = StatePaused
		s.ConsecutiveFailures = 5
		s.PauseReason = CircuitBreakerReason(5)
		return nil
	}); err != nil {
		t.Fatalf("setup MutateSyncState: %v", err)
	}

	cleared, err := ClearStaleFailureState(b, prefix)
	if err != nil {
		t.Fatalf("ClearStaleFailureState: %v", err)
	}
	if cleared {
		t.Error("expected cleared=false for a paused rig")
	}

	state, err := LoadSyncState(b, prefix)
	if err != nil {
		t.Fatalf("LoadSyncState: %v", err)
	}
	if state.State != StatePaused {
		t.Errorf("state = %q, want %q (paused must be untouched)", state.State, StatePaused)
	}
	if state.ConsecutiveFailures != 5 {
		t.Errorf("ConsecutiveFailures = %d, want 5 (paused must be untouched)", state.ConsecutiveFailures)
	}
}
