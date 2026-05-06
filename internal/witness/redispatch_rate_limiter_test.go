package witness

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRedispatchLimiter_Allow_UnderCap(t *testing.T) {
	t.Parallel()

	l := newRedispatchLimiter(time.Minute, 5)
	now := time.Now()

	for i := 0; i < 5; i++ {
		if !l.Allow(fmt.Sprintf("bead-%d", i), now.Add(time.Duration(i)*time.Millisecond)) {
			t.Errorf("Allow[%d] = false under cap, want true", i)
		}
	}
}

func TestRedispatchLimiter_Allow_AtCapReturnsFalse(t *testing.T) {
	t.Parallel()

	l := newRedispatchLimiter(time.Minute, 3)
	now := time.Now()

	// Fill the bucket.
	for i := 0; i < 3; i++ {
		if !l.Allow(fmt.Sprintf("ok-%d", i), now) {
			t.Fatalf("setup: Allow[%d] = false, want true", i)
		}
	}

	// Next call exceeds the cap.
	if l.Allow("blocked-1", now) {
		t.Error("Allow = true at cap, want false")
	}
	if l.Allow("blocked-2", now) {
		t.Error("Allow = true at cap (second attempt), want false")
	}

	beads := l.RateLimitedBeads()
	if len(beads) != 2 {
		t.Fatalf("RateLimitedBeads = %v (len %d), want 2", beads, len(beads))
	}
	if beads[0] != "blocked-1" || beads[1] != "blocked-2" {
		t.Errorf("RateLimitedBeads = %v, want [blocked-1 blocked-2]", beads)
	}
}

func TestRedispatchLimiter_MassDeath_10DispatchedWithinWindow(t *testing.T) {
	t.Parallel()

	// Acceptance criterion from gu-pq2q:
	//   "fire 10 simultaneous dead-session events, verify only N dispatched
	//    within the window"
	const cap = 4
	l := newRedispatchLimiter(time.Minute, cap)
	now := time.Now()

	allowed := 0
	denied := 0
	for i := 0; i < 10; i++ {
		if l.Allow(fmt.Sprintf("bead-%02d", i), now) {
			allowed++
		} else {
			denied++
		}
	}

	if allowed != cap {
		t.Errorf("allowed = %d, want %d", allowed, cap)
	}
	if denied != 10-cap {
		t.Errorf("denied = %d, want %d", denied, 10-cap)
	}
	if got := len(l.RateLimitedBeads()); got != 10-cap {
		t.Errorf("RateLimitedBeads count = %d, want %d", got, 10-cap)
	}
}

func TestRedispatchLimiter_SlidingWindowReopens(t *testing.T) {
	t.Parallel()

	l := newRedispatchLimiter(time.Minute, 2)
	start := time.Now()

	// Two dispatches fill the bucket at t=0.
	if !l.Allow("a", start) || !l.Allow("b", start) {
		t.Fatal("setup: first two dispatches should be allowed")
	}
	if l.Allow("c", start) {
		t.Fatal("setup: third dispatch at t=0 should be rate-limited")
	}

	// At t=61s, the original timestamps fall out of the window.
	later := start.Add(61 * time.Second)
	if !l.Allow("d", later) {
		t.Error("Allow after window expiry = false, want true")
	}
}

func TestRedispatchLimiter_ShouldSendMail_OncePerEpisode(t *testing.T) {
	t.Parallel()

	l := newRedispatchLimiter(time.Minute, 1)
	now := time.Now()

	// Saturate.
	if !l.Allow("a", now) {
		t.Fatal("setup: Allow should succeed once")
	}
	if l.Allow("b", now) {
		t.Fatal("setup: Allow should fail at cap")
	}

	if !l.ShouldSendMail() {
		t.Error("first ShouldSendMail = false, want true")
	}
	if l.ShouldSendMail() {
		t.Error("second ShouldSendMail = true (flood), want false")
	}

	// More rate-limited beads in the same episode still don't trigger mail.
	if l.Allow("c", now) {
		t.Fatal("Allow at cap should still fail")
	}
	if l.ShouldSendMail() {
		t.Error("ShouldSendMail within same episode = true, want false")
	}
}

