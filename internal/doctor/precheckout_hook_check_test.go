package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initTownRepo creates a git repo to stand in for the town root and returns its path.
func initTownRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := exec.Command("git", "init", root).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	return root
}

// TestBranchProtectionCheck_RespectsHooksPath is the gu-izs7x regression guard.
// The town root sets core.hooksPath=.beads/hooks (via beads), so git runs hooks
// only from there and ignores .git/hooks. The check must report missing and the
// fix must install into the effective dir — not .git/hooks, where the hook would
// be silently inert.
func TestBranchProtectionCheck_RespectsHooksPath(t *testing.T) {
	root := initTownRepo(t)
	hooksPath := filepath.Join(root, ".beads", "hooks")
	if err := os.MkdirAll(hooksPath, 0o755); err != nil {
		t.Fatalf("mkdir hooksPath: %v", err)
	}
	if err := exec.Command("git", "-C", root, "config", "core.hooksPath", ".beads/hooks").Run(); err != nil {
		t.Fatalf("set hooksPath: %v", err)
	}

	ctx := &CheckContext{TownRoot: root}
	check := NewBranchProtectionCheck()

	// A guard installed into .git/hooks must NOT satisfy the check, since git
	// ignores .git/hooks when core.hooksPath is set.
	legacyDir := filepath.Join(root, ".git", "hooks")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy hooks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "post-checkout"),
		[]byte("#!/bin/sh\n"+branchProtectionScript), 0o755); err != nil {
		t.Fatalf("write legacy hook: %v", err)
	}

	res := check.Run(ctx)
	if res.Status == StatusOK {
		t.Fatalf("Run returned OK but the guard is in .git/hooks (inert); want a warning")
	}

	// Fix must install into the effective hooks dir.
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix: %v", err)
	}
	effectiveHook := filepath.Join(hooksPath, "post-checkout")
	content, err := os.ReadFile(effectiveHook)
	if err != nil {
		t.Fatalf("reading effective post-checkout hook: %v", err)
	}
	if !strings.Contains(string(content), branchProtectionMarker) {
		t.Errorf("effective post-checkout hook missing branch protection marker")
	}

	// After the fix, the check should pass.
	if res := check.Run(ctx); res.Status != StatusOK {
		t.Errorf("Run after Fix = %v, want OK (%s)", res.Status, res.Message)
	}
}

// TestBranchProtectionCheck_PreservesExistingHookContent verifies the branch
// protection block is prepended ahead of an existing (beads-managed) hook so
// both run.
func TestBranchProtectionCheck_PreservesExistingHookContent(t *testing.T) {
	root := initTownRepo(t)
	hooksPath := filepath.Join(root, ".beads", "hooks")
	if err := os.MkdirAll(hooksPath, 0o755); err != nil {
		t.Fatalf("mkdir hooksPath: %v", err)
	}
	if err := exec.Command("git", "-C", root, "config", "core.hooksPath", ".beads/hooks").Run(); err != nil {
		t.Fatalf("set hooksPath: %v", err)
	}

	const beadsMarker = "BEADS INTEGRATION v1.0.3"
	beadsHook := "#!/usr/bin/env sh\n# --- BEGIN " + beadsMarker + " ---\nbd hooks run post-checkout \"$@\"\n"
	if err := os.WriteFile(filepath.Join(hooksPath, "post-checkout"), []byte(beadsHook), 0o755); err != nil {
		t.Fatalf("write beads hook: %v", err)
	}

	ctx := &CheckContext{TownRoot: root}
	check := NewBranchProtectionCheck()

	if res := check.Run(ctx); res.Status == StatusOK {
		t.Fatalf("Run returned OK but the existing hook lacks branch protection")
	}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(hooksPath, "post-checkout"))
	if err != nil {
		t.Fatalf("reading hook: %v", err)
	}
	got := string(content)
	if !strings.Contains(got, branchProtectionMarker) {
		t.Errorf("hook missing branch protection after Fix")
	}
	if !strings.Contains(got, beadsMarker) {
		t.Errorf("Fix clobbered the existing beads-managed hook content")
	}
}
