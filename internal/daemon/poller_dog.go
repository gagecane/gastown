package daemon

import (
	"time"

	"github.com/steveyegge/gastown/internal/nudge"
)

// poller_dog: the supervisor for nudge-poller processes (gu-23z4).
//
// Problem: `gt nudge-poller <session>` processes are launched detached by
// StartPoller and tracked only via PID files under .runtime/nudge_poller/.
// If one crashes (OOM, SIGKILL, daemon restart missed a cleanup pass) there
// is no respawn path: StartPoller is invoked at session-create time only,
// so the poller dies silent and nudges queued for that session sit stranded
// forever. This was observed live for hq-mayor in gu-qpj8/gu-gviq.
//
// Fix: a daemon-side dog that periodically scans all PID files and:
//   - Respawns pollers whose process is dead but whose tmux session is still
//     alive (StartPoller is idempotent — returns the existing PID when a
//     poller has already been respawned between ticks).
//   - Removes stale PID files whose tmux session has vanished so .runtime/
//     does not accumulate cruft.
//   - Cleans up corrupt / unreadable PID files (they block StartPoller's
//     own alive-check from respawning even when the session is live).
//
// The dog is opt-in disabled by default (consistent with other dogs added
// post-launch: doctor_dog, quota_dog). Enable it via mayor/daemon.json:
//
//   {"patrols": {"poller_dog": {"enabled": true, "interval": "60s"}}}

const (
	defaultPollerDogInterval = 60 * time.Second
)

// PollerDogConfig configures the poller supervisor patrol.
type PollerDogConfig struct {
	// Enabled toggles the patrol. Opt-in; default is false.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often the supervisor runs, as a parseable duration
	// string (e.g. "60s", "2m"). Defaults to 60s when empty/invalid.
	IntervalStr string `json:"interval,omitempty"`
}

// pollerDogInterval returns the configured interval, or the default (60s).
func pollerDogInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.PollerDog != nil {
		if config.Patrols.PollerDog.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.PollerDog.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultPollerDogInterval
}

// pollerSupervisor narrows the nudge package surface to what runPollerDog
// exercises. Tests substitute a stub to verify respawn/cleanup decisions
// without launching real poller processes or touching the filesystem.
type pollerSupervisor interface {
	ListPollers(townRoot string) ([]nudge.PollerEntry, error)
	StartPoller(townRoot, session string) (int, error)
	RemoveStalePIDFile(townRoot, session string) error
}

// sessionChecker narrows the tmux surface runPollerDog needs. Tests stub
// this to exercise the session-alive vs session-dead branches.
type sessionChecker interface {
	HasSession(name string) (bool, error)
}

// realPollerSupervisor bridges the interface to the live nudge package.
type realPollerSupervisor struct{}

func (realPollerSupervisor) ListPollers(townRoot string) ([]nudge.PollerEntry, error) {
	return nudge.ListPollers(townRoot)
}
func (realPollerSupervisor) StartPoller(townRoot, session string) (int, error) {
	return nudge.StartPoller(townRoot, session)
}
func (realPollerSupervisor) RemoveStalePIDFile(townRoot, session string) error {
	return nudge.RemoveStalePIDFile(townRoot, session)
}

// runPollerDog is the daemon-registered entry point.
func (d *Daemon) runPollerDog() {
	if !d.isPatrolActive("poller_dog") {
		return
	}
	supervisePollers(d.config.TownRoot, realPollerSupervisor{}, d.tmux, d.logger.Printf)
}

// supervisePollers is the testable core of the patrol. It reads every PID
// file under .runtime/nudge_poller/, classifies each entry, and either
// respawns the poller or removes a stale PID file. Logging is delivered
// through logf so tests can capture it without wiring up a real logger.
func supervisePollers(townRoot string, sup pollerSupervisor, sessions sessionChecker, logf func(string, ...interface{})) {
	entries, err := sup.ListPollers(townRoot)
	if err != nil {
		logf("poller_dog: list pollers failed: %v", err)
		return
	}
	if len(entries) == 0 {
		return
	}

	respawned, removed, skipped := 0, 0, 0
	for _, entry := range entries {
		if entry.Alive {
			skipped++
			continue
		}

		// Poller is not running. Decide between respawn and cleanup based
		// on whether the tmux session is still alive. Tmux errors are
		// treated as "session alive" so transient checks don't cause us
		// to delete a PID file for a session that is actually there.
		sessionAlive, sessionErr := sessions.HasSession(entry.Session)
		if sessionErr != nil {
			logf("poller_dog: HasSession(%q) failed: %v (leaving entry alone)", entry.Session, sessionErr)
			continue
		}

		if !sessionAlive {
			// Session is gone — PID file is just noise. Remove it.
			if err := sup.RemoveStalePIDFile(townRoot, entry.Session); err != nil {
				logf("poller_dog: remove stale PID file for %q failed: %v", entry.Session, err)
				continue
			}
			logf("poller_dog: removed stale PID file for dead session %q (old pid=%d)", entry.Session, entry.PID)
			removed++
			continue
		}

		// Session alive but poller dead → respawn.
		// StartPoller is idempotent: if another path respawned the poller
		// between ListPollers and here, StartPoller short-circuits via its
		// own pollerAlive check.
		newPID, err := sup.StartPoller(townRoot, entry.Session)
		if err != nil {
			logf("poller_dog: respawn for %q failed: %v", entry.Session, err)
			continue
		}
		logf("poller_dog: respawned nudge-poller for %q (was pid=%d, now pid=%d)", entry.Session, entry.PID, newPID)
		respawned++
	}

	if respawned > 0 || removed > 0 {
		logf("poller_dog: cycle complete — respawned=%d removed=%d alive=%d", respawned, removed, skipped)
	}
}
