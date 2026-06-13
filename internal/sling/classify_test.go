package sling

import "testing"

func TestClassifySlingFailure(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want SlingFailureClass
	}{
		{"empty", "", SlingFailureUnknown},
		{"unrelated", "dispatch failed: rig parked", SlingFailureUnknown},
		{"not found quoted", "bead 'ta-9emq' not found", SlingFailureNotFound},
		{"not found bd direct", "no issue found matching \"ta-9emq\"", SlingFailureNotFound},
		{"closed", "bead gt-x is closed (work already completed)", SlingFailureClosed},
		{"tombstone", "bead gt-x is tombstone (work already completed)", SlingFailureClosed},
		{"do-not-dispatch", "bead gt-x is a do-not-dispatch / pinned reference tripwire: refusing to schedule", SlingFailureDoNotDispatch},
		{"reference tripwire alt", "this is a reference tripwire and must stay open", SlingFailureDoNotDispatch},
		{"structural epic", "bead gt-x is an epic container", SlingFailureStructuralNonWork},
		{"structural children", "bead gt-x has open children", SlingFailureStructuralNonWork},
		{"structural identity", "bead gt-x is an identity/system bead", SlingFailureStructuralNonWork},
		{"actively worked hooked", "bead gt-x is already hooked to gastown/polecats/fury", SlingFailureActivelyWorked},
		{"actively worked in_progress", "already in_progress (use --force to re-sling)", SlingFailureActivelyWorked},
		{"deferred", "refusing to sling deferred bead gt-x: \"deferred to post-launch\"", SlingFailureDeferred},
		{"awaiting merge (sling.go)", `refusing to sling bead gt-x: "fix thing" is awaiting refinery merge (label awaiting_refinery_merge) — its MR is submitted and in the merge queue; the refinery will close it on merge`, SlingFailureAwaitingMerge},
		{"awaiting merge (schedule)", `bead gt-x is awaiting refinery merge (label awaiting_refinery_merge): "fix thing" — refusing to schedule`, SlingFailureAwaitingMerge},
		{"awaiting merge case-insensitive", "BEAD gt-x is Awaiting Refinery Merge", SlingFailureAwaitingMerge},
		{"capacity global ceiling", "polecat admission denied: 8/8 town-wide working polecats (scheduler.global_max_polecats ceiling reached; rig fce bead gt-x). Wait for a polecat to finish", SlingFailureCapacityAdmissionDenied},
		{"capacity host load", "polecat admission denied: host load/core 12.50 exceeds scheduler.max_load_per_core 8.00 (rig fce bead gt-x). Deferring spawn", SlingFailureCapacityAdmissionDenied},
		{"capacity max_polecats snapshot", "polecat admission denied: town at capacity (max=8 occupied=8 working=8 recovery_blocked=0 reservations=0 reusable_idle=0 pending_mr=0 free=0)", SlingFailureCapacityAdmissionDenied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifySlingFailure(tc.in); got != tc.want {
				t.Errorf("ClassifySlingFailure(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestClassifySlingFailure_Priority verifies the ordering guarantees: a closed
// bead whose message also contains "not found"-adjacent words still classifies
// by the most specific terminal disposition, and do-not-dispatch outranks the
// broader structural match (a tripwire is also "pinned"/reference but must route
// to its own permanent-untrack disposition, matching feedFirstReady's switch).
func TestClassifySlingFailure_Priority(t *testing.T) {
	// do-not-dispatch must win over structural for a tripwire line.
	in := "bead gt-x is a do-not-dispatch / pinned reference tripwire"
	if got := ClassifySlingFailure(in); got != SlingFailureDoNotDispatch {
		t.Errorf("tripwire classified as %v, want SlingFailureDoNotDispatch", got)
	}
}

// TestIsCapacityAdmissionDeniedSlingError covers the stderr shapes the capacity
// scheduler emits when refusing admission (gu-jaxdl) — and the shapes that must
// NOT match so they route to their own handlers / still escalate.
func TestIsCapacityAdmissionDeniedSlingError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"global ceiling", "polecat admission denied: 8/8 town-wide working polecats (scheduler.global_max_polecats ceiling reached; rig fce bead gt-x)", true},
		{"host load", "polecat admission denied: host load/core 12.50 exceeds scheduler.max_load_per_core 8.00 (rig fce bead gt-x)", true},
		{"max_polecats snapshot", "polecat admission denied: town at capacity (max=8 occupied=8 working=8 free=0)", true},
		{"bare reason", "polecat admission denied", true},
		{"case insensitive", "POLECAT ADMISSION DENIED: ceiling reached", true},
		// Must NOT match — distinct handler paths / genuinely-ambiguous.
		{"deferred", `refusing to sling deferred bead gt-x: "held"`, false},
		{"actively worked", "bead gt-x is already hooked to gastown/polecats/x", false},
		{"not found", "bead 'gt-x' not found", false},
		{"mayor-only", `"x" is labeled mayor-only / no-polecat`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsCapacityAdmissionDeniedSlingError(tc.in); got != tc.want {
				t.Errorf("IsCapacityAdmissionDeniedSlingError(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsTerminalSlingFailure(t *testing.T) {
	terminal := []SlingFailureClass{
		SlingFailureNotFound, SlingFailureClosed,
		SlingFailureStructuralNonWork, SlingFailureDoNotDispatch,
	}
	for _, c := range terminal {
		if !IsTerminalSlingFailure(c) {
			t.Errorf("IsTerminalSlingFailure(%v) = false, want true", c)
		}
	}
	nonTerminal := []SlingFailureClass{SlingFailureUnknown, SlingFailureActivelyWorked, SlingFailureDeferred, SlingFailureAwaitingMerge, SlingFailureCapacityAdmissionDenied}
	for _, c := range nonTerminal {
		if IsTerminalSlingFailure(c) {
			t.Errorf("IsTerminalSlingFailure(%v) = true, want false", c)
		}
	}
}
