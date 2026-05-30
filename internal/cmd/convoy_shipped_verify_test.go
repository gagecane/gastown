// Tests for the convoy ship-verification gate (gu-j7u5).
//
// closeConvoyIfComplete must NOT auto-close a convoy whose tracked beads
// report status=closed but never actually shipped to origin/main. Pattern B
// (gu-rh0g, gu-treq) and Pattern C false-closes leave beads closed without a
// citing commit on main; the convoy was previously firing 🚚 Convoy landed
// for those beads. The gate consults labels first, attachment fields second,
// and (when reachable) the rig's git history third.

package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCloseConvoyIfComplete_AwaitingRefineryMergeBlocksAutoClose verifies that
// a tracked bead carrying the awaiting_refinery_merge label keeps the convoy
// open even though its status is "closed". The polecat marks the bead with
// this label after submitting an MR; the refinery's PostMerge path will close
// it again when the MR actually lands. Until then, convoy-complete must not
// fire.
func TestCloseConvoyIfComplete_AwaitingRefineryMergeBlocksAutoClose(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on /tmp paths")
	}

	townBeads := t.TempDir()
	tracked := []trackedIssueInfo{
		{ID: "gt-shipped", Status: "closed", Labels: []string{"awaiting_refinery_merge"}},
	}

	out, err := captureConvoyStdoutErr(t, func() error {
		ready, err := closeConvoyIfComplete(townBeads, "hq-cv-await", "Awaiting refinery", tracked, false)
		if ready {
			t.Fatalf("closeConvoyIfComplete reported ready while a tracked bead carries awaiting_refinery_merge")
		}
		return err
	})
	if err != nil {
		t.Fatalf("closeConvoyIfComplete: %v", err)
	}
	if !strings.Contains(out, "awaiting_refinery_merge") {
		t.Fatalf("expected diagnostic to mention awaiting_refinery_merge, got:\n%s", out)
	}
	if !strings.Contains(out, "gt-shipped") {
		t.Fatalf("expected diagnostic to mention bead ID, got:\n%s", out)
	}
}

// TestCloseConvoyIfComplete_StrandedMergeBlocksAutoClose verifies that a
// tracked bead carrying the stranded-merge label keeps the convoy open. Push
// or MR creation failed for that bead; the work is on a polecat branch but
// not on origin/main. Refinery recovery will revisit the bead later — the
// convoy must NOT fire complete in the meantime.
func TestCloseConvoyIfComplete_StrandedMergeBlocksAutoClose(t *testing.T) {
	townBeads := t.TempDir()
	tracked := []trackedIssueInfo{
		{ID: "gt-stranded", Status: "closed", Labels: []string{"stranded-merge"}},
	}

	out, err := captureConvoyStdoutErr(t, func() error {
		ready, err := closeConvoyIfComplete(townBeads, "hq-cv-stranded", "Stranded", tracked, false)
		if ready {
			t.Fatalf("closeConvoyIfComplete reported ready while a tracked bead carries stranded-merge")
		}
		return err
	})
	if err != nil {
		t.Fatalf("closeConvoyIfComplete: %v", err)
	}
	if !strings.Contains(out, "stranded-merge") {
		t.Fatalf("expected diagnostic to mention stranded-merge, got:\n%s", out)
	}
}

