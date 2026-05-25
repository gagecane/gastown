// Tests for the operator-pause / resume / circuit-breaker-override
// mutators that ship in Phase 0 task 2b (gu-uez5w).
//
// We exercise the pure mutate functions (closures) directly here
// rather than through the full beads.Beads test double. The CAS
// retry loop is unit-tested separately in enabled_rigs_test.go;
// that surface is shared, so duplicating it here would just bloat
// coverage without catching new defects.
package autotestpr

import (
	"strings"
	"testing"
	"time"
)

// fixedNow is the wall-clock pinned by every test in this file. Using
// a fixed timestamp keeps the audit-log and pause-record assertions
// deterministic without a clock-injection dance.
var fixedNow = time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)

// freshState returns a TownState in the post-Provision shape — what
// LoadTownState returns immediately after EnsureTownStateBead. We
// don't use DefaultTownState() directly so the tests document the
// exact starting shape inline.
func freshState() TownState {
	return TownState{
		SchemaVersion:  TownStateSchemaVersion,
		EnabledRigs:    []string{},
		CircuitBreaker: CircuitBreakerState{Count: 0},
	}
}

// applyGlobalPause replicates the in-place mutate the SetGlobalPause
// closure performs against a TownState pointer. The production code
// path calls this through mutateTownState; the tests call it
// directly so we don't need a Beads test double for the pure
// state-shape contract.
func applyGlobalPause(s *TownState, req PauseRequest) {
	s.GlobalPauseUntil = req.Until.UTC().Format(time.RFC3339)
	s.GlobalPauseReason = req.Reason
	s.GlobalPausedBy = req.Actor
	appendIncident(s, Incident{
		At:      req.Now.UTC().Format(time.RFC3339),
		Actor:   req.Actor,
		Kind:    IncidentGlobalPause,
		Details: formatPauseDetails(req.Until, req.Now, req.Reason),
	})
}

// applyRigPause mirrors SetRigPause's mutate closure for the same
// reason as applyGlobalPause.
func applyRigPause(s *TownState, rigName string, req PauseRequest) {
	if s.RigPauses == nil {
		s.RigPauses = map[string]RigPauseEntry{}
	}
	s.RigPauses[rigName] = RigPauseEntry{
		PausedUntil: req.Until.UTC().Format(time.RFC3339),
		Reason:      req.Reason,
		PausedBy:    req.Actor,
		PausedAt:    req.Now.UTC().Format(time.RFC3339),
	}
	appendIncident(s, Incident{
		At:      req.Now.UTC().Format(time.RFC3339),
		Actor:   req.Actor,
		Kind:    IncidentRigPause,
		Rig:     rigName,
		Details: formatPauseDetails(req.Until, req.Now, req.Reason),
	})
}

