// Scanner for the gastown.hooked_beads.* OTel gauges (gu-hhqk AC#5).
//
// Counts status='hooked' mail beads across all rig databases and publishes
// the snapshot to daemonMetrics, which exposes it as observable gauges on
// the next OTel export interval.
//
// This runs inline from the heartbeat (cheap COUNT queries, no locking on
// the daemon loop). The queries mirror doctor.HookedDeadLetterCheck and
// reaper.ReapHookedMail so all three observability surfaces agree on the
// exclusion set (agent heartbeats and preserve-labeled beads are excluded).
package daemon

import (
	"time"

	"github.com/steveyegge/gastown/internal/reaper"
)

// hookedBeadsScanTimeout bounds the total time spent scanning hooked-mail
// counts across all databases. The underlying queries are very cheap
// (COUNT DISTINCT with indexed WHERE), so this is a safety net — a hung
// Dolt server should not block the heartbeat.
const hookedBeadsScanTimeout = 10 * time.Second

// updateHookedBeadsMetrics scans all rig databases for hooked-mail counts
// and publishes the snapshot to daemonMetrics. No-op when metrics are
// disabled or the Dolt server is not configured.
//
// The scan is best-effort: errors on individual databases are logged but do
// not fail the whole update — a stale rig should not blank out healthy ones.
func (d *Daemon) updateHookedBeadsMetrics() {
	if d.metrics == nil {
		return
	}
	if d.doltServer == nil || !d.doltServer.IsEnabled() {
		return
	}

	total, deadLetter := d.scanHookedBeads()
	d.metrics.updateHookedBeads(total, deadLetter)
}

// scanHookedBeads iterates all databases on the configured Dolt server and
// returns per-database counts. Databases with zero hooked mail are emitted
// so the gauges stabilize at zero after a reap cycle (otherwise disappearing
// series leave Prometheus holding stale rates).
func (d *Daemon) scanHookedBeads() (total, deadLetter map[string]int64) {
	total = make(map[string]int64)
	deadLetter = make(map[string]int64)

	port := d.doltServerPort()
	databases := reaper.DiscoverDatabases("127.0.0.1", port)
	if len(databases) == 0 {
		return total, deadLetter
	}

	threshold := reaper.DefaultDeadLetterThreshold

	for _, dbName := range databases {
		if err := reaper.ValidateDBName(dbName); err != nil {
			continue
		}

		db, err := reaper.OpenDB("127.0.0.1", port, dbName, hookedBeadsScanTimeout, hookedBeadsScanTimeout)
		if err != nil {
			d.logger.Printf("hooked_beads_metrics: %s: connect error: %v", dbName, err)
			continue
		}

		// Databases without the issues/labels tables (e.g., rig-local Dolt
		// instances) are silently skipped — ScanHookedMailCounts returns
		// zero counts with no error in that case.
		counts, err := reaper.ScanHookedMailCounts(db, dbName, threshold)
		db.Close()
		if err != nil {
			d.logger.Printf("hooked_beads_metrics: %s: scan error: %v", dbName, err)
			continue
		}

		total[dbName] = int64(counts.Total)
		deadLetter[dbName] = int64(counts.DeadLetter)
	}

	return total, deadLetter
}
