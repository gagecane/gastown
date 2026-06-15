package refinery

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestHandleMRInfoSuccess_SourceCloseFails_PreservesActiveMRAndBranch is the
// regression test for gu-xner6: when the source-issue close fails (the bead is
// still non-terminal afterward, not merely "already closed"), the refinery must
// NOT proceed to the irreversible cleanup — it must leave the agent bead's
// active_mr intact and preserve the branch.
//
// Why this matters: the reaper's ReconcileMergedOrphans (gu-7igu8) keys on a
// non-empty active_mr pointing at a PROVEN-MERGED MR to finish closing a source
// the refinery left non-terminal. Clearing active_mr on a swallowed close
// failure blinds that recovery path and strands the source bead HOOKED with a
// leaked awaiting_refinery_merge label. The MR bead is left closed-as-merged
// (that is the signature the reaper proves the work landed on), so keeping
// active_mr lets the reaper complete the reconcile on its next cycle.
func TestHandleMRInfoSuccess_SourceCloseFails_PreservesActiveMRAndBranch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	workDir, g, cleanup := testGitRepo(t)
	defer cleanup()

	// A polecat branch that must survive when the source close fails.
	const branch = "polecat/fury/gu-xner6-test"
	createFeatureBranch(t, workDir, branch, "feature.txt", "work\n")

	// bd stub: the source-issue close FAILS, and the source bead remains
	// non-terminal (status=open) on the follow-up Show — i.e. a genuine close
	// failure, not an "already closed by gt done" race. Any agent-bead `update`
	// (the active_mr clear) is recorded so we can assert it never ran. List/SQL
	// reads return empty so the conflict-artifact sweep is a no-op.
	binDir := t.TempDir()
	activeMRMarker := filepath.Join(binDir, "active_mr_cleared")
	bdPath := filepath.Join(binDir, "bd")
	script := `#!/bin/sh
case "$*" in
  *"update "*)
    # active_mr clear (or any update) must NOT happen on a source-close failure.
    echo cleared > "` + activeMRMarker + `"
    exit 0
    ;;
  *"close "*)
    # Source-issue terminal close fails.
    echo "close refused" >&2
    exit 1
    ;;
  *"show "*)
    # Source bead is still OPEN (non-terminal) after the failed close.
    echo '[{"id":"gu-xner6-src","status":"open","description":""}]'
    exit 0
    ;;
  *)
    # version probe, list, sql, etc. — benign empty/success.
    echo '[]'
    exit 0
    ;;
esac
`
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	e := newTestEngineer(t, workDir, g)
	e.output = &bytes.Buffer{}
	// Close-time landing guard is exercised by a separate test; not the subject here.
	e.verifyMergeLanded = nil

	mr := &MRInfo{
		// Empty ID skips the MR-bead update/close block, isolating the
		// source-close-failure → cleanup-skip behavior under test.
		ID:          "",
		Branch:      branch,
		Target:      "main",
		SourceIssue: "gu-xner6-src",
		AgentBead:   "agent-fury",
		Worker:      "polecats/fury",
	}

	e.HandleMRInfoSuccess(mr, ProcessResult{Success: true, MergeCommit: "deadbeef"})

	out := e.output.(*bytes.Buffer).String()

	// The new guard marker must be present.
	if !strings.Contains(out, "preserving active_mr + branch for reaper reconcile") {
		t.Errorf("expected reconcile-preservation marker on source-close failure, got: %s", out)
	}

	// No irreversible cleanup may run — these all live AFTER the guard.
	for _, marker := range []string{
		"Deleted local branch",
		"Deleted remote branch",
		"✓ Merged",
	} {
		if strings.Contains(out, marker) {
			t.Errorf("post-merge cleanup ran despite source-close failure (saw %q): %s", marker, out)
		}
	}

	// active_mr must NOT have been cleared (the reaper's only recovery handle).
	if _, err := os.Stat(activeMRMarker); err == nil {
		t.Error("active_mr was cleared on source-close failure — blinds reaper recovery")
	}

	// The branch must still exist (work is not orphaned off a deleted ref).
	if exists, err := g.BranchExists(branch); err != nil {
		t.Fatalf("BranchExists(%q) error: %v", branch, err)
	} else if !exists {
		t.Errorf("branch %q was deleted despite source-close failure", branch)
	}
}
