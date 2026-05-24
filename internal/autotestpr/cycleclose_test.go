// Unit tests for the Mayor cycle-close handler (Phase 0 task 3c, gu-xrxm6).
//
// Acceptance Criteria covered (per gu-xrxm6 description):
//   - merged → cooled-down                         (TestHandle_MergedPath_*)
//   - closed-unmerged → cooled-down + rejection log (TestHandle_RejectedPath_*)
//   - 3-closes-in-7d → paused-by-circuit-breaker  (TestHandle_CircuitBreaker_*)
//   - BUG-DISCOVERED: NOTES → P2 bug bead filed   (TestHandle_BugDiscovered_*,
//                                                   TestParseBugDiscovered_*)
//   - rig:<target_rig>-label → state bead lookup  (TestRigStateBeadID,
//                                                   TestHandle_RigLabelLookup_*)
package autotestpr

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeBeads is a hand-rolled stand-in for BeadsClient. Records every call
// and serves canned responses keyed by bead ID. We avoid a mocking library
// here — TALON convention is to mock dependencies of the class under test
// without leaning on third-party generators (per
// .designs/auto-test-pr/synthesis.md authoring style).
type fakeBeads struct {
	// metadata is the canned ShowMetadata response per bead ID. Missing
	// keys return ErrBeadNotFound. UpdateMetadata writes back here.
	metadata map[string]json.RawMessage

	// transitions is the canned ListTransitionsForRig response per rig.
	transitions map[string][]Transition

	// attachments captures every CreateAttachment call.
	attachments []capturedAttachment

	// bugBeads captures every CreateBugBead call.
	bugBeads []capturedBug

	// updates captures every UpdateMetadata call (in order).
	updates []capturedUpdate

	// errOnUpdate, when non-nil, is returned from UpdateMetadata for the
	// matching bead ID. Used to test partial-failure paths.
	errOnUpdate map[string]error

	// errOnCreateAttachment, when non-nil, is returned from
	// CreateAttachment for the given title prefix.
	errOnCreateAttachment error

	// errOnCreateBug, when non-nil, is returned from CreateBugBead.
	errOnCreateBug error

	// errOnListTransitions, when non-nil, is returned from
	// ListTransitionsForRig.
	errOnListTransitions error

	// nextID is the source for synthetic bead IDs returned by
	// CreateAttachment / CreateBugBead.
	nextID int
}

type capturedAttachment struct {
	Title    string
	Labels   []string
	ParentID string
	Metadata json.RawMessage
	ID       string
}

type capturedBug struct {
	Title    string
	Body     string
	ParentID string
	Labels   []string
	ID       string
}

type capturedUpdate struct {
	BeadID   string
	Metadata json.RawMessage
}

func newFakeBeads() *fakeBeads {
	return &fakeBeads{
		metadata:    map[string]json.RawMessage{},
		transitions: map[string][]Transition{},
		errOnUpdate: map[string]error{},
	}
}

func (f *fakeBeads) ShowMetadata(id string) (json.RawMessage, error) {
	raw, ok := f.metadata[id]
	if !ok {
		return nil, ErrBeadNotFound
	}
	// Return a copy so callers can't mutate our store.
	cp := make(json.RawMessage, len(raw))
	copy(cp, raw)
	return cp, nil
}

func (f *fakeBeads) UpdateMetadata(id string, raw json.RawMessage) error {
	if err, ok := f.errOnUpdate[id]; ok && err != nil {
		return err
	}
	cp := make(json.RawMessage, len(raw))
	copy(cp, raw)
	f.metadata[id] = cp
	f.updates = append(f.updates, capturedUpdate{BeadID: id, Metadata: cp})
	return nil
}

func (f *fakeBeads) CreateAttachment(title string, labels []string, parentID string, meta json.RawMessage) (string, error) {
	if f.errOnCreateAttachment != nil {
		return "", f.errOnCreateAttachment
	}
	f.nextID++
	id := fmt.Sprintf("att-%d", f.nextID)
	f.attachments = append(f.attachments, capturedAttachment{
		Title:    title,
		Labels:   append([]string(nil), labels...),
		ParentID: parentID,
		Metadata: append(json.RawMessage(nil), meta...),
		ID:       id,
	})
	return id, nil
}

func (f *fakeBeads) CreateBugBead(title, body, parentID string, labels []string) (string, error) {
	if f.errOnCreateBug != nil {
		return "", f.errOnCreateBug
	}
	f.nextID++
	id := fmt.Sprintf("bug-%d", f.nextID)
	f.bugBeads = append(f.bugBeads, capturedBug{
		Title:    title,
		Body:     body,
		ParentID: parentID,
		Labels:   append([]string(nil), labels...),
		ID:       id,
	})
	return id, nil
}

func (f *fakeBeads) ListTransitionsForRig(rig string) ([]Transition, error) {
	if f.errOnListTransitions != nil {
		return nil, f.errOnListTransitions
	}
	return append([]Transition(nil), f.transitions[rig]...), nil
}

// fakeNotifier records Overseer notification calls.
type fakeNotifier struct {
	calls []notifyCall
	err   error
}

