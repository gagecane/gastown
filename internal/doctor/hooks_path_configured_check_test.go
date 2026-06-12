package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewHooksPathConfiguredCheck(t *testing.T) {
	check := NewHooksPathConfiguredCheck()

	if check.Name() != "hooks-path-configured" {
		t.Errorf("expected name 'hooks-path-configured', got %q", check.Name())
	}
	if !check.CanFix() {
		t.Error("expected CanFix to return true")
	}
}

// TestHooksPathConfiguredCheck_NewLayoutPolecat verifies the check inspects
// new-layout polecat clones (polecats/<name>/<rig>/) rather than silently
// skipping them. Regression test for gu-nid89.36.
func TestHooksPathConfiguredCheck_NewLayoutPolecat(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	// New-layout polecat clone: polecats/<name>/<rig>/
	polecatClone := filepath.Join(rigDir, "polecats", "alice", rigName)
	initGitRepo(t, polecatClone)
	// .githooks must exist for the check to evaluate the clone
	if err := os.MkdirAll(filepath.Join(polecatClone, ".githooks"), 0755); err != nil {
		t.Fatal(err)
	}
	// Note: core.hooksPath is intentionally left unset.

	check := NewHooksPathConfiguredCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning for new-layout polecat with unset hooksPath, got %v (%s)", result.Status, result.Message)
	}
	if len(result.Details) != 1 {
		t.Fatalf("expected 1 unconfigured clone, got %d: %v", len(result.Details), result.Details)
	}

	// Fix should set core.hooksPath, after which Run reports OK.
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}
	if result := check.Run(ctx); result.Status != StatusOK {
		t.Errorf("expected StatusOK after Fix, got %v (%s)", result.Status, result.Message)
	}
}

// TestHooksPathConfiguredCheck_OldLayoutPolecat verifies the old flat layout
// (polecats/<name>/) is still handled.
func TestHooksPathConfiguredCheck_OldLayoutPolecat(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	// Old-layout polecat clone: polecats/<name>/
	polecatClone := filepath.Join(rigDir, "polecats", "bob")
	initGitRepo(t, polecatClone)
	if err := os.MkdirAll(filepath.Join(polecatClone, ".githooks"), 0755); err != nil {
		t.Fatal(err)
	}

	check := NewHooksPathConfiguredCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning for old-layout polecat with unset hooksPath, got %v (%s)", result.Status, result.Message)
	}
	if len(result.Details) != 1 {
		t.Fatalf("expected 1 unconfigured clone, got %d: %v", len(result.Details), result.Details)
	}
}
