// Shared Dolt circuit breaker for daemon patrol dogs.
//
// Per-process (not per-dog) breaker that wraps bd subprocess invocations
// from the daemon's patrol dogs. When Dolt is unhealthy, every dog
// retries on its own cadence — amplifying load on a recovering server
// and producing N independent error streams in daemon.log. The shared
// breaker:
//
//   - Tracks consecutive bd-call failures across all participating dogs.
//   - Trips OPEN at DoltCircuitBreakerThreshold consecutive failures.
//   - Stays OPEN for DoltCircuitBreakerCooldown, short-circuiting
//     `Allow()` so dogs skip their tick with a single 'dolt-degraded'
//     log line instead of issuing another failing subprocess.
//   - Half-opens after the cooldown elapses: the next caller is allowed
//     through; success closes the breaker, failure re-opens it.
//
// Source: gu-8f20q (P2-19 from the x2kby code-review synthesis). The
// design parallels CircuitBreakerState in internal/autotestpr (which
// counts consecutive auto-test-pr cycle closes) and ShouldTrip in
// internal/upstreamsync (which gates upstream-sync attempts) — same
// vocabulary so all three breakers read alike.
package daemon

import (
	"sync"
	"time"
)

// DoltCircuitBreakerThreshold is the number of consecutive bd-subprocess
// failures across patrol dogs before the breaker trips OPEN. Five is
// roughly "one full patrol cycle of bad" — at 60s intervals across 3
// participating dogs that's ~1-2 minutes of consistent failure, which
// is well past the transient hiccup classes (CGO restarts, build
// mismatches) the daemon already recovers from on its own.
const DoltCircuitBreakerThreshold = 5

// DoltCircuitBreakerCooldown is how long the breaker stays OPEN once
// tripped. After this window expires the next call is allowed through
// (half-open); a success closes the breaker, a failure re-opens it for
// another DoltCircuitBreakerCooldown.
//
// 30s is short enough that a recovered Dolt is back in service inside
// one patrol cycle, long enough to absorb the burst of failures that
// typically surrounds a Dolt restart.
const DoltCircuitBreakerCooldown = 30 * time.Second

// DoltCircuitBreakerState is the breaker's current operational mode.
type DoltCircuitBreakerState int

const (
	// DoltBreakerClosed is the normal state — calls are allowed through
	// and failures accumulate toward the trip threshold.
	DoltBreakerClosed DoltCircuitBreakerState = iota

	// DoltBreakerOpen is the tripped state — calls are short-circuited
	// (Allow returns false) until the cooldown elapses.
	DoltBreakerOpen

	// DoltBreakerHalfOpen is the recovery probe state — exactly one
	// caller per cooldown window is allowed through; the result of that
	// call decides whether the breaker closes again or re-opens.
	DoltBreakerHalfOpen
)

// DoltCircuitBreaker is a shared per-process circuit breaker for bd
// subprocess calls. Safe for concurrent use; all state is guarded by
// the embedded mutex.
//
// Constructed once at daemon startup and shared across all patrol dogs.
// Tests construct one directly with NewDoltCircuitBreakerForTest to pin
// the clock.
type DoltCircuitBreaker struct {
	mu sync.Mutex

	// state is the breaker's current Closed/Open/HalfOpen mode.
	state DoltCircuitBreakerState

	// failures is the consecutive-failure count in Closed state. Resets
	// to 0 on any success. The trip predicate is failures >= threshold.
	failures int

	// trippedAt anchors the current cooldown window. In Open it is when
	// the breaker last tripped — the breaker auto-promotes Open ->
	// HalfOpen once trippedAt + cooldown has passed. In HalfOpen it is
	// when the single in-flight probe was admitted; a probe that never
	// Records is presumed abandoned once trippedAt + cooldown passes and
	// a fresh probe is admitted in its place.
	trippedAt time.Time

	// threshold is the consecutive-failure count that trips the breaker.
	threshold int

	// cooldown is how long the breaker stays Open after a trip.
	cooldown time.Duration

	// nowFn is the clock indirection point for tests. Production reads
	// time.Now via this; tests override during setup.
	nowFn func() time.Time
}

// NewDoltCircuitBreaker returns a closed breaker with the production
// threshold and cooldown.
func NewDoltCircuitBreaker() *DoltCircuitBreaker {
	return &DoltCircuitBreaker{
		state:     DoltBreakerClosed,
		threshold: DoltCircuitBreakerThreshold,
		cooldown:  DoltCircuitBreakerCooldown,
		nowFn:     time.Now,
	}
}