type notifyCall struct {
	Subject string
	Body    string
}

func (n *fakeNotifier) NotifyOverseer(subject, body string) error {
	n.calls = append(n.calls, notifyCall{Subject: subject, Body: body})
	return n.err
}

// fixedClock returns the same instant every time. Tests use this to make
// the rolling-7d window deterministic.
func fixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

// seedRigState writes a per-rig state bead's Issue.Metadata with the given
// from-state. Round-trips through json.Marshal so the fake's storage
// matches what production reads.
func seedRigState(t *testing.T, f *fakeBeads, rig, state string) {
	t.Helper()
	id := RigStateBeadID(rig)
	payload := map[string]interface{}{
		"schema_version": 1,
		"rig":            rig,
		"state":          state,
		// Realistic placeholder fields the handler must round-trip
		// through Other so it doesn't clobber Phase 0 task 15's writes.
		"language":     "go",
		"cadence_days": 7,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal seed rig state: %v", err)
	}
	f.metadata[id] = raw
}

// seedTownState provisions the town-state bead with the default shape
// (count=0). Returns the seed for assertions.
func seedTownState(t *testing.T, f *fakeBeads) {
	t.Helper()
	state := DefaultTownState()
	raw, err := state.MarshalMetadata()
	if err != nil {
		t.Fatalf("marshal seed town state: %v", err)
	}
	f.metadata[TownStateBeadID] = raw
}

// loadRigState parses the per-rig state bead's stored metadata and
// returns the typed payload.
func loadRigState(t *testing.T, f *fakeBeads, rig string) rigStatePayload {
	t.Helper()
	raw := f.metadata[RigStateBeadID(rig)]
	got, err := decodeRigState(raw)
	if err != nil {
		t.Fatalf("decode rig state: %v", err)
	}
	return got
}

// loadTownState parses the town-state bead's stored metadata and returns
// the typed payload.
func loadTownState(t *testing.T, f *fakeBeads) TownState {
	t.Helper()
	raw := f.metadata[TownStateBeadID]
	got, err := UnmarshalTownState(raw)
	if err != nil {
		t.Fatalf("unmarshal town state: %v", err)
	}
	return got
}

// fixedNow is the test wall-clock. Round number for arithmetic clarity.
var fixedNow = time.Date(2026, 5, 21, 14, 23, 0, 0, time.UTC)

// --- AC: rig:<target_rig>-label → state bead lookup -----------------------

func TestRigStateBeadID(t *testing.T) {
	got := RigStateBeadID("gastown_upstream")
	want := "gastown_upstream-auto-test-state"
	if got != want {
		t.Errorf("RigStateBeadID(gastown_upstream) = %q, want %q", got, want)
	}
}

func TestHandle_RigLabelLookup_ResolvesPerRigStateBead(t *testing.T) {
	// AC: "verify the rig:<target_rig>-label-based lookup resolves to
	// the correct per-rig state bead on a fixture MR-bead with
	// rig:gastown_upstream."
	//
	// The handler doesn't see the label directly — the dog parses it
	// and hands TargetRig in. We assert that the rig name from the
	// event resolves to the deterministic per-rig state bead ID and
	// that the handler reads/writes that exact bead ID.
	f := newFakeBeads()
	seedRigState(t, f, "gastown_upstream", rigStateMRPending)
	notifier := &fakeNotifier{}
	h := NewCycleCloseHandler(f, notifier, WithNowFunc(fixedClock(fixedNow)))

	if err := h.Handle(CycleCloseEvent{
		MRID:        "gu-mr-abc",
		TargetRig:   "gastown_upstream",
		CloseReason: CloseReasonMerged,
		Body:        "close_reason: merged\n",
	}); err != nil {
		t.Fatalf("Handle merged: %v", err)
	}

	// The handler must have updated exactly the
	// gastown_upstream-auto-test-state bead, not (e.g.) some
	// list-by-label query result.
	if len(f.updates) == 0 {
		t.Fatal("expected at least one UpdateMetadata call, got none")
	}
	for _, u := range f.updates {
		if u.BeadID != "gastown_upstream-auto-test-state" && u.BeadID != TownStateBeadID {
			t.Errorf("unexpected UpdateMetadata target: %q", u.BeadID)
		}
	}

	// And every transition attachment depends-on the per-rig state
	// bead — not (say) the MR bead.
	if len(f.attachments) == 0 {
		t.Fatal("expected a transition attachment, got none")
	}
	for _, a := range f.attachments {
		if a.ParentID != "gastown_upstream-auto-test-state" {
			t.Errorf("attachment parent = %q, want gastown_upstream-auto-test-state", a.ParentID)
		}
		// Per design, attachments carry rig:<rig> on every bead.
		if !labelSliceContains(a.Labels, "rig:gastown_upstream") {
			t.Errorf("attachment labels missing rig:gastown_upstream: %v", a.Labels)
		}
	}
}

// --- AC: merged → cooled-down ----------------------------------------------

