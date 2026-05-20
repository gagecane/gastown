package refinery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/steveyegge/gastown/internal/git"
)

// testForkSyncRepo builds a realistic fork-sync scenario:
//
//   - A bare "upstream" repo with commits the fork hasn't integrated.
//   - A bare "origin" repo (the fork) cloned from a common ancestor.
//   - A working clone of origin with `upstream` registered as a remote.
//
// The common ancestor is a single initial commit (like `README.md`). After
// that, origin and upstream each add their own commits independently.
//
// Returns the working dir and a *gitpkg.Git bound to it. Cleanup is handled
// by t.TempDir.
func testForkSyncRepo(t *testing.T) (workDir string, g *gitpkg.Git) {
	t.Helper()
	tmpDir := t.TempDir()

	upstreamBare := filepath.Join(tmpDir, "upstream.git")
	originBare := filepath.Join(tmpDir, "origin.git")
	seed := filepath.Join(tmpDir, "seed")
	workDir = filepath.Join(tmpDir, "fork-work")

	// Create both bare repos with main as default branch.
	run(t, tmpDir, "git", "init", "--bare", "--initial-branch=main", upstreamBare)
	run(t, tmpDir, "git", "init", "--bare", "--initial-branch=main", originBare)

	// Seed both bare repos with the same initial commit so they share a
	// common ancestor. This mirrors a real fork — the divergence starts
	// after the initial seed.
	run(t, tmpDir, "git", "init", "--initial-branch=main", seed)
	run(t, seed, "git", "config", "user.email", "test@test.com")
	run(t, seed, "git", "config", "user.name", "Test")
	writeFile(t, seed, "README.md", "# Shared base\n")
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-m", "seed: shared base")
	run(t, seed, "git", "remote", "add", "upstream", upstreamBare)
	run(t, seed, "git", "remote", "add", "origin", originBare)
	run(t, seed, "git", "push", "upstream", "main")
	run(t, seed, "git", "push", "origin", "main")

	// Add divergent commits on upstream (unique file so no content conflict).
	upstreamStage := filepath.Join(tmpDir, "upstream-stage")
	run(t, tmpDir, "git", "clone", upstreamBare, upstreamStage)
	run(t, upstreamStage, "git", "config", "user.email", "upstream@test.com")
	run(t, upstreamStage, "git", "config", "user.name", "Upstream")
	writeFile(t, upstreamStage, "upstream_feature.md", "# From upstream\n")
	run(t, upstreamStage, "git", "add", ".")
	run(t, upstreamStage, "git", "commit", "-m", "upstream: add feature")
	run(t, upstreamStage, "git", "push", "origin", "main")

	// Clone origin (the fork) as the polecat's working dir.
	run(t, tmpDir, "git", "clone", originBare, workDir)
	run(t, workDir, "git", "config", "user.email", "fork@test.com")
	run(t, workDir, "git", "config", "user.name", "Fork")
	run(t, workDir, "git", "remote", "add", "upstream", upstreamBare)
	run(t, workDir, "git", "fetch", "upstream")

	// Give the fork its own commit so `origin/main` has diverged from upstream.
	writeFile(t, workDir, "fork_feature.md", "# From fork\n")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "fork: add feature")
	run(t, workDir, "git", "push", "origin", "main")

	g = gitpkg.NewGit(workDir)
	return workDir, g
}

