package cmd

import (
	"testing"
)

// TestEvaluateCapacityExhaustion locks down the hq-ly5yj escalation state
// machine: escalate exactly once, only after the pool has been exhausted for
// capacityExhaustionThreshold consecutive cycles, and re-arm on recovery.
func TestEvaluateCapacityExhaustion(t *testing.T) {
	const now = "2026-05-30T12:00:00Z"

	// A single exhausted blip below threshold must NOT escalate.
	st, esc := evaluateCapacityExhaustion(capacityExhaustionState{}, true, now)
	if esc || st.Consecutive != 1 {
		t.Fatalf("first exhausted cycle: esc=%v consec=%d, want esc=false consec=1", esc, st.Consecutive)
	}
	if st.FirstSeen != now {
		t.Errorf("FirstSeen = %q, want %q", st.FirstSeen, now)
	}

	// Walk up to the threshold; escalation fires exactly on the crossing cycle.
	for i := 2; i < capacityExhaustionThreshold; i++ {
		st, esc = evaluateCapacityExhaustion(st, true, "later")
		if esc {
			t.Fatalf("cycle %d: escalated before threshold %d", i, capacityExhaustionThreshold)
		}
	}
	st, esc = evaluateCapacityExhaustion(st, true, "later")
	if !esc {
		t.Fatalf("cycle %d (threshold): expected escalation", capacityExhaustionThreshold)
	}
	if !st.Escalated || st.Consecutive != capacityExhaustionThreshold {
		t.Fatalf("at threshold: %+v, want Escalated=true Consecutive=%d", st, capacityExhaustionThreshold)
	}

	// Still exhausted past threshold: keep counting but do NOT re-escalate
	// (the fingerprint + Escalated flag suppress repeats within the episode).
	st, esc = evaluateCapacityExhaustion(st, true, "later")
	if esc {
		t.Errorf("post-threshold cycle re-escalated; want suppressed")
	}
	if st.Consecutive != capacityExhaustionThreshold+1 {
		t.Errorf("Consecutive = %d, want %d", st.Consecutive, capacityExhaustionThreshold+1)
	}

	// Recovery resets to zero so a fresh episode re-arms and re-escalates.
	st, esc = evaluateCapacityExhaustion(st, false, "later")
	if esc || st != (capacityExhaustionState{}) {
		t.Errorf("recovery: esc=%v state=%+v, want esc=false zero-state", esc, st)
	}
}

// TestMonitorCapacityExhaustion_EndToEnd drives the file-backed monitor across
// cycles and asserts the escalation fires once at threshold, is suppressed
// after, and re-arms on a successful dispatch (reset).
func TestMonitorCapacityExhaustion_EndToEnd(t *testing.T) {
	town := t.TempDir()

	var fired int
	orig := fireCapacityExhaustionEscalation
	fireCapacityExhaustionEscalation = func(_ polecatCapacitySnapshot, _ int, _ capacityExhaustionState) { fired++ }
	defer func() { fireCapacityExhaustionEscalation = orig }()

	wedged := polecatCapacitySnapshot{Max: 8, RecoveryBlocked: 8} // working+reusable_idle == 0

	// Below threshold: no escalation.
	for i := 1; i < capacityExhaustionThreshold; i++ {
		monitorCapacityExhaustion(town, wedged, 30)
	}
	if fired != 0 {
		t.Fatalf("escalated %d times before threshold, want 0", fired)
	}

	// Crossing the threshold escalates once; further exhausted cycles do not.
	monitorCapacityExhaustion(town, wedged, 30)
	monitorCapacityExhaustion(town, wedged, 30)
	if fired != 1 {
		t.Fatalf("escalated %d times, want exactly 1 across the episode", fired)
	}

	// A successful dispatch resets the episode; a fresh outage re-escalates.
	resetCapacityExhaustion(town)
	for i := 0; i < capacityExhaustionThreshold; i++ {
		monitorCapacityExhaustion(town, wedged, 30)
	}
	if fired != 2 {
		t.Fatalf("after reset+re-exhaust: fired=%d, want 2", fired)
	}

	// Sanity: a skip with usable capacity (reusable_idle>0) is NOT exhaustion.
	resetCapacityExhaustion(town)
	healthy := polecatCapacitySnapshot{Max: 8, ReusableIdle: 2}
	for i := 0; i < capacityExhaustionThreshold+2; i++ {
		monitorCapacityExhaustion(town, healthy, 30)
	}
	if fired != 2 {
		t.Errorf("usable capacity must not escalate: fired=%d, want 2", fired)
	}

	// hq-q943s: TRICKLE starvation — one bead dispatches each cycle so working
	// stays 0-2, but a recovery_blocked MAJORITY persists and ready beads keep
	// being skipped. The old alarm reset on the nonzero working and never fired;
	// it must now escalate.
	resetCapacityExhaustion(town)
	trickle := polecatCapacitySnapshot{Max: 8, Working: 2, RecoveryBlocked: 6} // working>0 yet 6/8 wedged
	for i := 0; i < capacityExhaustionThreshold; i++ {
		monitorCapacityExhaustion(town, trickle, 30)
	}
	if fired != 3 {
		t.Errorf("trickle starvation (working>0, recovery_blocked majority) must escalate: fired=%d, want 3", fired)
	}
}

// TestPoolDegraded pins the hq-q943s degraded-pool detection: hard-zero OR a
// strict recovery_blocked majority counts; a healthy or exactly-half pool does not.
func TestPoolDegraded(t *testing.T) {
	cases := []struct {
		name string
		s    polecatCapacitySnapshot
		want bool
	}{
		{"hard zero (no working, no reusable)", polecatCapacitySnapshot{Max: 8, RecoveryBlocked: 8}, true},
		{"recovery majority despite trickle working", polecatCapacitySnapshot{Max: 8, Working: 2, RecoveryBlocked: 6}, true},
		{"recovery just over half", polecatCapacitySnapshot{Max: 8, Working: 3, RecoveryBlocked: 5}, true},
		{"recovery exactly half is not a majority", polecatCapacitySnapshot{Max: 8, Working: 4, RecoveryBlocked: 4}, false},
		{"healthy pool", polecatCapacitySnapshot{Max: 8, Working: 5, ReusableIdle: 2, RecoveryBlocked: 1}, false},
		{"max unknown, some working → not degraded", polecatCapacitySnapshot{Working: 2, RecoveryBlocked: 9}, false},
	}
	for _, tc := range cases {
		if got := poolDegraded(tc.s); got != tc.want {
			t.Errorf("%s: poolDegraded(%+v) = %v, want %v", tc.name, tc.s, got, tc.want)
		}
	}
}
