package cmd

// Tests for gu-hz3vx: `gt done` must auto-recover the safe slice of a
// non-fast-forward push rejection on the polecat's OWN private branch.
//
// The recurring work-loss incident (shiny gu-qx6rn): a polecat pushed an
// early commit to its feature branch, then `gt done` amended/rebased the work
// onto a different base, producing a commit that was no longer a fast-forward
// of the already-pushed tip. The branch:branch push was rejected
// non-fast-forward, the amended commit stayed local-only, and Mayor had to
// force-push by hand.
//
// recoverNonFFOwnBranch force-updates the private branch ONLY when the local
// HEAD tree is byte-identical to origin's tip (pure history-shape divergence:
// amend or rebase that picked up no new content). When the trees differ — the
// contamination footgun the Mayor flagged, where the amend bundled unrelated
// files — it refuses and lets the caller strand the work loudly for review.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// runGitEnv runs a git command in dir with deterministic author/committer
// identity and returns trimmed stdout.
func runGitEnv(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return trimTrailingWS(string(out))
}

func trimTrailingWS(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// setupDivergedOwnBranch builds a bare remote + worktree where the feature
// branch on origin points at an EARLIER commit, and local HEAD is a divergent
// (non-fast-forward) commit. The caller chooses whether local HEAD keeps the
// same tree as origin's tip (sameTree=true → safe amend) or introduces extra
// file content (sameTree=false → contamination).
//
// Returns (worktreePath, localHEADSHA, originTipSHA, branch).
func setupDivergedOwnBranch(t *testing.T, branch string, sameTree bool) (string, string, string, string) {
	t.Helper()
	bare := t.TempDir()
	runGitEnv(t, bare, "init", "--bare", "-b", "main")

	wt := t.TempDir()
	runGitEnv(t, wt, "init", "-b", "main")
	runGitEnv(t, wt, "config", "user.email", "test@example.com")
	runGitEnv(t, wt, "config", "user.name", "Test")
	runGitEnv(t, wt, "remote", "add", "origin", bare)

	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitEnv(t, wt, "add", "README.md")
	runGitEnv(t, wt, "commit", "-m", "seed")
	runGitEnv(t, wt, "push", "origin", "main")

	// Feature branch: the "real work" lands feature.txt and is pushed.
	runGitEnv(t, wt, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(wt, "feature.txt"), []byte("the work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitEnv(t, wt, "add", "feature.txt")
	runGitEnv(t, wt, "commit", "-m", "feat: the work")
	runGitEnv(t, wt, "push", "origin", branch)
	originTip := runGitEnv(t, wt, "rev-parse", "HEAD")

	// Now diverge: reset back to main and re-create a commit that is NOT a
	// descendant of originTip (mirrors `gt done` amending onto a different
	// base). This makes the branch:branch push non-fast-forward.
	runGitEnv(t, wt, "reset", "--hard", "main")
	if sameTree {
		// Identical content to originTip → identical tree, divergent history.
		if err := os.WriteFile(filepath.Join(wt, "feature.txt"), []byte("the work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitEnv(t, wt, "add", "feature.txt")
		runGitEnv(t, wt, "commit", "-m", "feat: the work (re-based, same content)")
	} else {
		// Bundle an UNRELATED file → different tree (the contamination case).
		if err := os.WriteFile(filepath.Join(wt, "feature.txt"), []byte("the work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wt, "unrelated.txt"), []byte("contamination\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitEnv(t, wt, "add", "-A")
		runGitEnv(t, wt, "commit", "-m", "feat: the work + unrelated bundle")
	}
	localHEAD := runGitEnv(t, wt, "rev-parse", "HEAD")

	return wt, localHEAD, originTip, branch
}

// attemptPush tries the branch:branch push and returns the resulting error
// (expected to be a non-fast-forward rejection).
func attemptPush(t *testing.T, wt, branch string) error {
	t.Helper()
	g := git.NewGit(wt)
	return g.Push("origin", branch+":"+branch, false)
}

// TestRecoverNonFFOwnBranch_SameTreeForcePushes verifies the safe path: a
// non-FF rejection where local HEAD has the same tree as origin's tip is
// recovered by force-updating the private branch to the local SHA.
func TestRecoverNonFFOwnBranch_SameTreeForcePushes(t *testing.T) {
	wt, localHEAD, originTip, branch := setupDivergedOwnBranch(t, "polecat/test/same--abc", true)
	if localHEAD == originTip {
		t.Fatalf("test setup broken: local HEAD must differ from origin tip")
	}
	pushErr := attemptPush(t, wt, branch)
	if pushErr == nil {
		t.Fatalf("expected non-fast-forward push rejection, got nil")
	}
	if !isNonFastForwardPushError(pushErr) {
		t.Fatalf("push error not classified as non-FF: %v", pushErr)
	}

	g := git.NewGit(wt)
	if !recoverNonFFOwnBranch(g, branch, localHEAD, pushErr) {
		t.Fatalf("expected recoverNonFFOwnBranch to force-update identical-tree divergence")
	}

	// Origin tip must now be the local HEAD SHA.
	tip, err := g.RemoteBranchTip("origin", branch)
	if err != nil {
		t.Fatalf("RemoteBranchTip: %v", err)
	}
	if trimTrailingWS(tip) != localHEAD {
		t.Fatalf("origin tip = %s, want force-updated to %s", tip, localHEAD)
	}
}

// TestRecoverNonFFOwnBranch_DifferentTreeRefuses verifies the footgun guard:
// when local HEAD introduces content not on origin's tip (the contamination
// case), recovery must REFUSE so the work is stranded loudly rather than
// force-pushing unrelated files into the merge queue.
func TestRecoverNonFFOwnBranch_DifferentTreeRefuses(t *testing.T) {
	wt, localHEAD, originTip, branch := setupDivergedOwnBranch(t, "polecat/test/diff--abc", false)
	pushErr := attemptPush(t, wt, branch)
	if pushErr == nil {
		t.Fatalf("expected non-fast-forward push rejection, got nil")
	}

	g := git.NewGit(wt)
	if recoverNonFFOwnBranch(g, branch, localHEAD, pushErr) {
		t.Fatalf("expected recoverNonFFOwnBranch to REFUSE when trees differ (contamination guard)")
	}

	// Origin tip must be UNCHANGED — the original work is preserved, nothing
	// was force-clobbered.
	tip, err := g.RemoteBranchTip("origin", branch)
	if err != nil {
		t.Fatalf("RemoteBranchTip: %v", err)
	}
	if trimTrailingWS(tip) != originTip {
		t.Fatalf("origin tip = %s, want unchanged %s", tip, originTip)
	}
}

// TestRecoverNonFFOwnBranch_NonNonFFErrorRefuses verifies that recovery only
// fires on a genuine non-fast-forward rejection. Any other push failure
// (network, auth, gate) must not trigger a force-push.
func TestRecoverNonFFOwnBranch_NonNonFFErrorRefuses(t *testing.T) {
	wt, localHEAD, originTip, branch := setupDivergedOwnBranch(t, "polecat/test/other--abc", true)
	g := git.NewGit(wt)

	bogus := os.ErrPermission // not a non-FF error
	if recoverNonFFOwnBranch(g, branch, localHEAD, bogus) {
		t.Fatalf("expected refusal for non-non-FF error")
	}
	tip, _ := g.RemoteBranchTip("origin", branch)
	if trimTrailingWS(tip) != originTip {
		t.Fatalf("origin tip changed on non-non-FF error: %s", tip)
	}
}

// TestRecoverNonFFOwnBranch_RejectsDegenerateInputs locks in fail-closed
// behavior on nil/empty inputs so a reflog/HEAD read failure can never lead
// to a blind force-push.
func TestRecoverNonFFOwnBranch_RejectsDegenerateInputs(t *testing.T) {
	wt, localHEAD, _, branch := setupDivergedOwnBranch(t, "polecat/test/degen--abc", true)
	g := git.NewGit(wt)
	ffErr := attemptPush(t, wt, branch) // non-FF error to satisfy the classifier

	if recoverNonFFOwnBranch(nil, branch, localHEAD, ffErr) {
		t.Errorf("nil git client must return false")
	}
	if recoverNonFFOwnBranch(g, "", localHEAD, ffErr) {
		t.Errorf("empty branch must return false")
	}
	if recoverNonFFOwnBranch(g, branch, "", ffErr) {
		t.Errorf("empty SHA must return false")
	}
}

// TestIsNonFastForwardPushError_Classification locks in the substring matcher
// so a future git phrasing change is caught by a test rather than silently
// disabling recovery.
func TestIsNonFastForwardPushError_Classification(t *testing.T) {
	if isNonFastForwardPushError(nil) {
		t.Errorf("nil must not be classified as non-FF")
	}
	positives := []string{
		"git push: ! [rejected] branch -> branch (non-fast-forward)",
		"Updates were rejected because the tip of your current branch is behind",
		"failed to push some refs to 'origin'",
		"hint: Updates were rejected; fetch first",
	}
	for _, m := range positives {
		if !isNonFastForwardPushError(errString(m)) {
			t.Errorf("expected non-FF classification for %q", m)
		}
	}
	negatives := []string{
		"fatal: Authentication failed",
		"fatal: unable to access: Could not resolve host",
		"error: gofmt found unformatted files",
	}
	for _, m := range negatives {
		if isNonFastForwardPushError(errString(m)) {
			t.Errorf("did not expect non-FF classification for %q", m)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// setupPeerMergeStrand models the gs-y7g peer-merge-during-work strand: the
// feature branch is pushed to origin on the OLD main, then a PEER commit lands
// on main, and `gt done` rebases the feature work onto the NEW main and tries to
// re-push. The rebased local HEAD is patch-identical to origin's tip but has a
// DIFFERENT tree (it carries the peer's merged content), so the re-push is
// rejected non-fast-forward and the gu-hz3vx tree check cannot recover it.
//
// When contaminate=true, the rebased commit also bundles an unrelated file, so
// its patch-id diverges from origin's clean commit (the recovery must refuse).
//
// Returns (worktreePath, localHEADSHA, originTipSHA, branch).
func setupPeerMergeStrand(t *testing.T, branch string, contaminate bool) (string, string, string, string) {
	t.Helper()
	bare := t.TempDir()
	runGitEnv(t, bare, "init", "--bare", "-b", "main")

	wt := t.TempDir()
	runGitEnv(t, wt, "init", "-b", "main")
	runGitEnv(t, wt, "config", "user.email", "test@example.com")
	runGitEnv(t, wt, "config", "user.name", "Test")
	runGitEnv(t, wt, "remote", "add", "origin", bare)

	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitEnv(t, wt, "add", "README.md")
	runGitEnv(t, wt, "commit", "-m", "seed")
	runGitEnv(t, wt, "push", "origin", "main")
	oldMain := runGitEnv(t, wt, "rev-parse", "HEAD")

	// Feature work on the OLD main, pushed to origin (the earlier push).
	runGitEnv(t, wt, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(wt, "feature.txt"), []byte("the work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitEnv(t, wt, "add", "feature.txt")
	runGitEnv(t, wt, "commit", "-m", "feat: the work")
	runGitEnv(t, wt, "push", "origin", branch)
	originTip := runGitEnv(t, wt, "rev-parse", "HEAD")

	// A PEER MR lands on main while we worked.
	runGitEnv(t, wt, "checkout", "main")
	if err := os.WriteFile(filepath.Join(wt, "peer.txt"), []byte("peer work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitEnv(t, wt, "add", "peer.txt")
	runGitEnv(t, wt, "commit", "-m", "feat: peer work")
	runGitEnv(t, wt, "push", "origin", "main")
	newMain := runGitEnv(t, wt, "rev-parse", "HEAD")
	if newMain == oldMain {
		t.Fatalf("test setup broken: peer commit did not advance main")
	}

	// `gt done` rebases the feature work onto the NEW main.
	runGitEnv(t, wt, "checkout", branch)
	runGitEnv(t, wt, "rebase", "main")
	if contaminate {
		if err := os.WriteFile(filepath.Join(wt, "unrelated.txt"), []byte("contamination\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitEnv(t, wt, "add", "unrelated.txt")
		runGitEnv(t, wt, "commit", "--amend", "--no-edit")
	}
	localHEAD := runGitEnv(t, wt, "rev-parse", "HEAD")

	return wt, localHEAD, originTip, branch
}

// TestRecoverNonFFAdoptOriginPatchIdentical_AdoptsOriginCommit verifies the
// gs-y7g path: a peer-merge-during-work strand where local HEAD is
// patch-identical (but tree-different) to origin's tip is recovered by adopting
// origin's already-pushed commit — without force-pushing.
func TestRecoverNonFFAdoptOriginPatchIdentical_AdoptsOriginCommit(t *testing.T) {
	wt, localHEAD, originTip, branch := setupPeerMergeStrand(t, "polecat/test/peer--abc", false)
	if localHEAD == originTip {
		t.Fatalf("test setup broken: local HEAD must differ from origin tip")
	}
	pushErr := attemptPush(t, wt, branch)
	if pushErr == nil {
		t.Fatalf("expected non-fast-forward push rejection, got nil")
	}
	if !isNonFastForwardPushError(pushErr) {
		t.Fatalf("push error not classified as non-FF: %v", pushErr)
	}

	g := git.NewGit(wt)

	// The gu-hz3vx tree check must NOT recover this case (trees differ).
	if recoverNonFFOwnBranch(g, branch, localHEAD, pushErr) {
		t.Fatalf("recoverNonFFOwnBranch must refuse: rebased tree differs from origin")
	}

	adopted := recoverNonFFAdoptOriginPatchIdentical(g, branch, localHEAD, pushErr)
	if adopted != originTip {
		t.Fatalf("expected to adopt origin tip %s, got %q", originTip, adopted)
	}

	// Origin must be UNCHANGED — recovery adopts, it does not force-push.
	tip, err := g.RemoteBranchTip("origin", branch)
	if err != nil {
		t.Fatalf("RemoteBranchTip: %v", err)
	}
	if trimTrailingWS(tip) != originTip {
		t.Fatalf("origin tip = %s, want unchanged %s (no force-push)", tip, originTip)
	}
}

// TestRecoverNonFFAdoptOriginPatchIdentical_RefusesContamination verifies the
// safety bar: when the rebased commit bundles unrelated files its patch-id
// diverges from origin's clean commit, so recovery must refuse and leave the
// work to strand loudly.
func TestRecoverNonFFAdoptOriginPatchIdentical_RefusesContamination(t *testing.T) {
	wt, localHEAD, originTip, branch := setupPeerMergeStrand(t, "polecat/test/peercontam--abc", true)
	pushErr := attemptPush(t, wt, branch)
	if pushErr == nil {
		t.Fatalf("expected non-fast-forward push rejection, got nil")
	}

	g := git.NewGit(wt)
	if adopted := recoverNonFFAdoptOriginPatchIdentical(g, branch, localHEAD, pushErr); adopted != "" {
		t.Fatalf("expected refusal on contaminating bundle, adopted %q", adopted)
	}

	tip, _ := g.RemoteBranchTip("origin", branch)
	if trimTrailingWS(tip) != originTip {
		t.Fatalf("origin tip changed on contamination refusal: %s", tip)
	}
}

// TestRecoverNonFFAdoptOriginPatchIdentical_NonNonFFErrorRefuses ensures the
// adopt path only fires on a genuine non-fast-forward rejection.
func TestRecoverNonFFAdoptOriginPatchIdentical_NonNonFFErrorRefuses(t *testing.T) {
	wt, localHEAD, _, branch := setupPeerMergeStrand(t, "polecat/test/peerother--abc", false)
	g := git.NewGit(wt)
	if adopted := recoverNonFFAdoptOriginPatchIdentical(g, branch, localHEAD, os.ErrPermission); adopted != "" {
		t.Fatalf("expected refusal for non-non-FF error, adopted %q", adopted)
	}
}

// TestRecoverNonFFAdoptOriginPatchIdentical_RejectsDegenerateInputs locks in
// fail-closed behavior on nil/empty inputs.
func TestRecoverNonFFAdoptOriginPatchIdentical_RejectsDegenerateInputs(t *testing.T) {
	wt, localHEAD, _, branch := setupPeerMergeStrand(t, "polecat/test/peerdegen--abc", false)
	g := git.NewGit(wt)
	ffErr := attemptPush(t, wt, branch)

	if recoverNonFFAdoptOriginPatchIdentical(nil, branch, localHEAD, ffErr) != "" {
		t.Errorf("nil git client must return empty")
	}
	if recoverNonFFAdoptOriginPatchIdentical(g, "", localHEAD, ffErr) != "" {
		t.Errorf("empty branch must return empty")
	}
	if recoverNonFFAdoptOriginPatchIdentical(g, branch, "", ffErr) != "" {
		t.Errorf("empty SHA must return empty")
	}
}