func TestHandle_MergedPath_TransitionsToCooledDown(t *testing.T) {
	f := newFakeBeads()
	seedRigState(t, f, "gastown_upstream", rigStateMRPending)
	notifier := &fakeNotifier{}
	h := NewCycleCloseHandler(f, notifier, WithNowFunc(fixedClock(fixedNow)))

	if err := h.Handle(CycleCloseEvent{
		MRID:        "gu-mr-merged-1",
		TargetRig:   "gastown_upstream",
		CloseReason: CloseReasonMerged,
	}); err != nil {
		t.Fatalf("Handle merged: %v", err)
	}

	got := loadRigState(t, f, "gastown_upstream")
	if got.State != rigStateCooledDown {
		t.Errorf("rig state after merged = %q, want %q", got.State, rigStateCooledDown)
	}
	// Round-trip preservation: cadence_days, language must survive.
	if _, ok := got.Other["cadence_days"]; !ok {
		t.Error("merged path clobbered cadence_days field — handler MUST round-trip non-owned fields")
	}
	if _, ok := got.Other["language"]; !ok {
		t.Error("merged path clobbered language field")
	}
}

func TestHandle_MergedPath_FilesOneTransitionAttachment(t *testing.T) {
	f := newFakeBeads()
	seedRigState(t, f, "gastown_upstream", rigStateMRPending)
	h := NewCycleCloseHandler(f, &fakeNotifier{}, WithNowFunc(fixedClock(fixedNow)))

	if err := h.Handle(CycleCloseEvent{
		MRID:        "gu-mr-merged-2",
		TargetRig:   "gastown_upstream",
		CloseReason: CloseReasonMerged,
	}); err != nil {
		t.Fatalf("Handle merged: %v", err)
	}

	if len(f.attachments) != 1 {
		t.Fatalf("merged path filed %d attachments, want 1 (transition only)", len(f.attachments))
	}
	att := f.attachments[0]
	if !labelSliceContains(att.Labels, labelKindTransition) {
		t.Errorf("merged attachment missing kind:transition: %v", att.Labels)
	}
	if labelSliceContains(att.Labels, labelKindRejection) {
		t.Errorf("merged attachment must NOT carry kind:rejection: %v", att.Labels)
	}

	var tr Transition
	if err := json.Unmarshal(att.Metadata, &tr); err != nil {
		t.Fatalf("unmarshal transition metadata: %v", err)
	}
	if tr.From != rigStateMRPending || tr.To != rigStateCooledDown {
		t.Errorf("transition (from,to) = (%q,%q), want (%q,%q)",
			tr.From, tr.To, rigStateMRPending, rigStateCooledDown)
	}
	if tr.Rig != "gastown_upstream" {
		t.Errorf("transition.rig = %q, want gastown_upstream", tr.Rig)
	}
	if tr.Context["mr_id"] != "gu-mr-merged-2" {
		t.Errorf("transition.context.mr_id = %v, want gu-mr-merged-2", tr.Context["mr_id"])
	}
}

func TestHandle_MergedPath_DoesNotIncrementTownCounter(t *testing.T) {
	// Merged path is a SUCCESS — the town circuit-breaker counter
	// counts close-unmerged events only.
	f := newFakeBeads()
	seedRigState(t, f, "gastown_upstream", rigStateMRPending)
	seedTownState(t, f)
	h := NewCycleCloseHandler(f, &fakeNotifier{}, WithNowFunc(fixedClock(fixedNow)))

	if err := h.Handle(CycleCloseEvent{
		MRID:        "gu-mr-merged-3",
		TargetRig:   "gastown_upstream",
		CloseReason: CloseReasonMerged,
	}); err != nil {
		t.Fatalf("Handle merged: %v", err)
	}

	got := loadTownState(t, f)
	if got.CircuitBreaker.Count != 0 {
		t.Errorf("merged path bumped town breaker count to %d, want 0", got.CircuitBreaker.Count)
	}
}

func TestHandle_MergedPath_FailsWhenStateNotMRPending(t *testing.T) {
	// CAS-from-mr-pending only: merged event when the rig is in `idle`
	// (e.g., dog observed an MR but the state machine moved on) should
	// fail loudly so a human can investigate.
	f := newFakeBeads()
	seedRigState(t, f, "gastown_upstream", "idle")
	h := NewCycleCloseHandler(f, &fakeNotifier{}, WithNowFunc(fixedClock(fixedNow)))

	err := h.Handle(CycleCloseEvent{
		MRID:        "gu-mr-merged-stale",
		TargetRig:   "gastown_upstream",
		CloseReason: CloseReasonMerged,
	})
	if err == nil {
		t.Fatal("expected CAS failure when state != mr-pending, got nil")
	}
	if !strings.Contains(err.Error(), "rig-state CAS failed") {
		t.Errorf("error message %q does not mention CAS", err.Error())
	}
}

// --- AC: closed-unmerged → cooled-down + rejection log ---------------------

