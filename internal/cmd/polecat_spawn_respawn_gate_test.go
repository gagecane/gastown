package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/witness"
)

// seedRespawns records n dispatch attempts for beadID against the witness
// respawn ledger rooted at townRoot.
func seedRespawns(t *testing.T, townRoot, beadID string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		witness.RecordBeadRespawn(townRoot, beadID)
	}
}

func newRespawnGateTownRoot(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}
	return tmpDir
}

// TestRespawnGate_SoftBlock_AutoRespawnRetryBypasses is the gu-i34ey regression:
// a bead whose prior attempts were polecat deaths (it tripped the SOFT respawn
// block) must remain auto-dispatchable by the daemon scheduler, which sets
// autoRespawnRetry=true. The same bead must still be blocked on the default
// (interactive, no-force) path so the witness can escalate.
func TestRespawnGate_SoftBlock_AutoRespawnRetryBypasses(t *testing.T) {
	townRoot := newRespawnGateTownRoot(t)
	maxRespawns := config.LoadOperationalConfig(townRoot).GetWitnessConfig().MaxBeadRespawnsV()
	if maxRespawns < 1 {
		t.Skipf("unexpected max respawns %d", maxRespawns)
	}
	const bead = "gu-soft"

	// Drive the bead to exactly the soft-block threshold (Count == max) without
	// crossing the permanent threshold (2 × max).
	seedRespawns(t, townRoot, bead, maxRespawns)
	if !witness.ShouldBlockRespawn(townRoot, bead) {
		t.Fatalf("precondition: bead should be soft-blocked after %d respawns", maxRespawns)
	}
	if witness.ShouldPermanentlyBlockRespawn(townRoot, bead) {
		t.Fatalf("precondition: bead must NOT be permanently blocked at %d respawns", maxRespawns)
	}

	// Default path (no force, no auto-retry): soft block is enforced.
	if err := respawnGateError(townRoot, bead, "testrig", false, false); err == nil {
		t.Error("default path: expected soft block error, got nil")
	} else if !strings.Contains(err.Error(), "respawn limit reached") {
		t.Errorf("default path: want 'respawn limit reached', got: %v", err)
	}

	// Scheduler auto-retry path: soft block is bypassed.
	if err := respawnGateError(townRoot, bead, "testrig", false, true); err != nil {
		t.Errorf("autoRespawnRetry path: expected soft block bypass, got error: %v", err)
	}

	// Operator --force path: soft block is bypassed (existing behavior).
	if err := respawnGateError(townRoot, bead, "testrig", true, false); err != nil {
		t.Errorf("force path: expected soft block bypass, got error: %v", err)
	}
}

// TestRespawnGate_PermanentBlock_NotBypassable verifies the permanent block
// stays the real circuit breaker: once cumulative attempts cross
// PermanentBlockMultiplier × MaxBeadRespawns, neither --force nor the
// scheduler's autoRespawnRetry may re-dispatch the bead. This bounds gu-i34ey's
// soft-block bypass so a chronically-failing bead cannot hot-loop forever.
func TestRespawnGate_PermanentBlock_NotBypassable(t *testing.T) {
	townRoot := newRespawnGateTownRoot(t)
	maxRespawns := config.LoadOperationalConfig(townRoot).GetWitnessConfig().MaxBeadRespawnsV()
	if maxRespawns < 1 {
		t.Skipf("unexpected max respawns %d", maxRespawns)
	}
	const bead = "gu-perm"

	// Cross the permanent threshold.
	seedRespawns(t, townRoot, bead, witness.PermanentBlockMultiplier*maxRespawns)
	if !witness.ShouldPermanentlyBlockRespawn(townRoot, bead) {
		t.Fatalf("precondition: bead should be permanently blocked")
	}

	for _, tc := range []struct {
		name             string
		force            bool
		autoRespawnRetry bool
	}{
		{"default", false, false},
		{"force", true, false},
		{"autoRespawnRetry", false, true},
		{"force+autoRespawnRetry", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := respawnGateError(townRoot, bead, "testrig", tc.force, tc.autoRespawnRetry)
			if err == nil {
				t.Fatalf("expected PERMANENT respawn block error, got nil")
			}
			if !strings.Contains(err.Error(), "PERMANENT respawn block") {
				t.Errorf("want 'PERMANENT respawn block', got: %v", err)
			}
		})
	}
}

// TestRespawnGate_BelowThreshold_AlwaysDispatchable confirms a bead under the
// soft threshold dispatches on every path (no regression for healthy beads).
func TestRespawnGate_BelowThreshold_AlwaysDispatchable(t *testing.T) {
	townRoot := newRespawnGateTownRoot(t)
	const bead = "gu-fresh"

	// One attempt — well below the default threshold of 3.
	seedRespawns(t, townRoot, bead, 1)

	for _, tc := range []struct {
		name             string
		force            bool
		autoRespawnRetry bool
	}{
		{"default", false, false},
		{"force", true, false},
		{"autoRespawnRetry", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := respawnGateError(townRoot, bead, "testrig", tc.force, tc.autoRespawnRetry); err != nil {
				t.Errorf("below-threshold bead should dispatch, got error: %v", err)
			}
		})
	}
}
