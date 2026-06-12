package doctor

import (
	"strconv"
	"testing"

	"github.com/steveyegge/gastown/internal/doltserver"
)

func TestDoltPortDataDirCheck_EmptyTownRoot(t *testing.T) {
	check := NewDoltPortDataDirCheck()
	result := check.Run(&CheckContext{TownRoot: ""})
	if result.Status != StatusOK {
		t.Fatalf("Status = %v, want %v (no town root is a no-op)", result.Status, StatusOK)
	}
}

func TestDoltPortDataDirCheck_OKWhenPortFree(t *testing.T) {
	// Point the check at a guaranteed-free port so CheckPortConflict finds no
	// server. This avoids flakiness from any real Dolt server on the default
	// 3307 in the test host's live town.
	freePort := doltserver.FindFreePort(34070)
	t.Setenv("GT_DOLT_PORT", strconv.Itoa(freePort))

	check := NewDoltPortDataDirCheck()
	result := check.Run(&CheckContext{TownRoot: t.TempDir()})
	if result.Status != StatusOK {
		t.Fatalf("Status = %v, want %v (free port has no imposter); message=%q",
			result.Status, StatusOK, result.Message)
	}
}
