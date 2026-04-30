package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

// TestLoadRigPolecatMaxConcurrent verifies that the per-rig cap is read from
// settings/config.json and gracefully handles missing files.
func TestLoadRigPolecatMaxConcurrent(t *testing.T) {
	t.Parallel()

	t.Run("missing settings file returns 0", func(t *testing.T) {
		t.Parallel()
		rigPath := t.TempDir()
		if got := loadRigPolecatMaxConcurrent(rigPath); got != 0 {
			t.Errorf("loadRigPolecatMaxConcurrent(no settings) = %d, want 0", got)
		}
	})

	t.Run("settings without polecat block returns 0", func(t *testing.T) {
		t.Parallel()
		rigPath := t.TempDir()
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings := config.NewRigSettings()
		if err := config.SaveRigSettings(settingsPath, settings); err != nil {
			t.Fatalf("save settings: %v", err)
		}
		if got := loadRigPolecatMaxConcurrent(rigPath); got != 0 {
			t.Errorf("loadRigPolecatMaxConcurrent(no cap) = %d, want 0", got)
		}
	})

	t.Run("configured cap returns value", func(t *testing.T) {
		t.Parallel()
		rigPath := t.TempDir()
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings := config.NewRigSettings()
		five := 5
		settings.Polecat = &config.PolecatPoolConfig{MaxConcurrent: &five}
		if err := config.SaveRigSettings(settingsPath, settings); err != nil {
			t.Fatalf("save settings: %v", err)
		}
		if got := loadRigPolecatMaxConcurrent(rigPath); got != 5 {
			t.Errorf("loadRigPolecatMaxConcurrent(cap=5) = %d, want 5", got)
		}
	})

	t.Run("zero cap returns 0 (no cap)", func(t *testing.T) {
		t.Parallel()
		rigPath := t.TempDir()
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings := config.NewRigSettings()
		zero := 0
		settings.Polecat = &config.PolecatPoolConfig{MaxConcurrent: &zero}
		if err := config.SaveRigSettings(settingsPath, settings); err != nil {
			t.Fatalf("save settings: %v", err)
		}
		if got := loadRigPolecatMaxConcurrent(rigPath); got != 0 {
			t.Errorf("loadRigPolecatMaxConcurrent(cap=0) = %d, want 0", got)
		}
	})
}

// TestFilterByPerRigCapacityNoCaps verifies that beads pass through unchanged
// when no rig has a configured cap.
func TestFilterByPerRigCapacityNoCaps(t *testing.T) {
	t.Parallel()

	townRoot := t.TempDir()
	// Create two rig directories without settings — loadRigPolecatMaxConcurrent
	// returns 0 for both, so nothing should be filtered.
	for _, rig := range []string{"rigA", "rigB"} {
		if err := os.MkdirAll(filepath.Join(townRoot, rig), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rig, err)
		}
	}

	pending := []capacity.PendingBead{
		{ID: "ctx-1", WorkBeadID: "w-1", TargetRig: "rigA"},
		{ID: "ctx-2", WorkBeadID: "w-2", TargetRig: "rigB"},
		{ID: "ctx-3", WorkBeadID: "w-3", TargetRig: "rigA"},
	}

	got := filterByPerRigCapacity(townRoot, pending)
	if len(got) != len(pending) {
		t.Errorf("len(got) = %d, want %d", len(got), len(pending))
	}
}

// TestFilterByPerRigCapacityEnforcesCap verifies that beads beyond the
// configured per-rig cap are dropped, while other rigs are unaffected.
// This is the critical unit test for the deferred scheduler path.
func TestFilterByPerRigCapacityEnforcesCap(t *testing.T) {
	t.Parallel()

	townRoot := t.TempDir()

	// rigA has cap=2, rigB has no cap.
	two := 2
	writeRigCap(t, townRoot, "rigA", &two)
	if err := os.MkdirAll(filepath.Join(townRoot, "rigB"), 0o755); err != nil {
		t.Fatalf("mkdir rigB: %v", err)
	}

	pending := []capacity.PendingBead{
		{ID: "ctx-A1", WorkBeadID: "wA1", TargetRig: "rigA"},
		{ID: "ctx-A2", WorkBeadID: "wA2", TargetRig: "rigA"},
		{ID: "ctx-A3", WorkBeadID: "wA3", TargetRig: "rigA"}, // Over cap — drop
		{ID: "ctx-A4", WorkBeadID: "wA4", TargetRig: "rigA"}, // Over cap — drop
		{ID: "ctx-B1", WorkBeadID: "wB1", TargetRig: "rigB"}, // No cap — keep
	}

	got := filterByPerRigCapacity(townRoot, pending)

	// Expect exactly 3 results: first two rigA beads (up to cap) + rigB bead.
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3 (got IDs: %s)", len(got), idsOf(got))
	}
	if got[0].ID != "ctx-A1" || got[1].ID != "ctx-A2" {
		t.Errorf("rigA first two not preserved in order: got %s %s", got[0].ID, got[1].ID)
	}

	// rigB bead must survive.
	foundB := false
	for _, b := range got {
		if b.ID == "ctx-B1" {
			foundB = true
		}
	}
	if !foundB {
		t.Error("rigB bead (no cap) was filtered out")
	}
}

// TestFilterByPerRigCapacityPreservesOrder ensures FIFO order survives
// per-rig filtering — older queued beads always dispatch first.
func TestFilterByPerRigCapacityPreservesOrder(t *testing.T) {
	t.Parallel()

	townRoot := t.TempDir()
	one := 1
	writeRigCap(t, townRoot, "rigA", &one)

	pending := []capacity.PendingBead{
		{ID: "ctx-1", WorkBeadID: "w-1", TargetRig: "rigA"},
		{ID: "ctx-2", WorkBeadID: "w-2", TargetRig: "rigA"}, // Dropped
		{ID: "ctx-3", WorkBeadID: "w-3", TargetRig: "rigA"}, // Dropped
	}

	got := filterByPerRigCapacity(townRoot, pending)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].ID != "ctx-1" {
		t.Errorf("expected oldest context ctx-1, got %s", got[0].ID)
	}
}

// TestFilterByPerRigCapacityEmptyTargetRig ensures beads with no TargetRig
// (should never happen, but be defensive) pass through the filter unchanged.
func TestFilterByPerRigCapacityEmptyTargetRig(t *testing.T) {
	t.Parallel()

	townRoot := t.TempDir()
	pending := []capacity.PendingBead{
		{ID: "ctx-1", WorkBeadID: "w-1", TargetRig: ""},
	}
	got := filterByPerRigCapacity(townRoot, pending)
	if len(got) != 1 {
		t.Errorf("expected 1 result (empty TargetRig passes through), got %d", len(got))
	}
}

// --- helpers ---

func writeRigCap(t *testing.T, townRoot, rigName string, cap *int) {
	t.Helper()
	rigPath := filepath.Join(townRoot, rigName)
	settingsPath := filepath.Join(rigPath, "settings", "config.json")
	settings := config.NewRigSettings()
	if cap != nil {
		settings.Polecat = &config.PolecatPoolConfig{MaxConcurrent: cap}
	}
	if err := config.SaveRigSettings(settingsPath, settings); err != nil {
		t.Fatalf("save settings for %s: %v", rigName, err)
	}
}

func idsOf(beads []capacity.PendingBead) string {
	out := ""
	for i, b := range beads {
		if i > 0 {
			out += ","
		}
		out += b.ID
	}
	return out
}
