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
		{"capacity global ceiling", "polecat admission denied: 3/3 town-wide working polecats (scheduler.global_max_polecats ceiling reached; rig gastown bead gs-x). Wait for a polecat to finish", SlingFailureCapacity},
		{"capacity max full", "polecat admission denied: configured scheduler.max_polecats capacity is full (max=4 occupied=4 working=4 ...)", SlingFailureCapacity},
		{"capacity host load", "polecat admission denied: host load/core 8.50 exceeds scheduler.max_load_per_core 4.00 (rig gastown bead gs-x)", SlingFailureCapacity},
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
	nonTerminal := []SlingFailureClass{SlingFailureUnknown, SlingFailureActivelyWorked, SlingFailureDeferred, SlingFailureAwaitingMerge, SlingFailureCapacity}
	for _, c := range nonTerminal {
		if IsTerminalSlingFailure(c) {
			t.Errorf("IsTerminalSlingFailure(%v) = true, want false", c)
		}
	}
}
