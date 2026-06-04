package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/util"
)

// dolt-backup completion watcher (gu-8xvpw).
//
// The dolt-backup plugin's failure path (exit 1 + `gt escalate`) only fires
// when the plugin actually RUNS and reaches its reporting step. It is blind to
// the silent failure modes that matter most: the process is killed mid-run,
// hangs past its timeout, is never scheduled (daemon/cron gap), or crashes
// before signaling. gu-t9xgf was one of those — a town-wide backup outage that
// went unnoticed for hours until a refinery happened to spot the exit-1 burst.
//
// This watcher is the positive-signal safety net. The plugin now writes a
// durable heartbeat to <town>/.runtime/dolt-backup-heartbeat.json on every run
// (see plugins/dolt-backup/run.sh write_heartbeat). The watcher checks that the
// heartbeat exists and is fresh; if it is absent or older than a staleness
// threshold, it escalates — catching "backups silently stopped happening",
// which the failure-only model structurally cannot.

const (
	// defaultDoltBackupWatcherInterval is how often the watcher checks the
	// heartbeat. It runs independently of the backup ticker so it still fires
	// when backups have stopped running entirely.
	defaultDoltBackupWatcherInterval = 15 * time.Minute

	// doltBackupHeartbeatFile is the heartbeat path under <town>/.runtime,
	// matching plugins/dolt-backup/run.sh heartbeat_path().
	doltBackupHeartbeatFile = "dolt-backup-heartbeat.json"

	// doltBackupStalenessFactor multiplies the backup interval to derive the
	// staleness threshold. A heartbeat older than factor*interval means roughly
	// this many backup cycles were missed — enough to ignore a single skipped
	// tick (restart, transient cooldown) while catching a real stall early.
	doltBackupStalenessFactor = 3
)

// DoltBackupWatcherConfig holds configuration for the dolt_backup_watcher patrol.
type DoltBackupWatcherConfig struct {
	// Enabled controls whether the watcher runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to check the heartbeat, as a string (e.g., "15m").
	IntervalStr string `json:"interval,omitempty"`

	// StalenessStr overrides the staleness threshold (e.g., "45m"). When empty,
	// the threshold is derived as doltBackupStalenessFactor * backup interval.
	StalenessStr string `json:"staleness,omitempty"`
}

// doltBackupWatcherInterval returns the configured watcher interval, or the
// default (15m).
func doltBackupWatcherInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.DoltBackupWatcher != nil {
		if config.Patrols.DoltBackupWatcher.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.DoltBackupWatcher.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultDoltBackupWatcherInterval
}

// doltBackupStalenessThreshold returns the age past which a heartbeat is
// considered stale. An explicit config override wins; otherwise it is
// doltBackupStalenessFactor times the backup interval.
func doltBackupStalenessThreshold(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.DoltBackupWatcher != nil {
		if s := config.Patrols.DoltBackupWatcher.StalenessStr; s != "" {
			if d, err := time.ParseDuration(s); err == nil && d > 0 {
				return d
			}
		}
	}
	return doltBackupStalenessFactor * doltBackupInterval(config)
}

// doltBackupHeartbeat is the JSON written by the plugin's write_heartbeat. Only
// the fields the watcher needs are decoded; unknown fields are ignored.
type doltBackupHeartbeat struct {
	Timestamp string `json:"timestamp"` // RFC3339 UTC
	Status    string `json:"status"`
	Failed    int    `json:"failed"`
}

// backupWatcherVerdict is the pure decision produced by evaluateBackupHeartbeat.
type backupWatcherVerdict struct {
	// Alarm is true when the watcher should escalate.
	Alarm bool
	// Reason is a short human-readable cause, used in the escalation body.
	Reason string
}

