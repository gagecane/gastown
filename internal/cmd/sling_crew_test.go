package cmd

import "testing"

// TestIsCrewTarget verifies the crew target pattern matching.
// Crew can be targeted via:
//   - "<rig>/crew/<name>" -> specific crew member in a rig
//   - "crew" -> crew in current rig (resolved from env by resolveTarget)
func TestIsCrewTarget(t *testing.T) {
	tests := []struct {
		target   string
		wantRig  string
		wantName string
		wantIs   bool
	}{
		// Bare "crew" shorthand
		{"crew", "", "", true},
		{"CREW", "", "", true}, // case insensitive
		{"Crew", "", "", true},

		// Full path form: <rig>/crew/<name>
		{"gastown/crew/mel", "gastown", "mel", true},
		{"casc_crud/crew/canewiw", "casc_crud", "canewiw", true},
		{"myrig/crew/alpha", "myrig", "alpha", true},

		// Invalid patterns - not crew targets
		{"", "", "", false},
		{"gastown", "", "", false},           // rig name, not crew
		{"gastown/crew", "", "", false},      // missing name segment
		{"gastown/crew/", "", "", false},     // empty name segment
		{"/crew/mel", "", "", false},         // empty rig segment
		{"gastown/polecats/mel", "", "", false}, // polecat, not crew
		{"deacon/dogs/alpha", "", "", false}, // dog target
		{"mayor", "", "", false},
		{"dog:alpha", "", "", false},
		{"gastown/crew/mel/extra", "", "", false}, // too many segments
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			gotRig, gotName, gotIs := IsCrewTarget(tt.target)
			if gotIs != tt.wantIs {
				t.Errorf("IsCrewTarget(%q) isCrew = %v, want %v", tt.target, gotIs, tt.wantIs)
			}
			if gotRig != tt.wantRig {
				t.Errorf("IsCrewTarget(%q) rigName = %q, want %q", tt.target, gotRig, tt.wantRig)
			}
			if gotName != tt.wantName {
				t.Errorf("IsCrewTarget(%q) crewName = %q, want %q", tt.target, gotName, tt.wantName)
			}
		})
	}
}

// TestCrewTargetsAreNotMistakenForRigs is a regression guard for gu-odjz.
// The deferred sling path (active when scheduler.max_polecats > 0) rejects
// targets that are neither rigs nor dogs. When --crew expands the target to
// "<rig>/crew/<name>", or the bare "crew" shorthand is used, the capacity
// check must recognize these as crew targets and fall through to direct
// dispatch — not reject them with "is not a known rig".
//
// This test locks in the classification invariant that crew targets
// satisfy IsCrewTarget (so sling.go can fall them through to direct dispatch).
func TestCrewTargetsAreNotMistakenForRigs(t *testing.T) {
	crewTargets := []string{
		"gastown/crew/mel",       // --crew expanded form
		"casc_crud/crew/canewiw", // from the original bug report
		"crew",                   // bare shorthand
	}

	for _, target := range crewTargets {
		t.Run(target, func(t *testing.T) {
			if _, _, isCrew := IsCrewTarget(target); !isCrew {
				t.Fatalf("IsCrewTarget(%q) = false — crew targets must be "+
					"recognized so the deferred sling path can fall through "+
					"to direct dispatch (gu-odjz regression)", target)
			}
		})
	}
}
