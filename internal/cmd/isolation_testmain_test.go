//go:build !integration

package cmd

import (
	"fmt"
	"net"
	"os"
	"testing"
)

// TestMain isolates the non-integration cmd test binary from the production
// Dolt server.
//
// These tests are frequently run as a quality gate from inside a live Gas Town
// agent session (e.g. a polecat). That session exports BEADS_DOLT_PORT=3307,
// GT_DOLT_PORT=3307 and a host pointing at the production Dolt server. Inherited
// verbatim, any cmd code path that opens a beads store (createStagedConvoy →
// addTrackingRelation, role/session setup, …) connects to production and
// initializes a non-namespaced "beads" database on it — the leak behind gs-2l1.
// It is invisible in clean CI (no server on 3307) but recurs "every patrol",
// where deacon repeatedly cleans it and the dropped-but-listed copy wedges the
// Dolt catalog.
//
// Pinning BEADS_DOLT_PORT/GT_DOLT_PORT to a guaranteed-closed port makes every
// inherited connection attempt fail fast (connection refused) instead of
// reaching production. Tests that genuinely need a Dolt backend opt in
// explicitly — they call testutil.RequireDoltContainer and pass the container
// port via beads.NewIsolatedWithPort / explicit DSNs, which override this
// default. BEADS_DOLT_AUTO_START=0 additionally prevents the SDK from spawning
// an embedded server in a temp dir (gs-4yf).
//
// The integration build has its own TestMain (integration_testmain_test.go)
// that routes to a real container, so this file is excluded there.
func TestMain(m *testing.M) {
	// Reserve an ephemeral port, then release it: the kernel won't re-hand it to
	// an unrelated listener for the life of this process, so connections to it
	// reliably fail fast.
	closedPort := "1"
	if ln, err := net.Listen("tcp", "127.0.0.1:0"); err == nil {
		closedPort = fmt.Sprintf("%d", ln.Addr().(*net.TCPAddr).Port)
		_ = ln.Close()
	}
	_ = os.Setenv("BEADS_DOLT_PORT", closedPort)
	_ = os.Setenv("GT_DOLT_PORT", closedPort)
	_ = os.Setenv("BEADS_DOLT_AUTO_START", "0")

	os.Exit(m.Run())
}
