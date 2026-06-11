package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gu-weo4x: checkpoint_dog must snapshot uncommitted work to a backup ref
// WITHOUT touching the branch tip, index, or working tree. These tests drive
// snapshotToBackupRef directly — the same way the checkpoint_dog gate tests
// drive their helpers — since checkpointWorktree itself needs a live tmux
// session and daemon logger.

// seedRepoWithCommit creates a repo with one real commit on main and returns
// its path plus the branch-tip SHA.
func seedRepoWithCommit(t *testing.T) (string, string) {
	t.Helper()
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	gitRun(t, "", "init", "-b", "main", repo)
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("write file.txt: %v", err)
	}
	gitRun(t, repo, "add", "file.txt")
	gitRun(t, repo, "commit", "-m", "feat: initial")
	tip := gitRun(t, repo, "rev-parse", "HEAD")
	return repo, tip
}

// TestSnapshot_CreatesBackupRef_LeavesTipUntouched is the primary acceptance
// test: a real uncommitted edit is captured to a backup ref, and the branch
// tip, index, and working tree all stay exactly as the agent left them.
func TestSnapshot_CreatesBackupRef_LeavesTipUntouched(t *testing.T) {
	repo, tipBefore := seedRepoWithCommit(t)

	// Uncommitted tracked-file edit + a new untracked file (real work).
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("edit file.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("added\n"), 0o644); err != nil {
		t.Fatalf("write new.txt: %v", err)
	}

	ref, created, err := snapshotToBackupRef(repo, "myrig", "nitro")
	if err != nil {
		t.Fatalf("snapshotToBackupRef: %v", err)
	}
	if !created {
		t.Fatal("expected a new backup ref to be created")
	}
	if !strings.HasPrefix(ref, "refs/backup/myrig/nitro/") {
		t.Errorf("ref %q does not have expected backup namespace", ref)
	}

	// Branch tip must be unchanged.
	tipAfter := gitRun(t, repo, "rev-parse", "HEAD")
	if tipAfter != tipBefore {
		t.Errorf("branch tip moved: before=%s after=%s (must be untouched)", tipBefore, tipAfter)
	}

	// The backup ref must exist and resolve to a commit.
	backupSHA := gitRun(t, repo, "rev-parse", "--verify", ref)
	if backupSHA == "" {
		t.Fatal("backup ref does not resolve to a commit")
	}

	// The backup's tree must contain the edited content + new file.
	got := gitRun(t, repo, "show", ref+":file.txt")
	if strings.TrimSpace(got) != "v2" {
		t.Errorf("backup file.txt = %q, want v2", got)
	}
	got = gitRun(t, repo, "show", ref+":new.txt")
	if strings.TrimSpace(got) != "added" {
		t.Errorf("backup new.txt = %q, want added", got)
	}

	// Working tree must still hold the agent's in-progress edit.
	wt, err := os.ReadFile(filepath.Join(repo, "file.txt"))
	if err != nil {
		t.Fatalf("read worktree file.txt: %v", err)
	}
	if strings.TrimSpace(string(wt)) != "v2" {
		t.Errorf("worktree file.txt = %q, want v2 (must be untouched)", strings.TrimSpace(string(wt)))
	}

	// The real index must be untouched: new.txt should still be untracked
	// (snapshot used a temp index, never `git add`-ed into .git/index).
	status := gitRun(t, repo, "status", "--porcelain", "new.txt")
	if !strings.HasPrefix(status, "??") {
		t.Errorf("new.txt status = %q, want untracked (??) — real index was disturbed", status)
	}
}

// TestSnapshot_Idempotent: a second snapshot with identical content does not
// create a new ref (created=false), and the ref name is stable.
func TestSnapshot_Idempotent(t *testing.T) {
	repo, _ := seedRepoWithCommit(t)
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("edit file.txt: %v", err)
	}

	ref1, created1, err := snapshotToBackupRef(repo, "myrig", "nitro")
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	if !created1 {
		t.Fatal("first snapshot should create a ref")
	}

	ref2, created2, err := snapshotToBackupRef(repo, "myrig", "nitro")
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if created2 {
		t.Error("second identical snapshot should NOT create a new ref")
	}
	if ref1 != ref2 {
		t.Errorf("ref name not stable: %q vs %q", ref1, ref2)
	}
}

// TestSnapshot_OnlyExcludedChurn: when the only changes are runtime/ephemeral
// dirs (e.g. .claude/), the snapshot tree equals HEAD's tree and nothing is
// backed up.
func TestSnapshot_OnlyExcludedChurn(t *testing.T) {
	repo, _ := seedRepoWithCommit(t)

	claudeDir := filepath.Join(repo, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "session.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write churn: %v", err)
	}

	_, created, err := snapshotToBackupRef(repo, "myrig", "nitro")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if created {
		t.Error("expected no backup when only excluded runtime churn is present")
	}
}

// TestSnapshot_ExcludesTrackedDeletions: a deleted tracked file must NOT be
// recorded in the backup — the snapshot preserves work, never deletions.
func TestSnapshot_ExcludesTrackedDeletions(t *testing.T) {
	repo, _ := seedRepoWithCommit(t)

	// Add a second tracked file so we have a real edit to back up alongside
	// the deletion (otherwise the tree would equal HEAD and short-circuit).
	if err := os.WriteFile(filepath.Join(repo, "keep.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("write keep.txt: %v", err)
	}
	gitRun(t, repo, "add", "keep.txt")
	gitRun(t, repo, "commit", "-m", "feat: add keep.txt")

	// Now delete a tracked file and edit another in the worktree.
	if err := os.Remove(filepath.Join(repo, "file.txt")); err != nil {
		t.Fatalf("rm file.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "keep.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("edit keep.txt: %v", err)
	}

	ref, created, err := snapshotToBackupRef(repo, "myrig", "nitro")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !created {
		t.Fatal("expected backup for the keep.txt edit")
	}

	// file.txt must still be present in the backup tree (deletion not recorded).
	if got := gitRun(t, repo, "show", ref+":file.txt"); strings.TrimSpace(got) != "v1" {
		t.Errorf("backup file.txt = %q, want v1 (deletion must not be recorded)", got)
	}
	// keep.txt edit must be captured.
	if got := gitRun(t, repo, "show", ref+":keep.txt"); strings.TrimSpace(got) != "changed" {
		t.Errorf("backup keep.txt = %q, want changed", got)
	}
}

func TestSanitizeBackupRefComponent(t *testing.T) {
	cases := map[string]string{
		"myrig":            "myrig",
		"casc_cdk":         "casc_cdk",
		"polecat/nitro":    "polecat-nitro",
		"weird name!":      "weird-name",
		"":                 "unknown",
		"..":               "unknown",
		"--lead":           "lead",
		"gastown_upstream": "gastown_upstream",
	}
	for in, want := range cases {
		if got := sanitizeBackupRefComponent(in); got != want {
			t.Errorf("sanitizeBackupRefComponent(%q) = %q, want %q", in, got, want)
		}
	}
}
