package curio

import (
	"testing"
	"time"
)

func TestEvalDeterministicChecks_EmptyInput(t *testing.T) {
	in := Input{Window: Window{ID: "test/empty"}}
	result := EvalDeterministicChecks(in)

	if result.RulesRun != len(DefaultRules()) {
		t.Errorf("RulesRun = %d, want %d", result.RulesRun, len(DefaultRules()))
	}
	if result.CandidatesFound != 0 {
		t.Errorf("CandidatesFound = %d, want 0", result.CandidatesFound)
	}
	if len(result.Findings) != 0 {
		t.Errorf("Findings = %d, want 0", len(result.Findings))
	}
}

func TestEvalDeterministicChecks_FiltersNonVerifiable(t *testing.T) {
	// The merged-not-landed rule fires but is NOT verifiable (judgment lane).
	// EvalDeterministicChecks must filter it out.
	in := Input{
		Window: Window{ID: "test/filter"},
		Beads: []BeadRecord{
			{ID: "b1", Rig: "r", CloseReason: "merged", Commit: "abc", CommitInMainAncestry: false, FiledBy: "polecat"},
		},
	}
	result := EvalDeterministicChecks(in)

	if result.CandidatesFound == 0 {
		t.Fatal("expected at least 1 candidate from merged-not-landed rule")
	}
	if len(result.Findings) != 0 {
		t.Errorf("Findings = %d, want 0 (merged-not-landed is judgment-lane)", len(result.Findings))
	}
}

func TestEvalDeterministicChecks_VerifiedFinding(t *testing.T) {
	// Construct an Input with a dead-owner admission that has a verify thunk.
	// Since we can't easily fake a dead PID in a unit test, we inject the
	// admission with OwnerAlive=false and then test that the rule produces a
	// verifiable candidate. The actual verify thunk calls kill(pid, 0), so we
	// use PID=1 (always alive on Linux) to test the verify-fails path, and
	// PID=999999999 (almost certainly dead) for the verify-succeeds path.
	in := Input{
		Window: Window{ID: "test/verify"},
		Admissions: []AdmissionRecord{
			{ID: "res1", PID: 999999999, Rig: "test_rig", OwnerAlive: false, FiledBy: "scheduler"},
		},
	}

	result := EvalDeterministicChecks(in)

	if result.CandidatesFound == 0 {
		t.Fatal("expected at least 1 candidate from dead_owner_admission rule")
	}

	// The dead-owner rule fires, and the verify thunk re-probes PID liveness.
	// PID 999999999 should be dead on any sane system, so Verify() returns true.
	if len(result.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1 (dead PID should verify)", len(result.Findings))
	}

	f := result.Findings[0]
	if f.RuleID != "dead_owner_admission" {
		t.Errorf("Finding.RuleID = %q, want %q", f.RuleID, "dead_owner_admission")
	}
	if f.Rig != "test_rig" {
		t.Errorf("Finding.Rig = %q, want %q", f.Rig, "test_rig")
	}
	if f.VerifiedAt.IsZero() {
		t.Error("Finding.VerifiedAt should be set")
	}
}

func TestEvalDeterministicChecks_VerifyFailsExcludesFinding(t *testing.T) {
	// PID 1 (init) is always alive. The dead_owner rule fires (OwnerAlive=false
	// is the normalized probe result from collection), but the Call 3 verify
	// thunk re-probes and finds PID 1 alive — so the finding is excluded.
	in := Input{
		Window: Window{ID: "test/verify-fail"},
		Admissions: []AdmissionRecord{
			{ID: "res1", PID: 1, Rig: "test_rig", OwnerAlive: false, FiledBy: "scheduler"},
		},
	}

	result := EvalDeterministicChecks(in)

	if result.CandidatesFound == 0 {
		t.Fatal("expected at least 1 candidate from dead_owner rule (OwnerAlive=false)")
	}
	// But Verify() re-probes PID 1 which IS alive, so the finding is excluded.
	if len(result.Findings) != 0 {
		t.Errorf("Findings = %d, want 0 (PID 1 is alive, verify should fail)", len(result.Findings))
	}
}

func TestEvalDeterministicChecks_AirGapSuppressesCurio(t *testing.T) {
	// Call 1(A): a record filed by curio is suppressed.
	in := Input{
		Window: Window{ID: "test/airgap"},
		Admissions: []AdmissionRecord{
			{ID: "res1", PID: 999999999, Rig: "test_rig", OwnerAlive: false, FiledBy: CurioActor},
		},
	}

	result := EvalDeterministicChecks(in)

	// The dead_owner rule should NOT fire because FiledBy == "curio" triggers
	// the loop-breaker.
	if result.CandidatesFound != 0 {
		t.Errorf("CandidatesFound = %d, want 0 (curio records are air-gapped)", result.CandidatesFound)
	}
	if len(result.Findings) != 0 {
		t.Errorf("Findings = %d, want 0", len(result.Findings))
	}
}

func TestEvalDeterministicChecks_StateHashDedup(t *testing.T) {
	// Call 1(B): two admissions in the same rig collapse to one finding
	// (state-hash damper keys on rig, not reservation ID).
	in := Input{
		Window: Window{ID: "test/dedup"},
		Admissions: []AdmissionRecord{
			{ID: "res1", PID: 999999999, Rig: "same_rig", OwnerAlive: false, FiledBy: "scheduler"},
			{ID: "res2", PID: 999999998, Rig: "same_rig", OwnerAlive: false, FiledBy: "scheduler"},
		},
	}

	result := EvalDeterministicChecks(in)

	// Both admissions target the same rig → same StateHash → one candidate
	// (first-writer-wins).
	if result.CandidatesFound != 1 {
		t.Errorf("CandidatesFound = %d, want 1 (state-hash should dedup same-rig)", result.CandidatesFound)
	}
	if len(result.Findings) != 1 {
		t.Errorf("Findings = %d, want 1", len(result.Findings))
	}
}

func TestFormatFindingSummary_Empty(t *testing.T) {
	r := PatrolResult{RulesRun: 4, CandidatesFound: 0}
	s := FormatFindingSummary(r)
	if s == "" {
		t.Error("summary should not be empty")
	}
}

func TestFormatFindingSummary_WithFindings(t *testing.T) {
	r := PatrolResult{
		RulesRun:        4,
		CandidatesFound: 2,
		Findings: []PatrolFinding{
			{RuleID: "dead_owner_admission", Summary: "admission res1 leaking", VerifiedAt: time.Now()},
		},
	}
	s := FormatFindingSummary(r)
	if s == "" {
		t.Error("summary should not be empty")
	}
}
