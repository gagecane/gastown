package daemon

import (
	"testing"
)

func TestEvaluateFeedStorm(t *testing.T) {
	const T = feedStormFailureThreshold
	const N = feedStormConsecutiveThreshold

	t.Run("below threshold re-arms", func(t *testing.T) {
		prev := feedStormState{Consecutive: 3, FirstSeen: "t0"}
		next, escalate := evaluateFeedStorm(prev, T-1, "now")
		if escalate {
			t.Fatal("should not escalate below per-scan threshold")
		}
		if next.Consecutive != 0 || next.FirstSeen != "" {
			t.Fatalf("expected reset state, got %+v", next)
		}
	})

	t.Run("accumulates and escalates once at threshold", func(t *testing.T) {
		st := feedStormState{}
		escalations := 0
		for i := 0; i < N+3; i++ {
			var esc bool
			st, esc = evaluateFeedStorm(st, T+5, "now")
			if esc {
				escalations++
			}
		}
		if escalations != 1 {
			t.Fatalf("expected exactly 1 escalation across the episode, got %d", escalations)
		}
		if !st.Escalated {
			t.Fatal("state should be marked Escalated")
		}
		if st.PeakPerScan != T+5 {
			t.Fatalf("peak = %d, want %d", st.PeakPerScan, T+5)
		}
	})

	t.Run("does not escalate before consecutive threshold", func(t *testing.T) {
		st := feedStormState{}
		for i := 0; i < N-1; i++ {
			var esc bool
			st, esc = evaluateFeedStorm(st, T, "now")
			if esc {
				t.Fatalf("escalated at scan %d, before threshold %d", i+1, N)
			}
		}
		if st.Consecutive != N-1 {
			t.Fatalf("consecutive = %d, want %d", st.Consecutive, N-1)
		}
	})

	t.Run("recovery mid-episode re-arms then re-escalates", func(t *testing.T) {
		st := feedStormState{}
		// Climb to one short of threshold.
		for i := 0; i < N-1; i++ {
			st, _ = evaluateFeedStorm(st, T, "now")
		}
		// A clean scan resets.
		st, _ = evaluateFeedStorm(st, 0, "now")
		if st.Consecutive != 0 {
			t.Fatalf("clean scan should reset, got %+v", st)
		}
		// New episode can escalate again.
		escalations := 0
		for i := 0; i < N; i++ {
			var esc bool
			st, esc = evaluateFeedStorm(st, T, "now2")
			if esc {
				escalations++
			}
		}
		if escalations != 1 {
			t.Fatalf("expected re-escalation in new episode, got %d", escalations)
		}
	})

	t.Run("FirstSeen stamped once", func(t *testing.T) {
		st := feedStormState{}
		st, _ = evaluateFeedStorm(st, T, "first")
		st, _ = evaluateFeedStorm(st, T, "second")
		if st.FirstSeen != "first" {
			t.Fatalf("FirstSeen = %q, want stable 'first'", st.FirstSeen)
		}
	})
}
