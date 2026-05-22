package testutil

import (
	"fmt"
	"hash/fnv"
	"sync/atomic"
	"testing"
	"time"
)

var uniqueTestPrefixCounter atomic.Uint64

// UniqueTestPrefix returns a unique beads prefix safe for test isolation when
// initializing a beads database against a shared Dolt server (e.g. via
// RequireDoltContainer / EnsureDoltContainerForTestMain).
//
// The returned prefix has the form "testdb-XXXXXXXX". `bd init` sanitizes
// hyphens to underscores when deriving the Dolt database name, so this prefix
// maps to a database named "testdb_XXXXXXXX" — which matches the testdb_*
// cleanup pattern recognized by the reaper (internal/reaper/reaper.go),
// jsonl_git_backup, dolt_remotes, and `gt dolt cleanup` shortlists. Leaked
// databases are therefore auto-purged instead of accumulating as unprefixed
// orphans on the shared Dolt server.
//
// Use this instead of hardcoded prefixes like "gt" / "beads" when calling
// beads.Init(prefix) from tests. The prefix conforms to the beads validation
// regex (^[a-zA-Z][a-zA-Z0-9-]{0,19}$): 15 characters, alpha-start,
// alphanumeric + hyphen.
func UniqueTestPrefix(t *testing.T) string {
	t.Helper()
	n := uniqueTestPrefixCounter.Add(1)
	h := fnv.New32a()
	_, _ = fmt.Fprintf(h, "%s_%d_%d", t.Name(), time.Now().UnixNano(), n)
	return fmt.Sprintf("testdb-%08x", h.Sum32())
}