func TestHandle_RejectedPath_TransitionsToCooledDown(t *testing.T) {
	f := newFakeBeads()
	seedRigState(t, f, "gastown_upstream", rigStateMRPending)
	seedTownState(t, f)
	h := NewCycleCloseHandler(f, &fakeNotifier{}, WithNowFunc(fixedClock(fixedNow)))

	if err := h.Handle(CycleCloseEvent{
		MRID:        "gu-mr-rejected-1",
		TargetRig:   "gastown_upstream",
		CloseReason: CloseReasonRejected,
		Body:        "target_path: internal/foo/bar.go\n",
	}); err != nil {
		t.Fatalf("Handle rejected: %v", err)
	}

	got := loadRigState(t, f, "gastown_upstream")
	if got.State != rigStateCooledDown {
		t.Errorf("rig state after rejected = %q, want %q", got.State, rigStateCooledDown)
	}
}

func TestHandle_RejectedPath_FilesTransitionAndRejectionAttachments(t *testing.T) {
	f := newFakeBeads()
	seedRigState(t, f, "gastown_upstream", rigStateMRPending)
	seedTownState(t, f)
	h := NewCycleCloseHandler(f, &fakeNotifier{}, WithNowFunc(fixedClock(fixedNow)))

	if err := h.Handle(CycleCloseEvent{
		MRID:        "gu-mr-rejected-2",
		TargetRig:   "gastown_upstream",
		CloseReason: CloseReasonRejected,
		Body:        "target_path: internal/foo/bar.go\nclose_reason: rejected\n",
	}); err != nil {
		t.Fatalf("Handle rejected: %v", err)
	}

	if len(f.attachments) != 2 {
		t.Fatalf("rejected path filed %d attachments, want 2 (transition + rejection)",
			len(f.attachments))
	}

	var sawTransition, sawRejection bool
	for _, att := range f.attachments {
		switch {
		case labelSliceContains(att.Labels, labelKindTransition):
			sawTransition = true
			var tr Transition
			if err := json.Unmarshal(att.Metadata, &tr); err != nil {
				t.Fatalf("unmarshal transition: %v", err)
			}
			if outcome, _ := tr.Context["outcome"].(string); outcome != "rejected" {
				t.Errorf("rejected-path transition.context.outcome = %v, want rejected",
					tr.Context["outcome"])
			}
		case labelSliceContains(att.Labels, labelKindRejection):
			sawRejection = true
			var rj Rejection
			if err := json.Unmarshal(att.Metadata, &rj); err != nil {
				t.Fatalf("unmarshal rejection: %v", err)
			}
			if rj.File != "internal/foo/bar.go" {
				t.Errorf("rejection.file = %q, want internal/foo/bar.go", rj.File)
			}
			if rj.MRID != "gu-mr-rejected-2" {
				t.Errorf("rejection.mr_id = %q, want gu-mr-rejected-2", rj.MRID)
			}
			if rj.RejectedAt != fixedNow.Format(time.RFC3339) {
				t.Errorf("rejection.rejected_at = %q, want %q",
					rj.RejectedAt, fixedNow.Format(time.RFC3339))
			}
			wantCooldown := fixedNow.Add(rejectionCooldownPerFile).Format(time.RFC3339)
			if rj.CooldownUntil != wantCooldown {
				t.Errorf("rejection.cooldown_until = %q, want %q (rejected_at + 21d)",
					rj.CooldownUntil, wantCooldown)
			}
		}
	}
	if !sawTransition {
		t.Error("rejected path missing kind:transition attachment")
	}
	if !sawRejection {
		t.Error("rejected path missing kind:rejection attachment")
	}
}

func TestHandle_RejectedPath_IncrementsTownCircuitBreakerCounter(t *testing.T) {
	f := newFakeBeads()
	seedRigState(t, f, "gastown_upstream", rigStateMRPending)
	seedTownState(t, f)
	h := NewCycleCloseHandler(f, &fakeNotifier{}, WithNowFunc(fixedClock(fixedNow)))

	if err := h.Handle(CycleCloseEvent{
		MRID:        "gu-mr-rejected-3",
		TargetRig:   "gastown_upstream",
		CloseReason: CloseReasonRejected,
	}); err != nil {
		t.Fatalf("Handle rejected: %v", err)
	}

	got := loadTownState(t, f)
	if got.CircuitBreaker.Count != 1 {
		t.Errorf("town breaker count = %d, want 1", got.CircuitBreaker.Count)
	}
}

func TestHandle_RejectedPath_RejectionMissingTargetPathYieldsEmptyFile(t *testing.T) {
	// If the polecat MR didn't carry target_path:, the rejection
	// attachment records file="" — the materializer treats that as "no
	// per-file cooldown applies", which is the correct fallback.
	f := newFakeBeads()
	seedRigState(t, f, "gastown_upstream", rigStateMRPending)
	seedTownState(t, f)
	h := NewCycleCloseHandler(f, &fakeNotifier{}, WithNowFunc(fixedClock(fixedNow)))

	if err := h.Handle(CycleCloseEvent{
		MRID:        "gu-mr-rejected-no-target",
		TargetRig:   "gastown_upstream",
		CloseReason: CloseReasonRejected,
		Body:        "no target path here",
	}); err != nil {
		t.Fatalf("Handle rejected: %v", err)
	}

	for _, att := range f.attachments {
		if !labelSliceContains(att.Labels, labelKindRejection) {
			continue
		}
		var rj Rejection
		if err := json.Unmarshal(att.Metadata, &rj); err != nil {
			t.Fatalf("unmarshal rejection: %v", err)
		}
		if rj.File != "" {
			t.Errorf("rejection.file = %q, want empty (no target_path: in body)", rj.File)
		}
	}
}

