package cmd

// Tests for gs-9sr: the main-view MR verify (gs-onu defense-in-depth).
//
// After gt done / gt mq submit create the MR bead and pass the local
// bd.Show(mrID) read-back, verifyMRVisibleOnMain re-runs the refinery's own
// discovery (FindMRForBranchAndSHA) through a FRESH bd connection. The local
// read-back lies under an auto-commit config drift — it sees the session's
// uncommitted working set — so this fresh-connection check is what converts a
// silent strand into a loud, recoverable one.
//
// The three outcomes the caller branches on:
//   (true, nil)  — discoverable on main → report COMPLETED
//   (false, nil) — absent → fail loud (stranded wisp)
//   (false, err) — query failed → warn, don't false-strand

import (
	"errors"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// fakeMainViewFinder is an injectable mrMainViewFinder for testing the verdict logic
// without a real bd binary or Dolt server.
type fakeMainViewFinder struct {
	issue *beads.Issue
	err   error

	gotBranch string
	gotSHA    string
}

func (f *fakeMainViewFinder) FindMRForBranchAndSHA(branch, commitSHA string) (*beads.Issue, error) {
	f.gotBranch = branch
	f.gotSHA = commitSHA
	return f.issue, f.err
}

// TestVerifyMRVisibleOnMain_Found verifies the happy path: the fresh query finds
// the MR on shared main, so the caller may report COMPLETED.
func TestVerifyMRVisibleOnMain_Found(t *testing.T) {
	f := &fakeMainViewFinder{issue: &beads.Issue{ID: "gs-mr1"}}
	visible, err := verifyMRVisibleOnMain(f, "polecat/furiosa/gs-9sr", "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !visible {
		t.Fatalf("expected visible=true when the fresh query returns an MR")
	}
	// The refinery's discovery key is branch+SHA — make sure we forward both.
	if f.gotBranch != "polecat/furiosa/gs-9sr" || f.gotSHA != "abc123" {
		t.Fatalf("query args not forwarded: branch=%q sha=%q", f.gotBranch, f.gotSHA)
	}
}

// TestVerifyMRVisibleOnMain_Absent is the strand case: the fresh connection
// cannot see the MR on main even though the local read-back passed. The caller
// must treat (false, nil) as a confirmed strand and fail loud.
func TestVerifyMRVisibleOnMain_Absent(t *testing.T) {
	f := &fakeMainViewFinder{issue: nil}
	visible, err := verifyMRVisibleOnMain(f, "polecat/furiosa/gs-9sr", "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if visible {
		t.Fatalf("expected visible=false when the MR is not discoverable on main")
	}
}

// TestVerifyMRVisibleOnMain_QueryError covers the inconclusive case: a transient
// Dolt error must surface as (false, err) so the caller warns rather than
// filing a false-positive stranded wisp.
func TestVerifyMRVisibleOnMain_QueryError(t *testing.T) {
	wantErr := errors.New("dolt: connection refused")
	f := &fakeMainViewFinder{err: wantErr}
	visible, err := verifyMRVisibleOnMain(f, "polecat/furiosa/gs-9sr", "abc123")
	if err == nil {
		t.Fatalf("expected the query error to be propagated")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped query error, got %v", err)
	}
	if visible {
		t.Fatalf("a query error must report visible=false (inconclusive, not confirmed)")
	}
}

// TestShouldTrustMRCheckpoint verifies the gs-onu resume verdict: gt done's
// restart-resume path may only skip MR creation when the checkpointed MR is
// confirmed visible on shared main. A definitive absence (false,nil) — the
// silent-strand signature — must distrust the checkpoint and re-enqueue; a
// transient query error stays conservative and trusts the checkpoint.
func TestShouldTrustMRCheckpoint(t *testing.T) {
	cases := []struct {
		name    string
		visible bool
		qErr    error
		want    bool
	}{
		{"visible on main → trust (skip create)", true, nil, true},
		{"definitively absent → distrust (re-enqueue, fixes the resume strand)", false, nil, false},
		{"query error → trust (inconclusive, avoid duplicate MR)", false, errors.New("dolt blip"), true},
		{"visible + spurious error → trust", true, errors.New("dolt blip"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldTrustMRCheckpoint(tc.visible, tc.qErr); got != tc.want {
				t.Errorf("shouldTrustMRCheckpoint(%v,%v) = %v, want %v", tc.visible, tc.qErr, got, tc.want)
			}
		})
	}
}
