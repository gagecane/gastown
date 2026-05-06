package daemon

import (
	"fmt"
	"time"
)

// Dog startup backoff
// ===================
//
// When a dog's tmux session dies during startup (e.g., OOM pressure, missing
// binary, bad config), the daemon's dog dispatcher previously re-dispatched
// on the next heartbeat with no backoff. For a persistent failure this turns
// into a rapid-fire retry loop that burns API credits, pollutes logs, and
// prevents recovery. See gu-ro75.
//
// The existing RestartTracker (restart_tracker.go) already implements
// exponential backoff with crash-loop detection and a stability-based reset,
// which is exactly what this fix needs. We just namespace dog startup
// attempts under "dog:<name>" and reuse the same tracker instance.
//
// Backoff schedule (from DefaultRestartTrackerConfig):
//
//	attempt 1 → 30s    attempt 2 → 1m     attempt 3 → 2m
//	attempt 4 → 4m     attempt 5 → 8m     attempt 6+ → 10m (capped)
//
// Crash loop: 5 failed starts within 15 minutes mutes the dog until
// `gt daemon clear-backoff dog:<name>` is invoked.
//
// Stability reset: after StabilityPeriod (30m) without a recorded restart,
// the backoff counter is cleared on the next RecordRestart/RecordSuccess.

// dogBackoffAgentID returns the restart-tracker agent ID used for a dog's
// startup-failure backoff state. Kept in one place so helpers and tests
// always agree on the namespace convention.
func dogBackoffAgentID(dogName string) string {
	return "dog:" + dogName
}

// isDogInStartupBackoff reports whether a dog is currently in startup backoff
// or crash loop and should be skipped for dispatch this heartbeat.
//
// Returns (skip, reason). When skip is true, reason contains a human-readable
// sentence suitable for a log line. When the daemon has no restart tracker
// (e.g., in tests that don't initialize one), this always returns (false, "").
func (d *Daemon) isDogInStartupBackoff(dogName string) (bool, string) {
	if d == nil || d.restartTracker == nil {
		return false, ""
	}

	id := dogBackoffAgentID(dogName)
	if d.restartTracker.IsInCrashLoop(id) {
		return true, fmt.Sprintf(
			"dog %s in startup crash loop (use: gt daemon clear-backoff %s)",
			dogName, id,
		)
	}
	if !d.restartTracker.CanRestart(id) {
		remaining := d.restartTracker.GetBackoffRemaining(id).Round(time.Second)
		return true, fmt.Sprintf(
			"dog %s in startup backoff, %s remaining",
			dogName, remaining,
		)
	}
	return false, ""
}

// recordDogStartFailure records a failed dog startup attempt, bumping the
// exponential backoff for the next retry. Safe to call when the daemon has
// no restart tracker (noop).
func (d *Daemon) recordDogStartFailure(dogName string) {
	if d == nil || d.restartTracker == nil {
		return
	}

	id := dogBackoffAgentID(dogName)
	d.restartTracker.RecordRestart(id)
	if err := d.restartTracker.Save(); err != nil {
		d.logger.Printf("Handler: failed to save restart state for %s: %v", id, err)
	}

	remaining := d.restartTracker.GetBackoffRemaining(id).Round(time.Second)
	if d.restartTracker.IsInCrashLoop(id) {
		d.logger.Printf(
			"Handler: dog %s entered startup crash loop after repeated failures "+
				"(use: gt daemon clear-backoff %s to reset)",
			dogName, id,
		)
		return
	}
	if remaining > 0 {
		d.logger.Printf("Handler: dog %s next startup retry in %s", dogName, remaining)
	}
}

// recordDogStartSuccess records a successful dog startup. If the dog has
// been stable for the tracker's StabilityPeriod this clears its counter;
// otherwise it is a noop, keeping the backoff state intact so a dog that
// dies again shortly after a successful start continues to back off.
func (d *Daemon) recordDogStartSuccess(dogName string) {
	if d == nil || d.restartTracker == nil {
		return
	}
	d.restartTracker.RecordSuccess(dogBackoffAgentID(dogName))
}
