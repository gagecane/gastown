// Package doltcleanup provides Dolt-process cleanup helpers for tests.
//
// This lives in a sub-package of testutil rather than testutil itself to
// avoid an import cycle: internal/doltserver/wisps_migrate_test.go (built
// only under -tags integration) imports internal/testutil, and the cleanup
// helper here imports internal/doltserver. Putting it under testutil
// directly would form a cycle that breaks the nightly integration build.
//
// Track: gu-x3vp.
package doltcleanup

import (
	"testing"

	"github.com/steveyegge/gastown/internal/doltserver"
)

// ReapOwnedDoltOnCleanup registers test cleanup for Dolt servers whose metadata
// and process args prove they belong to townRoot. It never kills by broad name or
// port, so production Dolt is protected when tests run inside a real workspace.
func ReapOwnedDoltOnCleanup(t testing.TB, townRoot string) {
	t.Helper()
	t.Cleanup(func() {
		stopped, err := doltserver.ReapOwnedTestServers(townRoot)
		if err != nil {
			t.Logf("owned Dolt cleanup skipped: %v", err)
			return
		}
		if stopped > 0 {
			t.Logf("stopped %d owned Dolt sql-server process(es)", stopped)
		}
	})
}
