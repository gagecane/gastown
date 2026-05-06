package witness

import (
	"sort"
	"sync"
	"time"
)

// RedispatchRateLimitWindow is the sliding window used by the per-rig
// re-dispatch rate limiter. Capacity is refilled continuously — tokens
// older than the window are pruned on each Allow() call.
const RedispatchRateLimitWindow = time.Minute

// redispatchLimiter is a per-rig sliding-window rate limiter for witness
// RECOVERED_BEAD dispatches. It complements the per-bead circuit breaker in
// spawn_count.go: where ShouldBlockRespawn caps how many times a single
// bead can bounce, redispatchLimiter caps how many different beads can be
// re-dispatched per rig per minute. This is the mass-death backstop described
// in gu-pq2q (gu-ronb root cause).
//
// Design:
//   - Allow(beadID, now) records a successful dispatch (append timestamp) or
//     returns false when the cap is hit (append beadID to rateLimitedBeads).
//   - While the cap is saturated, the first rate-limited bead triggers one
//     SPAWN_STORM_RATE_LIMITED mail via ShouldSendMail(); subsequent rate-limited
//     beads in the same episode are silently queued to avoid mail floods.
//   - When capacity returns (a subsequent Allow() succeeds), the mail flag and
//     the rate-limited-bead set are reset so a future storm triggers a fresh
//     notification.
//
// Thread-safe: Allow may be called concurrently from multiple goroutines.
// A single witness process is bound to one rig, so in-process state is
// sufficient — no file-backed persistence is required.
type redispatchLimiter struct {
	window       time.Duration
	maxPerWindow int

	mu               sync.Mutex
	dispatchedAt     []time.Time
	rateLimitedBeads map[string]struct{}
	mailSent         bool
}

// newRedispatchLimiter returns a limiter with the given window and cap.
// If maxPerWindow <= 0 the limiter is effectively disabled (Allow always
// returns true).
func newRedispatchLimiter(window time.Duration, maxPerWindow int) *redispatchLimiter {
	return &redispatchLimiter{
		window:           window,
		maxPerWindow:     maxPerWindow,
		rateLimitedBeads: make(map[string]struct{}),
	}
}

// Allow returns true if the caller may dispatch a RECOVERED_BEAD mail now,
// false if the rate limit would be exceeded. On false, the caller MUST NOT
// reset the bead or send mail — leave the bead in its hooked/in_progress
// state so the next witness patrol re-discovers it and retries.
//
// On true, Allow records the dispatch. The beadID is only used for the
// rate-limited set (ignored on success).
//
// A maxPerWindow of 0 disables rate limiting: Allow always returns true and
// no state is tracked.
func (l *redispatchLimiter) Allow(beadID string, now time.Time) bool {
	if l.maxPerWindow <= 0 {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.pruneLocked(now)

	if len(l.dispatchedAt) >= l.maxPerWindow {
		if beadID != "" {
			l.rateLimitedBeads[beadID] = struct{}{}
		}
		return false
	}

	l.dispatchedAt = append(l.dispatchedAt, now)

	// Capacity is available — the rate-limited episode has cleared.
	// Reset so a future storm gets a fresh consolidated mail.
	if l.mailSent || len(l.rateLimitedBeads) > 0 {
		l.mailSent = false
		l.rateLimitedBeads = make(map[string]struct{})
	}

	return true
}

// ShouldSendMail returns true exactly once per rate-limited episode. Callers
// should only invoke it after Allow() has returned false. Returns false for
// every subsequent call until capacity frees up and a new episode can start.
func (l *redispatchLimiter) ShouldSendMail() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.mailSent {
		return false
	}
	l.mailSent = true
	return true
}

// RateLimitedBeads returns a sorted snapshot of beads that have been
// rate-limited in the current episode. Used as the body of the single
// consolidated SPAWN_STORM_RATE_LIMITED mail.
func (l *redispatchLimiter) RateLimitedBeads() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	ids := make([]string, 0, len(l.rateLimitedBeads))
	for id := range l.rateLimitedBeads {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Reset clears all limiter state. Intended for tests only.
func (l *redispatchLimiter) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dispatchedAt = nil
	l.rateLimitedBeads = make(map[string]struct{})
	l.mailSent = false
}

// pruneLocked removes dispatch timestamps older than the current window.
// Caller must hold l.mu.
func (l *redispatchLimiter) pruneLocked(now time.Time) {
	cutoff := now.Add(-l.window)
	// Timestamps are appended in monotonic order; find the first in-window.
	i := 0
	for ; i < len(l.dispatchedAt); i++ {
		if !l.dispatchedAt[i].Before(cutoff) {
			break
		}
	}
	if i > 0 {
		l.dispatchedAt = l.dispatchedAt[i:]
	}
}

// Package-level per-rig limiter registry.
//
// A single witness process binds to one rig, but the registry keys by rig
// name anyway so:
//  1. Future multi-rig processes (e.g. `gt up` orphan recovery) don't alias
//     state across rigs.
//  2. Tests can exercise multiple rigs within the same process.
var (
	redispatchLimiterMu sync.Mutex
	redispatchLimiters  = make(map[string]*redispatchLimiter)
)

// getRedispatchLimiter returns the per-rig limiter, creating it on first use.
// The first caller's maxPerMinute wins — subsequent calls reuse the existing
// limiter regardless of the maxPerMinute argument. This is intentional:
// operational config is loaded once per patrol cycle anyway, and changing
// the cap mid-flight would make the sliding-window semantics murky. A config
// change is picked up on the next witness restart.
func getRedispatchLimiter(rigName string, maxPerMinute int) *redispatchLimiter {
	redispatchLimiterMu.Lock()
	defer redispatchLimiterMu.Unlock()
	l, ok := redispatchLimiters[rigName]
	if !ok {
		l = newRedispatchLimiter(RedispatchRateLimitWindow, maxPerMinute)
		redispatchLimiters[rigName] = l
	}
	return l
}

// resetRedispatchLimitersForTest clears the package-level registry.
// Intended for tests only — production code must never call this.
func resetRedispatchLimitersForTest() {
	redispatchLimiterMu.Lock()
	defer redispatchLimiterMu.Unlock()
	redispatchLimiters = make(map[string]*redispatchLimiter)
}