// --- AC: 3-closes-in-7d → paused-by-circuit-breaker ------------------------

func TestHandle_CircuitBreaker_TripsAtThreeClosesInSevenDays(t *testing.T) {
	f := newFakeBeads()
	seedRigState(t, f, "gastown_upstream", rigStateMRPending)
	seedTownState(t, f)

	// Pre-seed two prior closed-unmerged transitions inside the rolling
	// 7-day window. The third (the one we Handle) will tip past the
	// threshold and trip the breaker.
	prior1 := fixedNow.Add(-72 * time.Hour) // 3 days ago
	prior2 := fixedNow.Add(-24 * time.Hour) // 1 day ago
	f.transitions["gastown_upstream"] = []Transition{
		{
			SchemaVersion: 1, Rig: "gastown_upstream",
			From: rigStateMRPending, To: rigStateCooledDown,
			At:    prior1.Format(time.RFC3339),
			Actor: "refinery",
			Context: map[string]interface{}{
				"mr_id": "gu-mr-prior1", "outcome": "rejected",
			},
		},
		{
			SchemaVersion: 1, Rig: "gastown_upstream",
			From: rigStateMRPending, To: rigStateCooledDown,
			At:    prior2.Format(time.RFC3339),
			Actor: "refinery",
			Context: map[string]interface{}{
				"mr_id": "gu-mr-prior2", "outcome": "rejected",
			},
		},
	}

	// The handler also adds the just-handled transition to the count.
	// We simulate that by appending it to the fake's list inside
	// CreateAttachment — but the simpler approach is to pre-add it: the
	// production code reads transitions AFTER writing the new one.
	f.transitions["gastown_upstream"] = append(f.transitions["gastown_upstream"], Transition{
		SchemaVersion: 1, Rig: "gastown_upstream",
		From: rigStateMRPending, To: rigStateCooledDown,
		At:    fixedNow.Format(time.RFC3339),
		Actor: "refinery",
		Context: map[string]interface{}{
			"mr_id": "gu-mr-trip", "outcome": "rejected",
		},
	})

	notifier := &fakeNotifier{}
	h := NewCycleCloseHandler(f, notifier, WithNowFunc(fixedClock(fixedNow)))

	if err := h.Handle(CycleCloseEvent{
		MRID:        "gu-mr-trip",
		TargetRig:   "gastown_upstream",
		CloseReason: CloseReasonRejected,
	}); err != nil {
		t.Fatalf("Handle rejected (breaker): %v", err)
	}

	got := loadRigState(t, f, "gastown_upstream")
	if got.State != rigStatePausedByCircuitBreaker {
		t.Errorf("rig state after breaker trip = %q, want %q",
			got.State, rigStatePausedByCircuitBreaker)
	}

	// Overseer must have been notified.
	if len(notifier.calls) != 1 {
		t.Fatalf("expected 1 Overseer notify, got %d", len(notifier.calls))
	}
	call := notifier.calls[0]
	if !strings.Contains(call.Subject, "circuit breaker") {
		t.Errorf("notify subject = %q, want it to mention 'circuit breaker'", call.Subject)
	}
	if !strings.Contains(call.Body, "gastown_upstream") {
		t.Errorf("notify body missing rig name; body=%q", call.Body)
	}

	// The breaker-trip transition is filed as a separate attachment in
	// addition to the cooled-down transition + rejection.
	var breakerTransitions int
	for _, att := range f.attachments {
		if !labelSliceContains(att.Labels, labelKindTransition) {
			continue
		}
		var tr Transition
		if err := json.Unmarshal(att.Metadata, &tr); err != nil {
			t.Fatalf("unmarshal transition: %v", err)
		}
		if tr.To == rigStatePausedByCircuitBreaker {
			breakerTransitions++
			if trigger, _ := tr.Context["trigger"].(string); trigger != "circuit-breaker" {
				t.Errorf("breaker transition.trigger = %v, want circuit-breaker", trigger)
			}
		}
	}
	if breakerTransitions != 1 {
		t.Errorf("expected 1 breaker-trip transition attachment, got %d", breakerTransitions)
	}
}

