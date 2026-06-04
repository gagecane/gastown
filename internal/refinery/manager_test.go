package refinery

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/testutil"
)

func setupTestRegistry(t *testing.T) {
	t.Helper()
	// Use a prefix that won't collide with real gastown sessions.
	// The "tr" prefix conflicts with actual rigs running on the host
	// (e.g., tr-refinery, tr-witness), causing tests that assert
	// "no session exists" to fail in gastown workspaces.
	reg := session.NewPrefixRegistry()
	reg.Register("xut", "testrig")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

func setupTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	setupTestRegistry(t)

	// Create temp directory structure
	tmpDir := t.TempDir()
	rigPath := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(filepath.Join(rigPath, ".runtime"), 0755); err != nil {
		t.Fatalf("mkdir .runtime: %v", err)
	}

	r := &rig.Rig{
		Name: "testrig",
		Path: rigPath,
	}

	mgr := NewManager(r)
	// Default to a passing merge-landed verification so beads-only tests don't
	// require a real git worktree. Tests exercising the gu-ilf86 guard override
	// this field explicitly.
	mgr.verifyMergeLanded = func(*MergeRequest) error { return nil }
	return mgr, rigPath
}

func TestManager_StartForegroundDeprecated(t *testing.T) {
	mgr, _ := setupTestManager(t)
	err := mgr.Start(true, "")
	if err == nil {
		t.Fatal("expected foreground mode deprecation error")
	}
	if !strings.Contains(err.Error(), "foreground mode is deprecated") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestManager_SessionName(t *testing.T) {
	mgr, _ := setupTestManager(t)

	want := "xut-refinery"
	got := mgr.SessionName()
	if got != want {
		t.Errorf("SessionName() = %s, want %s", got, want)
	}
}

func TestManager_IsRunning_NoSession(t *testing.T) {
	mgr, _ := setupTestManager(t)

	// Without a tmux session, IsRunning should return false
	// Note: this test doesn't create a tmux session, so it tests the "not running" case
	running, err := mgr.IsRunning()
	if err != nil {
		// If tmux server isn't running, HasSession returns an error
		// This is expected in test environments without tmux
		t.Logf("IsRunning returned error (expected without tmux): %v", err)
		return
	}

	if running {
		t.Error("IsRunning() = true, want false (no session created)")
	}
}

func TestManager_Status_NotRunning(t *testing.T) {
	mgr, _ := setupTestManager(t)

	// Without a tmux session, Status should return ErrNotRunning
	_, err := mgr.Status()
	if err == nil {
		t.Error("Status() expected error when not running")
	}
	// May return ErrNotRunning or a tmux server error
	t.Logf("Status returned error (expected): %v", err)
}

func TestManager_Queue_NoBeads(t *testing.T) {
	mgr, _ := setupTestManager(t)

	// Queue returns error when no beads database exists
	// This is expected - beads requires initialization
	_, err := mgr.Queue()
	if err == nil {
		// If beads is somehow available, queue should be empty
		t.Log("Queue() succeeded unexpectedly (beads may be available)")
		return
	}
	// Error is expected when beads isn't initialized
	t.Logf("Queue() returned error (expected without beads): %v", err)
}

func TestManager_Queue_FiltersClosedMergeRequests(t *testing.T) {
	mgr, rigPath := setupTestManager(t)
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())
	b := beads.NewIsolatedWithPort(rigPath, port)
	if err := b.Init(testutil.UniqueTestPrefix(t)); err != nil {
		t.Skipf("bd init unavailable in test environment: %v", err)
	}

	openIssue, err := b.Create(beads.CreateOptions{
		Title:  "Open MR",
		Labels: []string{"gt:merge-request"},
	})
	if err != nil {
		t.Fatalf("create open merge-request issue: %v", err)
	}
	closedIssue, err := b.Create(beads.CreateOptions{
		Title:  "Closed MR",
		Labels: []string{"gt:merge-request"},
	})
	if err != nil {
		t.Fatalf("create closed merge-request issue: %v", err)
	}
	closedStatus := "closed"
	if err := b.Update(closedIssue.ID, beads.UpdateOptions{Status: &closedStatus}); err != nil {
		t.Fatalf("close merge-request issue: %v", err)
	}

	queue, err := mgr.Queue()
	if err != nil {
		t.Fatalf("Queue() error: %v", err)
	}

	var sawOpen bool
	for _, item := range queue {
		if item.MR == nil {
			continue
		}
		if item.MR.ID == closedIssue.ID {
			t.Fatalf("queue contains closed merge-request %s", closedIssue.ID)
		}
		if item.MR.ID == openIssue.ID {
			sawOpen = true
		}
	}
	if !sawOpen {
		t.Fatalf("queue missing expected open merge-request %s", openIssue.ID)
	}
}

