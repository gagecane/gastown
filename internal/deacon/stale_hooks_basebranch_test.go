package deacon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDeaconRigBaseRef(t *testing.T) {
	town := t.TempDir()
	writeRigConfig(t, town, "lia_bac", `{"default_branch":"gagecane/gt"}`)
	writeRigConfig(t, town, "gastown", `{"default_branch":"main"}`)
	writeRigConfig(t, town, "norigbranch", `{}`)

	cases := []struct {
		assignee string
		want     string
	}{
		{"lia_bac/polecats/furiosa", "origin/gagecane/gt"},
		{"gastown/polecats/slit", "origin/main"},
		{"norigbranch/polecats/x", ""}, // unset default_branch
		{"missing/polecats/x", ""},     // no config.json
		{"", ""},
	}
	for _, tc := range cases {
		if got := deaconRigBaseRef(town, tc.assignee); got != tc.want {
			t.Errorf("deaconRigBaseRef(%q) = %q, want %q", tc.assignee, got, tc.want)
		}
	}
}

// TestCheckWorktreeState_IntegrationBase proves hq-q943s: a polecat whose work
// is already on the rig's integration base (gagecane/gt) — but ahead of
// origin/main — is NOT flagged as having unpushed work needing recovery; one
// that has a commit NOT on the base still is.
func TestCheckWorktreeState_IntegrationBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	town := t.TempDir()
	writeRigConfig(t, town, "lia_bac", `{"default_branch":"gagecane/gt"}`)

	t.Run("work on origin/gagecane/gt is NOT partial work", func(t *testing.T) {
		repo := makePolecatWorktree(t, town, "furiosa")
		// C1 (the polecat's work) lands on the integration branch.
		runGitCmd(t, repo, "checkout", "-b", "polecat/furiosa/lb-f1gv.6", "main")
		write(t, repo, "ux.txt", "ux\n")
		runGitCmd(t, repo, "add", "ux.txt")
		runGitCmd(t, repo, "commit", "-m", "ux work")
		runGitCmd(t, repo, "update-ref", "refs/remotes/origin/gagecane/gt", "HEAD")
		// origin/main stays behind at C0, so vs main this looks "1 unpushed".

		var res StaleHookResult
		checkWorktreeState(town, "lia_bac/polecats/furiosa", &res)
		if res.PartialWork {
			t.Errorf("work already on gagecane/gt must NOT be partial work; UnpushedCount=%d", res.UnpushedCount)
		}
		if res.UnpushedCount != 0 {
			t.Errorf("UnpushedCount = %d, want 0 (work is on the rig base)", res.UnpushedCount)
		}
	})

	t.Run("commit not on the base IS partial work", func(t *testing.T) {
		repo := makePolecatWorktree(t, town, "rictus")
		runGitCmd(t, repo, "checkout", "-b", "polecat/rictus/lb-x", "main")
		// origin/gagecane/gt exists but does NOT contain this commit.
		runGitCmd(t, repo, "update-ref", "refs/remotes/origin/gagecane/gt", "main")
		write(t, repo, "real.txt", "real\n")
		runGitCmd(t, repo, "add", "real.txt")
		runGitCmd(t, repo, "commit", "-m", "genuinely unmerged work")

		var res StaleHookResult
		checkWorktreeState(town, "lia_bac/polecats/rictus", &res)
		if !res.PartialWork || res.UnpushedCount == 0 {
			t.Errorf("a commit not on the base must be flagged: PartialWork=%v UnpushedCount=%d", res.PartialWork, res.UnpushedCount)
		}
	})

	// hq-q943s third class (the gs-4lk case): the polecat's patch was landed on
	// the base via a REBASE/SQUASH, so it exists on the base under a DIFFERENT
	// SHA. A SHA-ancestry check (merge-base --is-ancestor) would call this
	// stranded; the deacon's git-cherry (patch-id) preservation check must not.
	t.Run("rebased/squashed onto base under a different SHA is NOT partial work", func(t *testing.T) {
		repo := makePolecatWorktree(t, town, "slit")
		// The landed copy on the base: same patch, distinct commit.
		runGitCmd(t, repo, "checkout", "-b", "based", "main")
		write(t, repo, "fix.txt", "the fix\n")
		runGitCmd(t, repo, "add", "fix.txt")
		runGitCmd(t, repo, "commit", "-m", "landed via refinery rebase")
		runGitCmd(t, repo, "update-ref", "refs/remotes/origin/gagecane/gt", "HEAD")
		// The polecat's original commit: identical patch from the same base C0,
		// different message → different SHA, same patch-id.
		runGitCmd(t, repo, "checkout", "-b", "polecat/slit/gs-4lk", "main")
		write(t, repo, "fix.txt", "the fix\n")
		runGitCmd(t, repo, "add", "fix.txt")
		runGitCmd(t, repo, "commit", "-m", "original polecat commit (pre-rebase)")

		var res StaleHookResult
		checkWorktreeState(town, "lia_bac/polecats/slit", &res)
		if res.PartialWork {
			t.Errorf("a patch already on the base under a different SHA must NOT be partial work; UnpushedCount=%d", res.UnpushedCount)
		}
	})
}

func writeRigConfig(t *testing.T, town, rig, json string) {
	t.Helper()
	dir := filepath.Join(town, rig)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(json), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// makePolecatWorktree creates townRoot/lia_bac/polecats/<name>/lia_bac as a git
// repo with an initial commit and origin/main pinned to it, returning its path.
func makePolecatWorktree(t *testing.T, town, name string) string {
	t.Helper()
	repo := filepath.Join(town, "lia_bac", "polecats", name, "lia_bac")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	runGitCmd(t, repo, "init", "-q")
	runGitCmd(t, repo, "checkout", "-b", "main")
	runGitCmd(t, repo, "config", "user.email", "test@example.com")
	runGitCmd(t, repo, "config", "user.name", "test")
	runGitCmd(t, repo, "config", "commit.gpgsign", "false")
	write(t, repo, "README.md", "hi\n")
	runGitCmd(t, repo, "add", "README.md")
	runGitCmd(t, repo, "commit", "-m", "init")
	runGitCmd(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	return repo
}

func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