// evaluateBackupHeartbeat is the pure staleness decision, separated from I/O so
// it is exhaustively unit-testable. Given the raw heartbeat bytes (and whether
// the file was found), the current time, and the staleness threshold, it
// decides whether to alarm.
//
// Alarm conditions:
//   - heartbeat file missing entirely (backups never ran, or runtime wiped);
//   - heartbeat unparseable or missing a timestamp (corrupt signal);
//   - heartbeat timestamp older than `threshold` (backups stalled).
//
// A "failed" status is NOT itself an alarm here: the plugin already escalates
// HIGH on a backup-sync failure (gu-8xvpw). The watcher's distinct job is to
// catch SILENCE — the absence of any recent signal. Double-paging a failure the
// plugin already reported would just add noise.
func evaluateBackupHeartbeat(data []byte, found bool, now time.Time, threshold time.Duration) backupWatcherVerdict {
	if !found {
		return backupWatcherVerdict{
			Alarm:  true,
			Reason: "no dolt-backup heartbeat found — backups may have never run or stopped entirely",
		}
	}

	var hb doltBackupHeartbeat
	if err := json.Unmarshal(data, &hb); err != nil || hb.Timestamp == "" {
		return backupWatcherVerdict{
			Alarm:  true,
			Reason: "dolt-backup heartbeat is unparseable or missing a timestamp — backup signal is corrupt",
		}
	}

	ts, err := time.Parse(time.RFC3339, hb.Timestamp)
	if err != nil {
		return backupWatcherVerdict{
			Alarm:  true,
			Reason: fmt.Sprintf("dolt-backup heartbeat timestamp %q is not RFC3339 — backup signal is corrupt", hb.Timestamp),
		}
	}

	age := now.Sub(ts)
	if age > threshold {
		return backupWatcherVerdict{
			Alarm: true,
			Reason: fmt.Sprintf("dolt-backup heartbeat is stale: last run %s ago (status=%s) exceeds %s threshold — backups appear to have stalled",
				age.Round(time.Minute), hb.Status, threshold),
		}
	}

	return backupWatcherVerdict{Alarm: false}
}

// runDoltBackupWatcher checks the dolt-backup heartbeat freshness and escalates
// when it is absent or stale. Best-effort: any I/O error degrades to no-alarm
// rather than blocking the daemon. The escalation carries a stable fingerprint
// so `gt escalate`'s close-aware dedup suppresses repeats within one stall
// episode.
func (d *Daemon) runDoltBackupWatcher() {
	if !d.isPatrolActive("dolt_backup_watcher") {
		return
	}

	hbPath := filepath.Join(d.config.TownRoot, ".runtime", doltBackupHeartbeatFile)
	data, readErr := os.ReadFile(hbPath) //nolint:gosec // G304: path constructed internally
	found := readErr == nil

	threshold := doltBackupStalenessThreshold(d.patrolConfig)
	verdict := evaluateBackupHeartbeat(data, found, time.Now(), threshold)
	if !verdict.Alarm {
		return
	}

	d.logger.Printf("dolt_backup_watcher: ALARM — %s", verdict.Reason)
	d.escalateBackupHeartbeatStale(verdict.Reason)
}

// escalateBackupHeartbeatStale raises a HIGH escalation to the Mayor. Overridable
// in tests via the package-level hook. Best-effort: failure is logged only.
var escalateBackupHeartbeatStale = func(d *Daemon, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	msg := "dolt-backup heartbeat stale/absent — backups may have silently stopped"
	cmd := exec.CommandContext(ctx, d.gtPath, "escalate", //nolint:gosec // G204: args constructed internally
		"--severity", "high",
		"--source", "daemon:dolt-backup-watcher",
		"--fingerprint", "dolt-backup:heartbeat-stale",
		"--reason", reason, msg)
	util.SetDetachedProcessGroup(cmd)
	if err := cmd.Run(); err != nil {
		d.logger.Printf("dolt_backup_watcher: escalation failed: %v", err)
	}
}

// escalateBackupHeartbeatStale is a thin method wrapper so the hook can be
// swapped in tests while callers use a stable method name.
func (d *Daemon) escalateBackupHeartbeatStale(reason string) {
	escalateBackupHeartbeatStale(d, reason)
}
