package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestExecuteSling_IdentityBead_Label verifies that executeSling rejects beads
// carrying the gt:agent label. This is the primary ghost-dispatch vector that
// gu-ypjm addressed for convoy feeding and gu-3znx extends to sling dispatch.
func TestExecuteSling_IdentityBead_Label(t *testing.T) {
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
	// Open status + gt:agent label → identity bead.
	bdScript := `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"gu-gastown-polecat-toast","status":"open","assignee":"","description":"","labels":["gt:agent"]}]'
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	params := SlingParams{
		BeadID:   "test-identity1",
		RigName:  "testrig",
		TownRoot: townRoot,
	}

	result, err := executeSling(params)
	if err == nil {
		t.Fatal("expected error when slinging identity bead (label), got nil")
	}
	if result.ErrMsg != "identity bead" {
		t.Errorf("expected ErrMsg='identity bead', got %q", result.ErrMsg)
	}
	if !strings.Contains(err.Error(), "identity/system bead") {
		t.Errorf("error should mention identity bead: %v", err)
	}
}

// TestExecuteSling_IdentityBead_TitleRegex verifies that executeSling rejects
// beads with identity-style titles even when they lack the gt:agent label.
// This covers the case where gt doctor --fix re-creates an identity bead with
// status=open and no labels (see gu-ypjm for the doctor fragility note).
func TestExecuteSling_IdentityBead_TitleRegex(t *testing.T) {
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
	// Identity title, open status, no labels — the exact shape of the beads
	// that thrashed on cadk-casc_cdk-refinery and ta-talontriage-witness.
	bdScript := `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"cadk-casc_cdk-refinery","status":"open","assignee":"","description":"","labels":[]}]'
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	params := SlingParams{
		BeadID:   "test-identity2",
		RigName:  "testrig",
		TownRoot: townRoot,
	}

	result, err := executeSling(params)
	if err == nil {
		t.Fatal("expected error when slinging identity bead (title regex), got nil")
	}
	if result.ErrMsg != "identity bead" {
		t.Errorf("expected ErrMsg='identity bead', got %q", result.ErrMsg)
	}
	if !strings.Contains(err.Error(), "identity/system bead") {
		t.Errorf("error should mention identity bead: %v", err)
	}
}

// TestExecuteSling_IdentityBead_ForceDoesNotBypass verifies that --force does
// NOT bypass the identity guard. The guard exists to prevent loop thrash where
// a polecat hooks an identity bead and submits a stale auto-save branch.
func TestExecuteSling_IdentityBead_ForceDoesNotBypass(t *testing.T) {
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
    echo '[{"title":"ta-talontriage-polecat-nux","status":"open","assignee":"","description":"","labels":[]}]'
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	params := SlingParams{
		BeadID:   "test-identity3",
		RigName:  "testrig",
		TownRoot: townRoot,
		Force:    true, // --force should NOT bypass identity guard
	}

	_, err := executeSling(params)
	if err == nil {
		t.Fatal("expected error when slinging identity bead with --force, got nil")
	}
	if !strings.Contains(err.Error(), "identity/system bead") {
		t.Errorf("--force should not bypass identity guard: %v", err)
	}
}

// TestExecuteSling_IdentityBead_RigLabel verifies that executeSling rejects
// rig identity beads (e.g. gs-rig-gastown). These have:
//   - id pattern: <prefix>-rig-<name>
//   - title: just the rig name (e.g. "gastown") — does NOT match the identity
//     title regex
//   - issue_type: "rig"
//   - labels: gt:rig
//
// Prior to gs-2j6, the auto-dispatch plugin and gt sling missed them because
// none of the existing filters caught the rig-bead shape. Auto-dispatch slung
// them every 3 seconds, producing zombie convoys at 0/1 progress for hours.
func TestExecuteSling_IdentityBead_RigLabel(t *testing.T) {
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
    echo '[{"title":"gastown","status":"open","assignee":"","description":"","labels":["gt:rig"],"issue_type":"rig"}]'
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	params := SlingParams{
		BeadID:   "gs-rig-gastown",
		RigName:  "testrig",
		TownRoot: townRoot,
	}

	_, err := executeSling(params)
	if err == nil {
		t.Fatal("expected error when slinging rig identity bead, got nil")
	}
	if !strings.Contains(err.Error(), "identity/system bead") {
		t.Errorf("error should mention identity bead: %v", err)
	}
}

// TestExecuteSling_RealTaskDispatches verifies the negative case: a real task
// bead (open status, no gt:agent label, plain title) is NOT mistakenly
// classified as an identity bead. We stop checking the outcome once dispatch
// passes the guard and moves on to the rig-parked check (which fails in this
// minimal setup because we don't set up a full rig) — reaching that error
// proves the identity filter let the bead through.
func TestExecuteSling_RealTaskDispatches(t *testing.T) {
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
    echo '[{"title":"Fix parser NPE","status":"open","assignee":"","description":"","labels":["priority-high"]}]'
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
		// We did not expect full success in this minimal test harness; the
		// test passes either way as long as the identity guard does not trip.
		return
	}
	if strings.Contains(err.Error(), "identity/system bead") {
		t.Errorf("real task bead should pass identity guard, got: %v", err)
	}
}
