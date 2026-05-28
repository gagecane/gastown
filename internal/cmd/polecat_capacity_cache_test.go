package cmd

import (
	"testing"
	"time"
)

// TestPolecatCapacitySnapshot_CachedWithinTTL verifies that two calls to
// polecatCapacitySnapshotForTown within polecatCapacitySnapshotCacheTTL
// return the SAME cached result, avoiding the per-rig bd-list fan-out
// that previously dominated dispatch traffic (gu-yfv7).
//
// The check exercises the cache directly via load/store helpers since
// invoking polecatCapacitySnapshotForTown end-to-end requires a fully
// configured town. Per-rig fan-out reduction is validated by:
//   - the bd-subprocess count assertion in TestFindStrandedConvoys_Single*
//     (gu-c6ua, sibling fix)
//   - mayor's live trace at /tmp/bd-traces/ on the live town
func TestPolecatCapacitySnapshot_CachedWithinTTL(t *testing.T) {
	townRoot := t.TempDir()
	t.Cleanup(func() { invalidatePolecatCapacityCache(townRoot) })

	want := polecatCapacitySnapshot{Max: 8, Working: 3, Free: 5}
	storeCachedPolecatCapacitySnapshot(townRoot, want, nil)

	got, ok := loadCachedPolecatCapacitySnapshot(townRoot)
	if !ok {
		t.Fatal("loadCachedPolecatCapacitySnapshot returned ok=false immediately after store; " +
			"cache should serve fresh entries within polecatCapacitySnapshotCacheTTL " +
			"(currently 5s) — see gu-yfv7")
	}
	if got.snapshot != want {
		t.Errorf("cached snapshot = %+v, want %+v", got.snapshot, want)
	}
	if got.err != nil {
		t.Errorf("cached err = %v, want nil", got.err)
	}
}

// TestPolecatCapacitySnapshot_CacheExpiresAfterTTL verifies that a
// snapshot stored more than polecatCapacitySnapshotCacheTTL ago is
// treated as stale and a fresh recomputation is required.
//
// Cache freshness is the upper bound on staleness for the dispatcher's
// view of free polecat capacity. A too-long TTL would let dispatch
// overcommit (see polecatCapacitySnapshotCacheTTL doc); a too-short TTL
// reintroduces the per-rig bd fan-out that motivated this cache.
func TestPolecatCapacitySnapshot_CacheExpiresAfterTTL(t *testing.T) {
	townRoot := t.TempDir()
	t.Cleanup(func() { invalidatePolecatCapacityCache(townRoot) })

	// Inject a snapshot artificially aged past the TTL.
	polecatCapacityCacheMu.Lock()
	if polecatCapacityCache == nil {
		polecatCapacityCache = make(map[string]cachedCapacitySnapshot)
	}
	polecatCapacityCache[townRoot] = cachedCapacitySnapshot{
		snapshot: polecatCapacitySnapshot{Max: 8, Working: 3, Free: 5},
		at:       time.Now().Add(-polecatCapacitySnapshotCacheTTL - time.Second),
	}
	polecatCapacityCacheMu.Unlock()

	if _, ok := loadCachedPolecatCapacitySnapshot(townRoot); ok {
		t.Errorf("loadCachedPolecatCapacitySnapshot returned a stale entry (>TTL old); "+
			"cache must reject stale entries to bound capacity-snapshot drift "+
			"(TTL: %v) — see gu-yfv7", polecatCapacitySnapshotCacheTTL)
	}
}

// TestPolecatCapacitySnapshot_InvalidateDropsCachedEntry verifies that
// invalidatePolecatCapacityCache drops the named townRoot's cached
// entry without touching unrelated townRoot keys. Used by tests and by
// any future code path that needs to force a fresh read after a known
// state-changing operation.
func TestPolecatCapacitySnapshot_InvalidateDropsCachedEntry(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	t.Cleanup(func() {
		invalidatePolecatCapacityCache(a)
		invalidatePolecatCapacityCache(b)
	})

	storeCachedPolecatCapacitySnapshot(a, polecatCapacitySnapshot{Max: 1}, nil)
	storeCachedPolecatCapacitySnapshot(b, polecatCapacitySnapshot{Max: 2}, nil)

	invalidatePolecatCapacityCache(a)

	if _, ok := loadCachedPolecatCapacitySnapshot(a); ok {
		t.Errorf("invalidatePolecatCapacityCache(a) did not drop a's entry")
	}
	if _, ok := loadCachedPolecatCapacitySnapshot(b); !ok {
		t.Errorf("invalidatePolecatCapacityCache(a) incorrectly dropped b's entry; " +
			"invalidation must be scoped to the named townRoot only")
	}
}