// NewDoltCircuitBreakerForTest constructs a breaker with caller-pinned
// threshold, cooldown, and clock — for unit tests that need
// deterministic trip/recover behavior.
func NewDoltCircuitBreakerForTest(threshold int, cooldown time.Duration, nowFn func() time.Time) *DoltCircuitBreaker {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &DoltCircuitBreaker{
		state:     DoltBreakerClosed,
		threshold: threshold,
		cooldown:  cooldown,
		nowFn:     nowFn,
	}
}

// Allow reports whether the caller may proceed with a bd subprocess
// call. Returns false when the breaker is Open and the cooldown has not
// yet elapsed, and in HalfOpen once a probe is already in flight; true
// in Closed state and for the single HalfOpen probe.
//
// HalfOpen gating: HalfOpen admits exactly ONE probe per cooldown
// window. The Allow that promotes Open -> HalfOpen returns true and
// stamps trippedAt; every later Allow short-circuits (returns false)
// until the probe reports via Record (which closes or re-opens the
// breaker). This is what stops the breaker from silently becoming
// admit-all: before this gate, HalfOpen returned true unconditionally,
// so if the admitted probe early-returned without Record the breaker
// stayed HalfOpen forever and re-amplified load on a recovering Dolt
// (gu-nid89.41, the exact failure gu-8f20q exists to prevent).
//
// Abandoned-probe recovery: a probe that never Records (the dog hit an
// early return before its Record call) must not wedge the breaker in
// admit-none either. So once the admitted probe is older than one
// cooldown it is presumed abandoned and a fresh probe is admitted in
// its place. The net guarantee: HalfOpen can neither admit everyone nor
// block everyone indefinitely.
func (b *DoltCircuitBreaker) Allow() bool {
	// Nil-safe: tests construct Daemon literals with no breaker, and
	// any use of the breaker should fail open in that case (never
	// short-circuit the dog).
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case DoltBreakerOpen:
		if b.nowFn().Sub(b.trippedAt) >= b.cooldown {
			// Cooldown elapsed — promote to HalfOpen and admit the probe,
			// stamping trippedAt so a later Allow can detect an abandoned
			// probe and so concurrent callers short-circuit.
			b.state = DoltBreakerHalfOpen
			b.trippedAt = b.nowFn()
			return true
		}
		return false

	case DoltBreakerHalfOpen:
		// A probe is in flight. Admit a replacement only if the prior one
		// is presumed abandoned (older than one cooldown); otherwise
		// short-circuit so HalfOpen does not degrade into admit-all.
		if b.nowFn().Sub(b.trippedAt) >= b.cooldown {
			b.trippedAt = b.nowFn()
			return true
		}
		return false

	default:
		// Closed — admit.
		return true
	}
}

// Record updates the breaker with the outcome of a bd subprocess call.
// Pass err=nil for success, the call's error for failure.
//
// Closed state:
//   - success: failures counter resets to 0.
//   - failure: failures++; if failures >= threshold the breaker trips
//     OPEN (records trippedAt to start the cooldown).
//
// HalfOpen state:
//   - success: breaker closes; failures reset to 0.
//   - failure: breaker re-opens with a fresh trippedAt; failures
//     resets to threshold (so a future Closed transition starts clean).
//
// Open state: Record is a no-op. Allow() is the only path that
// promotes Open -> HalfOpen, and only Closed/HalfOpen callers ever
// observe an Allow=true that they then need to Record.
func (b *DoltCircuitBreaker) Record(err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case DoltBreakerClosed:
		if err == nil {
			b.failures = 0
			return
		}
		b.failures++
		if b.failures >= b.threshold {
			b.state = DoltBreakerOpen
			b.trippedAt = b.nowFn()
		}

	case DoltBreakerHalfOpen:
		if err == nil {
			b.state = DoltBreakerClosed
			b.failures = 0
			return
		}
		// Probe failed — re-open with a fresh trippedAt.
		b.state = DoltBreakerOpen
		b.trippedAt = b.nowFn()
		b.failures = b.threshold

	case DoltBreakerOpen:
		// Open is only entered/exited via the Closed/HalfOpen paths
		// above; a Record arriving while Open means a caller bypassed
		// Allow (or the state changed under us). No-op rather than
		// double-count.
	}
}

// State returns the breaker's current state. Intended for tests and
// observability — production callers gate on Allow() and feed the
// outcome through Record().
func (b *DoltCircuitBreaker) State() DoltCircuitBreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Failures returns the current consecutive-failure count. Useful for
// tests and for log lines that surface "how close are we to tripping?"
func (b *DoltCircuitBreaker) Failures() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failures
}
