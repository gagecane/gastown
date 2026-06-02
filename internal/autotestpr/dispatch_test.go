package autotestpr

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

var dispatchNow = time.Date(2026, 6, 2, 9, 30, 0, 0, time.UTC)

// fakeRigStateStore is an in-memory RigStateStore for CAS tests.
type fakeRigStateStore struct {
	state       RigState
	transitions []TransitionRecord

	// saveErr, when set, is returned by the next SaveRigState call and
	// then cleared — lets a test inject a single transient conflict.
	saveErr error

	// loadErr, when set, is returned by LoadRigState.
	loadErr error

	// appendErr, when set, is returned by AppendTransition.
	appendErr error
}

func (f *fakeRigStateStore) LoadRigState(string) (RigState, error) {
	if f.loadErr != nil {
		return RigState{}, f.loadErr
	}
	return f.state, nil
}

func (f *fakeRigStateStore) SaveRigState(_ string, s RigState) error {
	if f.saveErr != nil {
		err := f.saveErr
		f.saveErr = nil // one-shot
		return err
	}
	f.state = s
	return nil
}

func (f *fakeRigStateStore) AppendTransition(rec TransitionRecord) error {
	if f.appendErr != nil {
		return f.appendErr
	}
	f.transitions = append(f.transitions, rec)
	return nil
}

// transientErr is an error whose message matches isTransientDoltWriteError.
type transientErr struct{}

func (transientErr) Error() string { return "optimistic lock failure; try restarting transaction" }

// --- Dispatch envelope tests ---

// TestBuildDispatchEnvelope_Shape verifies the documented envelope shape
// (synthesis §Interface) is produced with the expected fields.
func TestBuildDispatchEnvelope_Shape(t *testing.T) {
	t.Parallel()

	chosen := TargetCandidate{
		Path: "internal/cmd/foo.go",
		UncoveredBranches: []UncoveredBranch{
			{Line: 100, Kind: "legacy"},
			{Line: 42, Kind: "near-churn"},
		},
		CoveragePctBefore: 0.62,
		ChurnRanges:       []ChurnRange{{Start: 40, End: 45}},
	}

	env := BuildDispatchEnvelope(
		"gu-work-1", "gastown_upstream", "go",
		".gt/auto-test-pr/conventions.md", ".gt/auto-test-pr/mr-template.md",
		chosen, DefaultSizeBudget(), dispatchNow,
	)

	if env.Version != DispatchEnvelopeVersion {
		t.Errorf("version = %d, want %d", env.Version, DispatchEnvelopeVersion)
	}
	if env.WorkBeadID != "gu-work-1" {
		t.Errorf("work_bead_id = %q", env.WorkBeadID)
	}
	if env.TargetRig != "gastown_upstream" {
		t.Errorf("target_rig = %q", env.TargetRig)
	}
	if env.Formula != DispatchFormula {
		t.Errorf("formula = %q, want %q", env.Formula, DispatchFormula)
	}
	if env.Args.Mode != "create" {
		t.Errorf("mode = %q, want create", env.Args.Mode)
	}
	if env.Args.Revision != nil {
		t.Errorf("create-mode revision should be nil, got %+v", env.Args.Revision)
	}
	if len(env.Args.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(env.Args.Targets))
	}
	tgt := env.Args.Targets[0]
	if tgt.Path != "internal/cmd/foo.go" {
		t.Errorf("target path = %q", tgt.Path)
	}
	// NG5: uncovered branches must be ordered by churn proximity — the
	// line-42 branch (5 from churn) before the line-100 legacy branch.
	if tgt.UncoveredBranches[0].Line != 42 {
		t.Errorf("expected churn-proximal branch (line 42) first, got line %d", tgt.UncoveredBranches[0].Line)
	}
	if env.Args.SizeBudget != DefaultSizeBudget() {
		t.Errorf("size_budget = %+v", env.Args.SizeBudget)
	}
	if env.EnqueuedAt != dispatchNow.UTC().Format(time.RFC3339) {
		t.Errorf("enqueued_at = %q", env.EnqueuedAt)
	}
}

