package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestVerifyFormulaExists_RigScopedFallback is the regression test for gu-sw6cx
// (and the related gu-b7xnj): a rig-scoped formula — one that ships ONLY at
// <townRoot>/<rig>/.beads/formulas/<name>.formula.toml and is absent at the
// town level and from the embedded set — must verify as existing when the
// target rig is supplied.
//
// Before the fix, verifyFormulaExists consulted only `bd formula show`, which
// is not rig-aware: run from the town/daemon context it cannot see a sibling
// rig's .beads/formulas dir, so casc-patrol-dispatch failed every daily run
// with "formula not found (all stages failed)" despite the formula existing.
func TestVerifyFormulaExists_RigScopedFallback(t *testing.T) {
	townRoot := t.TempDir()
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}

	// bd stub that always reports the formula as missing: exit 0 with empty
	// stdout (the documented "bd exit 0 but no output" not-found shape). This
	// forces verifyFormulaExists onto its rig-aware on-disk fallback, exactly
	// as the daemon/town context does for a rig-scoped formula.
	bdScript := "#!/bin/sh\nexit 0\n"
	bdScriptWindows := "@echo off\r\nexit /b 0\r\n"
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Ship casc-patrol ONLY at the rig level (mirrors casc_cdk).
	rigFormulasDir := filepath.Join(townRoot, "casc_cdk", ".beads", "formulas")
	if err := os.MkdirAll(rigFormulasDir, 0755); err != nil {
		t.Fatalf("mkdir rig formulas: %v", err)
	}
	formulaContent := []byte("formula = \"casc-patrol\"\nversion = 1\n")
	if err := os.WriteFile(filepath.Join(rigFormulasDir, "casc-patrol.formula.toml"), formulaContent, 0644); err != nil {
		t.Fatalf("write rig formula: %v", err)
	}

	t.Run("resolves when target rig supplied", func(t *testing.T) {
		if err := verifyFormulaExists("casc-patrol", townRoot, "casc_cdk"); err != nil {
			t.Fatalf("expected rig-scoped formula to resolve with rig context, got: %v", err)
		}
	})

	t.Run("not found without rig context (bd-only)", func(t *testing.T) {
		// Town-only resolution cannot see the rig-scoped formula — confirms the
		// fallback genuinely depends on the rig argument and is not a false pass.
		if err := verifyFormulaExists("casc-patrol", townRoot, ""); err == nil {
			t.Fatal("expected formula-not-found without rig context, got nil")
		}
	})

	t.Run("genuinely missing formula still errors", func(t *testing.T) {
		if err := verifyFormulaExists("no-such-formula", townRoot, "casc_cdk"); err == nil {
			t.Fatal("expected error for nonexistent formula, got nil")
		}
	})
}
