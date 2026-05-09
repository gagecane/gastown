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
