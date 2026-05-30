package daemon

import (
	"time"

	"github.com/steveyegge/gastown/internal/nudge"
)

// nudge_queue_gc patrol: periodic eviction of expired nudges from the
// disk-backed queue under <townRoot>/.runtime/nudge_queue/<session>.
//
// Problem (gu-gsry): Drain discards expired entries lazily — only when the
// recipient's UserPromptSubmit hook fires. If a session is wedged, dead, or
// idle for longer than the TTL, expired files accumulate until
// MaxQueueDepth is reached. Senders then fail with "queue is full"
// silently, dropping legitimate nudges on the floor. Observed live with
// hq-deacon, hq-overseer, and gt-witness all at 50/50 expired in a single
// day, dropping mayor reply-reminders.
//
// Fix: a daemon-side dog that runs nudge.GCExpired on a periodic schedule
// (default 5m) regardless of recipient liveness. Eviction respects each
// nudge's expires_at field and never touches in-flight .claimed files or
// future-dated entries.

const (
	defaultNudgeQueueGCInterval = 5 * time.Minute
)

// NudgeQueueGCConfig configures the nudge_queue_gc patrol.
type NudgeQueueGCConfig struct {
	// Enabled toggles the patrol. Defaults to true (configured into
	// DefaultLifecycleConfig so a fresh town gets it for free).
	Enabled bool `json:"enabled"`

	// IntervalStr is how often the GC runs, as a parseable duration
	// string (e.g. "5m"). Defaults to 5m when empty/invalid.
	IntervalStr string `json:"interval,omitempty"`
}

// nudgeQueueGCInterval returns the configured interval, or the default (5m).
func nudgeQueueGCInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.NudgeQueueGC != nil {
		if config.Patrols.NudgeQueueGC.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.NudgeQueueGC.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultNudgeQueueGCInterval
}

// runNudgeQueueGC is the daemon-registered entry point. It scans every
// session directory and removes expired entries. Logging is best-effort
// — the patrol never fails the heartbeat.
func (d *Daemon) runNudgeQueueGC() {
	if !d.isPatrolActive("nudge_queue_gc") {
		return
	}

	result, err := nudge.GCExpired(d.config.TownRoot)
	if err != nil {
		d.logger.Printf("nudge_queue_gc: error: %v", err)
		return
	}

	// Only log on activity to keep daemon.log quiet during steady state.
	if result.Removed > 0 || result.Errors > 0 {
		d.logger.Printf("nudge_queue_gc: scanned %d session(s), removed %d expired nudge(s), errors=%d",
			result.Sessions, result.Removed, result.Errors)
	}
}
