package curio

import "testing"

// --- Call 1(A): CausalRoot air-gap (loop-breaker extended beyond FiledBy) ---

func TestLoopBreaker_DropsReactionToCurioBead_Beads(t *testing.T) {
	// A bead filed by a downstream subsystem (not "curio") but whose causal
	// chain ROOTS at a Curio-filed bead must be suppressed (self-reaction).
	in := Input{
		Window:     Window{ID: "w"},
		CurioBeads: map[string]bool{"gu-curio-1": true},
		Beads: []BeadRecord{{
			ID: "b1", CloseReason: "merged", Commit: "", FiledBy: "polecat",
			causalProvenance: causalProvenance{CausalParent: "x", CausalRoot: "gu-curio-1"},
		}},
	}
	got := mergedNotLandedRule{}.Eval(in)
	if len(got) != 0 {
		t.Errorf("must suppress a reaction to a curio-filed bead, got %+v", got)
	}
}

func TestLoopBreaker_KeepsUnrelatedCausalRoot(t *testing.T) {
	// Same shape, but the chain roots at a NON-curio bead — must stay visible.
	in := Input{
		Window:     Window{ID: "w"},
		CurioBeads: map[string]bool{"gu-curio-1": true},
		Beads: []BeadRecord{{
			ID: "b1", CloseReason: "merged", Commit: "", FiledBy: "polecat",
			causalProvenance: causalProvenance{CausalRoot: "gu-other"},
		}},
	}
	got := mergedNotLandedRule{}.Eval(in)
	if len(got) != 1 {
		t.Errorf("must NOT suppress a reaction rooted at a non-curio bead, got %+v", got)
	}
}

func TestLoopBreaker_EmptyCausalRootIsNotSuppressed(t *testing.T) {
	// Unknown provenance (empty CausalRoot) is conservatively kept visible.
	in := Input{
		Window:     Window{ID: "w"},
		CurioBeads: map[string]bool{"gu-curio-1": true},
		Admissions: []AdmissionRecord{{ID: "a1", PID: 1, Rig: "r", OwnerAlive: false, FiledBy: "scheduler"}},
	}
	got := deadOwnerAdmissionRule{}.Eval(in)
	if len(got) != 1 {
		t.Errorf("empty CausalRoot must stay visible, got %+v", got)
	}
}

func TestLoopBreaker_NilCurioBeadsSetIsDormant(t *testing.T) {
	// With no CurioBeads set, the causal half is dormant and only the FiledBy
	// half applies — exactly the pre-2a behavior.
	in := Input{
		Window: Window{ID: "w"},
		LogLines: []LogLine{{
			Source: "reaper", Text: "kill -QUIT dolt pid 1", NearDoltPID: true, FiledBy: "reaper",
			causalProvenance: causalProvenance{CausalRoot: "gu-anything"},
		}},
	}
	got := killSignalNearDoltRule{}.Eval(in)
	if len(got) != 1 {
		t.Errorf("nil CurioBeads set must leave the causal half dormant, got %+v", got)
	}
}

func TestLoopBreaker_DropsCurioSeriesEvents(t *testing.T) {
	// Curio's own telemetry series must never feed the rate rule, regardless of
	// which actor the events were attributed to.
	r := rateSpikeRule{thresholds: map[string]int{"curio.cycle": 0, "sling": 0}}
	in := Input{Window: Window{ID: "w"}, EventCounts: []SeriesCount{
		{Series: "curio.cycle", Observed: 9999, FiledBy: "daemon"},
		{Series: "sling", Observed: 1, FiledBy: "scheduler"},
	}}
	cands := r.Eval(in)
	if len(cands) != 1 || cands[0].Series != "sling" {
		t.Errorf("curio.* series must be dropped; want only sling, got %+v", cands)
	}
}

// --- Call 1(B): state-hash damper ---

