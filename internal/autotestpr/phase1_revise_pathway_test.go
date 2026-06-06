package autotestpr

import (
	"testing"
	"time"
)

// TestPhase1RevisePathway_CASTransition exercises the CAS transition
// from mr-pending → mr-revising that the revise CLI performs (Phase 0
// task 2c, exercised by Phase 1 task 18).
//
// This validates the documented state machine edge: only rigs in
// mr-pending can enter mr-revising, ensuring the manual revision
// pathway respects the cycle state contract.
func TestPhase1RevisePathway_CASTransition(t *testing.T) {
	t.Parallel()

	store := &fakeRigStateStore{state: RigState{
		SchemaVersion: RigStateSchemaVersion,
		State:         PerRigCycleStateMRPending,
		CurrentCycle: &CurrentCycle{
			CycleID:     "cycle-1",
			StartedAt:   "2026-06-01T10:00:00Z",
			PolecatBead: "gu-work-1",
			MRBead:      "gt-mr-abc12",
			Branch:      "auto-test/gastown_upstream/gu-work-1",
		},
	}}

	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

	err := CASTransition(
		store,
		"gastown_upstream",
		PerRigCycleStateMRPending,
		PerRigCycleStateMRRevising,
		"overseer",
		now,
		nil,
	)
	if err != nil {
		t.Fatalf("CASTransition(mr-pending → mr-revising): %v", err)
	}

	if store.state.State != PerRigCycleStateMRRevising {
		t.Errorf("state = %q; want %q", store.state.State, PerRigCycleStateMRRevising)
	}

	// Verify the transition was logged.
	if len(store.transitions) != 1 {
		t.Fatalf("transitions count = %d; want 1", len(store.transitions))
	}
	tr := store.transitions[0]
	if tr.From != string(PerRigCycleStateMRPending) {
		t.Errorf("transition.from = %q; want %q", tr.From, PerRigCycleStateMRPending)
	}
	if tr.To != string(PerRigCycleStateMRRevising) {
		t.Errorf("transition.to = %q; want %q", tr.To, PerRigCycleStateMRRevising)
	}
	if tr.Actor != "overseer" {
		t.Errorf("transition.actor = %q; want %q", tr.Actor, "overseer")
	}
	if tr.Rig != "gastown_upstream" {
		t.Errorf("transition.rig = %q; want %q", tr.Rig, "gastown_upstream")
	}
}

// TestPhase1RevisePathway_CASConflict_NotPending verifies that the CAS
// transition correctly rejects a revise attempt when the rig is NOT in
// mr-pending (e.g., still dispatched, already revising, or idle).
func TestPhase1RevisePathway_CASConflict_NotPending(t *testing.T) {
	t.Parallel()

	conflictStates := []PerRigCycleState{
		PerRigCycleStateIdle,
		PerRigCycleStateDispatched,
		PerRigCycleStateMRRevising,
		PerRigCycleStateCooledDown,
		PerRigCycleStatePausedByCircuitBreaker,
	}

	for _, state := range conflictStates {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()

			store := &fakeRigStateStore{state: RigState{
				SchemaVersion: RigStateSchemaVersion,
				State:         state,
			}}

			now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

			err := CASTransition(
				store,
				"gastown_upstream",
				PerRigCycleStateMRPending,
				PerRigCycleStateMRRevising,
				"overseer",
				now,
				nil,
			)
			if err == nil {
				t.Fatalf("expected ErrTransitionConflict for state %q, got nil", state)
			}
			if !isTransitionConflict(err) {
				t.Errorf("expected ErrTransitionConflict, got: %v", err)
			}

			// State should be unchanged.
			if store.state.State != state {
				t.Errorf("state changed from %q to %q; should be unchanged on conflict",
					state, store.state.State)
			}
		})
	}
}

