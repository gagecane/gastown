package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/lock"
)

// tryAcquireSlingBeadLock serializes assignment writes per bead so concurrent
// sling races cannot produce conflicting assignee/metadata updates.
func tryAcquireSlingBeadLock(townRoot, beadID string) (func(), error) {
	lockDir := filepath.Join(townRoot, ".runtime", "locks", "sling")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		return nil, fmt.Errorf("creating sling lock dir: %w", err)
	}

	safeBeadID := strings.NewReplacer("/", "_", ":", "_").Replace(beadID)
	lockPath := filepath.Join(lockDir, safeBeadID+".flock")
	release, locked, err := lock.FlockTryAcquire(lockPath)
	if err != nil {
		return nil, fmt.Errorf("acquiring sling lock for bead %s: %w", beadID, err)
	}
	if !locked {
		return nil, fmt.Errorf("bead %s is already being slung; retry after the current assignment completes", beadID)
	}

	return release, nil
}

// tryAcquireSlingAssigneeLock acquires a per-assignee file lock to serialize concurrent
// hook writes to the same polecat. The per-bead lock (tryAcquireSlingBeadLock) prevents
// double-sling of the same bead, but does not prevent concurrent slings from racing on
// the same assignee's hook_bead field in Dolt. This lock is held only during
// hookBeadWithRetry. Uses non-blocking try-acquire with retry and timeout to avoid
// indefinite blocking if a sling gets stuck.
// See: https://github.com/steveyegge/gastown/issues/3114
func tryAcquireSlingAssigneeLock(townRoot, targetAgent string) (func(), error) {
	lockDir := filepath.Join(townRoot, ".runtime", "locks", "sling")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		return nil, fmt.Errorf("creating sling lock dir: %w", err)
	}

	safeAgent := strings.NewReplacer("/", "_", ":", "_").Replace(targetAgent)
	lockPath := filepath.Join(lockDir, "assignee_"+safeAgent+".flock")

	// Try non-blocking acquire with retry. hookBeadWithRetry itself has 10 retries
	// with up to 30s backoff, so we allow generous total wait time for the lock.
	const maxAttempts = 20
	const retryInterval = 500 // milliseconds
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		release, locked, err := lock.FlockTryAcquire(lockPath)
		if err != nil {
			return nil, fmt.Errorf("acquiring assignee sling lock for %s: %w", targetAgent, err)
		}
		if locked {
			return release, nil
		}
		if attempt < maxAttempts {
			time.Sleep(time.Duration(retryInterval) * time.Millisecond)
		}
	}

	return nil, fmt.Errorf("timed out acquiring assignee sling lock for %s after %ds (another sling may be stuck)", targetAgent, maxAttempts*retryInterval/1000)
}