// TestDoMerge_ForkSync_PreservesAncestry is the end-to-end regression test
// for gu-9yi3. It reproduces the exact scenario that broke main:
//
//  1. origin/main and upstream/main have diverged.
//  2. A polecat branch merges upstream/main (creating a merge commit).
//  3. Refinery processes the branch.
//
// Before the fix, step 3 would `git merge --squash`, destroying the
// upstream parent edge. After the fix, refinery detects the fork-sync
// pattern and uses `git merge --no-ff`, keeping upstream as an ancestor.
//
// The assertion that matters is `git merge-base --is-ancestor upstream/main
// HEAD` on the target branch after the merge completes — precisely what
// `scripts/check-upstream-rebased.sh` checks at runtime.
func TestDoMerge_ForkSync_PreservesAncestry(t *testing.T) {
	workDir, g := testForkSyncRepo(t)

	// Verify the pre-merge state matches what we expect: origin/main and
	// upstream/main have both diverged from the seed, and upstream is NOT
	// yet an ancestor of origin/main. If this assertion fails the fixture
	// itself is wrong.
	if isAnc, err := g.IsAncestor("upstream/main", "origin/main"); err != nil {
		t.Fatalf("pre-check IsAncestor failed: %v", err)
	} else if isAnc {
		t.Fatal("fixture bug: upstream/main is already an ancestor of origin/main before fork-sync")
	}

	// Create the polecat branch and merge upstream into it, mimicking the
	// gu-nt9z polecat's actions.
	run(t, workDir, "git", "checkout", "-b", "polecat/fork-sync", "main")
	run(t, workDir, "git", "merge", "--no-ff", "-m", "Sync fork from upstream", "upstream/main")
	run(t, workDir, "git", "push", "-u", "origin", "polecat/fork-sync")

	// Back to main so doMerge's Checkout is a no-op (mirrors refinery state).
	run(t, workDir, "git", "checkout", "main")

	e := newTestEngineer(t, workDir, g)
	result := e.doMerge(context.Background(), "polecat/fork-sync", "main", "gu-9yi3")

	if !result.Success {
		t.Fatalf("doMerge failed: conflict=%v error=%s", result.Conflict, result.Error)
	}

	// The whole point of gu-9yi3: after the merge, upstream/main must be
	// an ancestor of the target's HEAD. This is what the rebase-check gate
	// enforces, and what squash-merging broke.
	isAnc, err := g.IsAncestor("upstream/main", "HEAD")
	if err != nil {
		t.Fatalf("post-merge IsAncestor failed: %v", err)
	}
	if !isAnc {
		// Surface the topology for debugging — makes triage far easier than
		// just "false".
		out := run(t, workDir, "git", "log", "--oneline", "--graph", "--all", "-20")
		t.Fatalf("upstream/main is NOT an ancestor of HEAD after fork-sync merge.\nGit topology:\n%s", out)
	}

	// Sanity: the merge commit should have two parents (proves --no-ff).
	parentsOut := run(t, workDir, "git", "log", "-1", "--format=%P", "HEAD")
	parents := strings.Fields(parentsOut)
	if len(parents) != 2 {
		t.Errorf("expected merge commit with 2 parents, got %d: %v", len(parents), parents)
	}

	// Content invariant: both upstream's file and the fork's file are
	// present, confirming a full merge (not an accidental fast-forward
	// that dropped either side).
	for _, f := range []string{"README.md", "upstream_feature.md", "fork_feature.md"} {
		if _, statErr := os.Stat(filepath.Join(workDir, f)); os.IsNotExist(statErr) {
			t.Errorf("expected %s to exist after fork-sync merge", f)
		}
	}
}

// TestDoMerge_NonForkSync_StillSquashes guards against regression in the
// squash path for the common case: a plain polecat branch in a fork repo
// that did NOT merge upstream should still be squash-merged. Preserving
// topology unnecessarily would pollute mainline history with meaningless
// merge commits.
func TestDoMerge_NonForkSync_StillSquashes(t *testing.T) {
	workDir, g := testForkSyncRepo(t)

	// Make a regular feature branch off main WITHOUT touching upstream.
	run(t, workDir, "git", "checkout", "-b", "polecat/plain-feature", "main")
	writeFile(t, workDir, "plain.md", "# Plain feature\n")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat: add plain feature")
	run(t, workDir, "git", "push", "-u", "origin", "polecat/plain-feature")
	run(t, workDir, "git", "checkout", "main")

	e := newTestEngineer(t, workDir, g)
	result := e.doMerge(context.Background(), "polecat/plain-feature", "main", "gt-plain")

	if !result.Success {
		t.Fatalf("doMerge failed: conflict=%v error=%s", result.Conflict, result.Error)
	}

	// Squash-merge: the new commit has exactly one parent (the previous main).
	parentsOut := run(t, workDir, "git", "log", "-1", "--format=%P", "HEAD")
	parents := strings.Fields(parentsOut)
	if len(parents) != 1 {
		t.Errorf("expected squash-merge commit with 1 parent, got %d: %v", len(parents), parents)
	}

	// Plain-feature branch did not merge upstream, so after the squash
	// upstream/main must NOT be an ancestor of HEAD (unchanged pre-state).
	isAnc, err := g.IsAncestor("upstream/main", "HEAD")
	if err != nil {
		t.Fatalf("IsAncestor failed: %v", err)
	}
	if isAnc {
		t.Error("non-fork-sync merge unexpectedly made upstream an ancestor of HEAD")
	}
}

