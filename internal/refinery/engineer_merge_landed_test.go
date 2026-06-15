package refinery

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// fakeMergeVerifyOps is a test double for gitMergeVerifyOps. It composes the
// ancestry surface (FetchBranch/IsAncestor) with merged-PR base resolution so
// the shared verifyMergeLandedOnTarget helper can be exercised without a repo.
type fakeMergeVerifyOps struct {
	isAncestor  bool
	ancestorErr error
	fetchErr    error
	prBaseRef   string
	prBaseErr   error

	ancestorArg   string
	descendantArg string
}

func (f *fakeMergeVerifyOps) FetchBranch(remote, branch string) error { return f.fetchErr }

func (f *fakeMergeVerifyOps) IsAncestor(ancestor, descendant string) (bool, error) {
	f.ancestorArg = ancestor
	f.descendantArg = descendant
	return f.isAncestor, f.ancestorErr
}

func (f *fakeMergeVerifyOps) FindMergedPRBaseRef(branch string) (string, error) {
	return f.prBaseRef, f.prBaseErr
}

// TestVerifyMergeLandedOnTarget pins the shared helper used by BOTH reconcile
// entry points (Manager.PostMerge and Engineer.HandleMRInfoSuccess), so they
// cannot drift apart on what "the merge landed" means (gu-mpgy8 / gu-ilf86).
func TestVerifyMergeLandedOnTarget(t *testing.T) {
	t.Run("landed on resolved target → nil", func(t *testing.T) {
		f := &fakeMergeVerifyOps{isAncestor: true}
		if err := verifyMergeLandedOnTarget(f, "abc123", "main", "polecat/x", "main"); err != nil {
			t.Fatalf("verifyMergeLandedOnTarget() = %v, want nil", err)
		}
		if f.ancestorArg != "abc123" || f.descendantArg != "origin/main" {
			t.Errorf("IsAncestor(%q,%q), want (abc123, origin/main)", f.ancestorArg, f.descendantArg)
		}
	})

	t.Run("not an ancestor → error (silent merge-loss)", func(t *testing.T) {
		f := &fakeMergeVerifyOps{isAncestor: false}
		err := verifyMergeLandedOnTarget(f, "abc123", "main", "polecat/x", "main")
		if err == nil {
			t.Fatal("verifyMergeLandedOnTarget() = nil, want error for unlanded commit")
		}
		if !strings.Contains(err.Error(), "did not land") {
			t.Errorf("error = %v, want mention of 'did not land'", err)
		}
	})

	t.Run("empty merge commit → error (fail closed)", func(t *testing.T) {
		f := &fakeMergeVerifyOps{isAncestor: true}
		if err := verifyMergeLandedOnTarget(f, "", "main", "polecat/x", "main"); err == nil {
			t.Fatal("verifyMergeLandedOnTarget() = nil, want error for empty merge_commit")
		}
	})

	t.Run("prefers merged-PR base ref over MR target (hq-fq1on)", func(t *testing.T) {
		// The MR records "main" but the PR actually merged into an integration
		// branch; the guard must verify ancestry against the PR base, not main.
		f := &fakeMergeVerifyOps{isAncestor: true, prBaseRef: "epic/batch-42"}
		if err := verifyMergeLandedOnTarget(f, "abc123", "main", "polecat/x", "main"); err != nil {
			t.Fatalf("verifyMergeLandedOnTarget() = %v, want nil", err)
		}
		if f.descendantArg != "origin/epic/batch-42" {
			t.Errorf("verified against %q, want origin/epic/batch-42", f.descendantArg)
		}
	})
}

// TestHandleMRInfoSuccess_UnverifiedMerge_SkipsCleanup is the regression test
// for gu-mpgy8: the primary automated merge path must NOT close beads or delete
// the branch when the close-time ancestry re-check fails. Before the fix this
// path trusted result.Success alone, so a merge that reported success without
// the commit actually landing on origin/<target> would close the source bead
// and nuke the branch — stranding the work off mainline (silent merge-loss).
func TestHandleMRInfoSuccess_UnverifiedMerge_SkipsCleanup(t *testing.T) {
	workDir, g, cleanup := testGitRepo(t)
	defer cleanup()

	e := newTestEngineer(t, workDir, g)
	e.output = &bytes.Buffer{}

	// Force the close-time guard to fail, as it would when result.Success is
	// true but the commit never reached origin/<target>.
	e.verifyMergeLanded = func(*MRInfo, string) error {
		return errors.New("merge commit deadbeef is not on origin/main")
	}

	mr := &MRInfo{
		ID:          "gt-mr-mpgy8",
		Branch:      "polecat/chrome/gu-mpgy8-test",
		Target:      "main",
		SourceIssue: "gu-mpgy8",
		Worker:      "polecats/chrome",
	}

	e.HandleMRInfoSuccess(mr, ProcessResult{Success: true, MergeCommit: "deadbeef"})

	out := e.output.(*bytes.Buffer).String()

	// The refusal marker must be present.
	if !strings.Contains(out, "Refusing post-merge cleanup") {
		t.Errorf("expected refusal marker on unverified merge, got: %s", out)
	}

	// No cleanup side effects may run — these all live AFTER the guard.
	for _, marker := range []string{
		"Closed MR bead",
		"Closed source issue",
		"Deleted local branch",
		"Deleted remote branch",
		"✓ Merged",
	} {
		if strings.Contains(out, marker) {
			t.Errorf("post-merge cleanup ran despite failed verification (saw %q): %s", marker, out)
		}
	}
}
