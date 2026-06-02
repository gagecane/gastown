package daemon

import (
	"time"

	"github.com/steveyegge/gastown/internal/channelevents"
)

// event_channel_gc patrol: periodic age-based pruning of file-based channel
// events under <townRoot>/events/<channel>/*.event.
//
// Problem (gu-5bf4f): channel events are a fire-and-forget fan-out. await-event
// reads all pending .event files on each wake and has no offset/cursor, so
// nothing prunes them after delivery. Consumers that pass --cleanup delete
// files as they read; but the witness/ and mayor/ channels have consumers that
// don't, so their directories grow unbounded (observed: witness/ at 3549 files
// dating back 24 days, mayor/ at 829 dating back 34 days).
//
// Fix: a daemon-side dog that runs channelevents.GCOlderThan on a periodic
// schedule (default 1h), deleting .event files older than the configured
// retention window (default 168h / 7d). Age-based pruning is safe because there
// is no replay-from-start consumer — any file older than the window has long
// since been delivered.

const (
	defaultEventChannelGCInterval  = 1 * time.Hour
	defaultEventChannelGCRetention = 7 * 24 * time.Hour
)

// EventChannelGCConfig configures the event_channel_gc patrol.
type EventChannelGCConfig struct {
	// Enabled toggles the patrol. Defaults to true (configured into
	// DefaultLifecycleConfig so a fresh town gets it for free).
	Enabled bool `json:"enabled"`

	// IntervalStr is how often the GC runs, as a parseable duration string
	// (e.g. "1h"). Defaults to 1h when empty/invalid.
	IntervalStr string `json:"interval,omitempty"`

	// RetentionStr is the max age a .event file may reach before it is
	// pruned, as a parseable duration string (e.g. "168h"). Defaults to
	// 168h (7d) when empty/invalid.
	RetentionStr string `json:"retention,omitempty"`
}

// eventChannelGCInterval returns the configured interval, or the default (1h).
func eventChannelGCInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.EventChannelGC != nil {
		if config.Patrols.EventChannelGC.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.EventChannelGC.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultEventChannelGCInterval
}

// eventChannelGCRetention returns the configured retention window, or the
// default (7d).
func eventChannelGCRetention(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.EventChannelGC != nil {
		if config.Patrols.EventChannelGC.RetentionStr != "" {
			if d, err := time.ParseDuration(config.Patrols.EventChannelGC.RetentionStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultEventChannelGCRetention
}

// runEventChannelGC is the daemon-registered entry point. It prunes stale
// .event files across all channel directories. Logging is best-effort — the
// patrol never fails the heartbeat.
func (d *Daemon) runEventChannelGC() {
	if !d.isPatrolActive("event_channel_gc") {
		return
	}

	retention := eventChannelGCRetention(d.patrolConfig)
	result, err := channelevents.GCOlderThan(d.config.TownRoot, retention)
	if err != nil {
		d.logger.Printf("event_channel_gc: error: %v", err)
		return
	}

	// Only log on activity to keep daemon.log quiet during steady state.
	if result.Removed > 0 || result.Errors > 0 {
		d.logger.Printf("event_channel_gc: scanned %d channel(s), removed %d stale event(s) older than %v, errors=%d",
			result.Channels, result.Removed, retention, result.Errors)
	}
}