func TestPauseRequestValidate(t *testing.T) {
	t.Parallel()

	dur := 1 * time.Hour
	good := PauseRequest{
		Until:  fixedNow.Add(dur),
		Reason: "release window",
		Actor:  "overseer",
		Now:    fixedNow,
	}
	if err := good.validate(); err != nil {
		t.Fatalf("validate(good): unexpected error %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*PauseRequest)
		wantSub string
	}{
		{"zero Now", func(r *PauseRequest) { r.Now = time.Time{} }, "Now is zero"},
		{"zero Until", func(r *PauseRequest) { r.Until = time.Time{} }, "Until is zero"},
		{"Until before Now", func(r *PauseRequest) { r.Until = r.Now.Add(-time.Hour) }, "is not after"},
		{"empty Actor", func(r *PauseRequest) { r.Actor = "" }, "Actor is empty"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := good
			tt.mutate(&r)
			err := r.validate()
			if err == nil {
				t.Fatalf("validate: expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("validate error = %v; want substring %q", err, tt.wantSub)
			}
		})
	}
}

func TestResumeRequestValidate(t *testing.T) {
	t.Parallel()

	good := ResumeRequest{Actor: "overseer", Now: fixedNow}
	if err := good.validate(); err != nil {
		t.Fatalf("validate(good): %v", err)
	}

	bad := ResumeRequest{Actor: "overseer"}
	if err := bad.validate(); err == nil || !strings.Contains(err.Error(), "Now is zero") {
		t.Errorf("validate(zero-now) = %v; want Now is zero", err)
	}
	bad2 := ResumeRequest{Now: fixedNow}
	if err := bad2.validate(); err == nil || !strings.Contains(err.Error(), "Actor is empty") {
		t.Errorf("validate(empty-actor) = %v; want Actor is empty", err)
	}
}

func TestApplyGlobalPauseRecordsAuditEntry(t *testing.T) {
	t.Parallel()

	s := freshState()
	req := PauseRequest{
		Until:  fixedNow.Add(2 * time.Hour),
		Reason: "release window",
		Actor:  "overseer",
		Now:    fixedNow,
	}
	applyGlobalPause(&s, req)

	if got, want := s.GlobalPauseUntil, fixedNow.Add(2*time.Hour).UTC().Format(time.RFC3339); got != want {
		t.Errorf("GlobalPauseUntil = %q; want %q", got, want)
	}
	if got, want := s.GlobalPauseReason, "release window"; got != want {
		t.Errorf("GlobalPauseReason = %q; want %q", got, want)
	}
	if got, want := s.GlobalPausedBy, "overseer"; got != want {
		t.Errorf("GlobalPausedBy = %q; want %q", got, want)
	}
	if len(s.Incidents) != 1 {
		t.Fatalf("len(Incidents) = %d; want 1", len(s.Incidents))
	}
	inc := s.Incidents[0]
	if inc.Kind != IncidentGlobalPause {
		t.Errorf("Incident.Kind = %q; want %q", inc.Kind, IncidentGlobalPause)
	}
	if inc.Actor != "overseer" {
		t.Errorf("Incident.Actor = %q; want %q", inc.Actor, "overseer")
	}
	if inc.Rig != "" {
		t.Errorf("Incident.Rig = %q; want empty for town-wide", inc.Rig)
	}
	if !strings.Contains(inc.Details, "duration=2h0m0s") {
		t.Errorf("Incident.Details = %q; want duration substring", inc.Details)
	}
	if !strings.Contains(inc.Details, "release window") {
		t.Errorf("Incident.Details = %q; want reason substring", inc.Details)
	}
}

func TestApplyRigPauseRecordsAuditEntry(t *testing.T) {
	t.Parallel()

	s := freshState()
	req := PauseRequest{
		Until: fixedNow.Add(30 * time.Minute),
		Actor: "gastown_upstream/polecats/radrat",
		Now:   fixedNow,
	}
	applyRigPause(&s, "gastown_upstream", req)

	pe, ok := s.RigPauses["gastown_upstream"]
	if !ok {
		t.Fatal("RigPauses[gastown_upstream] missing after applyRigPause")
	}
	if pe.PausedBy != "gastown_upstream/polecats/radrat" {
		t.Errorf("PausedBy = %q; want polecat path", pe.PausedBy)
	}
	if pe.Reason != "" {
		t.Errorf("Reason = %q; want empty (no --reason flag)", pe.Reason)
	}
	if len(s.Incidents) != 1 {
		t.Fatalf("len(Incidents) = %d; want 1", len(s.Incidents))
	}
	if got, want := s.Incidents[0].Kind, IncidentRigPause; got != want {
		t.Errorf("Incident.Kind = %q; want %q", got, want)
	}
	if got, want := s.Incidents[0].Rig, "gastown_upstream"; got != want {
		t.Errorf("Incident.Rig = %q; want %q", got, want)
	}
}

func TestAppendIncidentTrimsToCap(t *testing.T) {
	t.Parallel()

	s := freshState()
	// Push one more than the cap so the FIFO trim runs at least once.
	for i := 0; i < MaxIncidents+5; i++ {
		appendIncident(&s, Incident{
			At:    fixedNow.Add(time.Duration(i) * time.Minute).UTC().Format(time.RFC3339),
			Actor: "overseer",
			Kind:  IncidentGlobalPause,
		})
	}

	if got, want := len(s.Incidents), MaxIncidents; got != want {
		t.Fatalf("len(Incidents) after overfill = %d; want %d", got, want)
	}

	// First entry should now be the (offset+5)'th original push — i.e.
	// the head was dropped first.
	wantFirstAt := fixedNow.Add(5 * time.Minute).UTC().Format(time.RFC3339)
	if got := s.Incidents[0].At; got != wantFirstAt {
		t.Errorf("oldest entry At = %q; want %q (FIFO trim)", got, wantFirstAt)
	}

	// Last entry is the newest one.
	wantLastAt := fixedNow.Add(time.Duration(MaxIncidents+4) * time.Minute).UTC().Format(time.RFC3339)
	if got := s.Incidents[MaxIncidents-1].At; got != wantLastAt {
		t.Errorf("newest entry At = %q; want %q", got, wantLastAt)
	}
}

func TestFormatPauseDetailsElidesEmptyReason(t *testing.T) {
	t.Parallel()

	got := formatPauseDetails(fixedNow.Add(time.Hour), fixedNow, "")
	if got != "duration=1h0m0s" {
		t.Errorf("formatPauseDetails(no reason) = %q; want %q", got, "duration=1h0m0s")
	}
	got = formatPauseDetails(fixedNow.Add(time.Hour), fixedNow, "release")
	if !strings.Contains(got, "duration=1h0m0s") {
		t.Errorf("formatPauseDetails: missing duration; got %q", got)
	}
	if !strings.Contains(got, `reason="release"`) {
		t.Errorf("formatPauseDetails: missing reason; got %q", got)
	}
}

func TestCircuitBreakerIsTripped(t *testing.T) {
	t.Parallel()

	cb := CircuitBreakerState{}
	if cb.IsTripped() {
		t.Error("zero-value CircuitBreaker.IsTripped() = true; want false")
	}
	cb.TrippedUntil = fixedNow.Format(time.RFC3339)
	if !cb.IsTripped() {
		t.Error("CircuitBreaker with TrippedUntil set: IsTripped() = false; want true")
	}
	cb.TrippedUntil = ""
	cb.Count = 5 // counter alone is not "tripped"
	if cb.IsTripped() {
		t.Error("CircuitBreaker with Count>0 only: IsTripped() = true; want false")
	}
}

// TestTownStateRoundTripPreservesNewFields documents that the new
// fields ship with byte-for-byte JSON round-tripping. A future
// schema bump that drops or renames a field will fail here before
// it ships.
func TestTownStateRoundTripPreservesNewFields(t *testing.T) {
	t.Parallel()

	original := freshState()
	applyRigPause(&original, "gastown_upstream", PauseRequest{
		Until: fixedNow.Add(2 * time.Hour),
		Actor: "overseer",
		Now:   fixedNow,
	})
	applyGlobalPause(&original, PauseRequest{
		Until:  fixedNow.Add(time.Hour),
		Reason: "release",
		Actor:  "mayor/",
		Now:    fixedNow,
	})

	raw, err := original.MarshalMetadata()
	if err != nil {
		t.Fatalf("MarshalMetadata: %v", err)
	}

	parsed, err := UnmarshalTownState(raw)
	if err != nil {
		t.Fatalf("UnmarshalTownState: %v", err)
	}

	if got, want := parsed.GlobalPauseUntil, original.GlobalPauseUntil; got != want {
		t.Errorf("GlobalPauseUntil round-trip: got %q; want %q", got, want)
	}
	if got, want := parsed.GlobalPauseReason, "release"; got != want {
		t.Errorf("GlobalPauseReason round-trip: got %q; want %q", got, want)
	}
	if got, want := parsed.GlobalPausedBy, "mayor/"; got != want {
		t.Errorf("GlobalPausedBy round-trip: got %q; want %q", got, want)
	}
	pe, ok := parsed.RigPauses["gastown_upstream"]
	if !ok {
		t.Fatal("RigPauses[gastown_upstream] missing after round-trip")
	}
	if pe.PausedBy != "overseer" {
		t.Errorf("RigPauses.PausedBy round-trip: got %q; want overseer", pe.PausedBy)
	}
	if got, want := len(parsed.Incidents), 2; got != want {
		t.Errorf("Incidents round-trip len = %d; want %d", got, want)
	}
}

// TestDefaultTownStateOmitsNewFields verifies the default-shape
// invariant the gu-kn0j8 acceptance test checks against: the JSON
// MUST still encode to the literal
// `{enabled_rigs:[], paused:false, circuit_breaker:{count:0}}`-shape
// for the town-wide row when no operator has pressed a pause button.
// New fields (RigPauses, Incidents, GlobalPauseReason, GlobalPausedBy)
// MUST be omitempty so a fresh provision still serializes minimally.
func TestDefaultTownStateOmitsNewFields(t *testing.T) {
	t.Parallel()

	state := DefaultTownState()
	raw, err := state.MarshalMetadata()
	if err != nil {
		t.Fatalf("MarshalMetadata: %v", err)
	}

	got := string(raw)
	for _, sub := range []string{
		`"rig_pauses"`,
		`"incidents"`,
		`"global_pause_reason"`,
		`"global_paused_by"`,
		`"global_pause_until"`,
	} {
		if strings.Contains(got, sub) {
			t.Errorf("DefaultTownState JSON includes %s; expected omitempty path: %s", sub, got)
		}
	}
}
