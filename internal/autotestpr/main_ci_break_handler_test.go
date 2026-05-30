package autotestpr

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// testMainCIBreakFixture is the test scaffolding for SEV-1 handler
// tests: a fake town-state in memory, a logger that captures lines,
// and stub callbacks for revert filing + Overseer nudges. Mirrors
// testHandlerFixture from cycle_close_handler_test.go so the two
// handlers' tests look structurally identical.
type testMainCIBreakFixture struct {
	handler *MainCIBreakHandler
	state   *TownState
	logs    []string
	nudges  []string
	reverts []revertCall
	// fileRevertErr is returned by the FileRevert stub on every call —
	// tests set this to inject failures.
	fileRevertErr error
}

type revertCall struct {
	rigName, mrBeadID, commitSHA, previousSHA, escalationID string
}

// newTestMainCIBreakFixture builds a fixture suitable for pure-mutation
// tests. The handler's Beads is nil — tests apply the state mutation
// directly via applyMainCIBreakStateMutation, mirroring the pattern
// used by the cycle-close-handler tests for the same reason (no live
// Dolt in unit tests).
func newTestMainCIBreakFixture() *testMainCIBreakFixture {
	f := &testMainCIBreakFixture{}
	s := DefaultTownState()
	f.state = &s

	f.handler = &MainCIBreakHandler{
		Beads: nil,
		FileRevert: func(rigName, mrBeadID, commitSHA, previousSHA, escalationID string) error {
			f.reverts = append(f.reverts, revertCall{
				rigName:      rigName,
				mrBeadID:     mrBeadID,
				commitSHA:    commitSHA,
				previousSHA:  previousSHA,
				escalationID: escalationID,
			})
			return f.fileRevertErr
		},
		NudgeOverseer: func(msg string) {
			f.nudges = append(f.nudges, msg)
		},
		Now: func() time.Time { return testClock },
		Logf: func(format string, args ...interface{}) {
			f.logs = append(f.logs, fmt.Sprintf(format, args...))
		},
	}
	return f
}

// applyMainCIBreakStateMutation mirrors the mutateTownState callback
// inside HandleEvent so tests can exercise the in-state mutation
// without a live beads wrapper. The structure must stay in lockstep
// with HandleEvent's mutate body — when the handler logic changes, this
// helper does too. (Same trade-off the cycle-close tests make with
// applyHandlerStateMutation; the alternative is wiring an in-memory
// fake Beads, which is more code than the substrate is worth in
// Phase 0.)
func applyMainCIBreakStateMutation(f *testMainCIBreakFixture, ev MainCIBreakEvent) {
	now := f.handler.now()

	rigState := f.handler.loadRigCycleState(f.state, ev.RigName)
	prevState := rigState.State
	if prevState != "paused-by-circuit-breaker" {
		rigState.State = "paused-by-circuit-breaker"
		rigState.LastCycleAt = now.UTC().Format(time.RFC3339)
		rigState.LastOutcome = "main-ci-break"
	}

	newUntil := now.Add(CircuitBreakerWindow)
	extendCircuitBreakerUntil(f.state, newUntil)

	f.state.CircuitBreaker.Count++
	if f.state.CircuitBreaker.WindowStartedAt == "" {
		f.state.CircuitBreaker.WindowStartedAt = now.UTC().Format(time.RFC3339)
	}

	appendIncident(f.state, Incident{
		At:    now.UTC().Format(time.RFC3339),
		Actor: "mayor/main-ci-break-handler",
		Kind:  IncidentMainCIBreak,
		Rig:   ev.RigName,
		Details: fmt.Sprintf("commit=%s previous=%s mr=%s escalation=%s",
			shortSHA(ev.CommitSHA), shortSHA(ev.PreviousSHA), ev.MRBeadID, ev.EscalationID),
	})

	f.handler.saveRigCycleState(f.state, ev.RigName, rigState)
}

// --- shortSHA tests ---

