// Store-mutation mutual exclusion between `dolt backup sync` and Dolt restarts.
//
// `dolt backup sync` runs as a separate `dolt` CLI subprocess that reads a
// database's live store table files under .dolt-data/<db> and copies them to
// the backup destination .dolt-backup/<db>/..., writing the dest manifest. When
// a Dolt server restart runs concurrently, the live store's table files are
// rewritten/pruned (NomsBlockStore.Close on stop + startup GC), so the in-flight
// sync copies a dest manifest that references a table file which no longer
// exists. The NEXT sync then fails with "Error 1105: table file not found" and
// the backup destination is corrupt (live data is never lost). This recurred
// and spread across 5 databases (gu-p02zy, first seen as gu-rhvsn).
//
// The fix is a single town-wide advisory flock that both the restart paths and
// the backup acquire, so the two never touch a store's table files at the same
// time. flock is used (over a timestamped marker file) because it auto-releases
// on process death — no stale-lock heuristic — and it is already the lock
// primitive this package uses for serializing concurrent server starts
// (dolt.lock in Start). The lock is held per store-lifecycle operation (each
// Stop, each Start) and per-DB on the backup side, so a restart waits at most
// one database's sync, never a whole backup run.

package doltserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// DefaultStoreLockWait is how long a restart path waits to acquire the store
// lock before proceeding fail-open. It must comfortably exceed the backup
// plugin's per-DB sync timeout (60s in plugins/dolt-backup/run.sh) so a restart
// normally waits out at most one in-flight database sync rather than aborting it.
const DefaultStoreLockWait = 90 * time.Second

// StoreLockPath returns the advisory store-lock file path for a town + port.
// Mirrors the port-canonical naming of pidFile/unhealthySignalFile: the
// production port (3307) gets the bare name so existing tooling and operators
// see a stable path, and any other port is suffixed to keep multi-server
// installs isolated.
func StoreLockPath(townRoot string, port int) string {
	if port == DefaultPort {
		return filepath.Join(townRoot, "daemon", "dolt-store.lock")
	}
	return filepath.Join(townRoot, "daemon", fmt.Sprintf("dolt-store.lock.%d", port))
}

// WithStoreLock runs fn while holding the town's advisory store lock, releasing
// it when fn returns. It waits up to `wait` to acquire the lock.
//
// Fail-open by design: if the lock cannot be created or is not acquired within
// `wait`, WithStoreLock logs via logf (when non-nil) and runs fn ANYWAY. The
// callers are Dolt restart paths — blocking a health-driven restart of an
// unhealthy server is worse than the small residual corruption risk a contended
// lock would otherwise prevent, and the backup side independently defers when it
// cannot get the lock. fn's own error is always returned unchanged; lock
// acquisition problems never mask it.
//
// logf may be nil (no logging). It matches the func(string, ...any) shape the
// daemon and CLI already pass around.
func WithStoreLock(townRoot string, port int, wait time.Duration, logf func(string, ...any), fn func() error) error {
	lockPath := StoreLockPath(townRoot, port)

	// The daemon dir normally exists (Start/Stop ensure it), but create it
	// defensively so a first-touch restart never fails to lock for a missing dir.
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		logfSafe(logf, "store-lock: cannot create lock dir, proceeding without lock: %v", err)
		return fn()
	}

	fl := flock.New(lockPath)
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()

	locked, err := fl.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil || !locked {
		logfSafe(logf, "store-lock: could not acquire %s within %s (proceeding fail-open): locked=%v err=%v",
			lockPath, wait, locked, err)
		return fn()
	}
	defer func() { _ = fl.Unlock() }()

	return fn()
}

// logfSafe calls logf only when it is non-nil.
func logfSafe(logf func(string, ...any), format string, args ...any) {
	if logf != nil {
		logf(format, args...)
	}
}
