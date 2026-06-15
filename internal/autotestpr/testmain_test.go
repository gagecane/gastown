//go:build integration

package autotestpr

import (
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testutil"
)

// TestMain starts an ephemeral Dolt container for this package's integration
// tests so that bd init (in initBeadsDB) targets the isolated container instead
// of the shared production Dolt server on :3307.
//
// Without this, TestAttachmentBeadRetention ran `bd init --prefix=ret<N>` with
// no --server-port; bd then auto-detected the production server on :3307 and
// created real beads_ret<N> databases with zero teardown. Because the leaked
// DBs hold user tables, `gt dolt cleanup` refuses to remove them — orphan-DB
// pollution that degrades the production data plane. This is the twin of the
// crew incident (gs-z76 / gu-4str3); crew was fixed with a stub-bd TestMain,
// autotestpr never was (gu-ke9l2).
//
// EnsureDoltContainerForTestMain sets GT_DOLT_PORT process-wide so initBeadsDB
// forwards --server --server-port; TerminateDoltContainer destroys the
// container (and every database it held) after the run.
func TestMain(m *testing.M) {
	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "autotestpr TestMain: skipping — %v\n", err)
		os.Exit(0)
	}

	code := m.Run()

	testutil.TerminateDoltContainer()
	os.Exit(code)
}