func TestManager_FindMR_NoBeads(t *testing.T) {
	mgr, _ := setupTestManager(t)

	// FindMR returns error when no beads database exists
	_, err := mgr.FindMR("nonexistent-mr")
	if err == nil {
		t.Error("FindMR() expected error")
	}
	// Any error is acceptable when beads isn't initialized
	t.Logf("FindMR() returned error (expected): %v", err)
}

func TestManager_RegisterMR_Deprecated(t *testing.T) {
	mgr, _ := setupTestManager(t)

	mr := &MergeRequest{
		ID:     "gt-mr-test",
		Branch: "polecat/Test/gt-123",
		Worker: "Test",
		Status: MROpen,
	}

	// RegisterMR should return an error indicating deprecation
	err := mgr.RegisterMR(mr)
	if err == nil {
		t.Error("RegisterMR() expected error (deprecated)")
	}
}

func TestManager_Retry_Deprecated(t *testing.T) {
	mgr, _ := setupTestManager(t)

	// Retry is deprecated and should not error, just print a message
	err := mgr.Retry("any-id", false)
	if err != nil {
		t.Errorf("Retry() unexpected error: %v", err)
	}
}

func TestCompareScoredIssues_UsesDeterministicIDTieBreaker(t *testing.T) {
	t.Helper()

	first := scoredIssue{
		issue: &beads.Issue{
			ID:        "gt-1",
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		},
		score: 10,
	}
	second := scoredIssue{
		issue: &beads.Issue{
			ID:        "gt-2",
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		},
		score: 10,
	}

	if !compareScoredIssues(first, second) {
		t.Fatalf("expected gt-1 to sort before gt-2 for equal scores")
	}
	if compareScoredIssues(second, first) {
		t.Fatalf("expected gt-2 to sort after gt-1 for equal scores")
	}
}

func TestManager_PostMerge_ClosesMRAndSourceIssue(t *testing.T) {
	mgr, rigPath := setupTestManager(t)
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())
	b := beads.NewIsolatedWithPort(rigPath, port)
	if err := b.Init(testutil.UniqueTestPrefix(t)); err != nil {
		t.Skipf("bd init unavailable: %v", err)
	}

	// Create a source issue
	srcIssue, err := b.Create(beads.CreateOptions{
		Title:  "Implement feature X",
		Labels: []string{"gt:task"},
	})
	if err != nil {
		t.Fatalf("create source issue: %v", err)
	}

	// Create an MR bead with branch and source_issue fields
	mrDesc := "branch: polecat/test/gt-xyz\nsource_issue: " + srcIssue.ID + "\nworker: test\ntarget: main"
	mrIssue, err := b.Create(beads.CreateOptions{
		Title:       "MR for feature X",
		Labels:      []string{"gt:merge-request"},
		Description: mrDesc,
	})
	if err != nil {
		t.Fatalf("create MR issue: %v", err)
	}

	// Run PostMerge
	result, err := mgr.PostMerge(mrIssue.ID)
	if err != nil {
		t.Fatalf("PostMerge() error: %v", err)
	}

	// Verify result
	if !result.MRClosed {
		t.Error("PostMerge() MRClosed = false, want true")
	}
	if !result.SourceIssueClosed {
		t.Error("PostMerge() SourceIssueClosed = false, want true")
	}
	if result.SourceIssueID != srcIssue.ID {
		t.Errorf("PostMerge() SourceIssueID = %s, want %s", result.SourceIssueID, srcIssue.ID)
	}
	if result.MR.Branch != "polecat/test/gt-xyz" {
		t.Errorf("PostMerge() MR.Branch = %s, want polecat/test/gt-xyz", result.MR.Branch)
	}
}