func TestHandle_CircuitBreaker_DoesNotTripWithTwoClosesInWindow(t *testing.T) {
	// Two prior closes + the current close = 3 — so this should TRIP.
	// We assert the boundary the OTHER way: only one prior close +
	// current = 2 in window → no trip.
	f := newFakeBeads()
	seedRigState(t, f, "gastown_upstream", rigStateMRPending)
	seedTownState(t, f)

	f.transitions["gastown_upstream"] = []Transition{
		{
			SchemaVersion: 1, Rig: "gastown_upstream",
			From: rigStateMRPending, To: rigStateCooledDown,
			At:    fixedNow.Add(-48 * time.Hour).Format(time.RFC3339),
			Actor: "refinery",
			Context: map[string]interface{}{
				"mr_id": "gu-mr-old", "outcome": "rejected",
			},
		},
		{
			SchemaVersion: 1, Rig: "gastown_upstream",
			From: rigStateMRPending, To: rigStateCooledDown,
			At:    fixedNow.Format(time.RFC3339),
			Actor: "refinery",
			Context: map[string]interface{}{
				"mr_id": "gu-mr-current", "outcome": "rejected",
			},
		},
	}

	notifier := &fakeNotifier{}
	h := NewCycleCloseHandler(f, notifier, WithNowFunc(fixedClock(fixedNow)))

	if err := h.Handle(CycleCloseEvent{
		MRID:        "gu-mr-current",
		TargetRig:   "gastown_upstream",
		CloseReason: CloseReasonRejected,
	}); err != nil {
		t.Fatalf("Handle rejected: %v", err)
	}

	got := loadRigState(t, f, "gastown_upstream")
	if got.State == rigStatePausedByCircuitBreaker {
		t.Error("breaker tripped at 2 closes in 7d — should require ≥3")
	}
	if got.State != rigStateCooledDown {
		t.Errorf("rig state = %q, want %q", got.State, rigStateCooledDown)
	}
	if len(notifier.calls) != 0 {
		t.Errorf("Overseer notified for sub-threshold close count: %v", notifier.calls)
	}
}

func TestHandle_CircuitBreaker_IgnoresClosesOutsideSevenDayWindow(t *testing.T) {
	// Three closes total but two are stale (>7d ago). Only the current
	// close is inside the window → 1 close in window → no trip.
	f := newFakeBeads()
	seedRigState(t, f, "gastown_upstream", rigStateMRPending)
	seedTownState(t, f)

	stale := fixedNow.Add(-10 * 24 * time.Hour)
	f.transitions["gastown_upstream"] = []Transition{
		{
			SchemaVersion: 1, Rig: "gastown_upstream",
			From: rigStateMRPending, To: rigStateCooledDown,
			At:    stale.Format(time.RFC3339),
			Actor: "refinery",
			Context: map[string]interface{}{
				"mr_id": "gu-mr-stale1", "outcome": "rejected",
			},
		},
		{
			SchemaVersion: 1, Rig: "gastown_upstream",
			From: rigStateMRPending, To: rigStateCooledDown,
			At:    stale.Add(-time.Hour).Format(time.RFC3339),
			Actor: "refinery",
			Context: map[string]interface{}{
				"mr_id": "gu-mr-stale2", "outcome": "rejected",
			},
		},
		{
			SchemaVersion: 1, Rig: "gastown_upstream",
			From: rigStateMRPending, To: rigStateCooledDown,
			At:    fixedNow.Format(time.RFC3339),
			Actor: "refinery",
			Context: map[string]interface{}{
				"mr_id": "gu-mr-current", "outcome": "rejected",
			},
		},
	}

	notifier := &fakeNotifier{}
	h := NewCycleCloseHandler(f, notifier, WithNowFunc(fixedClock(fixedNow)))

	if err := h.Handle(CycleCloseEvent{
		MRID:        "gu-mr-current",
		TargetRig:   "gastown_upstream",
		CloseReason: CloseReasonRejected,
	}); err != nil {
		t.Fatalf("Handle rejected: %v", err)
	}

	got := loadRigState(t, f, "gastown_upstream")
	if got.State == rigStatePausedByCircuitBreaker {
		t.Error("breaker tripped including stale closes — must filter by 7-day window")
	}
	if len(notifier.calls) != 0 {
		t.Errorf("Overseer notified despite stale-only closes: %v", notifier.calls)
	}
}

func TestHandle_CircuitBreaker_IgnoresMergedTransitionsInCount(t *testing.T) {
	// Merged-outcome transitions DO NOT count toward the breaker.
	f := newFakeBeads()
	seedRigState(t, f, "gastown_upstream", rigStateMRPending)
	seedTownState(t, f)

	// Three transitions inside the window — but two were merges.
	f.transitions["gastown_upstream"] = []Transition{
		{
			SchemaVersion: 1, Rig: "gastown_upstream",
			From: rigStateMRPending, To: rigStateCooledDown,
			At:    fixedNow.Add(-48 * time.Hour).Format(time.RFC3339),
			Actor: "refinery",
			// No outcome key (or outcome=merged): must be skipped.
			Context: map[string]interface{}{"mr_id": "gu-mr-merged-x"},
		},
		{
			SchemaVersion: 1, Rig: "gastown_upstream",
			From: rigStateMRPending, To: rigStateCooledDown,
			At:    fixedNow.Add(-24 * time.Hour).Format(time.RFC3339),
			Actor: "refinery",
			Context: map[string]interface{}{
				"mr_id": "gu-mr-merged-y", "outcome": "merged",
			},
		},
		{
			SchemaVersion: 1, Rig: "gastown_upstream",
			From: rigStateMRPending, To: rigStateCooledDown,
			At:    fixedNow.Format(time.RFC3339),
			Actor: "refinery",
			Context: map[string]interface{}{
				"mr_id": "gu-mr-current", "outcome": "rejected",
			},
		},
	}

	notifier := &fakeNotifier{}
	h := NewCycleCloseHandler(f, notifier, WithNowFunc(fixedClock(fixedNow)))

	if err := h.Handle(CycleCloseEvent{
		MRID:        "gu-mr-current",
		TargetRig:   "gastown_upstream",
		CloseReason: CloseReasonRejected,
	}); err != nil {
		t.Fatalf("Handle rejected: %v", err)
	}

	got := loadRigState(t, f, "gastown_upstream")
	if got.State == rigStatePausedByCircuitBreaker {
		t.Error("breaker counted merged transitions — must filter by outcome=rejected")
	}
}

