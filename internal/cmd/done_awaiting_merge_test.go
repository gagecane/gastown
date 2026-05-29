package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestDoneAwaitingRefineryMerge_DoesNotCloseHookedBead reproduces the
// gu-treq Pattern B refinery-stall variant. When a polecat on a merge-queue
// rig successfully submits an MR but the refinery has not yet merged it to
// origin/main, updateAgentStateOnDone must:
//   - NOT issue `bd close` against the hooked bead
//   - DO add the `awaiting_refinery_merge` label so the bead is auditable
//   - DO add an audit note recording the MR id and source branch
//
// Before the fix, gt done called forceCloseWithRetry on the hooked bead
// immediately after MR creation. If the refinery later wedged (gu-xn2z),
// the bead was reported "completed" while origin/main did NOT have the
// work, putting the polecat branch at risk of reaping. (gu-treq)
func TestDoneAwaitingRefineryMerge_DoesNotCloseHookedBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}
	withFastCloseBackoff(t)

	townRoot, closesLog := setupCloseHookedBeadTestEnv(t, stubBdRecordingCloseAndLabel())

	// awaitingRefineryMerge=true: simulates a successful MR submission on a
	// merge-queue rig where the refinery has not yet merged.
	updateAgentStateOnDone(
		filepath.Join(townRoot, "gastown"),
		townRoot,
		ExitCompleted,
		"gt-base-123",
		false,                  // not stranded
		true,                   // awaiting refinery merge
		"gt-mr-abc",            // mr id
		"polecat/nitro/abc123", // branch
	)

	logBytes, err := os.ReadFile(closesLog)
	log := ""
	if err == nil {
		log = string(logBytes)
	}

	// Must NOT have issued `bd close gt-base-123` — that's refinery's job
	// once the MR actually merges.
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, "close gt-base-123") {
			t.Errorf("awaiting-merge incorrectly closed hooked bead — gu-treq regression. log:\n%s", log)
		}
	}

	// Must have added the awaiting_refinery_merge label.
	if !strings.Contains(log, "label gt-base-123 awaiting_refinery_merge") {
		t.Errorf("expected `awaiting_refinery_merge` label on hooked bead, log:\n%s", log)
	}

	// Must have left an audit note (records mr_id / source_branch for refinery
	// recovery).
	if !strings.Contains(log, "note gt-base-123") {
		t.Errorf("expected audit note on hooked bead, log:\n%s", log)
	}
}

// TestDoneAwaitingRefineryMerge_FalseDoesNotChangeBehavior verifies the
// fix is gated correctly: when awaitingRefineryMerge=false (e.g. a non-MQ
// rig, or merge-strategy=direct), the hooked-bead close still happens. A
// regression in the guard would silently break every successful gt done on
// non-merge-queue rigs. (gu-treq)
func TestDoneAwaitingRefineryMerge_FalseDoesNotChangeBehavior(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}
	withFastCloseBackoff(t)

	townRoot, closesLog := setupCloseHookedBeadTestEnv(t, stubBdRecordingCloseAndLabel())

	// awaitingRefineryMerge=false: normal happy-path completion (non-MQ rig
	// or no MR), should still close the bead.
	updateAgentStateOnDone(
		filepath.Join(townRoot, "gastown"),
		townRoot,
		ExitCompleted,
		"gt-base-123",
		false, false, "", "",
	)

	logBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("read closes log: %v", err)
	}
	log := string(logBytes)

	if !strings.Contains(log, "close gt-base-123") {
		t.Errorf("non-awaiting ExitCompleted should still close hooked bead, log:\n%s", log)
	}
	if strings.Contains(log, "label gt-base-123 awaiting_refinery_merge") {
		t.Errorf("non-awaiting path must NOT add awaiting_refinery_merge label, log:\n%s", log)
	}
}

// TestDoneAwaitingRefineryMerge_StrandedTakesPrecedence verifies that when
// both stranded=true and awaitingRefineryMerge=true are set (impossible in
// practice but worth pinning), the stranded path wins — push/MR failure is
// the more severe condition and stranded-merge labeling preserves recovery
// instructions for the polecat branch.
func TestDoneAwaitingRefineryMerge_StrandedTakesPrecedence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}
	withFastCloseBackoff(t)

	townRoot, closesLog := setupCloseHookedBeadTestEnv(t, stubBdRecordingCloseAndLabel())

	updateAgentStateOnDone(
		filepath.Join(townRoot, "gastown"),
		townRoot,
		ExitCompleted,
		"gt-base-123",
		true, true, "gt-mr-abc", "polecat/nitro/abc123",
	)

	logBytes, _ := os.ReadFile(closesLog)
	log := string(logBytes)

	// Stranded path adds `stranded-merge`, NOT `awaiting_refinery_merge`.
	if !strings.Contains(log, "label gt-base-123 stranded-merge") {
		t.Errorf("stranded should win over awaiting-merge — expected stranded-merge label, log:\n%s", log)
	}
	if strings.Contains(log, "label gt-base-123 awaiting_refinery_merge") {
		t.Errorf("stranded path should NOT add awaiting_refinery_merge label, log:\n%s", log)
	}
	// And the bead must NOT be closed under either guard.
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, "close gt-base-123") {
			t.Errorf("stranded path closed hooked bead — gu-rh0g regression, log:\n%s", log)
		}
	}
}
