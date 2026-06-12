package daemon

import (
	"database/sql"
	"testing"
	"time"
)

// sampleAt advances the state machine by one sample at a given wall-clock time.
func sampleAt(st connLeakState, count int, at time.Time) (connLeakState, connLeakAction) {
	return evaluateConnLeak(st, count, at)
}

// TestConnLeak_ReturnToBaselineDoesNotTrip is the connection-LIFECYCLE regression
// guard the audit (gu-d1r8g, §8.2) asked for: it asserts that a convoy-close
// cycle whose connection count RETURNS TO BASELINE never trips the leak monitor.
// This is the test that fails if the leak class regresses into a not-returning
// trajectory — the ~10× re-file cycle (gu-g7q6z et al.) that no prior test caught
// because TestDaemonStoreIdleTimeout only checks the idle-timeout knob, not the
// post-cycle baseline.
func TestConnLeak_ReturnToBaselineDoesNotTrip(t *testing.T) {
	base := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	// A convoy-close cycle: connections rise during the cycle, then RETURN to
	// baseline when the cycle releases them. This is the healthy lifecycle.
	trajectory := []int{8, 12, 40, 70, 30, 9, 8}
	st := connLeakState{}
	for i, count := range trajectory {
		var act connLeakAction
		st, act = sampleAt(st, count, base.Add(time.Duration(i)*5*time.Minute))
		if act.Escalate {
			t.Fatalf("sample %d (count=%d): escalated on a return-to-baseline cycle — the leak guard misfired", i, count)
		}
	}
	// After the cycle returns to baseline, the monitor must be fully re-armed:
	// no lingering climbing episode, no escalation latch.
	if st.Consecutive != 0 {
		t.Errorf("after baseline return, Consecutive=%d, want 0 (monitor not re-armed)", st.Consecutive)
	}
	if st.Escalated {
		t.Error("after baseline return, Escalated=true, want false (episode not cleared)")
	}
}

// TestConnLeak_MonotonicClimbTripsOnce asserts the leak signature — a connection
// count climbing monotonically above the floor and never returning to baseline —
// escalates exactly once, after the sustained-climb threshold, and self-mitigates
// on every climbing sample before that.
func TestConnLeak_MonotonicClimbTripsOnce(t *testing.T) {
	base := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	// ~50/min climb (the storm rate): +250 conns per 5-min sample, well above the
	// 20/min threshold and clear of the floor.
	st := connLeakState{}
	escalations := 0
	mitigations := 0
	count := 60
	for i := 0; i < connLeakConsecutiveThreshold+3; i++ {
		var act connLeakAction
		st, act = sampleAt(st, count, base.Add(time.Duration(i)*5*time.Minute))
		if act.Escalate {
			escalations++
		}
		if act.Mitigate {
			mitigations++
		}
		count += 250
	}
	if escalations != 1 {
		t.Errorf("monotonic climb escalated %d times, want exactly 1 (deduped)", escalations)
	}
	if mitigations < connLeakConsecutiveThreshold {
		t.Errorf("self-mitigation fired %d times, want >= %d (every climbing sample)", mitigations, connLeakConsecutiveThreshold)
	}
	if !st.Escalated {
		t.Error("after sustained climb, Escalated=false, want true")
	}
}

// TestConnLeak_SeedSampleNeverTrips verifies the first sample of an episode (no
// prior trajectory) only seeds the state and takes no action, so a daemon
// cold-start reading a high count cannot trip the monitor without a measured rate.
func TestConnLeak_SeedSampleNeverTrips(t *testing.T) {
	base := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	st, act := sampleAt(connLeakState{}, 900, base)
	if act.Mitigate || act.Escalate {
		t.Fatalf("seed sample took action %+v, want none (no prior sample to measure a rate)", act)
	}
	if st.LastSample != 900 {
		t.Errorf("seed sample LastSample=%d, want 900 (trajectory must be recorded)", st.LastSample)
	}
}

// TestConnLeak_ClimbBelowFloorIgnored verifies a fast growth rate below the
// connection floor is ignored — a burst from 1→19 conns is normal concurrency,
// not a saturation risk, even if the rate momentarily exceeds the threshold.
func TestConnLeak_ClimbBelowFloorIgnored(t *testing.T) {
	base := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	st := connLeakState{}
	// Seed, then a sample that grows fast in RATE but stays under the floor.
	st, _ = sampleAt(st, 2, base)
	_, act := sampleAt(st, 19, base.Add(10*time.Second)) // ~102/min but count=19 < floor
	if act.Mitigate || act.Escalate {
		t.Fatalf("below-floor burst took action %+v, want none (normal concurrency)", act)
	}
}