// --- AC: BUG-DISCOVERED: NOTES → P2 bug bead filed -------------------------

func TestHandle_BugDiscovered_FilesP2BugBead(t *testing.T) {
	body := `
This MR fixes a coverage gap.

BUG-DISCOVERED: internal/foo/bar.go:42
expected: 7
actual: 5
test:
  func TestFooReturnsSeven(t *testing.T) {
      if got := foo.Compute(); got != 7 {
          t.Errorf("got %d, want 7", got)
      }
  }
`
	f := newFakeBeads()
	seedRigState(t, f, "gastown_upstream", rigStateMRPending)
	seedTownState(t, f)
	h := NewCycleCloseHandler(f, &fakeNotifier{}, WithNowFunc(fixedClock(fixedNow)))

	if err := h.Handle(CycleCloseEvent{
		MRID:        "gu-mr-buggy",
		TargetRig:   "gastown_upstream",
		CloseReason: CloseReasonRejected,
		Body:        body,
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(f.bugBeads) != 1 {
		t.Fatalf("expected 1 P2 bug bead filed, got %d", len(f.bugBeads))
	}
	bug := f.bugBeads[0]
	if !strings.Contains(bug.Title, "internal/foo/bar.go:42") {
		t.Errorf("bug title missing location; got %q", bug.Title)
	}
	if !labelSliceContains(bug.Labels, "gt:bug") {
		t.Errorf("bug labels missing gt:bug: %v", bug.Labels)
	}
	if !labelSliceContains(bug.Labels, "rig:gastown_upstream") {
		t.Errorf("bug labels missing rig:gastown_upstream: %v", bug.Labels)
	}
	if bug.ParentID != "gu-mr-buggy" {
		t.Errorf("bug parent (depends_on) = %q, want gu-mr-buggy (the MR bead)", bug.ParentID)
	}
	if !strings.Contains(bug.Body, "Expected: 7") || !strings.Contains(bug.Body, "Actual: 5") {
		t.Errorf("bug body missing expected/actual values; got %q", bug.Body)
	}
	if !strings.Contains(bug.Body, "TestFooReturnsSeven") {
		t.Errorf("bug body missing candidate test source; got %q", bug.Body)
	}
}

func TestHandle_BugDiscovered_FilesOneBugBeadPerOccurrence(t *testing.T) {
	body := `
BUG-DISCOVERED: internal/a.go:10
expected: 1
actual: 2

BUG-DISCOVERED: internal/b.go:20
expected: x
actual: y
`
	f := newFakeBeads()
	seedRigState(t, f, "gastown_upstream", rigStateMRPending)
	h := NewCycleCloseHandler(f, &fakeNotifier{}, WithNowFunc(fixedClock(fixedNow)))

	if err := h.Handle(CycleCloseEvent{
		MRID:        "gu-mr-multi-bug",
		TargetRig:   "gastown_upstream",
		CloseReason: CloseReasonMerged,
		Body:        body,
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(f.bugBeads) != 2 {
		t.Fatalf("expected 2 bug beads (one per BUG-DISCOVERED block), got %d", len(f.bugBeads))
	}
}

func TestHandle_BugDiscovered_NoNotesNoBugBead(t *testing.T) {
	f := newFakeBeads()
	seedRigState(t, f, "gastown_upstream", rigStateMRPending)
	h := NewCycleCloseHandler(f, &fakeNotifier{}, WithNowFunc(fixedClock(fixedNow)))

	if err := h.Handle(CycleCloseEvent{
		MRID:        "gu-mr-clean",
		TargetRig:   "gastown_upstream",
		CloseReason: CloseReasonMerged,
		Body:        "no bugs here\n",
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(f.bugBeads) != 0 {
		t.Errorf("expected 0 bug beads on body without BUG-DISCOVERED, got %d", len(f.bugBeads))
	}
}

func TestHandle_BugDiscovered_BugFilingFailureDoesNotBlockStateTransition(t *testing.T) {
	// The CR's design treats bug-bead filing as best-effort. A
	// transient CreateBugBead failure must NOT prevent the state
	// transition / breaker logic from running.
	f := newFakeBeads()
	seedRigState(t, f, "gastown_upstream", rigStateMRPending)
	f.errOnCreateBug = errors.New("bd timeout")

	h := NewCycleCloseHandler(f, &fakeNotifier{}, WithNowFunc(fixedClock(fixedNow)))

	if err := h.Handle(CycleCloseEvent{
		MRID:        "gu-mr-bug-fails",
		TargetRig:   "gastown_upstream",
		CloseReason: CloseReasonMerged,
		Body:        "BUG-DISCOVERED: x.go:1\nexpected: a\nactual: b\n",
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	got := loadRigState(t, f, "gastown_upstream")
	if got.State != rigStateCooledDown {
		t.Errorf("state transition blocked by bug-bead failure: state=%q", got.State)
	}
}

// --- ParseBugDiscovered unit coverage ---

func TestParseBugDiscovered_EmptyBody(t *testing.T) {
	if got := ParseBugDiscovered(""); got != nil {
		t.Errorf("ParseBugDiscovered(\"\") = %v, want nil", got)
	}
}

func TestParseBugDiscovered_WellFormedBlock(t *testing.T) {
	body := `BUG-DISCOVERED: foo.go:42
expected: 1
actual: 2
test:
  one
  two
`
	bugs := ParseBugDiscovered(body)
	if len(bugs) != 1 {
		t.Fatalf("len = %d, want 1", len(bugs))
	}
	bug := bugs[0]
	if bug.File != "foo.go" || bug.Line != 42 {
		t.Errorf("(file,line) = (%q,%d), want (foo.go,42)", bug.File, bug.Line)
	}
	if bug.Expected != "1" || bug.Actual != "2" {
		t.Errorf("(expected,actual) = (%q,%q), want (1,2)", bug.Expected, bug.Actual)
	}
	if !strings.Contains(bug.TestSource, "one") || !strings.Contains(bug.TestSource, "two") {
		t.Errorf("test source missing slurped lines: %q", bug.TestSource)
	}
}

func TestParseBugDiscovered_MultipleBlocks(t *testing.T) {
	body := `Some prose.

BUG-DISCOVERED: a.go:1
expected: alpha
actual: beta

Other prose.

BUG-DISCOVERED: b.go:2
expected: x
actual: y
`
	bugs := ParseBugDiscovered(body)
	if len(bugs) != 2 {
		t.Fatalf("len = %d, want 2", len(bugs))
	}
	if bugs[0].File != "a.go" || bugs[1].File != "b.go" {
		t.Errorf("files = (%q,%q), want (a.go,b.go)", bugs[0].File, bugs[1].File)
	}
}

func TestParseBugDiscovered_HeaderOnlyBlock(t *testing.T) {
	// Malformed but should still produce an entry so the human sees it.
	bugs := ParseBugDiscovered("BUG-DISCOVERED: x.go:1\n")
	if len(bugs) != 1 {
		t.Fatalf("len = %d, want 1", len(bugs))
	}
	if bugs[0].Expected != "" || bugs[0].Actual != "" {
		t.Errorf("expected/actual should be empty for header-only block, got %q/%q",
			bugs[0].Expected, bugs[0].Actual)
	}
}

// --- Validation paths ---

func TestHandle_RejectsEmptyTargetRig(t *testing.T) {
	h := NewCycleCloseHandler(newFakeBeads(), &fakeNotifier{}, WithNowFunc(fixedClock(fixedNow)))
	err := h.Handle(CycleCloseEvent{MRID: "x", CloseReason: CloseReasonMerged})
	if err == nil {
		t.Fatal("expected error for empty TargetRig, got nil")
	}
}

func TestHandle_RejectsEmptyCloseReason(t *testing.T) {
	h := NewCycleCloseHandler(newFakeBeads(), &fakeNotifier{}, WithNowFunc(fixedClock(fixedNow)))
	err := h.Handle(CycleCloseEvent{MRID: "x", TargetRig: "gastown_upstream"})
	if err == nil {
		t.Fatal("expected error for empty CloseReason, got nil")
	}
}

func TestHandle_UnknownCloseReason_NoStateChange(t *testing.T) {
	// Unknown close_reason values (e.g., "superseded") log but don't
	// transition. State must stay where it was.
	f := newFakeBeads()
	seedRigState(t, f, "gastown_upstream", rigStateMRPending)
	h := NewCycleCloseHandler(f, &fakeNotifier{}, WithNowFunc(fixedClock(fixedNow)))

	if err := h.Handle(CycleCloseEvent{
		MRID:        "gu-mr-superseded",
		TargetRig:   "gastown_upstream",
		CloseReason: "superseded",
	}); err != nil {
		t.Fatalf("Handle superseded: %v", err)
	}

	got := loadRigState(t, f, "gastown_upstream")
	if got.State != rigStateMRPending {
		t.Errorf("unknown close_reason changed state to %q, want %q (no change)",
			got.State, rigStateMRPending)
	}
	if len(f.attachments) != 0 {
		t.Errorf("unknown close_reason filed %d attachments, want 0",
			len(f.attachments))
	}
}

// --- helpers ---

// labelSliceContains is the test-side equivalent of HasLabel that operates
// on raw string slices (the captured-attachment shape).
func labelSliceContains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