// TestManager_PostMerge_StripsTimestampSuffix is a regression test for gu-y2w.
// If an older binary wrote a polecat branch timestamp suffix into the MR's
// source_issue field (e.g. "gu-aei--moiitf15" instead of "gu-aei"),
// PostMerge should still close the real bug bead rather than fail with
// "not found" and leave the bug stuck in HOOKED.
func TestManager_PostMerge_StripsTimestampSuffix(t *testing.T) {
	mgr, rigPath := setupTestManager(t)
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())
	b := beads.NewIsolatedWithPort(rigPath, port)
	if err := b.Init(testutil.UniqueTestPrefix(t)); err != nil {
		t.Skipf("bd init unavailable: %v", err)
	}

	srcIssue, err := b.Create(beads.CreateOptions{
		Title:  "Real bug bead",
		Labels: []string{"gt:task"},
	})
	if err != nil {
		t.Fatalf("create source issue: %v", err)
	}

	// Simulate the broken-submit MR: source_issue includes the "--<ts>"
	// suffix that parseBranchName didn't strip pre-gu-y2w.
	suffixedID := srcIssue.ID + "--moiitf15"
	mrDesc := "branch: polecat/test/" + suffixedID +
		"\nsource_issue: " + suffixedID +
		"\nworker: test\ntarget: main"
	mrIssue, err := b.Create(beads.CreateOptions{
		Title:       "MR with suffixed source_issue",
		Labels:      []string{"gt:merge-request"},
		Description: mrDesc,
	})
	if err != nil {
		t.Fatalf("create MR issue: %v", err)
	}

	result, err := mgr.PostMerge(mrIssue.ID)
	if err != nil {
		t.Fatalf("PostMerge() error: %v", err)
	}

	if !result.SourceIssueClosed {
		t.Errorf("PostMerge() SourceIssueClosed = false, want true (suffix should be stripped)")
	}
	if result.SourceIssueID != srcIssue.ID {
		t.Errorf("PostMerge() SourceIssueID = %q, want %q (stripped form)", result.SourceIssueID, srcIssue.ID)
	}
	// Verify the real bug bead is now terminal.
	got, err := b.Show(srcIssue.ID)
	if err != nil {
		t.Fatalf("show real bug bead: %v", err)
	}
	if !beads.IssueStatus(got.Status).IsTerminal() {
		t.Errorf("source issue status = %q, want terminal", got.Status)
	}
}

