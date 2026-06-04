package completion

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/steveyegge/gastown/internal/git"
)

func TestResolveRigName(t *testing.T) {
	townRoot := filepath.Join("home", "agent", "gt")
	tests := []struct {
		name    string
		cwd     string
		envRig  string
		want    string
		wantErr bool
	}{
		{
			name: "cwd-derived rig (polecat worktree)",
			cwd:  filepath.Join(townRoot, "vets", "polecats", "slit", "vets"),
			want: "vets",
		},
		{
			name: "cwd-derived rig (rig root)",
			cwd:  filepath.Join(townRoot, "vets", "mayor", "rig"),
			want: "vets",
		},
		{
			name:   "GT_RIG wins over cwd-derived",
			cwd:    filepath.Join(townRoot, "mayor", "rig"),
			envRig: "vets",
			want:   "vets",
		},
		{
			name:   "GT_RIG used when cwd empty (deleted worktree)",
			cwd:    "",
			envRig: "vets",
			want:   "vets",
		},
		{
			name:    "no rig derivable (cwd is town root, no GT_RIG)",
			cwd:     townRoot,
			wantErr: true,
		},
		{
			name:    "no rig derivable (empty cwd, no GT_RIG)",
			cwd:     "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveRigName(townRoot, tt.cwd, tt.envRig)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got rig=%q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("ResolveRigName = %q, want %q", got, tt.want)
			}
		})
	}
}

// newBranchRepo builds a temp git repo on a named branch with one commit and
// returns a *git.Git pointed at it.
func newBranchRepo(t *testing.T, branch string) (*gitpkg.Git, string) {
	t.Helper()
	dir := t.TempDir()
	testRunGit(t, dir, "init", "-q", "-b", branch)
	testRunGit(t, dir, "config", "user.email", "test@test.com")
	testRunGit(t, dir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	testRunGit(t, dir, "add", "-A")
	testRunGit(t, dir, "commit", "-q", "-m", "init")
	return gitpkg.NewGit(dir), dir
}

func TestResolveBranch_Normal(t *testing.T) {
	g, _ := newBranchRepo(t, "polecat/foo--abc123")
	got, err := ResolveBranch(g, true, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "polecat/foo--abc123" {
		t.Errorf("got %q, want polecat/foo--abc123", got)
	}
}

func TestResolveBranch_CwdUnavailable(t *testing.T) {
	// When cwd is gone, GT_BRANCH is the only source; git is never consulted.
	g, _ := newBranchRepo(t, "main")

	got, err := ResolveBranch(g, false, "polecat/saved--xyz", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "polecat/saved--xyz" {
		t.Errorf("got %q, want polecat/saved--xyz", got)
	}

	// No GT_BRANCH and cwd gone → hard error (must not fall back to the
	// mayor-clone branch).
	if _, err := ResolveBranch(g, false, "", "slit"); err == nil {
		t.Error("expected error when cwd unavailable and GT_BRANCH unset")
	}
}

func TestResolveBranch_DetachedHEAD(t *testing.T) {
	build := func(t *testing.T) *gitpkg.Git {
		g, dir := newBranchRepo(t, "polecat/foo--abc123")
		// Detach HEAD so CurrentBranch() reports the literal "HEAD".
		testRunGit(t, dir, "checkout", "-q", "--detach")
		return g
	}

	t.Run("salvage from GT_BRANCH", func(t *testing.T) {
		g := build(t)
		got, err := ResolveBranch(g, true, "polecat/foo--abc123", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "polecat/foo--abc123" {
			t.Errorf("got %q, want polecat/foo--abc123", got)
		}
	})

	t.Run("salvage from GT_POLECAT", func(t *testing.T) {
		g := build(t)
		got, err := ResolveBranch(g, true, "", "slit")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "polecat/slit" {
			t.Errorf("got %q, want polecat/slit", got)
		}
	})

	t.Run("no salvage → error", func(t *testing.T) {
		g := build(t)
		_, err := ResolveBranch(g, true, "", "")
		if err == nil {
			t.Fatal("expected error on detached HEAD with no fallback")
		}
		if !strings.Contains(err.Error(), "detached HEAD") {
			t.Errorf("error = %v, want mention of detached HEAD", err)
		}
	})

	t.Run("GT_BRANCH=HEAD is not salvaged", func(t *testing.T) {
		// A literal "HEAD" in GT_BRANCH must not be propagated.
		g := build(t)
		_, err := ResolveBranch(g, true, "HEAD", "")
		if err == nil {
			t.Fatal("expected error when GT_BRANCH is the literal HEAD with no other fallback")
		}
	})
}
