package doltserver

import (
	"os"
	"path/filepath"
	"strings"
)

// CircuitBreakerDir is the directory where the beads Dolt client writes one
// circuit-breaker state file per (host, port, database) it touches. The path
// mirrors beads' internal/storage/dolt/circuit.go (circuitBreakerDir).
//
// Every distinct database name a `bd` process touches mints a permanent
// closed-state file here, and beads' on-init cleanup only reaps open/half-open
// files — closed (healthy) files are never removed. Test-pollution DBs
// (testdb_*, beads_t*, doctest_*, etc.) therefore cause unbounded growth: at
// the time of gu-9ynqw the dir held 35,653 files (140MB), and beads globs +
// reads + parses every one on each `bd` init, adding ~650ms to every call —
// the root cause behind the dispatch outage and 5-minute timeouts.
const CircuitBreakerDir = "/tmp/beads-circuit"

// circuitBreakerFilePrefix is the constant filename prefix beads uses for
// breaker state files: beads-dolt-circuit-<safeHost>-<port>-<db>.json.
const circuitBreakerFilePrefix = "beads-dolt-circuit-"

// CircuitCleanupResult summarizes a circuit-breaker file sweep.
type CircuitCleanupResult struct {
	// Removed is the number of breaker files deleted.
	Removed int

	// BytesFreed is the total size of the deleted files.
	BytesFreed int64

	// Remaining is the number of breaker files left after the sweep.
	Remaining int
}

// CleanStaleCircuitBreakerFiles prunes test-pollution and legacy circuit-breaker
// state files from /tmp/beads-circuit so the directory cannot regrow unbounded
// and re-wedge dispatch (gu-9ynqw, durable fix #4).
//
// It removes:
//   - legacy port-0 breaker files (beads-dolt-circuit-0.json) — should never exist;
//   - files whose embedded database name matches a test-pollution prefix
//     (testPollutionPrefixes, the same set the reaper sweeps).
//
// It NEVER removes breaker files for live/production databases — those are keyed
// on stable database names and are cheap to keep (one per live DB). The upstream
// beads fix (bounding the on-init scan) is tracked separately; this sweep keeps
// the directory bounded regardless.
func CleanStaleCircuitBreakerFiles() (CircuitCleanupResult, error) {
	return cleanStaleCircuitBreakerFilesIn(CircuitBreakerDir)
}

// cleanStaleCircuitBreakerFilesIn is the testable implementation, parameterized
// on the directory.
func cleanStaleCircuitBreakerFilesIn(dir string) (CircuitCleanupResult, error) {
	var result CircuitCleanupResult

	matches, err := filepath.Glob(filepath.Join(dir, circuitBreakerFilePrefix+"*.json"))
	if err != nil {
		return result, err
	}

	for _, path := range matches {
		base := filepath.Base(path)

		if isTestPollutionCircuitFile(base) {
			size := fileSize(path)
			if err := os.Remove(path); err != nil {
				// Leave a file we couldn't remove counted as remaining.
				result.Remaining++
				continue
			}
			result.Removed++
			result.BytesFreed += size
			continue
		}

		result.Remaining++
	}

	return result, nil
}

// isTestPollutionCircuitFile reports whether a breaker filename corresponds to a
// legacy port-0 file or a test-pollution database name.
//
// Filenames are beads-dolt-circuit-<safeHost>-<port>-<db>.json. The database
// segment always follows the "<port>-" delimiter, so a test-pollution database
// surfaces as "-<prefix>" within the filename (e.g. "-testdb_"). Matching on the
// hyphen-anchored prefix avoids false positives from host/port digits.
func isTestPollutionCircuitFile(base string) bool {
	// Legacy port-0 file beads itself documents as "should never exist".
	if base == "beads-dolt-circuit-0.json" {
		return true
	}

	for _, prefix := range testPollutionCircuitPrefixes {
		if strings.Contains(base, "-"+prefix) {
			return true
		}
	}
	return false
}

// testPollutionCircuitPrefixes are the database-name prefixes created by tests
// and other ephemeral runs. Kept in sync with reaper.testPollutionPrefixes,
// extended with the additional throwaway-DB families called out in gu-9ynqw
// (beads_vr*, doctortest_*) that the manual filesystem cleanup also targets.
var testPollutionCircuitPrefixes = []string{
	"testdb_",
	"beads_t",
	"beads_pt",
	"beads_vr",
	"doctest_",
	"doctortest_",
}

// fileSize returns the size of a file in bytes, or 0 if it cannot be stat'd.
func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