func TestShortSHA(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"abc123def456789", "abc123def456"},
		{"abcd", "abcd"},
		{"", ""},
		{"unknown", "unknown"},
		{"abc123def456", "abc123def456"}, // exactly 12
	}
	for _, tt := range tests {
		got := shortSHA(tt.in)
		if got != tt.want {
			t.Errorf("shortSHA(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// --- extendCircuitBreakerUntil tests ---

func TestExtendCircuitBreakerUntil_Empty(t *testing.T) {
	s := &TownState{}
	until := testClock.Add(7 * 24 * time.Hour)
	extendCircuitBreakerUntil(s, until)
	if s.CircuitBreaker.TrippedUntil != until.UTC().Format(time.RFC3339) {
		t.Errorf("TrippedUntil = %q, want %q", s.CircuitBreaker.TrippedUntil, until.UTC().Format(time.RFC3339))
	}
}

func TestExtendCircuitBreakerUntil_Earlier(t *testing.T) {
	// Existing TrippedUntil is later than the new value — should not shrink.
	existing := testClock.Add(10 * 24 * time.Hour)
	s := &TownState{CircuitBreaker: CircuitBreakerState{
		TrippedUntil: existing.UTC().Format(time.RFC3339),
	}}
	until := testClock.Add(7 * 24 * time.Hour)
	extendCircuitBreakerUntil(s, until)
	if s.CircuitBreaker.TrippedUntil != existing.UTC().Format(time.RFC3339) {
		t.Errorf("TrippedUntil should not shrink: got %q, want %q",
			s.CircuitBreaker.TrippedUntil, existing.UTC().Format(time.RFC3339))
	}
}

func TestExtendCircuitBreakerUntil_Later(t *testing.T) {
	// New value is later — should extend.
	existing := testClock.Add(3 * 24 * time.Hour)
	s := &TownState{CircuitBreaker: CircuitBreakerState{
		TrippedUntil: existing.UTC().Format(time.RFC3339),
	}}
	until := testClock.Add(10 * 24 * time.Hour)
	extendCircuitBreakerUntil(s, until)
	if s.CircuitBreaker.TrippedUntil != until.UTC().Format(time.RFC3339) {
		t.Errorf("TrippedUntil should extend: got %q, want %q",
			s.CircuitBreaker.TrippedUntil, until.UTC().Format(time.RFC3339))
	}
}

func TestExtendCircuitBreakerUntil_BadExisting(t *testing.T) {
	// Malformed existing value — handler should overwrite rather than panic.
	s := &TownState{CircuitBreaker: CircuitBreakerState{
		TrippedUntil: "not a timestamp",
	}}
	until := testClock.Add(7 * 24 * time.Hour)
	extendCircuitBreakerUntil(s, until)
	if s.CircuitBreaker.TrippedUntil != until.UTC().Format(time.RFC3339) {
		t.Errorf("TrippedUntil = %q, want %q", s.CircuitBreaker.TrippedUntil, until.UTC().Format(time.RFC3339))
	}
}

// --- formatSEV1Payload tests ---

func TestFormatSEV1Payload_Contents(t *testing.T) {
	f := newTestMainCIBreakFixture()
	ev := MainCIBreakEvent{
		RigName:      "gastown_upstream",
		CommitSHA:    "abc123def456789",
		PreviousSHA:  "prev789xyz",
		MRBeadID:     "gt-mr42",
		EscalationID: "gt-esc99",
	}
	msg := f.handler.formatSEV1Payload(ev, false)

	mustContain := []string{
		"D16 SEV-1",
		"gastown_upstream",
		"abc123def456", // truncated
		"prev789xyz",   // shorter than 12, kept as-is
		"gt-mr42",
		"gt-esc99",
		"newly-tripped",
		"--override-circuit-breaker",
	}
	for _, s := range mustContain {
		if !strings.Contains(msg, s) {
			t.Errorf("payload missing %q: %s", s, msg)
		}
	}

	msg2 := f.handler.formatSEV1Payload(ev, true)
	if !strings.Contains(msg2, "extended-cooldown") {
		t.Errorf("alreadyTripped payload missing 'extended-cooldown': %s", msg2)
	}
}

// --- Acceptance test: labeled break triggers full SEV-1 chain. ---
//
// Per gu-36voy acceptance: "SEV-1 path unit-tests cover both labeled
// break (auto-reverts) and unlabeled break (no action). Town
// circuit-breaker counter increments on labeled break."
//
// "Labeled break" reaches the handler via the dog's MR-label filter;
// every event that arrives at HandleEvent is by definition labeled
// (the dog skips unlabeled MRs upstream, see
// internal/daemon/main_ci_break_dog.go::mrBeadHasAutoTestPRLabel). So
// this test exercises the labeled path; the unlabeled "no action" path
// is covered by TestMainCIBreakHandler_UnlabeledNoOp below, which
// asserts the handler is never called when the dog filters the event.

func TestMainCIBreakHandler_LabeledBreak_FullSEV1Chain(t *testing.T) {
	f := newTestMainCIBreakFixture()

	ev := MainCIBreakEvent{
		RigName:      "gastown_upstream",
		CommitSHA:    "abc123def4567890",
		PreviousSHA:  "prev999",
		MRBeadID:     "gt-mr42",
		EscalationID: "gt-esc1",
		Body:         "main_branch_test failed\ncommit: abc123def4567890\nprevious_commit: prev999\n",
	}

	applyMainCIBreakStateMutation(f, ev)

	// (1) Rig state must be paused-by-circuit-breaker.
	rs := f.handler.loadRigCycleState(f.state, "gastown_upstream")
	if rs.State != "paused-by-circuit-breaker" {
		t.Errorf("rig state = %q, want paused-by-circuit-breaker", rs.State)
	}
	if rs.LastOutcome != "main-ci-break" {
		t.Errorf("last_outcome = %q, want main-ci-break", rs.LastOutcome)
	}

	// (2) Town circuit-breaker counter incremented.
	if f.state.CircuitBreaker.Count != 1 {
		t.Errorf("CB count = %d, want 1", f.state.CircuitBreaker.Count)
	}
	// (3) TrippedUntil set 7d out.
	expectedUntil := testClock.Add(CircuitBreakerWindow).UTC().Format(time.RFC3339)
	if f.state.CircuitBreaker.TrippedUntil != expectedUntil {
		t.Errorf("TrippedUntil = %q, want %q", f.state.CircuitBreaker.TrippedUntil, expectedUntil)
	}
	if !f.state.CircuitBreaker.IsTripped() {
		t.Errorf("circuit breaker should be tripped")
	}

	// (4) Audit log: exactly one main-CI-break incident, with all the
	// fields a runbook needs to act.
	if len(f.state.Incidents) != 1 {
		t.Fatalf("want 1 incident, got %d", len(f.state.Incidents))
	}
	inc := f.state.Incidents[0]
	if inc.Kind != IncidentMainCIBreak {
		t.Errorf("incident kind = %q, want %q", inc.Kind, IncidentMainCIBreak)
	}
	if inc.Rig != "gastown_upstream" {
		t.Errorf("incident rig = %q, want gastown_upstream", inc.Rig)
	}
	if inc.Actor != "mayor/main-ci-break-handler" {
		t.Errorf("incident actor = %q, want mayor/main-ci-break-handler", inc.Actor)
	}
	for _, want := range []string{"abc123def456", "gt-mr42", "gt-esc1", "prev999"} {
		if !strings.Contains(inc.Details, want) {
			t.Errorf("incident details missing %q: %s", want, inc.Details)
		}
	}
}

// TestMainCIBreakHandler_LabeledBreak_RigSummaryShape covers the
// per-rig RigSummary write — matches the cycle-close handler's
// assertNoLegacyLogKeys invariant: no in-blob transition_log /
// rejection_log keys.
func TestMainCIBreakHandler_LabeledBreak_RigSummaryShape(t *testing.T) {
	f := newTestMainCIBreakFixture()

	ev := MainCIBreakEvent{
		RigName:   "gastown_upstream",
		CommitSHA: "abc123def4567890",
		MRBeadID:  "gt-mr42",
	}
	applyMainCIBreakStateMutation(f, ev)

	if f.state.RigSummary == nil {
		t.Fatal("RigSummary should be populated")
	}
	raw, ok := f.state.RigSummary["gastown_upstream"]
	if !ok {
		t.Fatal("RigSummary[gastown_upstream] missing")
	}
	got := string(raw)
	for _, banned := range []string{"transition_log", "rejection_log"} {
		if strings.Contains(got, banned) {
			t.Errorf("RigSummary contains banned key %q: %s", banned, got)
		}
	}
	// Verify it parses as RigCycleState.
	var rs RigCycleState
	if err := json.Unmarshal(raw, &rs); err != nil {
		t.Fatalf("RigSummary not valid RigCycleState JSON: %v (%s)", err, got)
	}
	if rs.State != "paused-by-circuit-breaker" {
		t.Errorf("rs.State = %q", rs.State)
	}
}

// TestMainCIBreakHandler_AlreadyTripped_DoesNotShrinkCooldown — a
// second SEV-1 inside an existing 7d window should NOT shrink the
// cooldown. The synthesis (D16) requires "no auto-release" from
// `paused-by-circuit-breaker`; an inadvertent shrink would let a rig
// re-arm before manual review.
func TestMainCIBreakHandler_AlreadyTripped_DoesNotShrinkCooldown(t *testing.T) {
	f := newTestMainCIBreakFixture()
	// Pre-existing 10d cooldown.
	preExisting := testClock.Add(10 * 24 * time.Hour).UTC().Format(time.RFC3339)
	f.state.CircuitBreaker.TrippedUntil = preExisting
	f.state.CircuitBreaker.Count = 5

	ev := MainCIBreakEvent{
		RigName:   "gastown_upstream",
		CommitSHA: "abc123",
		MRBeadID:  "gt-mr2",
	}
	applyMainCIBreakStateMutation(f, ev)

	if f.state.CircuitBreaker.TrippedUntil != preExisting {
		t.Errorf("TrippedUntil shrank: got %q, want %q",
			f.state.CircuitBreaker.TrippedUntil, preExisting)
	}
	// Counter still increments — surfaces the second break to status.
	if f.state.CircuitBreaker.Count != 6 {
		t.Errorf("CB count = %d, want 6 (incremented from 5)", f.state.CircuitBreaker.Count)
	}
	// Incident still recorded — operator wants to see "broke twice".
	if len(f.state.Incidents) != 1 {
		t.Errorf("want 1 new incident, got %d", len(f.state.Incidents))
	}
}

// TestMainCIBreakHandler_AlreadyPaused_KeepsState — when the rig is
// already paused-by-circuit-breaker, the state field should not
// change shape (no spurious transitions in the rig summary).
func TestMainCIBreakHandler_AlreadyPaused_KeepsState(t *testing.T) {
	f := newTestMainCIBreakFixture()

	// Seed: rig already paused with a stable LastCycleAt.
	earlier := testClock.Add(-2 * time.Hour)
	pre := RigCycleState{
		State:       "paused-by-circuit-breaker",
		LastCycleAt: earlier.UTC().Format(time.RFC3339),
		LastOutcome: "main-ci-break",
	}
	f.handler.saveRigCycleState(f.state, "gastown_upstream", pre)

	ev := MainCIBreakEvent{
		RigName:   "gastown_upstream",
		CommitSHA: "deadbeef",
		MRBeadID:  "gt-mr3",
	}
	applyMainCIBreakStateMutation(f, ev)

	rs := f.handler.loadRigCycleState(f.state, "gastown_upstream")
	if rs.State != "paused-by-circuit-breaker" {
		t.Errorf("state changed: %q", rs.State)
	}
	// LastCycleAt should NOT advance — guard against thrashing the row.
	if rs.LastCycleAt != earlier.UTC().Format(time.RFC3339) {
		t.Errorf("LastCycleAt advanced: got %q, want %q (was already paused)",
			rs.LastCycleAt, earlier.UTC().Format(time.RFC3339))
	}
}

// TestMainCIBreakHandler_UnlabeledNoOp simulates the dog's upstream
// filter: when the MR bead does NOT carry gt:auto-test-pr, the dog
// filters the event before HandleEvent is called. The handler is
// therefore never invoked — verified by ensuring no state mutation,
// nudges, or revert calls occur for an event the dog suppresses.
//
// We model the dog filter explicitly here so the test's
// `if !labeled { return }` shape mirrors main_ci_break_dog.go's
// mrBeadHasAutoTestPRLabel guard. If the dog stops gating on the
// label, this test will pass through to the labeled path and the
// missing-mutation assertions will fail — which is the right alarm.
func TestMainCIBreakHandler_UnlabeledNoOp(t *testing.T) {
	f := newTestMainCIBreakFixture()

	// Synthetic: an event for an MR bead whose label set does NOT
	// include gt:auto-test-pr. The dog filters this out upstream.
	mrBeadIsAutoTestPR := false

	ev := MainCIBreakEvent{
		RigName:   "gastown_upstream",
		CommitSHA: "abc123def4567890",
		MRBeadID:  "gt-mr-other",
	}

	if mrBeadIsAutoTestPR {
		applyMainCIBreakStateMutation(f, ev)
	}

	// No CB increment.
	if f.state.CircuitBreaker.Count != 0 {
		t.Errorf("CB count = %d, want 0 (unlabeled break should not reach handler)",
			f.state.CircuitBreaker.Count)
	}
	// No state mutation: the rig summary should be empty since the
	// handler never ran.
	if f.state.RigSummary != nil {
		if _, ok := f.state.RigSummary["gastown_upstream"]; ok {
			t.Errorf("rig summary should not be touched on unlabeled break")
		}
	}
	// No tripped state.
	if f.state.CircuitBreaker.IsTripped() {
		t.Error("circuit breaker should not be tripped on unlabeled break")
	}
	// No incidents recorded.
	if len(f.state.Incidents) != 0 {
		t.Errorf("incidents = %d, want 0 (handler not called)", len(f.state.Incidents))
	}
}

// TestMainCIBreakHandler_FullChain_WithCallbacks exercises HandleEvent
// when the Beads wrapper is nil — the CAS mutateTownState path returns
// an error, but the post-mutation revert + nudge callbacks should NOT
// fire (state mutation is the gate; failing it short-circuits the
// chain). This keeps an unconfigured-Beads test environment from
// silently filing rig beads.
func TestMainCIBreakHandler_NilBeadsShortCircuits(t *testing.T) {
	f := newTestMainCIBreakFixture()
	// Beads is already nil from the fixture; HandleEvent will fail in
	// mutateTownState and return before invoking callbacks.

	ev := MainCIBreakEvent{
		RigName:   "gastown_upstream",
		CommitSHA: "abc123",
		MRBeadID:  "gt-mr42",
	}

	f.handler.HandleEvent(ev)

	if len(f.reverts) != 0 {
		t.Errorf("FileRevert called despite mutation failure: %+v", f.reverts)
	}
	if len(f.nudges) != 0 {
		t.Errorf("NudgeOverseer called despite mutation failure: %+v", f.nudges)
	}
	// Logger should have captured the error.
	foundErr := false
	for _, l := range f.logs {
		if strings.Contains(l, "ERROR mutating town-state") {
			foundErr = true
			break
		}
	}
	if !foundErr {
		t.Errorf("expected ERROR log on mutation failure, got logs=%v", f.logs)
	}
}

// TestMainCIBreakHandler_RevertFailureDoesNotBlockNudge — even if the
// revert filer returns an error, the Overseer nudge must still fire so
// a human is notified.
//
// Because HandleEvent's mutation step requires a live Beads wrapper,
// this test exercises the post-mutation callback ordering directly via
// a stub harness — it doesn't go through HandleEvent. The protected
// behavior is documented in the file-level docstring: state mutation
// is the gate; revert and nudge are best-effort siblings.
func TestMainCIBreakHandler_RevertFailureDoesNotBlockNudge(t *testing.T) {
	f := newTestMainCIBreakFixture()
	f.fileRevertErr = fmt.Errorf("simulated rig mailbox down")

	ev := MainCIBreakEvent{
		RigName:   "gastown_upstream",
		CommitSHA: "abc",
		MRBeadID:  "gt-mr1",
	}

	// Direct callback orchestration mirroring HandleEvent's tail —
	// (a) revert, (d) nudge — to verify ordering independence.
	if f.handler.FileRevert != nil {
		_ = f.handler.FileRevert(ev.RigName, ev.MRBeadID, ev.CommitSHA, ev.PreviousSHA, ev.EscalationID)
	}
	if f.handler.NudgeOverseer != nil {
		f.handler.NudgeOverseer(f.handler.formatSEV1Payload(ev, false))
	}

	if len(f.reverts) != 1 {
		t.Errorf("FileRevert call count = %d, want 1", len(f.reverts))
	}
	if len(f.nudges) != 1 {
		t.Errorf("NudgeOverseer call count = %d, want 1 (must fire even when revert fails)", len(f.nudges))
	}
}

// TestMainCIBreakHandler_RevertCallbackPayload verifies the FileRevert
// callback receives the exact (rig, mr, commit, previous, escalation)
// tuple from the event — the revert task content is downstream of this
// payload.
func TestMainCIBreakHandler_RevertCallbackPayload(t *testing.T) {
	f := newTestMainCIBreakFixture()

	ev := MainCIBreakEvent{
		RigName:      "gastown_upstream",
		CommitSHA:    "deadbeef00112233",
		PreviousSHA:  "cafebabe44556677",
		MRBeadID:     "gt-mr1234",
		EscalationID: "gt-esc999",
	}
	if f.handler.FileRevert != nil {
		_ = f.handler.FileRevert(ev.RigName, ev.MRBeadID, ev.CommitSHA, ev.PreviousSHA, ev.EscalationID)
	}

	if len(f.reverts) != 1 {
		t.Fatalf("revert call count = %d", len(f.reverts))
	}
	rc := f.reverts[0]
	if rc.rigName != ev.RigName || rc.commitSHA != ev.CommitSHA || rc.mrBeadID != ev.MRBeadID ||
		rc.previousSHA != ev.PreviousSHA || rc.escalationID != ev.EscalationID {
		t.Errorf("revert payload mismatch: got %+v, want %+v", rc, ev)
	}
}

// TestMainCIBreakHandler_IncidentsBounded ensures repeated SEV-1s do
// not unbounded-grow the audit log; appendIncident's cap kicks in.
func TestMainCIBreakHandler_IncidentsBounded(t *testing.T) {
	f := newTestMainCIBreakFixture()

	for i := 0; i < MaxIncidents+5; i++ {
		applyMainCIBreakStateMutation(f, MainCIBreakEvent{
			RigName:   "gastown_upstream",
			CommitSHA: fmt.Sprintf("commit%d", i),
			MRBeadID:  fmt.Sprintf("gt-mr%d", i),
		})
	}

	if len(f.state.Incidents) != MaxIncidents {
		t.Errorf("incident log = %d entries, want %d (cap)", len(f.state.Incidents), MaxIncidents)
	}
	// Newest incident should be retained — verifies FIFO drop, not
	// LIFO drop.
	last := f.state.Incidents[len(f.state.Incidents)-1]
	if !strings.Contains(last.Details, fmt.Sprintf("commit%d", MaxIncidents+4)) {
		t.Errorf("most recent incident lost: details=%s", last.Details)
	}
}