// TestManager_PostMerge_ClearsAwaitingRefineryMergeLabel is a regression test
// for gu-mhwn. When the polecat's gt done submits an MR, it adds the
// awaiting_refinery_merge label to the source bead. PostMerge force-closes
// the source bead but historically did NOT clear that label, so a closed +
// cited bead could still carry the in-flight label, confusing downstream
// consumers (convoy ship-verify, etc.).
func TestManager_PostMerge_ClearsAwaitingRefineryMergeLabel(t *testing.T) {
	mgr, rigPath := setupTestManager(t)
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())
	b := beads.NewIsolatedWithPort(rigPath, port)
	if err := b.Init(testutil.UniqueTestPrefix(t)); err != nil {
		t.Skipf("bd init unavailable: %v", err)
	}

	// Create a source issue that already carries awaiting_refinery_merge,
	// matching the state after the polecat's gt done submitted the MR.
	srcIssue, err := b.Create(beads.CreateOptions{
		Title:  "Implement feature X",
		Labels: []string{"gt:task", "awaiting_refinery_merge"},
	})
	if err != nil {
		t.Fatalf("create source issue: %v", err)
	}

	mrDesc := "branch: polecat/test/gt-xyz\nsource_issue: " + srcIssue.ID + "\nworker: test\ntarget: main"
	mrIssue, err := b.Create(beads.CreateOptions{
		Title:       "MR for feature X",
		Labels:      []string{"gt:merge-request"},
		Description: mrDesc,
	})
	if err != nil {
		t.Fatalf("create MR issue: %v", err)
	}

	result, err := mgr.PostMerge(mrIssue.ID)
	if err != nil {
		t.Fatalf("PostMerge() error: %v", err)
	}
	if !result.SourceIssueClosed {
		t.Fatal("PostMerge() SourceIssueClosed = false, want true")
	}

	got, err := b.Show(srcIssue.ID)
	if err != nil {
		t.Fatalf("show source issue: %v", err)
	}
	for _, label := range got.Labels {
		if label == "awaiting_refinery_merge" {
			t.Errorf("source issue still carries awaiting_refinery_merge after PostMerge; labels=%v", got.Labels)
		}
	}
}

// TestManager_PostMerge_AlreadyClosedMR is a regression test for gu-3f02d.
// A previous post-merge run may close the MR bead but be interrupted before
// finishing branch-delete / source-issue-close. A retry must NOT error with
// "merge request not found" (which abandoned the leftover cleanup); it must
// recover the closed MR bead by ID and complete idempotently.
func TestManager_PostMerge_AlreadyClosedMR(t *testing.T) {
	mgr, rigPath := setupTestManager(t)
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())
	b := beads.NewIsolatedWithPort(rigPath, port)
	if err := b.Init(testutil.UniqueTestPrefix(t)); err != nil {
		t.Skipf("bd init unavailable: %v", err)
	}

	// Create and close an MR bead (simulating a prior partial post-merge run)
	mrIssue, err := b.Create(beads.CreateOptions{
		Title:       "Already merged MR",
		Labels:      []string{"gt:merge-request"},
		Description: "branch: polecat/old/gt-old\ntarget: main",
	})
	if err != nil {
		t.Fatalf("create MR issue: %v", err)
	}
	if err := b.Close(mrIssue.ID); err != nil {
		t.Fatalf("close MR issue: %v", err)
	}

	// PostMerge must succeed idempotently even though the MR is already closed
	// and absent from the open queue.
	result, err := mgr.PostMerge(mrIssue.ID)
	if err != nil {
		t.Fatalf("PostMerge() on already-closed MR error = %v, want nil (idempotent)", err)
	}
	if !result.MRClosed {
		t.Error("PostMerge() MRClosed = false, want true")
	}
	if result.MR.Branch != "polecat/old/gt-old" {
		t.Errorf("PostMerge() MR.Branch = %q, want polecat/old/gt-old", result.MR.Branch)
	}
}