// TestCloseConvoyIfComplete_ReviewOnlyAcceptedAsShipped verifies that a
// review_only bead does not require a citing commit. Analysis-only legs
// (mol-prd-review, mol-plan-review, etc.) finish with zero commits by design
// and must not block convoy completion when their status is closed.
//
// To avoid invoking the git fallback (which would otherwise be reached when
// labels are absent and the description does NOT carry review_only/no_merge),
// we use a non-existent town path so resolveRigWorktreePath returns "" and
// the verifier fails open. But because review_only is set, the verifier
// short-circuits before that path entirely — and we assert no diagnostic
// surfaces.
func TestCloseConvoyIfComplete_ReviewOnlyAcceptedAsShipped(t *testing.T) {
	townBeads := t.TempDir()
	tracked := []trackedIssueInfo{
		{ID: "gt-review", Status: "closed", Description: "review_only: true\nattached_molecule: gt-wisp-x"},
	}

	var ready bool
	out, err := captureConvoyStdoutErr(t, func() error {
		// dryRun=true so we don't actually invoke `bd close` — we only care that
		// the verification gate accepts the bead.
		var innerErr error
		ready, innerErr = closeConvoyIfComplete(townBeads, "hq-cv-review", "Review", tracked, true)
		return innerErr
	})
	if err != nil {
		t.Fatalf("closeConvoyIfComplete: %v", err)
	}
	if !ready {
		t.Fatalf("review_only tracked bead must be accepted as shipped; convoy was rejected. out:\n%s", out)
	}
	if strings.Contains(out, "closed-but-unshipped") {
		t.Fatalf("review_only bead should not surface unshipped warning, got:\n%s", out)
	}
}

// TestCloseConvoyIfComplete_NoMergeAcceptedAsShipped — same contract for
// no_merge beads (email, research, ops tasks with no code commits).
func TestCloseConvoyIfComplete_NoMergeAcceptedAsShipped(t *testing.T) {
	townBeads := t.TempDir()
	tracked := []trackedIssueInfo{
		{ID: "gt-noop", Status: "closed", Description: "no_merge: true\ndispatched_by: mayor"},
	}

	var ready bool
	out, err := captureConvoyStdoutErr(t, func() error {
		var innerErr error
		ready, innerErr = closeConvoyIfComplete(townBeads, "hq-cv-noop", "No-op", tracked, true)
		return innerErr
	})
	if err != nil {
		t.Fatalf("closeConvoyIfComplete: %v", err)
	}
	if !ready {
		t.Fatalf("no_merge tracked bead must be accepted as shipped; convoy was rejected. out:\n%s", out)
	}
	if strings.Contains(out, "closed-but-unshipped") {
		t.Fatalf("no_merge bead should not surface unshipped warning, got:\n%s", out)
	}
}

// TestCloseConvoyIfComplete_FailsOpenWhenRigPathUnresolvable verifies the
// fail-open contract for citation lookups. When the bead's home rig cannot be
// resolved (no routes.jsonl, no rig worktree), we cannot prove non-shipping;
// blocking the convoy here would deadlock convoys that legitimately track
// beads in unrouted external rigs. Accept as shipped instead.
func TestCloseConvoyIfComplete_FailsOpenWhenRigPathUnresolvable(t *testing.T) {
	townBeads := t.TempDir()
	// No rig worktree, no mayor/rig — lookupCitingCommit must return
	// (verified=false) and evaluateTrackedBeadShipped must accept the bead.
	tracked := []trackedIssueInfo{
		{ID: "ws-foo", Status: "closed"},
	}

	var ready bool
	out, err := captureConvoyStdoutErr(t, func() error {
		var innerErr error
		ready, innerErr = closeConvoyIfComplete(townBeads, "hq-cv-extern", "External", tracked, true)
		return innerErr
	})
	if err != nil {
		t.Fatalf("closeConvoyIfComplete: %v", err)
	}
	if !ready {
		t.Fatalf("unresolvable rig path must fail open (accept as shipped), got rejection. out:\n%s", out)
	}
	if strings.Contains(out, "closed-but-unshipped") {
		t.Fatalf("unverifiable bead should fail open, got:\n%s", out)
	}
}

