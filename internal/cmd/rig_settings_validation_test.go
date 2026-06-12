package cmd

import (
	"strings"
	"testing"
)

// TestValidatePerRigPolecatCap covers the gu-su334 invariant: a per-rig
// polecat.max_concurrent must not exceed the town-wide ceiling
// (scheduler.global_max_polecats).
func TestValidatePerRigPolecatCap(t *testing.T) {
	t.Run("rejects above ceiling", func(t *testing.T) {
		townRoot := t.TempDir()
		setSchedulerGlobalCeiling(t, townRoot, -1, 8)

		err := validatePerRigPolecatCap(townRoot, "gastown", "9")
		if err == nil {
			t.Fatal("expected rejection for per-rig cap above ceiling")
		}
		if !strings.Contains(err.Error(), "global_max_polecats") {
			t.Fatalf("error %q should reference the global ceiling", err.Error())
		}
	})

	t.Run("allows at ceiling", func(t *testing.T) {
		townRoot := t.TempDir()
		setSchedulerGlobalCeiling(t, townRoot, -1, 8)
		if err := validatePerRigPolecatCap(townRoot, "gastown", "8"); err != nil {
			t.Fatalf("per-rig cap equal to ceiling should be allowed: %v", err)
		}
	})

	t.Run("allows below ceiling", func(t *testing.T) {
		townRoot := t.TempDir()
		setSchedulerGlobalCeiling(t, townRoot, -1, 8)
		if err := validatePerRigPolecatCap(townRoot, "gastown", "3"); err != nil {
			t.Fatalf("per-rig cap below ceiling should be allowed: %v", err)
		}
	})

	t.Run("no ceiling imposes no constraint", func(t *testing.T) {
		townRoot := t.TempDir()
		configureScheduler(t, townRoot, -1, 1) // no global ceiling
		if err := validatePerRigPolecatCap(townRoot, "gastown", "999"); err != nil {
			t.Fatalf("with no ceiling any per-rig cap is allowed: %v", err)
		}
	})

	t.Run("non-integer deferred to normal set path", func(t *testing.T) {
		townRoot := t.TempDir()
		setSchedulerGlobalCeiling(t, townRoot, -1, 8)
		if err := validatePerRigPolecatCap(townRoot, "gastown", "notanint"); err != nil {
			t.Fatalf("non-integer value should not be rejected here: %v", err)
		}
	})
}