// TestManager_PostMerge_IdempotentClosesSourceIssue is a regression test for
// gu-3f02d. When a prior run closed the MR bead but left the source issue
// HOOKED/open, the retry must close the source issue rather than bail.
func TestManager_PostMerge_IdempotentClosesSourceIssue(t *testing.T) {
	mgr, rigPath := setupTestManager(t)
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())
	b := beads.NewIsolatedWithPort(rigPath, port)
	if err := b.Init(testutil.UniqueTestPrefix(t)); err != nil {
		t.Skipf("bd init unavailable: %v", err)
	}

	// Source issue that a prior partial run left open.
	srcIssue, err := b.Create(beads.CreateOptions{
		Title:  "Implement feature Y",
		Labels: []string{"gt:task"},
	})
	if err != nil {
		t.Fatalf("create source issue: %v", err)
	}

	// MR bead already closed, but source issue still open.
	mrDesc := "branch: polecat/test/gt-yz\nsource_issue: " + srcIssue.ID + "\nworker: test\ntarget: main"
	mrIssue, err := b.Create(beads.CreateOptions{
		Title:       "MR for feature Y",
		Labels:      []string{"gt:merge-request"},
		Description: mrDesc,
	})
	if err != nil {
		t.Fatalf("create MR issue: %v", err)
	}
	if err := b.Close(mrIssue.ID); err != nil {
		t.Fatalf("close MR issue: %v", err)
	}

	result, err := mgr.PostMerge(mrIssue.ID)
	if err != nil {
		t.Fatalf("PostMerge() error = %v, want nil", err)
	}
	if !result.SourceIssueClosed {
		t.Error("PostMerge() SourceIssueClosed = false, want true (idempotent retry must close source)")
	}
	got, err := b.Show(srcIssue.ID)
	if err != nil {
		t.Fatalf("show source issue: %v", err)
	}
	if !beads.IssueStatus(got.Status).IsTerminal() {
		t.Errorf("source issue status = %q, want terminal", got.Status)
	}
}

func TestManager_PostMerge_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)

	_, err := mgr.PostMerge("nonexistent-mr-id")
	if err == nil {
		t.Error("PostMerge() expected error for nonexistent MR")
	}
}

