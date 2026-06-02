package autotestpr

import (
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
)

var processNow = time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)

// newProcessCfg builds a CycleConfig wired with a fake store and the
// supplied hooks, with a minimal valid base config.
func newProcessCfg(store RigStateStore) *CycleConfig {
	return &CycleConfig{
		TownRoot:   "/tmp/atpr-test",
		RigsConfig: &config.RigsConfig{Rigs: map[string]config.RigEntry{"rig1": {}}},
		Now:        processNow,
		RigStore:   store,
	}
}

// TestProcessRig_Phase0Inert verifies that with no hooks wired,
// processRig is a no-op (Phase 0 inert path).
func TestProcessRig_Phase0Inert(t *testing.T) {
	t.Parallel()

	cfg := &CycleConfig{TownRoot: "/tmp", Now: processNow} // no RigStore/Targets/Dispatch
	processed, err := processRig(cfg, "rig1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed {
		t.Error("Phase 0 inert path must not process any rig")
	}
}

// TestProcessRig_FullDispatch verifies the happy path: idle→picking→
// dispatched with a chosen candidate and a filed dispatch bead.
func TestProcessRig_FullDispatch(t *testing.T) {
	t.Parallel()

	store := &fakeRigStateStore{state: RigState{State: PerRigCycleStateIdle}}
	cfg := newProcessCfg(store)

	var dispatchedEnv DispatchEnvelope
	cfg.Targets = func(string) ([]TargetCandidate, []RejectionRecord, error) {
		return []TargetCandidate{
			{Path: "low.go", Churn: 1, UncoveredBranches: []UncoveredBranch{{Line: 1}}},
			{Path: "high.go", Churn: 9, UncoveredBranches: []UncoveredBranch{{Line: 1}, {Line: 2}}},
		}, nil, nil
	}
	cfg.Dispatch = func(_ string, env DispatchEnvelope) (string, error) {
		dispatchedEnv = env
		return "gu-work-42", nil
	}

	processed, err := processRig(cfg, "rig1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !processed {
		t.Fatal("expected rig to be processed")
	}
	if store.state.State != PerRigCycleStateDispatched {
		t.Errorf("end state = %q, want dispatched", store.state.State)
	}
	if store.state.CurrentCycle == nil || store.state.CurrentCycle.PolecatBead != "gu-work-42" {
		t.Errorf("current cycle not stamped: %+v", store.state.CurrentCycle)
	}
	// Highest-score candidate chosen.
	if dispatchedEnv.Args.Targets[0].Path != "high.go" {
		t.Errorf("expected high.go dispatched, got %q", dispatchedEnv.Args.Targets[0].Path)
	}
	// Two transitions recorded: idle→picking, picking→dispatched.
	if len(store.transitions) != 2 {
		t.Fatalf("expected 2 transitions, got %d: %+v", len(store.transitions), store.transitions)
	}
	if store.transitions[0].To != "picking" || store.transitions[1].To != "dispatched" {
		t.Errorf("transition sequence wrong: %+v", store.transitions)
	}
}

// TestProcessRig_NotIdleSkips verifies a non-idle rig is skipped without
// any transition.
func TestProcessRig_NotIdleSkips(t *testing.T) {
	t.Parallel()

	store := &fakeRigStateStore{state: RigState{State: PerRigCycleStateMRPending}}
	cfg := newProcessCfg(store)
	cfg.Targets = func(string) ([]TargetCandidate, []RejectionRecord, error) { return nil, nil, nil }
	cfg.Dispatch = func(string, DispatchEnvelope) (string, error) { return "", nil }

	processed, err := processRig(cfg, "rig1")
	if err != nil || processed {
		t.Fatalf("expected skip, got processed=%v err=%v", processed, err)
	}
	if len(store.transitions) != 0 {
		t.Errorf("non-idle rig must record no transitions, got %d", len(store.transitions))
	}
}

// TestProcessRig_NoCandidatesRollsBack verifies that when no candidate
// survives ranking, the rig is rolled back picking→idle (no dispatch).
func TestProcessRig_NoCandidatesRollsBack(t *testing.T) {
	t.Parallel()

	store := &fakeRigStateStore{state: RigState{State: PerRigCycleStateIdle}}
	cfg := newProcessCfg(store)
	cfg.Targets = func(string) ([]TargetCandidate, []RejectionRecord, error) {
		return nil, nil, nil // no candidates
	}
	dispatchCalled := false
	cfg.Dispatch = func(string, DispatchEnvelope) (string, error) {
		dispatchCalled = true
		return "", nil
	}

	processed, err := processRig(cfg, "rig1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed {
		t.Error("expected not processed with no candidates")
	}
	if dispatchCalled {
		t.Error("dispatch must not be called with no candidates")
	}
	if store.state.State != PerRigCycleStateIdle {
		t.Errorf("expected rollback to idle, got %q", store.state.State)
	}
}

// TestProcessRig_DispatchFailureRollsBack verifies a dispatch error rolls
// the rig back to idle and returns the error.
func TestProcessRig_DispatchFailureRollsBack(t *testing.T) {
	t.Parallel()

	store := &fakeRigStateStore{state: RigState{State: PerRigCycleStateIdle}}
	cfg := newProcessCfg(store)
	cfg.Targets = func(string) ([]TargetCandidate, []RejectionRecord, error) {
		return []TargetCandidate{{Path: "a.go", Churn: 1, UncoveredBranches: []UncoveredBranch{{Line: 1}}}}, nil, nil
	}
	cfg.Dispatch = func(string, DispatchEnvelope) (string, error) {
		return "", fmt.Errorf("sling failed")
	}

	processed, err := processRig(cfg, "rig1")
	if err == nil {
		t.Fatal("expected dispatch error")
	}
	if processed {
		t.Error("expected not processed on dispatch failure")
	}
	if store.state.State != PerRigCycleStateIdle {
		t.Errorf("expected rollback to idle after dispatch failure, got %q", store.state.State)
	}
}

// TestReleaseCooldownForRig_WiredHook verifies the cycle's cooldown-
// release step transitions an eligible rig via the wired hooks.
func TestReleaseCooldownForRig_WiredHook(t *testing.T) {
	t.Parallel()

	store := &fakeRigStateStore{state: RigState{State: PerRigCycleStateCooledDown}}
	cfg := newProcessCfg(store)
	cfg.LastTransitionAt = func(string) (time.Time, error) {
		return processNow.Add(-8 * 24 * time.Hour), nil // 8d ago
	}
	cfg.RigCadenceDays = func(string) int { return 7 }

	releaseCooldownForRig(cfg, "rig1")
	if store.state.State != PerRigCycleStateIdle {
		t.Errorf("expected cooldown release to idle, got %q", store.state.State)
	}
}

// TestReleaseCooldownForRig_NoHookIsNoop verifies the Phase-0 inert path
// (no LastTransitionAt hook) does nothing.
func TestReleaseCooldownForRig_NoHookIsNoop(t *testing.T) {
	t.Parallel()

	store := &fakeRigStateStore{state: RigState{State: PerRigCycleStateCooledDown}}
	cfg := newProcessCfg(store) // no LastTransitionAt
	releaseCooldownForRig(cfg, "rig1")
	if store.state.State != PerRigCycleStateCooledDown {
		t.Errorf("Phase-0 inert path must not transition, got %q", store.state.State)
	}
}
