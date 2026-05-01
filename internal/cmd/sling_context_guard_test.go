package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestExecuteSling_SlingContextBead verifies that executeSling rejects beads
// carrying the gt:sling-context label (gu-6dx7, defense-in-depth for gu-hfr3).
// Sling-context beads are scheduler bookkeeping, never work. Without this
// guard, direct `gt sling <wrapper-id> <rig>`, deacon redispatch, and batch
// sling could hand a polecat a wrapper to hook — the "work" would be the
// bookkeeping bead itself, which cannot be completed.
func TestExecuteSling_SlingContextBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	// Open status + gt:sling-context label → wrapper, not work.
	bdScript := `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"sling-context: Fix parser NPE","status":"open","assignee":"","description":"{\"version\":1,\"work_bead_id\":\"gu-abc\",\"target_rig\":\"testrig\"}","labels":["gt:sling-context"]}]'
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	params := SlingParams{
		BeadID:   "test-wrapper1",
		RigName:  "testrig",
		TownRoot: townRoot,
	}

	result, err := executeSling(params)
	if err == nil {
		t.Fatal("expected error when slinging sling-context wrapper, got nil")
	}
	if result.ErrMsg != "sling-context wrapper" {
		t.Errorf("expected ErrMsg='sling-context wrapper', got %q", result.ErrMsg)
	}
	if !strings.Contains(err.Error(), "sling-context wrapper") {
		t.Errorf("error should mention sling-context wrapper: %v", err)
	}
}

// TestExecuteSling_SlingContextBead_ForceDoesNotBypass verifies that --force
// does NOT bypass the sling-context guard. The guard protects against data
// shape errors (a wrapper ID reaching dispatch) — --force cannot fix that.
func TestExecuteSling_SlingContextBead_ForceDoesNotBypass(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"sling-context: Fix parser NPE","status":"open","assignee":"","description":"{\"version\":1,\"work_bead_id\":\"gu-abc\"}","labels":["gt:sling-context"]}]'
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	params := SlingParams{
		BeadID:   "test-wrapper2",
		RigName:  "testrig",
		TownRoot: townRoot,
		Force:    true, // --force should NOT bypass the wrapper guard
	}

	_, err := executeSling(params)
	if err == nil {
		t.Fatal("expected error when slinging sling-context wrapper with --force, got nil")
	}
	if !strings.Contains(err.Error(), "sling-context wrapper") {
		t.Errorf("--force should not bypass sling-context guard: %v", err)
	}
}

// TestExecuteSling_RealTaskPassesSlingContextGuard is the negative case: a
// real task bead (no gt:sling-context label) must not be mis-classified as a
// wrapper. Reaching a later failure in the dispatch pipeline (rig missing)
// proves the guard let the bead through.
func TestExecuteSling_RealTaskPassesSlingContextGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	// Real task: no gt:sling-context label. Other labels are fine.
	bdScript := `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"Fix parser NPE","status":"open","assignee":"","description":"","labels":["priority-high","gt:task"]}]'
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	params := SlingParams{
		BeadID:   "test-real-task",
		RigName:  "nonexistent-rig",
		TownRoot: townRoot,
	}

	_, err := executeSling(params)
	if err == nil {
		// Full success is not expected in this minimal harness. The test
		// passes as long as the sling-context guard does not trip.
		return
	}
	if strings.Contains(err.Error(), "sling-context wrapper") {
		t.Errorf("real task bead should pass sling-context guard, got: %v", err)
	}
}