// TestStripMRIssueTimestampSuffix is a pure-unit regression test for gu-y2w.
func TestStripMRIssueTimestampSuffix(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"dashdash suffix (current)", "gu-aei--moiitf15", "gu-aei"},
		{"at suffix (legacy)", "gt-abc@mk123456", "gt-abc"},
		{"no suffix", "gu-aei", "gu-aei"},
		{"subtask no suffix", "gu-aei.1", "gu-aei.1"},
		{"subtask with dashdash", "gu-aei.1--mk123", "gu-aei.1"},
		{"empty", "", ""},
		{"leading dashdash rejected (nothing to strip)", "--mk123", "--mk123"},
		{"leading at rejected (nothing to strip)", "@mk123", "@mk123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripMRIssueTimestampSuffix(tc.in); got != tc.want {
				t.Errorf("stripMRIssueTimestampSuffix(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// fakeAncestryOps is a test double for gitAncestryOps used by the gu-ilf86
// merge-landed verification tests.
type fakeAncestryOps struct {
	fetchErr      error
	fetchedRemote string
	fetchedBranch string
	isAncestor    bool
	ancestorErr   error
	ancestorArg   string
	descendantArg string
}

func (f *fakeAncestryOps) FetchBranch(remote, branch string) error {
	f.fetchedRemote = remote
	f.fetchedBranch = branch
	return f.fetchErr
}

func (f *fakeAncestryOps) IsAncestor(ancestor, descendant string) (bool, error) {
	f.ancestorArg = ancestor
	f.descendantArg = descendant
	return f.isAncestor, f.ancestorErr
}

// TestVerifyMergeCommitLanded is a pure-unit test for the gu-ilf86 guard.
func TestVerifyMergeCommitLanded(t *testing.T) {
	t.Run("landed on target → nil", func(t *testing.T) {
		f := &fakeAncestryOps{isAncestor: true}
		if err := verifyMergeCommitLanded(f, "abc123", "main"); err != nil {
			t.Fatalf("verifyMergeCommitLanded() = %v, want nil", err)
		}
		if f.fetchedRemote != "origin" || f.fetchedBranch != "main" {
			t.Errorf("fetched %s/%s, want origin/main", f.fetchedRemote, f.fetchedBranch)
		}
		if f.ancestorArg != "abc123" || f.descendantArg != "origin/main" {
			t.Errorf("IsAncestor(%q,%q), want (abc123, origin/main)", f.ancestorArg, f.descendantArg)
		}
	})

	t.Run("not an ancestor → error (silent merge-loss)", func(t *testing.T) {
		f := &fakeAncestryOps{isAncestor: false}
		err := verifyMergeCommitLanded(f, "abc123", "main")
		if err == nil {
			t.Fatal("verifyMergeCommitLanded() = nil, want error for unlanded commit")
		}
		if !strings.Contains(err.Error(), "did not land") {
			t.Errorf("error = %v, want mention of 'did not land'", err)
		}
	})

	t.Run("empty merge commit → error", func(t *testing.T) {
		f := &fakeAncestryOps{isAncestor: true}
		if err := verifyMergeCommitLanded(f, "  ", "main"); err == nil {
			t.Fatal("verifyMergeCommitLanded() = nil, want error for empty merge_commit")
		}
		if f.fetchedRemote != "" {
			t.Error("should not fetch when merge_commit is empty")
		}
	})

	t.Run("empty target → error", func(t *testing.T) {
		f := &fakeAncestryOps{isAncestor: true}
		if err := verifyMergeCommitLanded(f, "abc123", ""); err == nil {
			t.Fatal("verifyMergeCommitLanded() = nil, want error for empty target")
		}
	})

	t.Run("fetch failure → error (fail closed)", func(t *testing.T) {
		f := &fakeAncestryOps{fetchErr: errors.New("network down"), isAncestor: true}
		if err := verifyMergeCommitLanded(f, "abc123", "main"); err == nil {
			t.Fatal("verifyMergeCommitLanded() = nil, want error when fetch fails")
		}
	})

	t.Run("ancestry lookup error → error (fail closed)", func(t *testing.T) {
		f := &fakeAncestryOps{ancestorErr: errors.New("bad object")}
		if err := verifyMergeCommitLanded(f, "abc123", "main"); err == nil {
			t.Fatal("verifyMergeCommitLanded() = nil, want error when ancestry lookup fails")
		}
	})

	t.Run("nil git ops → error", func(t *testing.T) {
		if err := verifyMergeCommitLanded(nil, "abc123", "main"); err == nil {
			t.Fatal("verifyMergeCommitLanded(nil) = nil, want error")
		}
	})
}

// TestManager_PostMerge_RefusesCloseWhenMergeNotLanded is the integration-level
// regression test for gu-ilf86: PostMerge must NOT close the MR or source bead
// when the merge commit never landed on origin/<target>.
func TestManager_PostMerge_RefusesCloseWhenMergeNotLanded(t *testing.T) {
	mgr, rigPath := setupTestManager(t)
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())
	b := beads.NewIsolatedWithPort(rigPath, port)
	if err := b.Init(testutil.UniqueTestPrefix(t)); err != nil {
		t.Skipf("bd init unavailable: %v", err)
	}

	srcIssue, err := b.Create(beads.CreateOptions{
		Title:  "Implement feature Z",
		Labels: []string{"gt:task"},
	})
	if err != nil {
		t.Fatalf("create source issue: %v", err)
	}
	mrDesc := "branch: polecat/test/gt-zzz\nsource_issue: " + srcIssue.ID + "\nworker: test\ntarget: main\nmerge_commit: deadbeef"
	mrIssue, err := b.Create(beads.CreateOptions{
		Title:       "MR for feature Z",
		Labels:      []string{"gt:merge-request"},
		Description: mrDesc,
	})
	if err != nil {
		t.Fatalf("create MR issue: %v", err)
	}

	// Simulate a merge that did NOT land on origin/main.
	mgr.verifyMergeLanded = func(*MergeRequest) error {
		return errors.New("merge commit deadbeef is not on origin/main — merge did not land")
	}

	_, err = mgr.PostMerge(mrIssue.ID)
	if err == nil {
		t.Fatal("PostMerge() = nil error, want refusal when merge did not land")
	}

	// The MR bead must remain OPEN.
	gotMR, showErr := b.Show(mrIssue.ID)
	if showErr != nil {
		t.Fatalf("show MR issue: %v", showErr)
	}
	if beads.IssueStatus(gotMR.Status).IsTerminal() {
		t.Errorf("MR bead closed despite unlanded merge; status=%q", gotMR.Status)
	}
	// The source issue must remain OPEN.
	gotSrc, showErr := b.Show(srcIssue.ID)
	if showErr != nil {
		t.Fatalf("show source issue: %v", showErr)
	}
	if beads.IssueStatus(gotSrc.Status).IsTerminal() {
		t.Errorf("source issue closed despite unlanded merge; status=%q", gotSrc.Status)
	}
}

// TestManager_PostMerge_AlreadyClosedMR_SkipsVerification confirms the
// idempotent recovery path (gu-3f02d) is not blocked by the gu-ilf86 guard:
// an already-closed MR is past the verification point, so PostMerge must still
// complete the leftover source-issue close even with a failing verifier.
func TestManager_PostMerge_AlreadyClosedMR_SkipsVerification(t *testing.T) {
	mgr, rigPath := setupTestManager(t)
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())
	b := beads.NewIsolatedWithPort(rigPath, port)
	if err := b.Init(testutil.UniqueTestPrefix(t)); err != nil {
		t.Skipf("bd init unavailable: %v", err)
	}

	srcIssue, err := b.Create(beads.CreateOptions{
		Title:  "Implement feature W",
		Labels: []string{"gt:task"},
	})
	if err != nil {
		t.Fatalf("create source issue: %v", err)
	}
	mrDesc := "branch: polecat/test/gt-www\nsource_issue: " + srcIssue.ID + "\nworker: test\ntarget: main"
	mrIssue, err := b.Create(beads.CreateOptions{
		Title:       "MR for feature W",
		Labels:      []string{"gt:merge-request"},
		Description: mrDesc,
	})
	if err != nil {
		t.Fatalf("create MR issue: %v", err)
	}
	// MR already closed by a prior partial run.
	if err := b.Close(mrIssue.ID); err != nil {
		t.Fatalf("close MR issue: %v", err)
	}

	// A failing verifier must NOT be consulted on the already-closed path.
	mgr.verifyMergeLanded = func(*MergeRequest) error {
		t.Fatal("verifyMergeLanded must not be called for an already-closed MR")
		return nil
	}

	result, err := mgr.PostMerge(mrIssue.ID)
	if err != nil {
		t.Fatalf("PostMerge() on already-closed MR error = %v, want nil", err)
	}
	if !result.SourceIssueClosed {
		t.Error("PostMerge() SourceIssueClosed = false, want true (idempotent recovery)")
	}
}

