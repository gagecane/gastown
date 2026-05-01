package daemon

import (
	"testing"
	"time"
)

// newTestBackoff returns a DogStartupBackoff whose clock is driven by the
// returned advance() function. Call advance(d) to push the virtual clock
// forward by d.
func newTestBackoff() (*DogStartupBackoff, func(time.Duration)) {
	now := time.Unix(1_700_000_000, 0).UTC()
	b := &DogStartupBackoff{
		now:    func() time.Time { return now },
		states: make(map[string]*dogStartupFailure),
	}
	advance := func(d time.Duration) {
		now = now.Add(d)
	}
	return b, advance
}

func TestDogStartupBackoff_FirstFailureHasNoDelay(t *testing.T) {
	b, _ := newTestBackoff()

	count, delay := b.RecordFailure("alpha")
	if count != 1 {
		t.Errorf("consecutive = %d, want 1", count)
	}
	if delay != 0 {
		t.Errorf("delay = %v, want 0 (first failure is transient)", delay)
	}
	// ShouldSkip is false immediately: a zero-delay failure does not defer.
	if skip, reason := b.ShouldSkip("alpha"); skip {
		t.Errorf("ShouldSkip after 1st failure = true (%s), want false", reason)
	}
}

func TestDogStartupBackoff_SecondFailureBacksOff(t *testing.T) {
	b, advance := newTestBackoff()

	b.RecordFailure("alpha")
	count, delay := b.RecordFailure("alpha")
	if count != 2 {
		t.Errorf("consecutive = %d, want 2", count)
	}
	if delay != 3*time.Minute {
		t.Errorf("delay = %v, want 3m", delay)
	}

	// Immediately after the 2nd failure, dispatch must be deferred.
	if skip, _ := b.ShouldSkip("alpha"); !skip {
		t.Error("ShouldSkip immediately after 2nd failure = false, want true")
	}

	// Just before the window ends, still skipping.
	advance(3*time.Minute - time.Second)
	if skip, _ := b.ShouldSkip("alpha"); !skip {
		t.Error("ShouldSkip 2s59s into 3m backoff = false, want true")
	}

	// After the window elapses, the skip clears.
	advance(2 * time.Second)
	if skip, reason := b.ShouldSkip("alpha"); skip {
		t.Errorf("ShouldSkip after 3m backoff elapsed = true (%s), want false", reason)
	}
}

func TestDogStartupBackoff_ScheduleMatchesProposal(t *testing.T) {
	b, _ := newTestBackoff()

	// Walk through the documented schedule. Each RecordFailure happens
	// "immediately" in virtual time, which is fine — the reset window is
	// 5 minutes and we never advance far enough to trip it.
	cases := []struct {
		attempt   int
		wantDelay time.Duration
	}{
		{1, 0},
		{2, 3 * time.Minute},
		{3, 6 * time.Minute},
		{4, 15 * time.Minute},
		{5, 15 * time.Minute}, // beyond the table: clamps to longest delay
		{6, 15 * time.Minute},
	}
	for _, tc := range cases {
		got, delay := b.RecordFailure("alpha")
		if got != tc.attempt {
			t.Errorf("attempt %d: consecutive = %d, want %d", tc.attempt, got, tc.attempt)
		}
		if delay != tc.wantDelay {
			t.Errorf("attempt %d: delay = %v, want %v", tc.attempt, delay, tc.wantDelay)
		}
	}
}

func TestDogStartupBackoff_RecordSuccessResets(t *testing.T) {
	b, _ := newTestBackoff()

	b.RecordFailure("alpha")
	b.RecordFailure("alpha")
	b.RecordFailure("alpha") // 3 consecutive failures
	if got := b.ConsecutiveFailures("alpha"); got != 3 {
		t.Fatalf("consecutive before success = %d, want 3", got)
	}

	b.RecordSuccess("alpha")
	if got := b.ConsecutiveFailures("alpha"); got != 0 {
		t.Errorf("consecutive after success = %d, want 0", got)
	}
	if skip, reason := b.ShouldSkip("alpha"); skip {
		t.Errorf("ShouldSkip after success = true (%s), want false", reason)
	}

	// A fresh failure after success starts at attempt 1 (no delay).
	count, delay := b.RecordFailure("alpha")
	if count != 1 {
		t.Errorf("consecutive after success+failure = %d, want 1", count)
	}
	if delay != 0 {
		t.Errorf("delay after success+failure = %v, want 0", delay)
	}
}