// TestDoMerge_ForkSync_BypassesRebaseCheckGate is the regression test for
// gu-ofsg. Before the fix, a fork-sync MR would have its rebase-check gate
// (scripts/check-upstream-rebased.sh) fail pre-merge because origin/main is
// behind upstream/main — but the MR's whole purpose is to fix exactly that
// invariant. The gate blocked its own resolution, deadlocking the MR until
// a human force-pushed.
//
// After the fix, doMerge detects the fork-sync pattern before running
// pre-merge gates and skips rebase-check for that run. Other pre-merge
// gates still run as normal. After the no-ff merge, doMerge verifies the
// invariant rebase-check would have checked (upstream is ancestor of HEAD),
// so we don't push a result that violates it.
func TestDoMerge_ForkSync_BypassesRebaseCheckGate(t *testing.T) {
	workDir, g := testForkSyncRepo(t)

	// Pre-condition: upstream is NOT yet an ancestor of origin/main. This
	// is the state in which scripts/check-upstream-rebased.sh would fail
	// when run against origin/main.
	if isAnc, err := g.IsAncestor("upstream/main", "origin/main"); err != nil {
		t.Fatalf("pre-check IsAncestor failed: %v", err)
	} else if isAnc {
		t.Fatal("fixture bug: upstream/main is already an ancestor of origin/main")
	}

	// Build the fork-sync polecat branch (HEAD merges upstream).
	run(t, workDir, "git", "checkout", "-b", "polecat/fork-sync", "main")
	run(t, workDir, "git", "merge", "--no-ff", "-m", "Sync fork from upstream", "upstream/main")
	run(t, workDir, "git", "push", "-u", "origin", "polecat/fork-sync")
	run(t, workDir, "git", "checkout", "main")

	e := newTestEngineer(t, workDir, g)

	// Configure the exact gate set described in the bead. rebase-check uses
	// the real script — same one that deadlocked in the gu-6k7h incident.
	// build / test / vet are simulated with `true` so the test exercises
	// the bypass logic, not the real Go toolchain.
	scriptPath := mustResolveCheckUpstreamScript(t)
	otherGateRan := filepath.Join(t.TempDir(), "other-gate-ran")
	e.config.Gates = map[string]*GateConfig{
		"rebase-check": {Cmd: scriptPath, Phase: GatePhasePreMerge},
		"build":        {Cmd: "touch " + otherGateRan, Phase: GatePhasePreMerge},
	}

	result := e.doMerge(context.Background(), "polecat/fork-sync", "main", "gu-ofsg")
	if !result.Success {
		t.Fatalf("doMerge unexpectedly failed: conflict=%v error=%s", result.Conflict, result.Error)
	}

	// The other pre-merge gate must still have run — bypass is targeted,
	// not a wholesale skip.
	if _, err := os.Stat(otherGateRan); os.IsNotExist(err) {
		t.Error("non-rebase-check pre-merge gate did NOT run; bypass over-skipped")
	}

	// And the post-merge invariant rebase-check would check is now satisfied:
	// upstream/main is an ancestor of HEAD on the merged target.
	isAnc, err := g.IsAncestor("upstream/main", "HEAD")
	if err != nil {
		t.Fatalf("post-merge IsAncestor failed: %v", err)
	}
	if !isAnc {
		t.Fatal("post-merge: upstream/main is NOT an ancestor of HEAD — invariant violated")
	}
}

// TestDoMerge_NonForkSync_RebaseCheckStillRuns guards against the bypass
// over-firing on ordinary code MRs. A plain feature branch in a fork repo
// must still have rebase-check enforced — that's the gate's whole purpose
// for non-rebase MRs.
func TestDoMerge_NonForkSync_RebaseCheckStillRuns(t *testing.T) {
	workDir, g := testForkSyncRepo(t)

	// Plain feature branch — does NOT merge upstream.
	run(t, workDir, "git", "checkout", "-b", "polecat/plain-feature", "main")
	writeFile(t, workDir, "plain.md", "# Plain feature\n")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat: add plain feature")
	run(t, workDir, "git", "push", "-u", "origin", "polecat/plain-feature")
	run(t, workDir, "git", "checkout", "main")

	e := newTestEngineer(t, workDir, g)
	scriptPath := mustResolveCheckUpstreamScript(t)
	e.config.Gates = map[string]*GateConfig{
		"rebase-check": {Cmd: scriptPath, Phase: GatePhasePreMerge},
	}

	result := e.doMerge(context.Background(), "polecat/plain-feature", "main", "gt-plain")
	if result.Success {
		t.Fatal("doMerge should have failed: rebase-check must still block non-fork-sync MRs when fork is behind upstream")
	}
	if !strings.Contains(result.Error, "rebase-check") {
		t.Errorf("expected error to mention rebase-check, got: %s", result.Error)
	}
}

// mustResolveCheckUpstreamScript locates scripts/check-upstream-rebased.sh
// relative to the test binary's source dir. The script is part of the repo
// and is what real rigs invoke for rebase-check.
func mustResolveCheckUpstreamScript(t *testing.T) string {
	t.Helper()
	// Walk up from the test file's directory to find the repo root, then
	// append scripts/check-upstream-rebased.sh.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	dir := wd
	for i := 0; i < 6; i++ {
		candidate := filepath.Join(dir, "scripts", "check-upstream-rebased.sh")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate scripts/check-upstream-rebased.sh starting from %s", wd)
	return ""
}
