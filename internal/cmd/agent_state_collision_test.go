package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// writeCollisionBdStub installs a fake `bd` on PATH that exits 1 with the
// supplied stderr, mimicking bd's behavior when a bead ID is ambiguous across
// the issues and wisps tables. Restored automatically via t.Setenv cleanup.
func writeCollisionBdStub(t *testing.T, stderr string, exitCode int) {
	t.Helper()
	stubDir := t.TempDir()
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
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestGetAllAgentLabels_IDCollision verifies that when bd reports the agent
// identity bead ID exists in both the issues and wisps tables, getAllAgentLabels
// surfaces the shared beads.ErrIDCollision sentinel rather than a generic error.
// The await-signal/await-event state machine relies on this distinction to
// disable bead-state writes gracefully (one de-dup hint) instead of churning
// three confusing per-operation errors every patrol cycle. Regression test for
// gu-yjj79 (agent-bead sibling of the rig-bead fix gu-feg02).
func TestGetAllAgentLabels_IDCollision(t *testing.T) {
	// A beadsDir that does not exist short-circuits the town fallback probe so
	// the single stubbed bd invocation drives the result.
	beadsDir := filepath.Join(t.TempDir(), "nonexistent", ".beads")
	writeCollisionBdStub(t, `Error: id "hq-deacon" exists in both issues and wisps`, 1)

	_, err := getAllAgentLabels("hq-deacon", beadsDir)
	if err == nil {
		t.Fatal("expected an error for ID collision, got nil")
	}
	if !errors.Is(err, beads.ErrIDCollision) {
		t.Errorf("expected beads.ErrIDCollision, got %v (type %T)", err, err)
	}
}

// TestGetAllAgentLabels_NotFoundNotCollision guards the negative case: a plain
// "not found" must NOT be misclassified as an ID collision, since the two are
// handled differently by callers (start-at-idle-0 vs. disable-state-this-cycle).
func TestGetAllAgentLabels_NotFoundNotCollision(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), "nonexistent", ".beads")
	writeCollisionBdStub(t, `Error: no issue found matching "hq-deacon"`, 1)

	_, err := getAllAgentLabels("hq-deacon", beadsDir)
	if err == nil {
		t.Fatal("expected an error for not-found, got nil")
	}
	if errors.Is(err, beads.ErrIDCollision) {
		t.Errorf("not-found must NOT be classified as ErrIDCollision; got %v", err)
	}
}