func TestDogStartupBackoff_ResetWindowElapsesBetweenFailures(t *testing.T) {
	b, advance := newTestBackoff()

	// Two failures close together put us at attempt 2 with a 3m delay.
	b.RecordFailure("alpha")
	count, _ := b.RecordFailure("alpha")
	if count != 2 {
		t.Fatalf("consecutive after back-to-back = %d, want 2", count)
	}

	// Advance well beyond the 5-minute reset window with no new failure.
	// The next failure should be treated as a fresh streak (attempt 1).
	advance(10 * time.Minute)
	count, delay := b.RecordFailure("alpha")
	if count != 1 {
		t.Errorf("consecutive after window elapsed = %d, want 1", count)
	}
	if delay != 0 {
		t.Errorf("delay after window elapsed = %v, want 0", delay)
	}
}

func TestDogStartupBackoff_WithinWindowDoesNotReset(t *testing.T) {
	b, advance := newTestBackoff()

	b.RecordFailure("alpha")
	// Advance less than the reset window.
	advance(dogFailureResetWindow - time.Second)
	count, delay := b.RecordFailure("alpha")
	if count != 2 {
		t.Errorf("consecutive within window = %d, want 2", count)
	}
	if delay != 3*time.Minute {
		t.Errorf("delay within window = %v, want 3m", delay)
	}
}

func TestDogStartupBackoff_PerDogIsolation(t *testing.T) {
	b, _ := newTestBackoff()

	// Fail alpha twice — it should be in backoff.
	b.RecordFailure("alpha")
	b.RecordFailure("alpha")

	if skip, _ := b.ShouldSkip("alpha"); !skip {
		t.Error("alpha: ShouldSkip = false, want true")
	}
	if skip, _ := b.ShouldSkip("bravo"); skip {
		t.Error("bravo: ShouldSkip = true, want false (no failures recorded)")
	}

	// bravo's first failure is independent — no delay.
	_, delay := b.RecordFailure("bravo")
	if delay != 0 {
		t.Errorf("bravo first failure delay = %v, want 0", delay)
	}
}

func TestDogStartupBackoff_WarnThresholdReached(t *testing.T) {
	b, _ := newTestBackoff()

	var finalCount int
	for i := 0; i < dogFailureWarnThreshold; i++ {
		finalCount, _ = b.RecordFailure("alpha")
	}
	if finalCount != dogFailureWarnThreshold {
		t.Errorf("final count = %d, want %d", finalCount, dogFailureWarnThreshold)
	}
	// At and above the warn threshold, the delay is the maximum of the schedule.
	if d := backoffDelay(finalCount); d != dogBackoffSchedule[len(dogBackoffSchedule)-1] {
		t.Errorf("delay at warn threshold = %v, want %v", d, dogBackoffSchedule[len(dogBackoffSchedule)-1])
	}
}

func TestDogStartupBackoff_ShouldSkipUnknownDog(t *testing.T) {
	b, _ := newTestBackoff()
	if skip, reason := b.ShouldSkip("ghost"); skip {
		t.Errorf("ShouldSkip for unknown dog = true (%s), want false", reason)
	}
}

func TestDogStartupBackoff_RecordSuccessOnUnknownDogIsSafe(t *testing.T) {
	b, _ := newTestBackoff()
	b.RecordSuccess("ghost") // must not panic
	if got := b.ConsecutiveFailures("ghost"); got != 0 {
		t.Errorf("ConsecutiveFailures for ghost = %d, want 0", got)
	}
}

func TestDogStartupBackoff_DefaultClockIsWallTime(t *testing.T) {
	// NewDogStartupBackoff should use time.Now by default. We can't assert
	// an exact timestamp, but we can verify the clock advances between
	// calls and that it's close to the real wall clock.
	b := NewDogStartupBackoff()
	before := time.Now()
	b.RecordFailure("alpha")
	b.RecordFailure("alpha")
	after := time.Now()

	state := b.states["alpha"]
	if state == nil {
		t.Fatal("no state recorded for alpha")
	}
	if state.lastFailure.Before(before) || state.lastFailure.After(after) {
		t.Errorf("lastFailure = %v, want between %v and %v", state.lastFailure, before, after)
	}
}

func TestBackoffDelay_EdgeCases(t *testing.T) {
	if got := backoffDelay(0); got != 0 {
		t.Errorf("backoffDelay(0) = %v, want 0", got)
	}
	if got := backoffDelay(-1); got != 0 {
		t.Errorf("backoffDelay(-1) = %v, want 0", got)
	}
	last := dogBackoffSchedule[len(dogBackoffSchedule)-1]
	if got := backoffDelay(100); got != last {
		t.Errorf("backoffDelay(100) = %v, want %v (clamped to longest delay)", got, last)
	}
}
