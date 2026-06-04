package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// circuit_breaker_gc patrol: periodic age-based pruning of stale Dolt
// circuit-breaker state files under /tmp/beads-circuit/.
//
// Problem (gu-9ynqw / gu-hm0vw): beads writes one circuit-breaker state file
// per (host, port, database) to /tmp/beads-circuit/. The filename keys on the
// database name, so every ephemeral/test DB (testdb_*, beads_t*, wisp,
// migration, etc.) mints a permanent file. beads' own cleanup
// (cleanStaleCircuitBreakerFilesIn) only deletes open/half-open files past its
// TTL — CLOSED files (≈100% of them, healthy) are never removed. Worse, that
// cleanup globs + reads + json-parses EVERY file on every bd init, so the
// directory's unbounded growth turns into a per-call latency tax (observed:
// 35,653 files / 140MB → ~650ms added to every bd call, which amplified Dolt
// connection churn to ~196 conn/s and burned 154 CPU-hours in 7.5 days).
//
// The durable fix is upstream in beads (don't glob the dir on the hot path;
// expire closed files). Until that lands, this daemon-side dog is the
// recurrence guard: it deletes CLOSED-state files older than a short TTL,
// leaving open/half-open breakers untouched so a genuinely-tripped breaker is
// never cleared out from under a live failure.
//
// Why age-by-mtime is safe: a live database's breaker file has its mtime
// refreshed on every successful call (beads writeState rewrites it on
// RecordSuccess), so an actively-used DB's file never reaches the TTL. Only
// files for DBs that have gone quiet (orphans, finished ephemeral/test DBs)
// age out — exactly the pollution we want to reap. A missing file is the
// healthy default (beads recreates a closed breaker on demand), so deletion is
// non-destructive.

const (
	// circuitBreakerDir is the directory beads writes breaker state files to.
	// Mirrors beads internal/storage/dolt/circuit.go circuitBreakerDir.
	circuitBreakerDir = "/tmp/beads-circuit"

	// circuitBreakerFileGlob matches beads breaker state files:
	// beads-dolt-circuit-<host>-<port>-<db>.json
	circuitBreakerFileGlob = "beads-dolt-circuit-*.json"

	// defaultCircuitBreakerGCInterval is how often the sweep runs. It runs off
	// its own ticker, independent of dispatch health, so the directory can't
	// regrow even when dispatch is wedged.
	defaultCircuitBreakerGCInterval = 5 * time.Minute

	// defaultCircuitBreakerGCRetention is the max age a CLOSED breaker file may
	// reach (by mtime) before it is pruned. Short by design: a live DB refreshes
	// its file far more often than this, so anything older is a quiet/orphan DB.
	defaultCircuitBreakerGCRetention = 15 * time.Minute

	// circuit breaker state strings, mirroring beads circuit.go. Only "closed"
	// files are eligible for age-based pruning; open/half-open are preserved.
	circuitStateClosed = "closed"
)

// CircuitBreakerGCConfig configures the circuit_breaker_gc patrol.
type CircuitBreakerGCConfig struct {
	// Enabled toggles the patrol. Defaults to true (configured into
	// DefaultLifecycleConfig so a fresh town gets it for free).
	Enabled bool `json:"enabled"`

	// IntervalStr is how often the GC runs, as a parseable duration string
	// (e.g. "5m"). Defaults to 5m when empty/invalid.
	IntervalStr string `json:"interval,omitempty"`

	// RetentionStr is the max age a closed breaker file may reach before it is
	// pruned, as a parseable duration string (e.g. "15m"). Defaults to 15m when
	// empty/invalid.
	RetentionStr string `json:"retention,omitempty"`
}

// circuitBreakerGCInterval returns the configured interval, or the default (5m).
func circuitBreakerGCInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.CircuitBreakerGC != nil {
		if config.Patrols.CircuitBreakerGC.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.CircuitBreakerGC.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultCircuitBreakerGCInterval
}

// circuitBreakerGCRetention returns the configured retention window, or the
// default (15m).
func circuitBreakerGCRetention(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.CircuitBreakerGC != nil {
		if config.Patrols.CircuitBreakerGC.RetentionStr != "" {
			if d, err := time.ParseDuration(config.Patrols.CircuitBreakerGC.RetentionStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultCircuitBreakerGCRetention
}

// circuitBreakerFile is the subset of beads' circuitState we need to decide
// eligibility. Only the state field matters; unknown fields are ignored.
type circuitBreakerFile struct {
	State string `json:"state"`
}

// shouldReapCircuitBreakerFile is the pure pruning decision, separated from I/O
// so it is exhaustively unit-testable. Given a file's raw bytes, its mtime, the
// current time, and the retention window, it decides whether the file should be
// deleted.
//
// Reap only when ALL hold:
//   - the file parses AND its state is "closed" (open/half-open breakers are a
//     live signal and must be preserved regardless of age);
//   - the file's mtime is older than the retention window (a live DB refreshes
//     its file well within the window, so age implies the DB went quiet).
//
// An unparseable/corrupt file is reaped (beads recreates a clean closed breaker
// on demand). A file with no state field is treated as not-closed and kept, to
// stay conservative about anything we don't understand.
func shouldReapCircuitBreakerFile(data []byte, mtime, now time.Time, retention time.Duration) bool {
	var cb circuitBreakerFile
	if err := json.Unmarshal(data, &cb); err != nil {
		// Corrupt/unparseable — safe to drop; beads regenerates a closed breaker.
		return true
	}
	if cb.State != circuitStateClosed {
		// open/half-open (or unknown/empty) — preserve the live signal.
		return false
	}
	return now.Sub(mtime) > retention
}

// circuitBreakerGCResult summarizes one sweep.
type circuitBreakerGCResult struct {
	Scanned int
	Removed int
	Errors  int
}

// gcCircuitBreakerFiles scans the breaker directory once and removes stale
// closed files. Best-effort: per-file errors are counted, never fatal. A
// missing directory is a no-op (nothing has created breakers yet).
func gcCircuitBreakerFiles(dir string, now time.Time, retention time.Duration) circuitBreakerGCResult {
	var res circuitBreakerGCResult

	matches, err := filepath.Glob(filepath.Join(dir, circuitBreakerFileGlob))
	if err != nil {
		return res
	}

	for _, path := range matches {
		res.Scanned++

		info, statErr := os.Stat(path)
		if statErr != nil {
			res.Errors++
			continue
		}

		data, readErr := os.ReadFile(path) //nolint:gosec // G304: path from Glob with controlled pattern in /tmp
		if readErr != nil {
			res.Errors++
			continue
		}

		if !shouldReapCircuitBreakerFile(data, info.ModTime(), now, retention) {
			continue
		}

		if rmErr := os.Remove(path); rmErr != nil {
			res.Errors++
			continue
		}
		res.Removed++
	}

	return res
}

// runCircuitBreakerGC is the daemon-registered entry point. It prunes stale
// closed breaker files from /tmp/beads-circuit. Logging is best-effort — the
// patrol never fails the heartbeat.
func (d *Daemon) runCircuitBreakerGC() {
	if !d.isPatrolActive("circuit_breaker_gc") {
		return
	}

	retention := circuitBreakerGCRetention(d.patrolConfig)
	res := gcCircuitBreakerFiles(circuitBreakerDir, time.Now(), retention)

	// Only log on activity to keep daemon.log quiet during steady state.
	if res.Removed > 0 || res.Errors > 0 {
		d.logger.Printf("circuit_breaker_gc: scanned %d file(s), removed %d stale closed breaker(s) older than %v, errors=%d",
			res.Scanned, res.Removed, retention, res.Errors)
	}
}