// TestConnLeak_SlowGrowthBelowRateIgnored verifies a count above the floor that
// grows SLOWLY (below the rate threshold) is treated as legitimate sustained
// load, not a leak — the leak is identified by its RATE, not by absolute count
// (which is already covered by the connection-count warning).
func TestConnLeak_SlowGrowthBelowRateIgnored(t *testing.T) {
	base := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	st := connLeakState{}
	st, _ = sampleAt(st, 60, base)
	// +10 conns over 5 min = 2/min, well below the 20/min threshold.
	_, act := sampleAt(st, 70, base.Add(5*time.Minute))
	if act.Mitigate || act.Escalate {
		t.Fatalf("slow growth above floor took action %+v, want none (sustained load, not a leak)", act)
	}
}

// TestConnLeak_RecoveryMidEpisodeReArms verifies that if a climb is interrupted
// by a return-to-baseline sample before reaching the escalation threshold, the
// monitor re-arms (resets) and a fresh episode can escalate again — mirroring the
// feed-storm recovery semantics.
func TestConnLeak_RecoveryMidEpisodeReArms(t *testing.T) {
	base := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	st := connLeakState{}
	// Seed + climb one short of the threshold.
	st, _ = sampleAt(st, 60, base)
	for i := 1; i < connLeakConsecutiveThreshold; i++ {
		st, _ = sampleAt(st, 60+250*i, base.Add(time.Duration(i)*5*time.Minute))
	}
	if st.Consecutive >= connLeakConsecutiveThreshold {
		t.Fatalf("test setup climbed too far: Consecutive=%d", st.Consecutive)
	}
	// A return-to-baseline sample re-arms.
	st, _ = sampleAt(st, 8, base.Add(10*time.Hour))
	if st.Consecutive != 0 {
		t.Fatalf("baseline sample did not re-arm: Consecutive=%d", st.Consecutive)
	}
	// A new monotonic climb escalates again.
	escalations := 0
	count := 60
	t0 := base.Add(20 * time.Hour)
	st, _ = sampleAt(st, count, t0) // seed the new episode
	for i := 1; i <= connLeakConsecutiveThreshold; i++ {
		count += 250
		var act connLeakAction
		st, act = sampleAt(st, count, t0.Add(time.Duration(i)*5*time.Minute))
		if act.Escalate {
			escalations++
		}
	}
	if escalations != 1 {
		t.Errorf("new episode escalated %d times, want 1 (re-escalation after re-arm)", escalations)
	}
}

// TestConnLeak_StateRoundTrips verifies the persisted state survives a
// save/load round-trip so a leak episode that spans a daemon restart keeps its
// climb history rather than resetting.
func TestConnLeak_StateRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := connLeakStatePath(dir)
	want := connLeakState{
		LastSample:   843,
		LastSampleAt: "2026-06-12T00:00:00Z",
		Consecutive:  2,
		FirstSeen:    "2026-06-12T00:00:00Z",
		PeakSample:   843,
		Escalated:    true,
	}
	saveConnLeakState(path, want)
	got := loadConnLeakState(path)
	if got != want {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

// TestConnLeak_LoadMissingStateIsZero verifies a missing state file loads as the
// zero value (fresh monitor) rather than erroring.
func TestConnLeak_LoadMissingStateIsZero(t *testing.T) {
	got := loadConnLeakState(connLeakStatePath(t.TempDir()))
	if (got != connLeakState{}) {
		t.Errorf("missing state loaded as %+v, want zero value", got)
	}
}

// TestRecyclePoolDB_RestoresSymmetricPool verifies the self-heal pool surgery
// leaves the pool with the gu-g7q6z symmetric-pool hardening intact
// (MaxIdleConns == MaxOpenConns) after releasing idle connections — i.e. the
// recycle does not leave the pool in the degraded close-on-return shape the leak
// fix removed. sql.Open is lazy so this needs no live server.
func TestRecyclePoolDB_RestoresSymmetricPool(t *testing.T) {
	db, err := sql.Open("mysql", "root@tcp(127.0.0.1:3307)/hq")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)

	maxIdle := recyclePoolDB(db, 15*time.Second)
	if maxIdle != 10 {
		t.Errorf("recyclePoolDB restored MaxIdleConns=%d, want 10 (== MaxOpenConns, symmetric pool)", maxIdle)
	}
}

// TestRecyclePoolDB_NilPoolNoPanic verifies the self-heal is a safe no-op for a
// store that does not expose a raw pool.
func TestRecyclePoolDB_NilPoolNoPanic(t *testing.T) {
	if got := recyclePoolDB(nil, 15*time.Second); got != 0 {
		t.Errorf("recyclePoolDB(nil) = %d, want 0", got)
	}
}
