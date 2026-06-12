package upstreamsync

import (
	"crypto/rand"
	"encoding/hex"
	"os/exec"
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
