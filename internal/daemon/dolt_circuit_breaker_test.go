package daemon

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// TestDoltCircuitBreaker_ClosedAdmitsCalls covers the steady-state happy
// path: a fresh breaker is Closed, Allow() returns true, and successful
// Record(nil) calls keep the failure counter pinned at 0.
func TestDoltCircuitBreaker_ClosedAdmitsCalls(t *testing.T) {
	t.Parallel()
	b := NewDoltCircuitBreakerForTest(3, 30*time.Second, fixedClock(time.Now()))
	for i := 0; i < 10; i++ {
		if !b.Allow() {
			t.Fatalf("call %d: Allow=false in Closed state", i)
		}
		b.Record(nil)
	}
	if got := b.State(); got != DoltBreakerClosed {
		t.Fatalf("state=%d, want Closed", got)
	}
	if got := b.Failures(); got != 0 {
		t.Fatalf("failures=%d, want 0", got)
	}
}

// TestDoltCircuitBreaker_TripsAtThreshold ensures consecutive failures
// trip the breaker exactly at the configured threshold (Nth failure
// trips, not (N+1)th). Mirrors ShouldTrip's contract in upstreamsync.
func TestDoltCircuitBreaker_TripsAtThreshold(t *testing.T) {
	t.Parallel()
	now := time.Now()
	b := NewDoltCircuitBreakerForTest(3, 30*time.Second, fixedClock(now))

	// Two failures: still Closed, Allow continues to admit.
	for i := 0; i < 2; i++ {
		if !b.Allow() {
			t.Fatalf("pre-trip call %d: Allow=false too early", i)
		}
		b.Record(errors.New("dolt down"))
	}
	if got := b.State(); got != DoltBreakerClosed {
		t.Fatalf("after 2 failures: state=%d, want Closed", got)
	}

	// Third failure trips.
	if !b.Allow() {
		t.Fatal("call before trip: Allow=false")
	}
	b.Record(errors.New("dolt down"))
	if got := b.State(); got != DoltBreakerOpen {
		t.Fatalf("after 3 failures: state=%d, want Open", got)
	}

	// Subsequent Allow short-circuits.
	if b.Allow() {
		t.Fatal("Allow=true while Open")
	}
}

// TestDoltCircuitBreaker_SuccessResetsFailures ensures a single success
// in Closed state clears accumulated failures so a transient hiccup
// followed by recovery does not gradually trip the breaker.
func TestDoltCircuitBreaker_SuccessResetsFailures(t *testing.T) {
	t.Parallel()
	b := NewDoltCircuitBreakerForTest(3, 30*time.Second, fixedClock(time.Now()))

	b.Record(errors.New("hiccup"))
	b.Record(errors.New("hiccup"))
	if got := b.Failures(); got != 2 {
		t.Fatalf("failures=%d, want 2", got)
	}

	b.Record(nil) // success
	if got := b.Failures(); got != 0 {
		t.Fatalf("after success: failures=%d, want 0", got)
	}

	// Two more failures must not re-trip yet (threshold still 3).
	b.Record(errors.New("hiccup"))
	b.Record(errors.New("hiccup"))
	if got := b.State(); got != DoltBreakerClosed {
		t.Fatalf("state=%d after 2 fresh failures, want Closed", got)
	}
}

// TestDoltCircuitBreaker_HalfOpenAfterCooldown covers the recovery
// path: once the cooldown elapses, the next Allow promotes Open ->
// HalfOpen and admits the probe.
func TestDoltCircuitBreaker_HalfOpenAfterCooldown(t *testing.T) {
	t.Parallel()
	now := time.Now()
	clock := &mockClock{now: now}
	b := NewDoltCircuitBreakerForTest(2, 10*time.Second, clock.Now)

	// Trip the breaker.
	b.Record(errors.New("fail"))
	b.Record(errors.New("fail"))
	if got := b.State(); got != DoltBreakerOpen {
		t.Fatalf("state=%d, want Open", got)
	}
	if b.Allow() {
		t.Fatal("Allow=true immediately after trip")
	}

	// Halfway through the cooldown — still Open.
	clock.advance(5 * time.Second)
	if b.Allow() {
		t.Fatal("Allow=true mid-cooldown")
	}

	// Cooldown elapsed — promote to HalfOpen and admit the probe.
	clock.advance(6 * time.Second)
	if !b.Allow() {
		t.Fatal("Allow=false after cooldown elapsed")
	}
	if got := b.State(); got != DoltBreakerHalfOpen {
		t.Fatalf("state=%d, want HalfOpen", got)
	}

	// Probe succeeds — breaker closes.
	b.Record(nil)
	if got := b.State(); got != DoltBreakerClosed {
		t.Fatalf("after successful probe: state=%d, want Closed", got)
	}
	if got := b.Failures(); got != 0 {
		t.Fatalf("after successful probe: failures=%d, want 0", got)
	}
}