// TestBuildDispatchEnvelope_JSONRoundTrip verifies the envelope marshals
// to the documented JSON keys.
func TestBuildDispatchEnvelope_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	env := BuildDispatchEnvelope(
		"gu-work-1", "gastown_upstream", "go", "c.md", "t.md",
		TargetCandidate{Path: "a.go", UncoveredBranches: []UncoveredBranch{{Line: 1, Kind: "if-true"}}},
		DefaultSizeBudget(), dispatchNow,
	)
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var back DispatchEnvelope
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.WorkBeadID != env.WorkBeadID || back.Args.Targets[0].Path != "a.go" {
		t.Errorf("round-trip mismatch: %+v", back)
	}
	// Verify documented key names are present.
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, key := range []string{"version", "work_bead_id", "target_rig", "formula", "args", "enqueued_at"} {
		if _, ok := m[key]; !ok {
			t.Errorf("envelope JSON missing key %q", key)
		}
	}
}

// TestDispatchPriorityFloor_IsLowest verifies the cycle dispatches with
// the lowest priority bucket (synthesis step 5; acceptance criterion 6).
func TestDispatchPriorityFloor_IsLowest(t *testing.T) {
	t.Parallel()
	if DispatchPriorityFloor != capacity.PriorityFloorLowest {
		t.Errorf("DispatchPriorityFloor = %d, want PriorityFloorLowest (%d)",
			DispatchPriorityFloor, capacity.PriorityFloorLowest)
	}
}

// --- CAS transition tests ---

// TestCASTransition_IdleToPicking verifies a clean idle→picking
// transition writes the new state and appends a transition record.
func TestCASTransition_IdleToPicking(t *testing.T) {
	t.Parallel()

	store := &fakeRigStateStore{state: RigState{State: PerRigCycleStateIdle}}
	err := CASTransition(store, "rig1", PerRigCycleStateIdle, PerRigCycleStatePicking, "mayor", dispatchNow, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.state.State != PerRigCycleStatePicking {
		t.Errorf("state = %q, want picking", store.state.State)
	}
	if len(store.transitions) != 1 {
		t.Fatalf("expected 1 transition record, got %d", len(store.transitions))
	}
	tr := store.transitions[0]
	if tr.From != "idle" || tr.To != "picking" || tr.Rig != "rig1" || tr.Actor != "mayor" {
		t.Errorf("transition record = %+v", tr)
	}
}

// TestCASTransition_PickingToDispatched_WithMutate verifies the mutate
// callback can stamp current-cycle data in the same write.
func TestCASTransition_PickingToDispatched_WithMutate(t *testing.T) {
	t.Parallel()

	store := &fakeRigStateStore{state: RigState{State: PerRigCycleStatePicking}}
	err := CASTransition(store, "rig1", PerRigCycleStatePicking, PerRigCycleStateDispatched, "mayor", dispatchNow,
		func(s *RigState) {
			s.CurrentCycle = &CurrentCycle{CycleID: "cyc-1", PolecatBead: "gu-work-1"}
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.state.State != PerRigCycleStateDispatched {
		t.Errorf("state = %q, want dispatched", store.state.State)
	}
	if store.state.CurrentCycle == nil || store.state.CurrentCycle.CycleID != "cyc-1" {
		t.Errorf("current cycle not stamped: %+v", store.state.CurrentCycle)
	}
}

// TestCASTransition_Conflict verifies a state mismatch returns
// ErrTransitionConflict and does NOT write or record.
func TestCASTransition_Conflict(t *testing.T) {
	t.Parallel()

	// Rig already advanced to picking by another tick; we expected idle.
	store := &fakeRigStateStore{state: RigState{State: PerRigCycleStatePicking}}
	err := CASTransition(store, "rig1", PerRigCycleStateIdle, PerRigCycleStatePicking, "mayor", dispatchNow, nil)
	if !errors.Is(err, ErrTransitionConflict) {
		t.Fatalf("expected ErrTransitionConflict, got %v", err)
	}
	if len(store.transitions) != 0 {
		t.Errorf("conflict should append no transition, got %d", len(store.transitions))
	}
	if store.state.State != PerRigCycleStatePicking {
		t.Errorf("conflict should not mutate state, got %q", store.state.State)
	}
}

// TestCASTransition_RetriesTransientConflict verifies the CAS loop
// retries on a transient Dolt write conflict and then succeeds.
func TestCASTransition_RetriesTransientConflict(t *testing.T) {
	t.Parallel()

	store := &fakeRigStateStore{
		state:   RigState{State: PerRigCycleStateIdle},
		saveErr: transientErr{}, // one-shot transient failure
	}
	err := CASTransition(store, "rig1", PerRigCycleStateIdle, PerRigCycleStatePicking, "mayor", dispatchNow, nil)
	if err != nil {
		t.Fatalf("expected retry to succeed, got %v", err)
	}
	if store.state.State != PerRigCycleStatePicking {
		t.Errorf("state = %q, want picking after retry", store.state.State)
	}
}
