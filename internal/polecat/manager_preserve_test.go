package polecat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
)

// TestPreserveUnpushedHead proves the hq-kpodq teardown invariant: a worktree's
// unpushed HEAD commit is anchored to a GC-safe ref in the shared object store
// before removal (so detached-HEAD / merge=local / failed-push work survives),
// while a commit already on the base branch is left alone (no ref clutter).
func TestPreserveUnpushedHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}

	gitRun := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// setup builds a shared object store on `main` and a linked worktree, and
	// returns (manager, storePath, worktreePath). The worktree shares the
	// store's objects so an anchored ref keeps the worktree commit reachable.
	setup := func(t *testing.T) (*Manager, string, string) {
		t.Helper()
		root := t.TempDir()
		store := filepath.Join(root, "store")
		if err := os.MkdirAll(store, 0o755); err != nil {
			t.Fatal(err)
		}
		gitRun(t, store, "init", "-q", "-b", "main")
		gitRun(t, store, "config", "commit.gpgsign", "false")
		if err := os.WriteFile(filepath.Join(store, "README.md"), []byte("hi\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(t, store, "add", "README.md")
		gitRun(t, store, "commit", "-q", "-m", "init")

		wt := filepath.Join(root, "wt")
		gitRun(t, store, "worktree", "add", "-q", "--detach", wt, "main")

		r := &rig.Rig{Name: "rig", Path: root}
		return NewManager(r, git.NewGit(root), nil), store, wt
	}

	t.Run("unpushed detached-HEAD commit is anchored", func(t *testing.T) {
		m, store, wt := setup(t)

		// A commit that exists only in the worktree, on no branch and not on main.
		if err := os.WriteFile(filepath.Join(wt, "proto.txt"), []byte("prototype\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(t, wt, "add", "proto.txt")
		gitRun(t, wt, "commit", "-q", "-m", "merge=local prototype work")
		want := gitRun(t, wt, "rev-parse", "HEAD")

		m.preserveUnpushedHead("furiosa", wt, git.NewGit(store))

		ref := "refs/preserved/furiosa/" + want[:12]
		got := gitRun(t, store, "rev-parse", ref)
		if got != want {
			t.Errorf("anchored ref %s = %s, want %s", ref, got, want)
		}
	})

	t.Run("commit already on base is left alone", func(t *testing.T) {
		m, store, wt := setup(t)
		// HEAD == main tip, nothing unmerged.
		head := gitRun(t, wt, "rev-parse", "HEAD")

		m.preserveUnpushedHead("nux", wt, git.NewGit(store))

		out := gitRun(t, store, "for-each-ref", "refs/preserved/")
		if out != "" {
			t.Errorf("no ref should be anchored for already-merged HEAD %s; got:\n%s", head[:12], out)
		}
	})
}
