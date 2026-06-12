package upstreamsync

import (
	"errors"
	"testing"
)

func TestIsValidTransition(t *testing.T) {
	tests := []struct {
		name string
		from SyncState
		to   SyncState
		want bool
	}{
		// Valid edges from the design diagram
		{"idle→checking", StateIdle, StateChecking, true},
		{"checking→idle", StateChecking, StateIdle, true},
		{"checking→syncing", StateChecking, StateSyncing, true},
		{"checking→resolving", StateChecking, StateResolving, true},
		{"syncing→gating", StateSyncing, StateGating, true},
		{"resolving→gating", StateResolving, StateGating, true},
		{"resolving→failed", StateResolving, StateFailed, true},
		{"gating→pushing", StateGating, StatePushing, true},
		{"gating→failed", StateGating, StateFailed, true},
		{"pushing→idle", StatePushing, StateIdle, true},
		{"pushing→failed", StatePushing, StateFailed, true},
		{"failed→idle", StateFailed, StateIdle, true},
		{"failed→checking (retry)", StateFailed, StateChecking, true},
		{"paused→idle", StatePaused, StateIdle, true},

		// "* → paused" should be valid from any active state
		{"idle→paused", StateIdle, StatePaused, true},
		{"checking→paused", StateChecking, StatePaused, true},
		{"syncing→paused", StateSyncing, StatePaused, true},
		{"resolving→paused", StateResolving, StatePaused, true},
		{"gating→paused", StateGating, StatePaused, true},
		{"pushing→paused", StatePushing, StatePaused, true},
		{"failed→paused", StateFailed, StatePaused, true},

		// Forbidden edges (would skip steps in the machine)
		{"idle→syncing (no checking)", StateIdle, StateSyncing, false},
		{"idle→gating (skip)", StateIdle, StateGating, false},
		{"syncing→pushing (skip gating)", StateSyncing, StatePushing, false},
		{"checking→gating (skip merge)", StateChecking, StateGating, false},
		{"paused→syncing (must idle first)", StatePaused, StateSyncing, false},

		// Self-loops are not transitions
		{"idle→idle", StateIdle, StateIdle, false},
		{"paused→paused", StatePaused, StatePaused, false},

		// Invalid states
		{"bogus→idle", SyncState("bogus"), StateIdle, false},
		{"idle→bogus", StateIdle, SyncState("bogus"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsValidTransition(tt.from, tt.to)
			if got != tt.want {
				t.Errorf("IsValidTransition(%q, %q) = %v, want %v",
					tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestValidNextStates(t *testing.T) {
	// idle should reach exactly checking + paused
	got := ValidNextStates(StateIdle)
	want := map[SyncState]bool{StateChecking: true, StatePaused: true}
	if len(got) != len(want) {
		t.Fatalf("ValidNextStates(idle) = %v (len %d), want set %v (len %d)",
			got, len(got), want, len(want))
	}
	for _, s := range got {
		if !want[s] {
			t.Errorf("ValidNextStates(idle) includes unexpected %q", s)
		}
	}

	// Invalid input returns nil
	if got := ValidNextStates(SyncState("bogus")); got != nil {
		t.Errorf("ValidNextStates(bogus) = %v, want nil", got)
	}
}

func TestErrInvalidTransition_Error(t *testing.T) {
	err := &ErrInvalidTransition{From: StateIdle, To: StateGating}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() returned empty string")
	}
	// Spot-check key tokens are present so callers/tests can grep them.
	for _, tok := range []string{"idle", "gating", "invalid"} {
		if !contains(msg, tok) {
			t.Errorf("Error() = %q, missing token %q", msg, tok)
		}
	}
}

// TestTransitionTo_RejectsInvalidTarget covers the public surface
// without requiring a real Beads instance: when the target state is
// itself unrecognized, TransitionTo errors out before reaching Dolt.
func TestTransitionTo_RejectsInvalidTarget(t *testing.T) {
	err := TransitionTo(nil, "gu", SyncState("bogus"), nil)
	if err == nil {
		t.Fatal("expected error for invalid target state, got nil")
	}
	var invalidTransition *ErrInvalidTransition
	if errors.As(err, &invalidTransition) {
		t.Errorf("expected non-ErrInvalidTransition for unrecognized state, got %v", err)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
