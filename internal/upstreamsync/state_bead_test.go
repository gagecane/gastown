package upstreamsync

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/testutil"
)

// newProvisionTestBeads spins up an isolated beads DB against the shared
// Dolt test container, mirroring the pattern in
// internal/cmd/patrol_helpers_test.go's setupPatrolTestDB.
func newProvisionTestBeads(t *testing.T) (*beads.Beads, string) {
	t.Helper()
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping test")
	}
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())
	b := beads.NewIsolatedWithPort(t.TempDir(), port)

	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	prefix := "us" + hex.EncodeToString(buf[:])
	if err := b.Init(prefix); err != nil {
		t.Fatalf("bd init: %v", err)
	}
	return b, prefix
}

// TestEnsureStateBead_ProvisionsThenIdempotent is the regression guard for
// the fork-sync deadlock: before this fix EnsureStateBead had zero callers,
// so LoadSyncState always returned ErrStateBeadNotProvisioned and
// `gt upstream sync` could never run. This verifies the provisioning path
// actually creates a loadable, pinned state bead and is a no-op on repeat.
func TestEnsureStateBead_ProvisionsThenIdempotent(t *testing.T) {
	b, prefix := newProvisionTestBeads(t)

	// Pre-condition: not provisioned.
	if _, err := LoadSyncState(b, prefix); err == nil {
		t.Fatalf("expected ErrStateBeadNotProvisioned before provisioning, got nil")
	}

	// First call provisions.
	issue, err := EnsureStateBead(b, prefix, "gastown_upstream", nil)
	if err != nil {
		t.Fatalf("EnsureStateBead (first): %v", err)
	}
	if issue == nil {
		t.Fatal("EnsureStateBead returned nil issue")
	}
	if issue.Status != beads.StatusPinned {
		t.Errorf("state bead status = %q, want pinned", issue.Status)
	}

	// State is now loadable and defaults to idle.
	state, err := LoadSyncState(b, prefix)
	if err != nil {
		t.Fatalf("LoadSyncState after provision: %v", err)
	}
	if state.State != StateIdle {
		t.Errorf("provisioned state = %q, want %q", state.State, StateIdle)
	}

	// Second call is idempotent: same bead ID, no error.
	again, err := EnsureStateBead(b, prefix, "gastown_upstream", nil)
	if err != nil {
		t.Fatalf("EnsureStateBead (second): %v", err)
	}
	if again.ID != issue.ID {
		t.Errorf("idempotent call returned different bead: %q vs %q", again.ID, issue.ID)
	}
}

// TestEnsureStateBead_RoutedTownToRig is the regression guard for gu-erckc:
// `gt upstream sync` failed with "pinning state bead: issue not found" because
// the caller passes a town-rooted beads wrapper, but the state bead ID carries
// a rig prefix. Show/Update route by prefix to the RIG database, while
// CreateWithID (no Rig/Parent hint) fell back to the wrapper's own TOWN
// database — a split-brain provision: the bead was created in the town DB, then
// the pin and reload looked in the rig DB and failed.
//
// Unlike TestEnsureStateBead_ProvisionsThenIdempotent (isolated DB, no
// routes.jsonl, so routing is a no-op), this test stands up a real town with a
// routes.jsonl entry pointing the rig prefix at a separate rig database, so the
// town/rig routing split is actually exercised.
func TestEnsureStateBead_RoutedTownToRig(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping test")
	}
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())

	townRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	// Mark this directory as a Gas Town root so FindTownRoot detects it and
	// prefix routing engages.
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte(`{"name":"test"}`), 0o644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}

	// Unique prefixes so parallel/repeat runs don't collide on the shared server.
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	suffix := hex.EncodeToString(buf[:])
	townPrefix := "ut" + suffix
	rigPrefix := "ur" + suffix

	// routes.jsonl: town prefix → ".", rig prefix → the rig's .beads.
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigPath := filepath.Join("gastown_upstream", "mayor", "rig")
	if err := beads.WriteRoutes(townBeadsDir, []beads.Route{
		{Prefix: townPrefix + "-", Path: "."},
		{Prefix: rigPrefix + "-", Path: rigPath},
	}); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	// Init both databases on the test container.
	town := beads.NewIsolatedWithPort(townRoot, port)
	if err := town.Init(townPrefix); err != nil {
		t.Fatalf("town bd init: %v", err)
	}
	rigDir := filepath.Join(townRoot, rigPath)
	// Pre-create the rig's own .beads/config.yaml so bd init anchors locally
	// instead of walking up and finding the town database (mirrors the
	// initBeadsDBWithPrefix pattern in beads_routing_integration_test.go).
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0o700); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "config.yaml"), []byte("prefix: "+rigPrefix+"\n"), 0o644); err != nil {
		t.Fatalf("write rig config: %v", err)
	}
	rig := beads.NewIsolatedWithPort(rigDir, port)
	if err := rig.Init(rigPrefix); err != nil {
		t.Fatalf("rig bd init: %v", err)
	}

	// Caller wrapper is rooted at the TOWN .beads, exactly as runUpstreamSync
	// constructs it (beads.NewWithBeadsDir(townRoot, townRoot/.beads)).
	caller := beads.NewIsolatedWithPort(townRoot, port)

	// Before the fix this failed with "pinning state bead: issue not found".
	issue, err := EnsureStateBead(caller, rigPrefix, "gastown_upstream", nil)
	if err != nil {
		t.Fatalf("EnsureStateBead (routed): %v", err)
	}
	if issue == nil {
		t.Fatal("EnsureStateBead returned nil issue")
	}
	if issue.Status != beads.StatusPinned {
		t.Errorf("state bead status = %q, want pinned", issue.Status)
	}

	// The bead must live in the RIG database (where prefix routing points), not
	// the town database. LoadSyncState routes by prefix, so a successful load
	// proves create + pin landed in the routed DB.
	state, err := LoadSyncState(caller, rigPrefix)
	if err != nil {
		t.Fatalf("LoadSyncState after routed provision: %v", err)
	}
	if state.State != StateIdle {
		t.Errorf("provisioned state = %q, want %q", state.State, StateIdle)
	}
}
