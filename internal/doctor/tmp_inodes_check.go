package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// leakedBunTempSuffix is the constant suffix on the .hm tempfiles bun leaks
// into /tmp on every invocation (see gs-a9n). Pattern: ".<16-hex>-00000000.hm",
// zero-byte, owned by the running user.
const leakedBunTempSuffix = "-00000000.hm"

// TmpInodesCheck verifies that the tmpfs backing /tmp has free inodes.
//
// Background: On Linux hosts with tmpfs mounted at /tmp, the default inode
// limit is typically ~1M. A Go test run (especially one that uses
// t.TempDir()) can create thousands of small directories per invocation.
// After many runs without cleanup, /tmp can hit 100% inode usage while
// still having plenty of byte-level free space. The next call to
// t.TempDir() then fails with:
//
//	TempDir: mkdir /tmp/TestXxx: no space left on device
//
// Symptom: Go test flakes that disappear when the specific test is
// re-run alone (because re-running frees an inode or two).
//
// This check is Linux-only; other platforms get an OK short-circuit.
// See gu-k3xh for the bug that motivated this check.
type TmpInodesCheck struct {
	FixableCheck
}

// NewTmpInodesCheck creates a new /tmp inode-usage check.
func NewTmpInodesCheck() *TmpInodesCheck {
	return &TmpInodesCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "tmp-inodes",
				CheckDescription: "Check /tmp tmpfs inode usage (prevents Go test flakes)",
				CheckCategory:    CategoryInfrastructure,
			},
		},
	}
}

// Thresholds for the /tmp inode check.
const (
	// tmpInodesWarnPercent is the usage percentage at which a warning is emitted.
	// Above this level, large test runs risk running out of inodes.
	tmpInodesWarnPercent = 85.0

	// tmpInodesCriticalPercent is the usage percentage at which an error is
	// emitted and the auto-fix will be attempted. Close to 100% because at
	// this point Go tests will start failing with ENOSPC on mkdir.
	tmpInodesCriticalPercent = 95.0

	// tmpCleanupAge is the minimum age of leftover /tmp/Test* directories
	// before the fix will remove them. Short enough to unblock a saturated
	// tmpfs, long enough to avoid racing with an in-flight test run.
	tmpCleanupAge = 1 * time.Hour

	// tmpDirToCheck is the path the check inspects. Hoisted to a variable
	// so tests can point the check at a fake filesystem.
	tmpDirToCheck = "/tmp"
)

// tmpDirPath is overridable so tests can target an isolated directory
// without touching the real /tmp.
var tmpDirPath = tmpDirToCheck

// Run inspects inode usage on /tmp and reports a status.
func (c *TmpInodesCheck) Run(ctx *CheckContext) *CheckResult {
	usage, err := readTmpInodeUsage(tmpDirPath)
	if err != nil {
		// Not an error from the user's point of view — /tmp may be a
		// filesystem (e.g. on Windows) that doesn't expose inode counts.
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("Inode usage not available on %s", tmpDirPath),
		}
	}

	pct := usage.UsedPercent()
	msg := fmt.Sprintf("%.1f%% used (%d/%d inodes, %d free)",
		pct, usage.Used, usage.Total, usage.Free)

	switch {
	case pct >= tmpInodesCriticalPercent:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: msg,
			Details: []string{
				"Inode exhaustion on /tmp causes Go test failures:",
				"  TempDir: mkdir /tmp/TestXxx: no space left on device",
				"Common causes: stale /tmp/Test* dirs from Go test runs, and",
				"leaked .<hex>-00000000.hm files from the bun runtime (see gs-a9n).",
			},
			FixHint: "gt doctor --fix (removes stale /tmp/Test* dirs and leaked bun .hm tempfiles older than 1h)",
		}

	case pct >= tmpInodesWarnPercent:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: msg,
			Details: []string{
				"Large Go test runs may start failing soon if inode usage grows.",
				"Consider running 'gt doctor --fix' to reclaim stale /tmp/Test* dirs",
				"and leaked .<hex>-00000000.hm tempfiles from the bun runtime (gs-a9n).",
			},
		}

	default:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: msg,
		}
	}
}

// Fix reclaims inodes by removing two kinds of stale tmpfs leftovers:
//
//  1. Go t.TempDir() directories ("TestSomething1234567") from prior test runs.
//  2. Zero-byte ".<16-hex>-00000000.hm" tempfiles leaked by the bun runtime
//     (per gs-a9n: bun's tempfile creation in CWD is not unlink-on-close, and
//     bunx is invoked every 10s by the Claude Code statusLine, so a single
//     host accumulates ~3 of these per second).
//
// Both cleanups are scoped to entries matching a specific naming convention
// and skip anything younger than tmpCleanupAge so we don't race in-flight
// work. The .hm cleanup additionally requires the file be zero-byte — a
// non-empty file with this name is not the bun leak and we leave it alone.
func (c *TmpInodesCheck) Fix(ctx *CheckContext) error {
	if ctx != nil && ctx.ReadOnly {
		return nil
	}

	now := time.Now()
	dirRemoved, dirSkipped, dirErr := cleanupStaleGoTestTempDirs(tmpDirPath, tmpCleanupAge, now)
	fileRemoved, fileSkipped, fileErr := cleanupLeakedBunTempFiles(tmpDirPath, tmpCleanupAge, now)

	firstErr := dirErr
	if firstErr == nil {
		firstErr = fileErr
	}

	// If nothing was removed or skipped at all and we saw an error, surface it.
	// Otherwise swallow per-entry errors — racing cleanup is benign.
	if dirRemoved+dirSkipped+fileRemoved+fileSkipped == 0 && firstErr != nil {
		return fmt.Errorf("tmp-inodes fix failed: %w", firstErr)
	}
	return nil
}