// TestPhase1RevisePathway_CASRetryOnTransient verifies that the CAS
// loop retries on transient Dolt write errors (optimistic lock failure).
func TestPhase1RevisePathway_CASRetryOnTransient(t *testing.T) {
	t.Parallel()

	store := &fakeRigStateStore{
		state: RigState{
			SchemaVersion: RigStateSchemaVersion,
			State:         PerRigCycleStateMRPending,
		},
		saveErr: transientErr{}, // First save attempt fails transiently.
	}

	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

	err := CASTransition(
		store,
		"gastown_upstream",
		PerRigCycleStateMRPending,
		PerRigCycleStateMRRevising,
		"overseer",
		now,
		nil,
	)
	if err != nil {
		t.Fatalf("CASTransition should retry on transient: %v", err)
	}

	if store.state.State != PerRigCycleStateMRRevising {
		t.Errorf("state = %q; want %q after retry", store.state.State, PerRigCycleStateMRRevising)
	}
}

// TestPhase1RevisePathway_MRBannerCLIRendering validates that the
// Phase1ReviseCLI helper produces the correct command for the MR banner
// template's {{phase1_revise_cli}} placeholder.
func TestPhase1RevisePathway_MRBannerCLIRendering(t *testing.T) {
	t.Parallel()

	// Phase 1: revision routing is OFF (manual fallback active).
	got := Phase1ReviseCLI("gt-mr-abc12", false)
	want := "gt auto-test-pr revise --mr=gt-mr-abc12"
	if got != want {
		t.Errorf("Phase1ReviseCLI() = %q; want %q", got, want)
	}

	// Verify the output matches what the MR template expects:
	// "gt auto-test-pr revise --mr=<this-mr-bead>"
	// This is documented in .gt/auto-test-pr/mr-template.md line 46.
	if got != "gt auto-test-pr revise --mr=gt-mr-abc12" {
		t.Errorf("CLI output does not match mr-template.md placeholder contract")
	}
}

// TestPhase1RevisePathway_PostTransitionCurrentCyclePreserved verifies
// that the CAS transition preserves CurrentCycle metadata through the
// mr-pending → mr-revising transition (the cycle data is still needed
// by the revision polecat to identify the branch and MR bead).
func TestPhase1RevisePathway_PostTransitionCurrentCyclePreserved(t *testing.T) {
	t.Parallel()

	cycle := &CurrentCycle{
		CycleID:     "cycle-42",
		StartedAt:   "2026-06-01T10:00:00Z",
		PolecatBead: "gu-work-99",
		MRBead:      "gt-mr-xyz",
		Branch:      "auto-test/gastown_upstream/gu-work-99",
	}

	store := &fakeRigStateStore{state: RigState{
		SchemaVersion: RigStateSchemaVersion,
		State:         PerRigCycleStateMRPending,
		CurrentCycle:  cycle,
	}}

	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

	err := CASTransition(
		store,
		"gastown_upstream",
		PerRigCycleStateMRPending,
		PerRigCycleStateMRRevising,
		"overseer",
		now,
		nil,
	)
	if err != nil {
		t.Fatalf("CASTransition: %v", err)
	}

	// CurrentCycle must survive the transition unchanged.
	if store.state.CurrentCycle == nil {
		t.Fatal("CurrentCycle is nil after transition; should be preserved")
	}
	if store.state.CurrentCycle.CycleID != "cycle-42" {
		t.Errorf("CycleID = %q; want %q", store.state.CurrentCycle.CycleID, "cycle-42")
	}
	if store.state.CurrentCycle.MRBead != "gt-mr-xyz" {
		t.Errorf("MRBead = %q; want %q", store.state.CurrentCycle.MRBead, "gt-mr-xyz")
	}
	if store.state.CurrentCycle.Branch != "auto-test/gastown_upstream/gu-work-99" {
		t.Errorf("Branch = %q; want %q", store.state.CurrentCycle.Branch, "auto-test/gastown_upstream/gu-work-99")
	}
}

// isTransitionConflict checks whether err wraps ErrTransitionConflict.
func isTransitionConflict(err error) bool {
	return err != nil && (err == ErrTransitionConflict ||
		(err.Error() != "" && containsTransitionConflict(err.Error())))
}

func containsTransitionConflict(s string) bool {
	return len(s) >= len(ErrTransitionConflict.Error()) &&
		containsString(s, ErrTransitionConflict.Error())
}

func containsString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
