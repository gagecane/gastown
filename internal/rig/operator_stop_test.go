package rig

import (
	"testing"

	"github.com/steveyegge/gastown/internal/wisp"
)

// TestRefineryStoppedByOperator_RoundTrip verifies the set/clear/read cycle
// on a fresh rig's wisp config. The state must persist across new Config
// instances because the daemon and the cmd package construct their own
// configs when making the auto-restart decision.
func TestRefineryStoppedByOperator_RoundTrip(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"

	// Baseline: flag is unset → not stopped.
	if IsRefineryStoppedByOperator(townRoot, rigName) {
		t.Fatal("expected operator-stop flag to be false for fresh rig")
	}

	// Set it and verify a new reader sees the change.
	if err := SetRefineryStoppedByOperator(townRoot, rigName); err != nil {
		t.Fatalf("SetRefineryStoppedByOperator: %v", err)
	}
	if !IsRefineryStoppedByOperator(townRoot, rigName) {
		t.Fatal("expected operator-stop flag to be true after Set")
	}

	// Clear it and verify return to baseline.
	if err := ClearRefineryStoppedByOperator(townRoot, rigName); err != nil {
		t.Fatalf("ClearRefineryStoppedByOperator: %v", err)
	}
	if IsRefineryStoppedByOperator(townRoot, rigName) {
		t.Fatal("expected operator-stop flag to be false after Clear")
	}
}

// TestIsRefineryStoppedByOperator_MissingFile verifies that a rig with no
// wisp config file at all (the normal case for freshly provisioned rigs)
// reports false rather than surfacing an error upward. The daemon's
// heartbeat must not misinterpret "no file" as "don't restart".
func TestIsRefineryStoppedByOperator_MissingFile(t *testing.T) {
	townRoot := t.TempDir()
	if IsRefineryStoppedByOperator(townRoot, "never-touched") {
		t.Fatal("expected false when wisp config file does not exist")
	}
}

// TestIsRefineryStoppedByOperator_NonBoolValue guards against regressions
// in wisp's type coercion: a non-bool value stored under the key must be
// treated as "not stopped" so a corrupted wisp file cannot silently block
// the refinery from ever restarting.
func TestIsRefineryStoppedByOperator_NonBoolValue(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "coerce"

	cfg := wisp.NewConfig(townRoot, rigName)
	if err := cfg.Set(WispRefineryStoppedKey, "yes"); err != nil {
		t.Fatalf("wisp.Set: %v", err)
	}

	if IsRefineryStoppedByOperator(townRoot, rigName) {
		t.Error("expected non-bool value to be treated as not-stopped (safe default)")
	}
}

// TestIsRefineryStoppedByOperator_BlockedKey verifies that blocking the
// wisp key (a legitimate operator action for pinning auto-restart on)
// reports false, matching wisp's general "blocked = nil" semantics.
func TestIsRefineryStoppedByOperator_BlockedKey(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "blocked"

	cfg := wisp.NewConfig(townRoot, rigName)
	if err := cfg.Set(WispRefineryStoppedKey, true); err != nil {
		t.Fatalf("wisp.Set: %v", err)
	}
	if err := cfg.Block(WispRefineryStoppedKey); err != nil {
		t.Fatalf("wisp.Block: %v", err)
	}

	if IsRefineryStoppedByOperator(townRoot, rigName) {
		t.Error("expected blocked key to report not-stopped (blocked == nil)")
	}
}
