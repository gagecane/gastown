package beads

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveBeadsDir tests the redirect following logic.
func TestResolveBeadsDir(t *testing.T) {
	// Create temp directory structure
	tmpDir, err := os.MkdirTemp("", "beads-redirect-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	t.Run("no redirect", func(t *testing.T) {
		// Create a simple .beads directory without redirect
		workDir := filepath.Join(tmpDir, "no-redirect")
		beadsDir := filepath.Join(workDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		got := ResolveBeadsDir(workDir)
		want := beadsDir
		if got != want {
			t.Errorf("ResolveBeadsDir() = %q, want %q", got, want)
		}
	})

	t.Run("with redirect", func(t *testing.T) {
		// Create structure like: crew/max/.beads/redirect -> ../../mayor/rig/.beads
		workDir := filepath.Join(tmpDir, "crew", "max")
		localBeadsDir := filepath.Join(workDir, ".beads")
		targetBeadsDir := filepath.Join(tmpDir, "mayor", "rig", ".beads")

		// Create both directories
		if err := os.MkdirAll(localBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(targetBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create redirect file
		redirectPath := filepath.Join(localBeadsDir, "redirect")
		if err := os.WriteFile(redirectPath, []byte("../../mayor/rig/.beads\n"), 0644); err != nil {
			t.Fatal(err)
		}

		got := ResolveBeadsDir(workDir)
		want := targetBeadsDir
		if got != want {
			t.Errorf("ResolveBeadsDir() = %q, want %q", got, want)
		}
	})

	t.Run("no beads directory", func(t *testing.T) {
		// Directory with no .beads at all
		workDir := filepath.Join(tmpDir, "empty")
		if err := os.MkdirAll(workDir, 0755); err != nil {
			t.Fatal(err)
		}

		got := ResolveBeadsDir(workDir)
		want := filepath.Join(workDir, ".beads")
		if got != want {
			t.Errorf("ResolveBeadsDir() = %q, want %q", got, want)
		}
	})

	t.Run("empty redirect file", func(t *testing.T) {
		// Redirect file exists but is empty - should fall back to local
		workDir := filepath.Join(tmpDir, "empty-redirect")
		beadsDir := filepath.Join(workDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		redirectPath := filepath.Join(beadsDir, "redirect")
		if err := os.WriteFile(redirectPath, []byte("  \n"), 0644); err != nil {
			t.Fatal(err)
		}

		got := ResolveBeadsDir(workDir)
		want := beadsDir
		if got != want {
			t.Errorf("ResolveBeadsDir() = %q, want %q", got, want)
		}
	})

	t.Run("absolute path redirect", func(t *testing.T) {
		// Redirect file contains an absolute path (e.g., /Users/emech/.../gastown/.beads)
		// This was the path-doubling bug: filepath.Join(workDir, absPath) produces
		// workDir/Users/emech/... instead of using absPath directly.
		workDir := filepath.Join(tmpDir, "polecat", "chrome")
		localBeadsDir := filepath.Join(workDir, ".beads")
		targetBeadsDir := filepath.Join(tmpDir, "canonical", ".beads")

		if err := os.MkdirAll(localBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(targetBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Write absolute path redirect
		redirectPath := filepath.Join(localBeadsDir, "redirect")
		if err := os.WriteFile(redirectPath, []byte(targetBeadsDir+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		got := ResolveBeadsDir(workDir)
		if got != targetBeadsDir {
			t.Errorf("ResolveBeadsDir() = %q, want %q (absolute redirect should be used as-is)", got, targetBeadsDir)
		}
	})

	t.Run("absolute path in redirect chain", func(t *testing.T) {
		// Test absolute path handling in resolveBeadsDirWithDepth (chained redirects)
		firstBeadsDir := filepath.Join(tmpDir, "chain-test", "first", ".beads")
		finalBeadsDir := filepath.Join(tmpDir, "chain-test", "final", ".beads")

		if err := os.MkdirAll(firstBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(finalBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// First beads redirects via absolute path to final
		if err := os.WriteFile(filepath.Join(firstBeadsDir, "redirect"), []byte(finalBeadsDir+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		got := resolveBeadsDirWithDepth(firstBeadsDir, 3)
		if got != finalBeadsDir {
			t.Errorf("resolveBeadsDirWithDepth() = %q, want %q", got, finalBeadsDir)
		}
	})

	t.Run("circular redirect", func(t *testing.T) {
		// Redirect that points to itself (e.g., mayor/rig/.beads/redirect -> ../../mayor/rig/.beads)
		// This is the bug scenario from gt-csbjj
		workDir := filepath.Join(tmpDir, "mayor", "rig")
		beadsDir := filepath.Join(workDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create a circular redirect: ../../mayor/rig/.beads resolves back to .beads
		redirectPath := filepath.Join(beadsDir, "redirect")
		if err := os.WriteFile(redirectPath, []byte("../../mayor/rig/.beads\n"), 0644); err != nil {
			t.Fatal(err)
		}

		// ResolveBeadsDir should detect the circular redirect and return the original beadsDir
		got := ResolveBeadsDir(workDir)
		want := beadsDir
		if got != want {
			t.Errorf("ResolveBeadsDir() = %q, want %q (should ignore circular redirect)", got, want)
		}

		// The circular redirect file should have been removed
		if _, err := os.Stat(redirectPath); err == nil {
			t.Error("circular redirect file should have been removed, but it still exists")
		}
	})
}

// TestSetupRedirect tests the beads redirect setup for worktrees.
func TestSetupRedirect(t *testing.T) {
	t.Run("rig with own DB redirects to rig-level beads", func(t *testing.T) {
		// When rig has its own dolt_database in metadata.json, crew must
		// redirect to rig-level .beads (not town-level) to see correct prefix.
		townRoot := t.TempDir()
		townBeads := filepath.Join(townRoot, ".beads")
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		crewPath := filepath.Join(rigRoot, "crew", "max")

		// Create both town-level and rig-level beads
		if err := os.MkdirAll(filepath.Join(townBeads, "dolt"), 0755); err != nil {
			t.Fatalf("mkdir town beads: %v", err)
		}
		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		// Rig has its own database (e.g., laneassist with lc- prefix)
		meta := []byte(`{"dolt_database":"testrig","backend":"dolt"}`)
		if err := os.WriteFile(filepath.Join(rigBeads, "metadata.json"), meta, 0644); err != nil {
			t.Fatalf("write metadata: %v", err)
		}
		if err := os.MkdirAll(crewPath, 0755); err != nil {
			t.Fatalf("mkdir crew: %v", err)
		}

		if err := SetupRedirect(townRoot, crewPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		redirectPath := filepath.Join(crewPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		// 2 levels up to rig root: crew/max -> testrig, then .beads
		want := "../../.beads\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}

		// Verify redirect resolves to rig-level, NOT town-level
		resolved := ResolveBeadsDir(crewPath)
		if resolved != rigBeads {
			t.Errorf("resolved = %q, want %q (rig-level)", resolved, rigBeads)
		}
	})

	t.Run("rig without own DB redirects to town-level beads", func(t *testing.T) {
		// When rig has no own database, crew should use town-level .beads.
		townRoot := t.TempDir()
		townBeads := filepath.Join(townRoot, ".beads")
		rigRoot := filepath.Join(townRoot, "testrig")
		crewPath := filepath.Join(rigRoot, "crew", "max")

		// Create town-level beads with dolt DB
		if err := os.MkdirAll(filepath.Join(townBeads, "dolt"), 0755); err != nil {
			t.Fatalf("mkdir town beads: %v", err)
		}
		if err := os.MkdirAll(crewPath, 0755); err != nil {
			t.Fatalf("mkdir crew: %v", err)
		}

		if err := SetupRedirect(townRoot, crewPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		redirectPath := filepath.Join(crewPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		// 3 levels up: crew/max -> testrig -> townRoot, then .beads
		want := "../../../.beads\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}

		// Verify redirect resolves to town-level
		resolved := ResolveBeadsDir(crewPath)
		if resolved != townBeads {
			t.Errorf("resolved = %q, want %q", resolved, townBeads)
		}
	})

	t.Run("crew worktree falls back to rig-level beads", func(t *testing.T) {
		// When neither rig metadata nor town-level .beads exists, fall back to rig-level (2 levels up).
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		crewPath := filepath.Join(rigRoot, "crew", "max")

		// Create rig-level beads only (no town-level, no metadata.json)
		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		if err := os.MkdirAll(crewPath, 0755); err != nil {
			t.Fatalf("mkdir crew: %v", err)
		}

		// Run SetupRedirect
		if err := SetupRedirect(townRoot, crewPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		// Verify redirect was created
		redirectPath := filepath.Join(crewPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		want := "../../.beads\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}
	})

	t.Run("crew worktree with tracked beads", func(t *testing.T) {
		// Setup: town/rig/.beads/redirect -> mayor/rig/.beads (tracked)
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		mayorRigBeads := filepath.Join(rigRoot, "mayor", "rig", ".beads")
		crewPath := filepath.Join(rigRoot, "crew", "max")

		// Create rig structure with tracked beads
		if err := os.MkdirAll(mayorRigBeads, 0755); err != nil {
			t.Fatalf("mkdir mayor/rig beads: %v", err)
		}
		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		// Create rig-level redirect to mayor/rig/.beads
		if err := os.WriteFile(filepath.Join(rigBeads, "redirect"), []byte("mayor/rig/.beads\n"), 0644); err != nil {
			t.Fatalf("write rig redirect: %v", err)
		}
		if err := os.MkdirAll(crewPath, 0755); err != nil {
			t.Fatalf("mkdir crew: %v", err)
		}

		// Run SetupRedirect
		if err := SetupRedirect(townRoot, crewPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		// Verify redirect goes directly to mayor/rig/.beads (no chain - bd CLI doesn't support chains)
		redirectPath := filepath.Join(crewPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		want := "../../mayor/rig/.beads\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}

		// Verify redirect resolves correctly
		resolved := ResolveBeadsDir(crewPath)
		// crew/max -> ../../mayor/rig/.beads (direct, no chain)
		if resolved != mayorRigBeads {
			t.Errorf("resolved = %q, want %q", resolved, mayorRigBeads)
		}
	})

	t.Run("crew worktree with absolute rig redirect", func(t *testing.T) {
		// Setup: rig/.beads/redirect contains an absolute path
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		crewPath := filepath.Join(rigRoot, "crew", "max")

		// Create an absolute target beads directory (simulates a canonical .beads outside the town)
		absTarget := filepath.Join(t.TempDir(), "canonical", ".beads")
		if err := os.MkdirAll(absTarget, 0755); err != nil {
			t.Fatalf("mkdir abs target: %v", err)
		}

		// Create rig structure with absolute redirect
		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		if err := os.WriteFile(filepath.Join(rigBeads, "redirect"), []byte(absTarget+"\n"), 0644); err != nil {
			t.Fatalf("write rig redirect: %v", err)
		}
		if err := os.MkdirAll(crewPath, 0755); err != nil {
			t.Fatalf("mkdir crew: %v", err)
		}

		// Run SetupRedirect
		if err := SetupRedirect(townRoot, crewPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		// Verify redirect is the absolute path (not upPath + absolutePath)
		redirectPath := filepath.Join(crewPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		want := absTarget + "\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q (absolute path should be passed through)", string(content), want)
		}

		// Verify redirect resolves correctly
		resolved := ResolveBeadsDir(crewPath)
		if resolved != absTarget {
			t.Errorf("resolved = %q, want %q", resolved, absTarget)
		}
	})

	t.Run("polecat worktree", func(t *testing.T) {
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		polecatPath := filepath.Join(rigRoot, "polecats", "worker1")

		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		if err := os.MkdirAll(polecatPath, 0755); err != nil {
			t.Fatalf("mkdir polecat: %v", err)
		}

		if err := SetupRedirect(townRoot, polecatPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		redirectPath := filepath.Join(polecatPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		want := "../../.beads\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}
	})

	t.Run("refinery worktree", func(t *testing.T) {
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		refineryPath := filepath.Join(rigRoot, "refinery", "rig")

		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		if err := os.MkdirAll(refineryPath, 0755); err != nil {
			t.Fatalf("mkdir refinery: %v", err)
		}

		if err := SetupRedirect(townRoot, refineryPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		redirectPath := filepath.Join(refineryPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		want := "../../.beads\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}
	})

	t.Run("cleans runtime files but preserves tracked files", func(t *testing.T) {
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		crewPath := filepath.Join(rigRoot, "crew", "max")
		crewBeads := filepath.Join(crewPath, ".beads")

		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		// Simulate worktree with both runtime and tracked files
		if err := os.MkdirAll(crewBeads, 0755); err != nil {
			t.Fatalf("mkdir crew beads: %v", err)
		}
		// Runtime files (should be removed)
		if err := os.WriteFile(filepath.Join(crewBeads, "daemon.lock"), []byte("1234"), 0644); err != nil {
			t.Fatalf("write daemon.lock: %v", err)
		}
		if err := os.WriteFile(filepath.Join(crewBeads, "metadata.json"), []byte("{}"), 0644); err != nil {
			t.Fatalf("write metadata.json: %v", err)
		}
		// Tracked files (should be preserved)
		if err := os.WriteFile(filepath.Join(crewBeads, "config.yaml"), []byte("prefix: test"), 0644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		if err := os.WriteFile(filepath.Join(crewBeads, "README.md"), []byte("# Beads"), 0644); err != nil {
			t.Fatalf("write README: %v", err)
		}

		if err := SetupRedirect(townRoot, crewPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		// Verify runtime files were cleaned up
		if _, err := os.Stat(filepath.Join(crewBeads, "daemon.lock")); !os.IsNotExist(err) {
			t.Error("daemon.lock should have been removed")
		}
		if _, err := os.Stat(filepath.Join(crewBeads, "metadata.json")); !os.IsNotExist(err) {
			t.Error("metadata.json should have been removed")
		}

		// Verify tracked files were preserved
		if _, err := os.Stat(filepath.Join(crewBeads, "config.yaml")); err != nil {
			t.Errorf("config.yaml should have been preserved: %v", err)
		}
		if _, err := os.Stat(filepath.Join(crewBeads, "README.md")); err != nil {
			t.Errorf("README.md should have been preserved: %v", err)
		}

		// Verify redirect was created
		redirectPath := filepath.Join(crewBeads, "redirect")
		if _, err := os.Stat(redirectPath); err != nil {
			t.Errorf("redirect file should exist: %v", err)
		}
	})

	t.Run("rejects mayor/rig canonical location", func(t *testing.T) {
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		mayorRigPath := filepath.Join(rigRoot, "mayor", "rig")

		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		if err := os.MkdirAll(mayorRigPath, 0755); err != nil {
			t.Fatalf("mkdir mayor/rig: %v", err)
		}

		err := SetupRedirect(townRoot, mayorRigPath)
		if err == nil {
			t.Error("SetupRedirect should reject mayor/rig location")
		}
		if err != nil && !strings.Contains(err.Error(), "canonical") {
			t.Errorf("error should mention canonical location, got: %v", err)
		}
	})

	t.Run("rejects path too shallow", func(t *testing.T) {
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")

		if err := os.MkdirAll(rigRoot, 0755); err != nil {
			t.Fatalf("mkdir rig: %v", err)
		}

		err := SetupRedirect(townRoot, rigRoot)
		if err == nil {
			t.Error("SetupRedirect should reject rig root (too shallow)")
		}
	})

	t.Run("fails if rig beads missing", func(t *testing.T) {
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		crewPath := filepath.Join(rigRoot, "crew", "max")

		// No rig/.beads or mayor/rig/.beads created
		if err := os.MkdirAll(crewPath, 0755); err != nil {
			t.Fatalf("mkdir crew: %v", err)
		}

		err := SetupRedirect(townRoot, crewPath)
		if err == nil {
			t.Error("SetupRedirect should fail if rig .beads missing")
		}
	})

	t.Run("crew worktree with rig beads but no database", func(t *testing.T) {
		// Setup: rig/.beads exists (has metadata.json) but no actual database.
		// This is the dolt architecture where rig/.beads has metadata only and
		// the actual dolt DB lives at mayor/rig/.beads/dolt/.
		// The redirect should point to mayor/rig/.beads, not rig/.beads.
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		mayorRigBeads := filepath.Join(rigRoot, "mayor", "rig", ".beads")
		crewPath := filepath.Join(rigRoot, "crew", "max")

		// Create rig/.beads with metadata but NO database (no dolt/)
		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		if err := os.WriteFile(filepath.Join(rigBeads, "metadata.json"),
			[]byte(`{"database":"dolt","backend":"dolt","dolt_mode":"embedded"}`), 0644); err != nil {
			t.Fatalf("write metadata: %v", err)
		}
		// Create mayor/rig/.beads with dolt DB marker
		doltDir := filepath.Join(mayorRigBeads, "dolt")
		if err := os.MkdirAll(doltDir, 0755); err != nil {
			t.Fatalf("mkdir mayor dolt: %v", err)
		}
		if err := os.MkdirAll(crewPath, 0755); err != nil {
			t.Fatalf("mkdir crew: %v", err)
		}

		// Run SetupRedirect - should detect no DB at rig/.beads and fall back to mayor/rig/.beads
		if err := SetupRedirect(townRoot, crewPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		// Verify redirect points to mayor/rig/.beads (not rig/.beads)
		redirectPath := filepath.Join(crewPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		want := "../../mayor/rig/.beads\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}

		// Verify redirect resolves correctly
		resolved := ResolveBeadsDir(crewPath)
		if resolved != mayorRigBeads {
			t.Errorf("resolved = %q, want %q", resolved, mayorRigBeads)
		}
	})

	t.Run("crew worktree with mayor/rig beads only", func(t *testing.T) {
		// Setup: no rig/.beads, only mayor/rig/.beads exists
		// This is the tracked beads architecture where rig root has no .beads directory
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		mayorRigBeads := filepath.Join(rigRoot, "mayor", "rig", ".beads")
		crewPath := filepath.Join(rigRoot, "crew", "max")

		// Create only mayor/rig/.beads (no rig/.beads)
		if err := os.MkdirAll(mayorRigBeads, 0755); err != nil {
			t.Fatalf("mkdir mayor/rig beads: %v", err)
		}
		if err := os.MkdirAll(crewPath, 0755); err != nil {
			t.Fatalf("mkdir crew: %v", err)
		}

		// Run SetupRedirect - should succeed and point to mayor/rig/.beads
		if err := SetupRedirect(townRoot, crewPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		// Verify redirect points to mayor/rig/.beads
		redirectPath := filepath.Join(crewPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		want := "../../mayor/rig/.beads\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}

		// Verify redirect resolves correctly
		resolved := ResolveBeadsDir(crewPath)
		if resolved != mayorRigBeads {
			t.Errorf("resolved = %q, want %q", resolved, mayorRigBeads)
		}
	})

	t.Run("handles stale .beads file (not directory)", func(t *testing.T) {
		// Edge case: .beads exists as a file instead of directory
		// This can happen with unusual clone state or failed operations
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		crewPath := filepath.Join(rigRoot, "crew", "max")

		// Create rig structure
		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		if err := os.MkdirAll(crewPath, 0755); err != nil {
			t.Fatalf("mkdir crew: %v", err)
		}

		// Create .beads as a FILE (not directory) - simulating stale state
		staleBeadsFile := filepath.Join(crewPath, ".beads")
		if err := os.WriteFile(staleBeadsFile, []byte("stale content"), 0644); err != nil {
			t.Fatalf("write stale .beads file: %v", err)
		}

		// SetupRedirect should remove the file and create the directory
		if err := SetupRedirect(townRoot, crewPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		// Verify .beads is now a directory
		info, err := os.Stat(staleBeadsFile)
		if err != nil {
			t.Fatalf("stat .beads: %v", err)
		}
		if !info.IsDir() {
			t.Errorf(".beads should be a directory, but is a file")
		}

		// Verify redirect was created
		redirectPath := filepath.Join(crewPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		want := "../../.beads\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}
	})
}
