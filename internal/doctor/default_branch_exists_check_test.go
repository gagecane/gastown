package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultBranchExistsCheck_NoRig(t *testing.T) {
	check := NewDefaultBranchExistsCheck()
	ctx := &CheckContext{TownRoot: t.TempDir(), RigName: ""}

	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Errorf("expected StatusError with no rig, got %v", result.Status)
	}
}

func TestDefaultBranchExistsCheck_NoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}

	check := NewDefaultBranchExistsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning with no config, got %v", result.Status)
	}
}

func TestDefaultBranchExistsCheck_EmptyDefaultBranch(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Write config with no default_branch
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), []byte(`{"name":"testrig"}`), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewDefaultBranchExistsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK with no default_branch, got %v", result.Status)
	}
}

func TestDefaultBranchExistsCheck_NoBareRepo(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), []byte(`{"default_branch":"main"}`), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewDefaultBranchExistsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when no bare repo, got %v", result.Status)
	}
}

func TestDefaultBranchExistsCheck_NotFixable(t *testing.T) {
	check := NewDefaultBranchExistsCheck()
	if check.CanFix() {
		t.Error("DefaultBranchExistsCheck should not be fixable")
	}
}
