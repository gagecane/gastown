package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

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

	// bunHmLeakCleanupAge is the minimum age of leaked bun .hm files before
	// the fix will remove them. These files have no legitimate purpose —
	// they're zero-byte tempfile leftovers from bun (observed via bpftrace
	// during bunx ccstatusline invocations, see gs-a9n). A short cutoff is
	// safe because bun closes the fd before the file is even visible to us,
	// but we still leave a small grace window in case a future bun version
	// reuses the file before unlinking.
	bunHmLeakCleanupAge = 10 * time.Minute

	// tmpDirToCheck is the path the check inspects. Hoisted to a variable
	// so tests can point the check at a fake filesystem.
	tmpDirToCheck = "/tmp"
)

// bunHmLeakPattern matches the leaked bun tempfile naming convention:
// a dot-prefixed 16-char lowercase hex identifier followed by the constant
// "-00000000.hm" suffix. The suffix is invariant across all observed leaks,
// so anchoring on it keeps the pattern from colliding with anything else a
// user or tool might place in /tmp.
var bunHmLeakPattern = regexp.MustCompile(`^\.[0-9a-f]{16}-00000000\.hm$`)

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
				"Stale /tmp/Test* dirs from previous test runs are one cause.",
				"Leaked bun tempfiles (.<hex>-00000000.hm, from bunx ccstatusline) are another — see gs-a9n.",
			},
			FixHint: "gt doctor --fix (removes stale /tmp/Test* dirs and leaked bun .hm files)",
		}

	case pct >= tmpInodesWarnPercent:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: msg,
			Details: []string{
				"Large Go test runs may start failing soon if inode usage grows.",
				"Run 'gt doctor --fix' to reclaim stale /tmp/Test* dirs and leaked bun .hm files.",
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

// Fix reclaims inodes by removing leftover /tmp/Test* directories from
// previous Go test runs that are at least tmpCleanupAge old.
//
// We deliberately scope the cleanup to entries matching Go's t.TempDir()
// naming convention ("TestSomething1234567") to avoid touching anything
// else a user or tool may have written to /tmp. Anything younger than
// tmpCleanupAge is left alone so we don't race with an active test run.
func (c *TmpInodesCheck) Fix(ctx *CheckContext) error {
	if ctx != nil && ctx.ReadOnly {
		return nil
	}

	now := time.Now()
	removedDirs, skippedDirs, firstErr := cleanupStaleGoTestTempDirs(tmpDirPath, tmpCleanupAge, now)

	removedHm, skippedHm, hmErr := cleanupLeakedBunHmFiles(tmpDirPath, bunHmLeakCleanupAge, now)
	if firstErr == nil {
		firstErr = hmErr
	}

	// Report at most one error, but always surface progress via the re-run of
	// Run() that the doctor framework does after Fix(). If every single entry
	// errors and none are removed, propagate the first error so the user sees
	// something went wrong. Otherwise swallow per-entry errors (a test run
	// racing with us will sometimes rename/unlink entries mid-iteration).
	totalRemoved := removedDirs + removedHm
	totalSkipped := skippedDirs + skippedHm
	if totalRemoved == 0 && totalSkipped == 0 && firstErr != nil {
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

// cleanupLeakedBunHmFiles removes zero-byte files under dir whose names
// match the leaked-bun-tempfile pattern (.<16-hex>-00000000.hm) and whose
// mtime is older than now - maxAge. Returns (removed, skipped, firstErr).
//
// See gs-a9n for the root cause: bun (invoked via 'bunx -y ccstatusline')
// creates these as O_CREAT|O_EXCL tempfiles relative to /tmp and exits
// without unlinking them, leaking one inode per statusLine refresh per
// Claude Code instance. At steady state this is the dominant inode
// consumer on hosts running multiple Gas Town agents.
//
// We require a zero-byte size as a safety belt: the leaked files are
// always 0 bytes (the fd is closed before any write), and we never want
// to touch a file that has user data in it even if it accidentally
// matches the name pattern.
func cleanupLeakedBunHmFiles(dir string, maxAge time.Duration, now time.Time) (int, int, error) {
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
		if entry.IsDir() {
			continue
		}
		if !bunHmLeakPattern.MatchString(entry.Name()) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			if firstErr == nil && !os.IsNotExist(err) {
				firstErr = err
			}
			continue
		}
		if info.Size() != 0 {
			// Same-named file with content — leave it alone.
			continue
		}
		if info.ModTime().After(cutoff) {
			skipped++
			continue
		}

		full := filepath.Join(dir, entry.Name())
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