// TestDoltCircuitBreaker_HalfOpenProbeFailureReopens ensures a failed
// probe re-opens the breaker for another full cooldown — Dolt is still
// down, do not flood it.
func TestDoltCircuitBreaker_HalfOpenProbeFailureReopens(t *testing.T) {
	t.Parallel()
	now := time.Now()
	clock := &mockClock{now: now}
	b := NewDoltCircuitBreakerForTest(2, 10*time.Second, clock.Now)

	b.Record(errors.New("fail"))
	b.Record(errors.New("fail"))
	clock.advance(11 * time.Second)
	if !b.Allow() {
		t.Fatal("Allow=false after cooldown")
	}
	if got := b.State(); got != DoltBreakerHalfOpen {
		t.Fatalf("state=%d, want HalfOpen", got)
	}

	// Probe fails — re-open with fresh trippedAt.
	b.Record(errors.New("still down"))
	if got := b.State(); got != DoltBreakerOpen {
		t.Fatalf("after failed probe: state=%d, want Open", got)
	}
	if b.Allow() {
		t.Fatal("Allow=true immediately after failed probe")
	}

	// Re-opened cooldown timer is fresh, not piggybacking on the first.
	clock.advance(9 * time.Second) // 9s past failed probe
	if b.Allow() {
		t.Fatal("Allow=true before fresh cooldown elapses")
	}
	clock.advance(2 * time.Second) // total 11s past failed probe
	if !b.Allow() {
		t.Fatal("Allow=false after fresh cooldown elapses")
	}
}

// TestDoltCircuitBreaker_RecordWhileOpenIsNoop ensures that a stray
// Record while Open (caller bypassed Allow) does not corrupt state.
// In production this should not happen, but the guard keeps the
// breaker defensive.
func TestDoltCircuitBreaker_RecordWhileOpenIsNoop(t *testing.T) {
	t.Parallel()
	now := time.Now()
	clock := &mockClock{now: now}
	b := NewDoltCircuitBreakerForTest(2, 10*time.Second, clock.Now)

	b.Record(errors.New("fail"))
	b.Record(errors.New("fail"))
	tripAt := b.trippedAtUnsafe()

	// Record while Open should not change state or restart the cooldown.
	clock.advance(5 * time.Second)
	b.Record(errors.New("noise"))
	b.Record(nil)
	if got := b.State(); got != DoltBreakerOpen {
		t.Fatalf("state=%d after noise records, want Open", got)
	}
	if newTripAt := b.trippedAtUnsafe(); !newTripAt.Equal(tripAt) {
		t.Fatalf("trippedAt drifted: %v -> %v", tripAt, newTripAt)
	}
}

// TestDoltCircuitBreaker_ConcurrentSafe runs many goroutines through
// the breaker to surface any data race under the -race detector.
func TestDoltCircuitBreaker_ConcurrentSafe(t *testing.T) {
	t.Parallel()
	b := NewDoltCircuitBreakerForTest(50, 1*time.Second, fixedClock(time.Now()))

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if b.Allow() {
					if (i+j)%3 == 0 {
						b.Record(errors.New("fail"))
					} else {
						b.Record(nil)
					}
				}
			}
		}(i)
	}
	wg.Wait()
	// No assertion on final state — this test is for the race detector.
}

// trippedAtUnsafe is a whitebox accessor for the no-op-while-Open
// test. Defined in _test.go so the production type does not grow a
// public trippedAt accessor.
func (b *DoltCircuitBreaker) trippedAtUnsafe() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.trippedAt
}

// mockClock is a manually-advanced clock for breaker tests. Mirrors
// the nowFn pattern in internal/upstreamsync/circuit_breaker.go.
type mockClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *mockClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mockClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fixedClock returns a nowFn that always reports the same time. Used
// for tests that do not exercise the cooldown branch.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}