// tmpInodeUsage captures the inode counters we care about.
type tmpInodeUsage struct {
	Total uint64
	Free  uint64
	Used  uint64
}

// UsedPercent returns the inode usage as a percentage in [0, 100].
// Returns 0 if Total is zero (filesystem doesn't report inodes).
func (u tmpInodeUsage) UsedPercent() float64 {
	if u.Total == 0 {
		return 0
	}
	return float64(u.Used) / float64(u.Total) * 100
}

// isGoTestTempDir reports whether name looks like a directory Go's
// testing package created for a test (e.g. "TestFoo1234567890" or
// "TestBar_Subtest9876543").
//
// Go's testing.T.TempDir implementation uses the pattern
// "<parent>/<TestName><counter>", where the counter is a decimal integer
// appended with no separator. We require at least one digit at the end
// so we don't accidentally match a manually-created "TestNotes" folder.
func isGoTestTempDir(name string) bool {
	if !strings.HasPrefix(name, "Test") {
		return false
	}
	// Trim trailing digits and require that at least one was present,
	// AND that something other than "Test" precedes them.
	trimmed := strings.TrimRight(name, "0123456789")
	if trimmed == name {
		return false
	}
	if trimmed == "Test" {
		// e.g. "Test123" — too generic, skip to be safe.
		return false
	}
	return true
}

// cleanupStaleGoTestTempDirs removes directories under dir whose names
// match isGoTestTempDir and whose mtime is older than now - maxAge.
// Returns (removed, skipped, firstErr).
func cleanupStaleGoTestTempDirs(dir string, maxAge time.Duration, now time.Time) (int, int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, err
	}

	cutoff := now.Add(-maxAge)

	var (
		removed  int
		skipped  int
		firstErr error
	)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if !isGoTestTempDir(entry.Name()) {
			continue
		}

		full := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			// Entry disappeared between ReadDir and Info — that's fine,
			// it just means someone else cleaned it up first.
			if firstErr == nil && !os.IsNotExist(err) {
				firstErr = err
			}
			continue
		}
		if info.ModTime().After(cutoff) {
			skipped++
			continue
		}

		if err := os.RemoveAll(full); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
	}

	return removed, skipped, firstErr
}

// isLeakedBunTempFile reports whether name matches the bun runtime's leaked
// tempfile pattern: a literal '.' followed by exactly 16 lowercase hex digits,
// then "-00000000.hm". See gs-a9n for the bpftrace evidence pinning these to
// bun.
func isLeakedBunTempFile(name string) bool {
	if !strings.HasPrefix(name, ".") {
		return false
	}
	if !strings.HasSuffix(name, leakedBunTempSuffix) {
		return false
	}
	hex := name[1 : len(name)-len(leakedBunTempSuffix)]
	if len(hex) != 16 {
		return false
	}
	for i := 0; i < len(hex); i++ {
		c := hex[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// cleanupLeakedBunTempFiles removes zero-byte files in dir whose names match
// isLeakedBunTempFile and whose mtime is older than now - maxAge.
// Non-zero-byte files are skipped — same name but non-empty means it's not
// the bun leak. Returns (removed, skipped, firstErr).
func cleanupLeakedBunTempFiles(dir string, maxAge time.Duration, now time.Time) (int, int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, err
	}

	cutoff := now.Add(-maxAge)

	var (
		removed  int
		skipped  int
		firstErr error
	)

	for _, entry := range entries {
		// Must be a regular file (not a dir, not a symlink, etc.).
		if !entry.Type().IsRegular() {
			continue
		}
		if !isLeakedBunTempFile(entry.Name()) {
			continue
		}

		full := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			if firstErr == nil && !os.IsNotExist(err) {
				firstErr = err
			}
			continue
		}
		// Bun leak is always zero-byte. A non-empty file with this name is
		// something else — leave it alone.
		if info.Size() != 0 {
			continue
		}
		if info.ModTime().After(cutoff) {
			skipped++
			continue
		}

		if err := os.Remove(full); err != nil {
			if firstErr == nil && !os.IsNotExist(err) {
				firstErr = err
			}
			continue
		}
		removed++
	}

	return removed, skipped, firstErr
}