func TestStateHash_CollapsesDeadOwnerFlapWithinRig(t *testing.T) {
	// The same rig leaks capacity across two distinct reservation IDs/owners
	// (boot<->deacon flap). Distinct fingerprints, same rig-keyed StateHash →
	// Evaluate collapses to ONE candidate.
	in := Input{Window: Window{ID: "w"}, Admissions: []AdmissionRecord{
		{ID: "resv-boot", PID: 100, Rig: "rigA", OwnerAlive: false, FiledBy: "scheduler"},
		{ID: "resv-deacon", PID: 200, Rig: "rigA", OwnerAlive: false, FiledBy: "scheduler"},
	}}
	cands := Evaluate(DefaultRules(), in)
	if len(cands) != 1 {
		t.Fatalf("rig flap must collapse to 1 candidate, got %d: %+v", len(cands), cands)
	}
}

func TestStateHash_DistinctRigsStayDistinct(t *testing.T) {
	in := Input{Window: Window{ID: "w"}, Admissions: []AdmissionRecord{
		{ID: "r1", PID: 100, Rig: "rigA", OwnerAlive: false, FiledBy: "scheduler"},
		{ID: "r2", PID: 200, Rig: "rigB", OwnerAlive: false, FiledBy: "scheduler"},
	}}
	if cands := Evaluate(DefaultRules(), in); len(cands) != 2 {
		t.Fatalf("distinct rigs must stay distinct, got %d: %+v", len(cands), cands)
	}
}

func TestStateHash_UnknownRigFallsBackToFingerprint(t *testing.T) {
	// Two dead reservations with NO rig must NOT over-collapse — each keeps its
	// per-reservation fingerprint as its StateHash.
	in := Input{Window: Window{ID: "w"}, Admissions: []AdmissionRecord{
		{ID: "r1", PID: 100, OwnerAlive: false, FiledBy: "scheduler"},
		{ID: "r2", PID: 200, OwnerAlive: false, FiledBy: "scheduler"},
	}}
	if cands := Evaluate(DefaultRules(), in); len(cands) != 2 {
		t.Fatalf("rig-less reservations must not collapse, got %d: %+v", len(cands), cands)
	}
}

// --- Call 3: Verifiable / Verify fast path ---

func TestVerify_DeadOwnerVerifiableAndStillHolds(t *testing.T) {
	// A reservation owned by a reliably-dead PID: candidate is Verifiable and
	// Verify() confirms the finding still holds (PID still dead).
	in := Input{Window: Window{ID: "w"}, Admissions: []AdmissionRecord{
		{ID: "dead", PID: 2147480000, Rig: "r", OwnerAlive: false, FiledBy: "scheduler"},
	}}
	cands := deadOwnerAdmissionRule{}.Eval(in)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if !cands[0].Verifiable() {
		t.Fatal("dead_owner candidate must be Verifiable (LaneVerified)")
	}
	if !cands[0].Verify() {
		t.Error("Verify() should confirm: the huge PID is still dead")
	}
}

func TestVerify_LivePIDDoesNotHold(t *testing.T) {
	// A reservation whose PID is actually our own (alive): if the collector had
	// stale data and the rule fired, Verify() must REFUTE it (owner is alive).
	in := Input{Window: Window{ID: "w"}, Admissions: []AdmissionRecord{
		{ID: "alive", PID: 1, Rig: "r", OwnerAlive: false, FiledBy: "scheduler"},
	}}
	cands := deadOwnerAdmissionRule{}.Eval(in)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	// PID 1 (init) is always alive → finding no longer holds.
	if cands[0].Verify() {
		t.Error("Verify() must refute when the owning PID is alive")
	}
}

func TestVerify_NonVerifiableJudgmentLane(t *testing.T) {
	// Content rules without a syscall verifier are judgment-lane: not Verifiable,
	// and Verify() returns false (cannot self-confirm).
	in := Input{Window: Window{ID: "w"}, Beads: []BeadRecord{
		{ID: "b1", CloseReason: "merged", Commit: "", FiledBy: "polecat"},
	}}
	cands := mergedNotLandedRule{}.Eval(in)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if cands[0].Verifiable() {
		t.Error("merged_not_landed is judgment-lane, must not be Verifiable")
	}
	if cands[0].Verify() {
		t.Error("non-Verifiable candidate Verify() must be false")
	}
}
