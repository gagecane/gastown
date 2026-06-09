package cmd

// gu-oedcu: tests for the clean non-fast-forward merge path.
//
// These exercise the gitMergeUpstream helper directly with a real git
// fixture. The full runUpstreamSync flow is not unit-tested here — it
// requires town/beads/state-bead scaffolding that the rest of the
// upstream-sync test suite exercises through integration tests. The
// surface this file covers is the new bit: that gitMergeUpstream
// produces a real merge commit on a clean divergent fork, and that it
// aborts the merge cleanly when conflicts surface despite the
// caller's earlier merge-tree probe.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
)

// syncMergeGit runs a git command in dir and t.Fatals on failure. Mirrors
// the helper used in internal/refinery/batch_test.go but local so this
// package's test binary doesn't need to import refinery internals.
func syncMergeGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeSyncMergeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// makeForkScenario builds a divergent fork where origin/main and
// upstream/main share a common ancestor and have each added their own
// commit on disjoint files (so a merge is non-FF but conflict-free).
//
// Returns the path to a working clone with origin + upstream remotes
// configured and both fetched.
func makeForkScenario(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	upstreamBare := filepath.Join(tmp, "upstream.git")
	originBare := filepath.Join(tmp, "origin.git")
	seed := filepath.Join(tmp, "seed")
	work := filepath.Join(tmp, "work")

	syncMergeGit(t, tmp, "init", "--bare", "--initial-branch=main", upstreamBare)
	syncMergeGit(t, tmp, "init", "--bare", "--initial-branch=main", originBare)

	syncMergeGit(t, tmp, "init", "--initial-branch=main", seed)
	syncMergeGit(t, seed, "config", "user.email", "test@test.com")
	syncMergeGit(t, seed, "config", "user.name", "Test")
	writeSyncMergeFile(t, seed, "README.md", "# base\n")
	syncMergeGit(t, seed, "add", ".")
	syncMergeGit(t, seed, "commit", "-m", "seed: shared base")
	syncMergeGit(t, seed, "remote", "add", "upstream", upstreamBare)
	syncMergeGit(t, seed, "remote", "add", "origin", originBare)
	syncMergeGit(t, seed, "push", "upstream", "main")
	syncMergeGit(t, seed, "push", "origin", "main")

	// upstream-only commit (touches a file the fork hasn't touched).
	upstage := filepath.Join(tmp, "upstage")
	syncMergeGit(t, tmp, "clone", upstreamBare, upstage)
	syncMergeGit(t, upstage, "config", "user.email", "u@test.com")
	syncMergeGit(t, upstage, "config", "user.name", "U")
	writeSyncMergeFile(t, upstage, "upstream_only.md", "# upstream\n")
	syncMergeGit(t, upstage, "add", ".")
	syncMergeGit(t, upstage, "commit", "-m", "upstream: feature")
	syncMergeGit(t, upstage, "push", "origin", "main")

	// Working clone of origin with upstream registered.
	syncMergeGit(t, tmp, "clone", originBare, work)
	syncMergeGit(t, work, "config", "user.email", "f@test.com")
	syncMergeGit(t, work, "config", "user.name", "F")
	syncMergeGit(t, work, "remote", "add", "upstream", upstreamBare)
	syncMergeGit(t, work, "fetch", "upstream")

	// Fork-only commit on a different file so the merge is clean.
	writeSyncMergeFile(t, work, "fork_only.md", "# fork\n")
	syncMergeGit(t, work, "add", ".")
	syncMergeGit(t, work, "commit", "-m", "fork: feature")
	syncMergeGit(t, work, "push", "origin", "main")

	return work
}

