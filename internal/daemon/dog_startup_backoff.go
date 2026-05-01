package daemon

import (
	"fmt"
	"sync"
	"time"
)

// Dog startup backoff parameters.
//
// When sm.Start fails repeatedly for a given dog, dispatchPlugins backs off
// exponentially to avoid tight retry loops that burn API credits and pollute
// logs. See gu-cvbm.
//
// The schedule is time-based (not heartbeat-count-based) so it works the
// same regardless of how often dispatchPlugins is called. The durations are
// chosen to approximate "skip N heartbeats" at the default 3-minute
// recovery heartbeat interval:
//
//	attempt 1: no delay   (transient failure — try again next tick)
//	attempt 2: 3m delay   (~1 heartbeat)
//	attempt 3: 6m delay   (~2 heartbeats)
//	attempt 4+:15m delay  (~5 heartbeats) — escalated with a WARN log
//
// If 5 minutes pass with no new failure, the counter resets (see
// dogFailureResetWindow).
const (
	// dogFailureResetWindow is the time a dog must go without a new startup
	// failure before the failure counter resets. Matches the issue's
	// "within 5 min" window.
	dogFailureResetWindow = 5 * time.Minute

	// dogFailureWarnThreshold is the failure count at which we emit a WARN
	// log line and use the longest backoff delay. At this point the dog is
	// persistently failing and an operator may need to investigate.
	dogFailureWarnThreshold = 4
)

// dogBackoffSchedule maps consecutive-failure count to the retry delay.
// Index 0 is unused (counter starts at 1). Counts above the table use the
// last entry.
var dogBackoffSchedule = []time.Duration{
	0,                // placeholder for index 0
	0,                // 1st failure: no delay
	3 * time.Minute,  // 2nd failure: ~1 heartbeat
	6 * time.Minute,  // 3rd failure: ~2 heartbeats
	15 * time.Minute, // 4th+ failure: ~5 heartbeats (escalated)
}

// dogStartupFailure is the per-dog backoff state tracked by the daemon.
type dogStartupFailure struct {
	// consecutive is the number of back-to-back failures without an
	// intervening success or reset-window timeout.
	consecutive int

	// lastFailure is the timestamp of the most recent failure. Used both
	// to decide whether to reset the counter (window-based reset) and to
	// compute nextRetry.
	lastFailure time.Time

	// nextRetry is the earliest time the daemon will attempt to dispatch
	// to this dog again. If time.Now() is before nextRetry, the dispatch
	// is skipped.
	nextRetry time.Time
}

// DogStartupBackoff tracks dog-session startup failures and enforces
// exponential backoff between retries.
//
// All methods are safe for concurrent use. In practice all access happens
// from the daemon heartbeat goroutine today, but the mutex keeps this cheap
// and defensive.
type DogStartupBackoff struct {
	mu     sync.Mutex
	now    func() time.Time // injectable clock for tests
	states map[string]*dogStartupFailure
}

// NewDogStartupBackoff returns a new backoff tracker using the wall clock.
func NewDogStartupBackoff() *DogStartupBackoff {
	return &DogStartupBackoff{
		now:    time.Now,
		states: make(map[string]*dogStartupFailure),
	}
}

// ShouldSkip reports whether dispatch to the given dog should be skipped
// right now because of an active backoff window.
//
// When skipping, the second return value is a human-readable reason that
// callers may log.
func (b *DogStartupBackoff) ShouldSkip(dogName string) (bool, string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	s, ok := b.states[dogName]
	if !ok {
		return false, ""
	}

	now := b.now()
	if now.Before(s.nextRetry) {
		remaining := s.nextRetry.Sub(now).Round(time.Second)
		return true, fmt.Sprintf("startup backoff (%d consecutive failures, retry in %v)", s.consecutive, remaining)
	}
	return false, ""
}

// RecordFailure records a failed startup for the given dog and advances
// the backoff schedule.
//
// Returns the new consecutive failure count and the delay until the next
// allowed retry. When the count reaches dogFailureWarnThreshold, callers
// should emit a WARN log.
func (b *DogStartupBackoff) RecordFailure(dogName string) (consecutive int, delay time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	s, ok := b.states[dogName]
	if !ok {
		s = &dogStartupFailure{}
		b.states[dogName] = s
	}

	// Window-based reset: if the last failure was long enough ago, treat
	// this as a fresh failure rather than a continuation of the streak.
	if !s.lastFailure.IsZero() && now.Sub(s.lastFailure) > dogFailureResetWindow {
		s.consecutive = 0
	}

	s.consecutive++
	s.lastFailure = now
	delay = backoffDelay(s.consecutive)
	s.nextRetry = now.Add(delay)
	return s.consecutive, delay
}

// RecordSuccess clears any backoff state for the given dog. A successful
// start resets the consecutive-failure counter to zero.
func (b *DogStartupBackoff) RecordSuccess(dogName string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.states, dogName)
}

// ConsecutiveFailures returns the current failure count for a dog, or 0
// if no failures are recorded. Primarily for tests and debug logging.
func (b *DogStartupBackoff) ConsecutiveFailures(dogName string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.states[dogName]; ok {
		return s.consecutive
	}
	return 0
}

// backoffDelay returns the retry delay for the Nth consecutive failure.
// For counts beyond the schedule table, the longest delay is used.
func backoffDelay(consecutive int) time.Duration {
	if consecutive <= 0 {
		return 0
	}
	if consecutive >= len(dogBackoffSchedule) {
		return dogBackoffSchedule[len(dogBackoffSchedule)-1]
	}
	return dogBackoffSchedule[consecutive]
}