func TestRedispatchLimiter_MailFlagResetsAfterCapacityReturns(t *testing.T) {
	t.Parallel()

	l := newRedispatchLimiter(time.Minute, 1)
	start := time.Now()

	// Episode 1: saturate, rate-limit, claim mail.
	if !l.Allow("a", start) {
		t.Fatal("setup: first Allow should succeed")
	}
	if l.Allow("b", start) {
		t.Fatal("setup: Allow at cap should fail")
	}
	if !l.ShouldSendMail() {
		t.Fatal("first ShouldSendMail should return true")
	}

	// Capacity returns after the window expires.
	later := start.Add(61 * time.Second)
	if !l.Allow("c", later) {
		t.Fatal("Allow after window should succeed")
	}

	// Episode 2: saturate again — mail flag must have reset.
	if l.Allow("d", later) {
		t.Fatal("Allow at cap (episode 2) should fail")
	}
	if !l.ShouldSendMail() {
		t.Error("ShouldSendMail for episode 2 = false, want true (flag should reset)")
	}

	// Episode 2 should not carry over beads from episode 1.
	beads := l.RateLimitedBeads()
	if len(beads) != 1 || beads[0] != "d" {
		t.Errorf("RateLimitedBeads = %v, want [d] (episode 1 state should be cleared)", beads)
	}
}

func TestRedispatchLimiter_DisabledWhenCapZero(t *testing.T) {
	t.Parallel()

	l := newRedispatchLimiter(time.Minute, 0)
	now := time.Now()

	for i := 0; i < 50; i++ {
		if !l.Allow(fmt.Sprintf("bead-%d", i), now) {
			t.Fatalf("disabled limiter rejected Allow[%d]", i)
		}
	}
	if got := len(l.RateLimitedBeads()); got != 0 {
		t.Errorf("disabled limiter tracked %d rate-limited beads, want 0", got)
	}
}

func TestRedispatchLimiter_ConcurrentAllowIsSafe(t *testing.T) {
	t.Parallel()

	const (
		goroutines = 20
		perG       = 50
		cap        = 100
	)
	l := newRedispatchLimiter(time.Minute, cap)
	start := time.Now()

	var wg sync.WaitGroup
	var mu sync.Mutex
	var allowed int

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			local := 0
			for i := 0; i < perG; i++ {
				if l.Allow(fmt.Sprintf("g%d-b%d", id, i), start) {
					local++
				}
			}
			mu.Lock()
			allowed += local
			mu.Unlock()
		}(g)
	}
	wg.Wait()

	if allowed != cap {
		t.Errorf("total allowed = %d, want exactly %d (cap) under concurrent load", allowed, cap)
	}
}

func TestGetRedispatchLimiter_PerRigIsolation(t *testing.T) {
	resetRedispatchLimitersForTest()
	t.Cleanup(resetRedispatchLimitersForTest)

	now := time.Now()
	a := getRedispatchLimiter("rig-a", 1)
	b := getRedispatchLimiter("rig-b", 1)
	if a == b {
		t.Fatal("expected distinct limiter instances per rig, got same pointer")
	}

	// Saturating rig-a must not affect rig-b.
	if !a.Allow("a1", now) || a.Allow("a2", now) {
		t.Fatalf("rig-a: expected first Allow true then false")
	}
	if !b.Allow("b1", now) {
		t.Error("rig-b Allow = false despite rig-a being saturated")
	}

	// Second getRedispatchLimiter returns the same instance.
	if got := getRedispatchLimiter("rig-a", 99); got != a {
		t.Error("getRedispatchLimiter should return cached instance for known rig")
	}
}