// TestCloseConvoyIfComplete_MultipleUnshippedSurfaceAllOfThem verifies that
// when several tracked beads fail the gate, the diagnostic enumerates each
// one — operators need to know which beads are problematic, not just that
// "some" are.
func TestCloseConvoyIfComplete_MultipleUnshippedSurfaceAllOfThem(t *testing.T) {
	townBeads := t.TempDir()
	tracked := []trackedIssueInfo{
		{ID: "gt-a", Status: "closed", Labels: []string{"awaiting_refinery_merge"}},
		{ID: "gt-b", Status: "closed", Labels: []string{"stranded-merge"}},
		{ID: "gt-c", Status: "closed", Labels: []string{"awaiting_refinery_merge"}},
	}

	out, err := captureConvoyStdoutErr(t, func() error {
		ready, err := closeConvoyIfComplete(townBeads, "hq-cv-multi", "Multi", tracked, false)
		if ready {
			t.Fatalf("convoy must not be ready when any bead is unshipped")
		}
		return err
	})
	if err != nil {
		t.Fatalf("closeConvoyIfComplete: %v", err)
	}
	for _, id := range []string{"gt-a", "gt-b", "gt-c"} {
		if !strings.Contains(out, id) {
			t.Errorf("expected diagnostic to mention %s, got:\n%s", id, out)
		}
	}
	if !strings.Contains(out, "3 closed-but-unshipped") {
		t.Errorf("expected diagnostic to count all 3 unshipped beads, got:\n%s", out)
	}
}

// TestEvaluateTrackedBeadShipped_ResolutionOrder pins down the cheapest-first
// ordering of the verification helper. Awaiting_refinery_merge must win even
// when review_only is also set (the polecat already submitted an MR), and a
// review_only bead with no labels must short-circuit before the git lookup.
func TestEvaluateTrackedBeadShipped_ResolutionOrder(t *testing.T) {
	townBeads := t.TempDir()

	cases := []struct {
		name    string
		tracked trackedIssueInfo
		want    string // empty = shipped, otherwise substring of reason
	}{
		{
			name:    "awaiting_refinery_merge wins over review_only",
			tracked: trackedIssueInfo{ID: "gt-x", Status: "closed", Labels: []string{"awaiting_refinery_merge"}, Description: "review_only: true"},
			want:    "awaiting_refinery_merge",
		},
		{
			name:    "stranded-merge wins over no_merge",
			tracked: trackedIssueInfo{ID: "gt-x", Status: "closed", Labels: []string{"stranded-merge"}, Description: "no_merge: true"},
			want:    "stranded-merge",
		},
		{
			name:    "review_only short-circuits before git lookup",
			tracked: trackedIssueInfo{ID: "gt-x", Status: "closed", Description: "review_only: true"},
			want:    "",
		},
		{
			name:    "no_merge short-circuits before git lookup",
			tracked: trackedIssueInfo{ID: "gt-x", Status: "closed", Description: "no_merge: true"},
			want:    "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := evaluateTrackedBeadShipped(townBeads, c.tracked)
			if c.want == "" {
				if got != "" {
					t.Fatalf("expected shipped (empty reason), got %q", got)
				}
			} else if !strings.Contains(got, c.want) {
				t.Fatalf("expected reason to contain %q, got %q", c.want, got)
			}
		})
	}
}

// TestResolveRigWorktreePath_MayorFallback verifies that an unrouted prefix
// falls back to the mayor's worktree when present. This keeps the citation
// lookup useful for hq-* convoys that track beads sharing the mayor's git
// repo (rare but possible).
func TestResolveRigWorktreePath_MayorFallback(t *testing.T) {
	townBeads := t.TempDir()
	// Create the mayor worktree directory so resolveRigWorktreePath finds it.
	mayorWT := filepath.Join(townBeads, "mayor", "rig")
	if err := os.MkdirAll(mayorWT, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	got := resolveRigWorktreePath(townBeads, "hq-some-bead")
	if got != mayorWT {
		t.Fatalf("expected mayor fallback %q, got %q", mayorWT, got)
	}
}

// TestResolveRigWorktreePath_ReturnsEmptyWhenNothingExists confirms the
// fail-open path: when neither the rig refinery worktree nor mayor's
// worktree exists, the resolver returns "" and the caller treats it as
// "unverifiable" rather than "unshipped".
func TestResolveRigWorktreePath_ReturnsEmptyWhenNothingExists(t *testing.T) {
	townBeads := t.TempDir()
	if got := resolveRigWorktreePath(townBeads, "ws-orphan"); got != "" {
		t.Fatalf("expected empty resolution, got %q", got)
	}
}
