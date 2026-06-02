package autotestpr

import (
	"testing"
	"time"
)

var cooldownNow = time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)

// TestShouldReleaseCooldown_Elapsed verifies a cooled-down rig whose
// last transition is older than cadence_days*24h is eligible.
func TestShouldReleaseCooldown_Elapsed(t *testing.T) {
	t.Parallel()

	last := cooldownNow.Add(-8 * 24 * time.Hour) // 8 days ago, cadence 7
	if !ShouldReleaseCooldown(PerRigCycleStateCooledDown, last, 7, cooldownNow) {
		t.Error("expected release when cadence elapsed")
	}
}

// TestShouldReleaseCooldown_NotElapsed verifies a cooled-down rig still
// within its cadence window is NOT released.
func TestShouldReleaseCooldown_NotElapsed(t *testing.T) {
	t.Parallel()

	last := cooldownNow.Add(-3 * 24 * time.Hour) // 3 days ago, cadence 7
	if ShouldReleaseCooldown(PerRigCycleStateCooledDown, last, 7, cooldownNow) {
		t.Error("expected no release within cadence window")
	}
}

// TestShouldReleaseCooldown_ExactBoundary verifies the boundary is
// inclusive (>= cadence releases).
func TestShouldReleaseCooldown_ExactBoundary(t *testing.T) {
	t.Parallel()

	last := cooldownNow.Add(-7 * 24 * time.Hour) // exactly 7 days, cadence 7
	if !ShouldReleaseCooldown(PerRigCycleStateCooledDown, last, 7, cooldownNow) {
		t.Error("expected release at exact cadence boundary (>=)")
	}
}

// TestShouldReleaseCooldown_CircuitBreakerNeverReleases verifies a rig
// in paused-by-circuit-breaker does NOT auto-release even when the
// cadence has long elapsed (D18 explicit requirement).
func TestShouldReleaseCooldown_CircuitBreakerNeverReleases(t *testing.T) {
	t.Parallel()

	last := cooldownNow.Add(-100 * 24 * time.Hour) // ancient
	if ShouldReleaseCooldown(PerRigCycleStatePausedByCircuitBreaker, last, 7, cooldownNow) {
		t.Error("paused-by-circuit-breaker must NOT auto-release")
	}
}

// TestShouldReleaseCooldown_WrongStates verifies non-cooled-down states
// (idle, picking, dispatched) are never released.
func TestShouldReleaseCooldown_WrongStates(t *testing.T) {
	t.Parallel()

	last := cooldownNow.Add(-100 * 24 * time.Hour)
	for _, st := range []PerRigCycleState{
		PerRigCycleStateIdle, PerRigCycleStatePicking,
		PerRigCycleStateDispatched, PerRigCycleStateMRPending,
	} {
		if ShouldReleaseCooldown(st, last, 7, cooldownNow) {
			t.Errorf("state %q must not release", st)
		}
	}
}

// TestShouldReleaseCooldown_ZeroLastTransition verifies a rig with no
// recorded transition is not released (no cadence basis).
func TestShouldReleaseCooldown_ZeroLastTransition(t *testing.T) {
	t.Parallel()

	if ShouldReleaseCooldown(PerRigCycleStateCooledDown, time.Time{}, 7, cooldownNow) {
		t.Error("zero last-transition must not release")
	}
}

// TestReleaseCooldownIfElapsed_Transitions verifies the applier
// CAS-transitions an eligible cooled-down rig to idle and records the
// transition.
func TestReleaseCooldownIfElapsed_Transitions(t *testing.T) {
	t.Parallel()

	store := &fakeRigStateStore{state: RigState{
		State:        PerRigCycleStateCooledDown,
		CurrentCycle: &CurrentCycle{CycleID: "old"},
	}}
	last := cooldownNow.Add(-8 * 24 * time.Hour)

	released, err := ReleaseCooldownIfElapsed(store, "rig1", last, 7, cooldownNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !released {
		t.Fatal("expected released=true")
	}
	if store.state.State != PerRigCycleStateIdle {
		t.Errorf("state = %q, want idle", store.state.State)
	}
	if store.state.CurrentCycle != nil {
		t.Errorf("expected current cycle cleared, got %+v", store.state.CurrentCycle)
	}
	if len(store.transitions) != 1 || store.transitions[0].To != "idle" {
		t.Errorf("expected cooled-down→idle transition record, got %+v", store.transitions)
	}
}

// TestReleaseCooldownIfElapsed_NotEligible verifies no transition occurs
// when the cadence has not elapsed.
func TestReleaseCooldownIfElapsed_NotEligible(t *testing.T) {
	t.Parallel()

	store := &fakeRigStateStore{state: RigState{State: PerRigCycleStateCooledDown}}
	last := cooldownNow.Add(-1 * 24 * time.Hour) // 1 day, cadence 7

	released, err := ReleaseCooldownIfElapsed(store, "rig1", last, 7, cooldownNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if released {
		t.Error("expected released=false within cadence window")
	}
	if store.state.State != PerRigCycleStateCooledDown {
		t.Errorf("state should remain cooled-down, got %q", store.state.State)
	}
	if len(store.transitions) != 0 {
		t.Errorf("expected no transition, got %d", len(store.transitions))
	}
}

// TestReleaseCooldownIfElapsed_CircuitBreakerNotReleased verifies the
// applier leaves a paused-by-circuit-breaker rig untouched.
func TestReleaseCooldownIfElapsed_CircuitBreakerNotReleased(t *testing.T) {
	t.Parallel()

	store := &fakeRigStateStore{state: RigState{State: PerRigCycleStatePausedByCircuitBreaker}}
	last := cooldownNow.Add(-100 * 24 * time.Hour)

	released, err := ReleaseCooldownIfElapsed(store, "rig1", last, 7, cooldownNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if released {
		t.Error("circuit-breaker pause must not release")
	}
	if store.state.State != PerRigCycleStatePausedByCircuitBreaker {
		t.Errorf("state = %q, want unchanged paused-by-circuit-breaker", store.state.State)
	}
}