func TestManager_Start_ReturnsErrDisabledWhenRefineryDisabled(t *testing.T) {
	_, rigPath := setupTestManager(t)
	cfgJSON := `{"type":"rig","version":1,"name":"testrig","git_url":"https://github.com/example/repo","refinery_disabled":true}`
	if err := os.WriteFile(filepath.Join(rigPath, "config.json"), []byte(cfgJSON), 0644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	r := &rig.Rig{Name: "testrig", Path: rigPath}
	mgr := NewManager(r)
	err := mgr.Start(false, "")
	if err != ErrDisabled {
		t.Errorf("Start() with refinery_disabled=true: got %v, want ErrDisabled", err)
	}
}

func TestManager_Start_NotDisabledWhenFlagFalse(t *testing.T) {
	mgr, rigPath := setupTestManager(t)
	cfgJSON := `{"type":"rig","version":1,"name":"testrig","git_url":"https://github.com/example/repo","refinery_disabled":false}`
	if err := os.WriteFile(filepath.Join(rigPath, "config.json"), []byte(cfgJSON), 0644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	err := mgr.Start(false, "")
	// Eagerly stop the session before returning so it doesn't leak into other packages.
	_ = mgr.Stop()
	if err == ErrDisabled {
		t.Error("Start() with refinery_disabled=false returned ErrDisabled unexpectedly")
	}
}