// TestGitMergeUpstream_Clean covers the gu-oedcu happy path: a divergent
// fork with no conflicting files. The merge must succeed, leave
// upstream/main as an ancestor of HEAD (so the rebase-check gate goes
// green), and produce a 2-parent merge commit (so refinery's fork-sync
// topology preservation continues to work end-to-end).
func TestGitMergeUpstream_Clean(t *testing.T) {
	work := makeForkScenario(t)
	cfg := &config.UpstreamSyncConfig{Enabled: true}

	if err := gitMergeUpstream(work, cfg); err != nil {
		t.Fatalf("gitMergeUpstream returned error: %v", err)
	}

	// HEAD must now have upstream/main in its ancestry.
	if err := exec.Command("git", "-C", work, "merge-base",
		"--is-ancestor", "upstream/main", "HEAD").Run(); err != nil {
		t.Fatalf("upstream/main is not an ancestor of HEAD after clean merge: %v", err)
	}

	// The merge commit must have two parents (origin's tip + upstream's tip).
	parents := strings.Fields(syncMergeGit(t, work, "log", "-1", "--format=%P", "HEAD"))
	if len(parents) != 2 {
		t.Errorf("expected 2-parent merge commit, got %d: %v", len(parents), parents)
	}

	// Both files must be present (no content was lost on either side).
	for _, f := range []string{"fork_only.md", "upstream_only.md"} {
		if _, err := os.Stat(filepath.Join(work, f)); err != nil {
			t.Errorf("file %s missing after merge: %v", f, err)
		}
	}
}

// TestGitMergeUpstream_AbortsOnConflict covers the safety net: if a
// conflict surfaces despite the caller's earlier merge-tree probe
// (clock skew, concurrent upstream push, attribute-driven merge driver,
// etc.), gitMergeUpstream must abort the merge so the working tree is
// left clean and the next attempt has a sane starting point.
func TestGitMergeUpstream_AbortsOnConflict(t *testing.T) {
	tmp := t.TempDir()
	upstreamBare := filepath.Join(tmp, "upstream.git")
	originBare := filepath.Join(tmp, "origin.git")
	seed := filepath.Join(tmp, "seed")
	work := filepath.Join(tmp, "work")

	syncMergeGit(t, tmp, "init", "--bare", "--initial-branch=main", upstreamBare)
	syncMergeGit(t, tmp, "init", "--bare", "--initial-branch=main", originBare)
	syncMergeGit(t, tmp, "init", "--initial-branch=main", seed)
	syncMergeGit(t, seed, "config", "user.email", "test@test.com")
	syncMergeGit(t, seed, "config", "user.name", "Test")
	writeSyncMergeFile(t, seed, "shared.txt", "line1\nline2\nline3\n")
	syncMergeGit(t, seed, "add", ".")
	syncMergeGit(t, seed, "commit", "-m", "seed")
	syncMergeGit(t, seed, "remote", "add", "upstream", upstreamBare)
	syncMergeGit(t, seed, "remote", "add", "origin", originBare)
	syncMergeGit(t, seed, "push", "upstream", "main")
	syncMergeGit(t, seed, "push", "origin", "main")

	// Both sides edit the same line — guaranteed conflict.
	upstage := filepath.Join(tmp, "upstage")
	syncMergeGit(t, tmp, "clone", upstreamBare, upstage)
	syncMergeGit(t, upstage, "config", "user.email", "u@test.com")
	syncMergeGit(t, upstage, "config", "user.name", "U")
	writeSyncMergeFile(t, upstage, "shared.txt", "line1\nUPSTREAM\nline3\n")
	syncMergeGit(t, upstage, "add", ".")
	syncMergeGit(t, upstage, "commit", "-m", "upstream: edit")
	syncMergeGit(t, upstage, "push", "origin", "main")

	syncMergeGit(t, tmp, "clone", originBare, work)
	syncMergeGit(t, work, "config", "user.email", "f@test.com")
	syncMergeGit(t, work, "config", "user.name", "F")
	syncMergeGit(t, work, "remote", "add", "upstream", upstreamBare)
	syncMergeGit(t, work, "fetch", "upstream")
	writeSyncMergeFile(t, work, "shared.txt", "line1\nFORK\nline3\n")
	syncMergeGit(t, work, "add", ".")
	syncMergeGit(t, work, "commit", "-m", "fork: edit")

	cfg := &config.UpstreamSyncConfig{Enabled: true}
	err := gitMergeUpstream(work, cfg)
	if err == nil {
		t.Fatal("gitMergeUpstream returned nil error on conflict; expected merge failure")
	}

	// The merge must have been aborted. `git status --porcelain` should
	// show a clean tree, and there must be no MERGE_HEAD ref.
	status := syncMergeGit(t, work, "status", "--porcelain")
	if status != "" {
		t.Errorf("working tree not clean after aborted merge:\n%s", status)
	}
	if _, statErr := os.Stat(filepath.Join(work, ".git", "MERGE_HEAD")); statErr == nil {
		t.Error(".git/MERGE_HEAD still exists; merge was not aborted")
	}
}
