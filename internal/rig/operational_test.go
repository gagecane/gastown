package rig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// writeStubBd installs a shell script on PATH that stands in for the real
// `bd` binary. The script exits 1 with the supplied stderr, mimicking the
// canonical "issue not found" behavior that bd produces when a bead ID is
// queried and no such bead exists in the database. Test helper — restores
// PATH via t.Setenv's automatic cleanup.
func writeStubBd(t *testing.T, stderr string, exitCode int) {
	t.Helper()
	stubDir := t.TempDir()
	// Use single quotes around the heredoc delimiter to avoid $ expansion.
	script := fmt.Sprintf(`#!/bin/sh
cat >&2 <<'EOT'
%s
EOT
exit %d
`, stderr, exitCode)
	stubPath := filepath.Join(stubDir, "bd")
	if err := os.WriteFile(stubPath, []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+origPath)
}

// setupRig creates a minimal rig layout under tmpDir:
//   - <tmpDir>/<rigName>/ with a config.json supplying the beads prefix
//   - <tmpDir>/<rigName>/mayor/rig/ so checkParkedOrDocked picks the
//     mayor-scoped beads path
//   - <tmpDir>/mayor/rigs.json registering the rig
//
// Returns the rigPath.
func setupRig(t *testing.T, tmpDir, rigName, prefix string) string {
	t.Helper()
	rigPath := filepath.Join(tmpDir, rigName)
	if err := os.MkdirAll(filepath.Join(rigPath, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}
	configPath := filepath.Join(rigPath, "config.json")
	configJSON := fmt.Sprintf(`{"beads":{"prefix":%q}}`, prefix)
	if err := os.WriteFile(configPath, []byte(configJSON), 0644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	rigsJSON := fmt.Sprintf(`{"version":1,"rigs":{%q:{"beads":{"prefix":%q}}}}`, rigName, prefix)
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatalf("write rigs.json: %v", err)
	}
	return rigPath
}

// TestIsRigParkedOrDockedE_MissingRigBead verifies that when bd reports the
// rig identity bead does not exist, IsRigParkedOrDockedE returns the
// distinguishable ErrRigBeadNotFound sentinel rather than an opaque error.
// Callers (daemon.isRigOperational) rely on this distinction to implement
// log-once-for-missing-bead behavior without conflating persistent misses
// with transient Dolt outages. Regression test for gu-resv.
func TestIsRigParkedOrDockedE_MissingRigBead(t *testing.T) {
	tmpDir := t.TempDir()
	setupRig(t, tmpDir, "nobeadrig", "nb")

	// bd stub: exits non-zero with "no issue found matching" on stderr,
	// matching the real bd output that beads.wrapError translates to
	// ErrNotFound.
	writeStubBd(t, `Error: no issue found matching "nb-rig-nobeadrig"`, 1)

	blocked, reason, err := IsRigParkedOrDockedE(tmpDir, "nobeadrig")
	if err == nil {
		t.Fatal("expected an error for missing rig bead, got nil")
	}
	if !errors.Is(err, ErrRigBeadNotFound) {
		t.Errorf("expected ErrRigBeadNotFound, got %v (type %T)", err, err)
	}
	if blocked {
		t.Errorf("expected blocked=false for missing bead, got true")
	}
	if reason != "" {
		t.Errorf("expected empty reason for missing bead, got %q", reason)
	}
}

// TestIsRigParkedOrDockedE_OtherError verifies that errors other than
// ErrNotFound (e.g., Dolt connection failure, database missing) propagate
// through WITHOUT being wrapped as ErrRigBeadNotFound. The daemon uses this
// to keep its fail-safe behavior for transient failures while relaxing it
// only for the "bead genuinely does not exist" case.
func TestIsRigParkedOrDockedE_OtherError(t *testing.T) {
	tmpDir := t.TempDir()
	setupRig(t, tmpDir, "doltdownrig", "dd")

	// bd stub: Dolt-connection-style failure. Must NOT produce
	// ErrRigBeadNotFound (no "not found" phrase in stderr).
	writeStubBd(t, `Error: [mysql] read tcp 127.0.0.1:3307: connection refused`, 1)

	_, _, err := IsRigParkedOrDockedE(tmpDir, "doltdownrig")
	if err == nil {
		t.Fatal("expected an error for Dolt connection failure, got nil")
	}
	if errors.Is(err, ErrRigBeadNotFound) {
		t.Errorf("Dolt connection error must NOT be wrapped as ErrRigBeadNotFound — daemon relies on this distinction; got %v", err)
	}
}

// TestIsRigParkedOrDockedE_PrefixMissing verifies that when the rig prefix
// cannot be resolved (neither rigs.json nor the rig's config.json supplies
// one), a distinct error is returned so daemon fail-safe logic can
// differentiate it from a missing identity bead.
func TestIsRigParkedOrDockedE_PrefixMissing(t *testing.T) {
	tmpDir := t.TempDir()

	// Rig dir exists but no config.json and not registered in rigs.json.
	rigName := "nopfxrig"
	rigPath := filepath.Join(tmpDir, rigName)
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatal(err)
	}

	blocked, reason, err := IsRigParkedOrDockedE(tmpDir, rigName)
	if err == nil {
		t.Fatal("expected an error when prefix cannot be resolved, got nil")
	}
	if errors.Is(err, ErrRigBeadNotFound) {
		t.Errorf("prefix-missing error should NOT satisfy ErrRigBeadNotFound (callers treat these differently)")
	}
	if blocked {
		t.Errorf("expected blocked=false, got true")
	}
	if reason != "" {
		t.Errorf("expected empty reason, got %q", reason)
	}
}

// TestIsRigParkedOrDocked_MissingRigBead_SilentForDispatch verifies that the
// error-swallowing variant (used by dispatch paths) continues to report
// "not blocked" for a missing rig bead — no regression from the
// ErrRigBeadNotFound plumbing.
func TestIsRigParkedOrDocked_MissingRigBead_SilentForDispatch(t *testing.T) {
	tmpDir := t.TempDir()
	setupRig(t, tmpDir, "dispatchrig", "dp")
	writeStubBd(t, `Error: no issue found matching "dp-rig-dispatchrig"`, 1)

	blocked, reason := IsRigParkedOrDocked(tmpDir, "dispatchrig")
	if blocked {
		t.Errorf("dispatch-path variant must not block when rig bead is missing, got blocked=true (reason=%q)", reason)
	}
}

// TestErrRigBeadNotFound_Identity documents that ErrRigBeadNotFound is a
// stable, exported sentinel callers can match with errors.Is. This guards
// against accidental wrapping changes that would break daemon callers.
func TestErrRigBeadNotFound_Identity(t *testing.T) {
	if ErrRigBeadNotFound == nil {
		t.Fatal("ErrRigBeadNotFound must be a non-nil sentinel")
	}
	wrapped := errors.Join(errors.New("outer"), ErrRigBeadNotFound)
	if !errors.Is(wrapped, ErrRigBeadNotFound) {
		t.Error("ErrRigBeadNotFound must be discoverable via errors.Is even when wrapped")
	}
	// Confirm it is NOT the same as beads.ErrNotFound — callers that want
	// to handle both must check each explicitly. Keeping them distinct
	// avoids leaking beads-layer semantics into rig-layer callers.
	if errors.Is(beads.ErrNotFound, ErrRigBeadNotFound) {
		t.Error("ErrRigBeadNotFound must be distinct from beads.ErrNotFound")
	}
}
