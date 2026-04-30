package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckpointDogInterval_Default(t *testing.T) {
	interval := checkpointDogInterval(nil)
	if interval != defaultCheckpointDogInterval {
		t.Errorf("expected default interval %v, got %v", defaultCheckpointDogInterval, interval)
	}
}

func TestCheckpointDogInterval_NilPatrols(t *testing.T) {
	config := &DaemonPatrolConfig{}
	interval := checkpointDogInterval(config)
	if interval != defaultCheckpointDogInterval {
		t.Errorf("expected default interval %v, got %v", defaultCheckpointDogInterval, interval)
	}
}

func TestCheckpointDogInterval_NilCheckpointDog(t *testing.T) {
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{},
	}
	interval := checkpointDogInterval(config)
	if interval != defaultCheckpointDogInterval {
		t.Errorf("expected default interval %v, got %v", defaultCheckpointDogInterval, interval)
	}
}

func TestCheckpointDogInterval_Configured(t *testing.T) {
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CheckpointDog: &CheckpointDogConfig{
				Enabled:     true,
				IntervalStr: "5m",
			},
		},
	}
	interval := checkpointDogInterval(config)
	if interval != 5*time.Minute {
		t.Errorf("expected 5m, got %v", interval)
	}
}

func TestCheckpointDogInterval_InvalidFallsBack(t *testing.T) {
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CheckpointDog: &CheckpointDogConfig{
				Enabled:     true,
				IntervalStr: "not-a-duration",
			},
		},
	}
	interval := checkpointDogInterval(config)
	if interval != defaultCheckpointDogInterval {
		t.Errorf("expected default interval for invalid config, got %v", interval)
	}
}

func TestCheckpointDogInterval_ZeroFallsBack(t *testing.T) {
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CheckpointDog: &CheckpointDogConfig{
				Enabled:     true,
				IntervalStr: "0s",
			},
		},
	}
	interval := checkpointDogInterval(config)
	if interval != defaultCheckpointDogInterval {
		t.Errorf("expected default interval for zero config, got %v", interval)
	}
}

func TestCheckpointDogEnabled(t *testing.T) {
	// Nil config → disabled (opt-in patrol)
	if IsPatrolEnabled(nil, "checkpoint_dog") {
		t.Error("expected checkpoint_dog disabled for nil config")
	}

	// Explicitly enabled
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CheckpointDog: &CheckpointDogConfig{
				Enabled: true,
			},
		},
	}
	if !IsPatrolEnabled(config, "checkpoint_dog") {
		t.Error("expected checkpoint_dog enabled")
	}

	// Explicitly disabled
	config.Patrols.CheckpointDog.Enabled = false
	if IsPatrolEnabled(config, "checkpoint_dog") {
		t.Error("expected checkpoint_dog disabled when Enabled=false")
	}
}

// --- gu-y3r: gate WIP commits when polecat has no real edits ---------------
//
// These tests exercise the two helpers the checkpoint gate relies on:
// - noNewTrackedChangesVsHEAD: is the tracked-file diff vs HEAD empty?
// - headIsWIPCheckpoint:       is HEAD a "WIP: checkpoint (auto)" commit?
//
// Together they reproduce the two acceptance scenarios from the bead:
// 1. Worktree with no tracked diff since the last WIP commit → SKIP.
// 2. Worktree with real uncommitted tracked changes          → checkpoint.
//
// Because checkpointWorktree itself shells out to git, session.tmux, and the
// daemon logger, it is not easy to drive end-to-end in a unit test. The
// gate's correctness reduces to the correctness of the two helpers, which
// these tests cover directly. The existing guardDaemonCommit tests in
// commit_guard_test.go use the same local-git scaffolding pattern.

// seedWorktreeWithWIP returns a worktree whose HEAD is a
// "WIP: checkpoint (auto)" commit and whose tracked files match HEAD.
func seedWorktreeWithWIP(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	gitRun(t, "", "init", "-b", "main", repo)

	// Seed with a real file + commit so HEAD has a tree to diff against.
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("write file.txt: %v", err)
	}
	gitRun(t, repo, "add", "file.txt")
	gitRun(t, repo, "commit", "-m", "initial")

	// Modify the file and create a WIP commit on top so HEAD matches the
	// pattern checkpoint_dog would produce.
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("rewrite file.txt: %v", err)
	}
	gitRun(t, repo, "add", "file.txt")
	gitRun(t, repo, "commit", "-m", "WIP: checkpoint (auto)")

	return repo
}

// TestNoNewTrackedChangesVsHEAD_Clean: tracked files match HEAD → true.
func TestNoNewTrackedChangesVsHEAD_Clean(t *testing.T) {
	repo := seedWorktreeWithWIP(t)

	if !noNewTrackedChangesVsHEAD(repo) {
		t.Errorf("expected noNewTrackedChangesVsHEAD=true when tracked files match HEAD")
	}
}

// TestNoNewTrackedChangesVsHEAD_TrackedEdit: a modification to a tracked file
// flips the result to false — the current checkpoint behavior must still fire.
func TestNoNewTrackedChangesVsHEAD_TrackedEdit(t *testing.T) {
	repo := seedWorktreeWithWIP(t)

	// Edit the tracked file.
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("v3\n"), 0o644); err != nil {
		t.Fatalf("rewrite file.txt: %v", err)
	}

	if noNewTrackedChangesVsHEAD(repo) {
		t.Errorf("expected noNewTrackedChangesVsHEAD=false after tracked-file edit")
	}
}

// TestNoNewTrackedChangesVsHEAD_UntrackedOnly: untracked files alone do not
// count as tracked-file changes. This is what distinguishes the "idle polecat
// with ephemeral churn" case from real work.
func TestNoNewTrackedChangesVsHEAD_UntrackedOnly(t *testing.T) {
	repo := seedWorktreeWithWIP(t)

	// Drop an untracked file — simulates runtime churn the exclusion list
	// does not cover. `git diff HEAD` ignores untracked files.
	if err := os.WriteFile(filepath.Join(repo, "untracked.log"), []byte("noise\n"), 0o644); err != nil {
		t.Fatalf("write untracked.log: %v", err)
	}

	if !noNewTrackedChangesVsHEAD(repo) {
		t.Errorf("expected noNewTrackedChangesVsHEAD=true with only untracked noise")
	}
}

// TestHeadIsWIPCheckpoint_True: HEAD subject is the exact WIP prefix.
func TestHeadIsWIPCheckpoint_True(t *testing.T) {
	repo := seedWorktreeWithWIP(t)

	if !headIsWIPCheckpoint(repo) {
		t.Errorf("expected headIsWIPCheckpoint=true when HEAD is a WIP checkpoint")
	}
}

// TestHeadIsWIPCheckpoint_False: HEAD is an ordinary (non-WIP) commit →
// the gate must not fire and a real checkpoint can proceed.
func TestHeadIsWIPCheckpoint_False(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	gitRun(t, "", "init", "-b", "main", repo)
	gitRun(t, repo, "commit", "--allow-empty", "-m", "feat: real work")

	if headIsWIPCheckpoint(repo) {
		t.Errorf("expected headIsWIPCheckpoint=false for a real commit")
	}
}

// TestHeadIsWIPCheckpoint_EmptyRepo: a brand-new repo with no commits must
// not panic and must return false (the checkpoint path can then proceed,
// which is the safer default).
func TestHeadIsWIPCheckpoint_EmptyRepo(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	gitRun(t, "", "init", "-b", "main", repo)

	if headIsWIPCheckpoint(repo) {
		t.Errorf("expected headIsWIPCheckpoint=false for an empty repo")
	}
}

// TestCheckpointGate_SkipsOnIdleWIPHead is the primary acceptance test:
// when tracked files match HEAD AND HEAD is a WIP checkpoint, the gate
// should trigger (both helpers agree → skip).
func TestCheckpointGate_SkipsOnIdleWIPHead(t *testing.T) {
	repo := seedWorktreeWithWIP(t)

	if !(noNewTrackedChangesVsHEAD(repo) && headIsWIPCheckpoint(repo)) {
		t.Fatalf("expected gate to fire (skip) on idle WIP head: "+
			"noNewTrackedChangesVsHEAD=%v headIsWIPCheckpoint=%v",
			noNewTrackedChangesVsHEAD(repo), headIsWIPCheckpoint(repo))
	}
}

// TestCheckpointGate_ProceedsOnRealEdit is the other acceptance test:
// an uncommitted tracked-file modification must not trigger the gate,
// preserving checkpoint_dog's primary duty of saving in-flight work.
func TestCheckpointGate_ProceedsOnRealEdit(t *testing.T) {
	repo := seedWorktreeWithWIP(t)

	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("v3\n"), 0o644); err != nil {
		t.Fatalf("rewrite file.txt: %v", err)
	}

	// Gate must NOT fire: noNewTrackedChangesVsHEAD is now false.
	if noNewTrackedChangesVsHEAD(repo) && headIsWIPCheckpoint(repo) {
		t.Errorf("expected gate NOT to fire when tracked file is modified")
	}
}

func TestResolveCheckpointWorkDir_NestedLayout(t *testing.T) {
	// New polecat layout: polecats/<name>/<rigName>/.git is the worktree.
	tmp := t.TempDir()
	rig := "myrig"
	polecat := "alice"
	polecatsDir := filepath.Join(tmp, "polecats")
	worktree := filepath.Join(polecatsDir, polecat, rig)
	if err := os.MkdirAll(filepath.Join(worktree, ".git"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got := resolveCheckpointWorkDir(polecatsDir, polecat, rig)
	if got != worktree {
		t.Errorf("got %q, want %q", got, worktree)
	}
}

func TestResolveCheckpointWorkDir_LegacyFlatLayout(t *testing.T) {
	// Legacy layout: polecats/<name>/.git directly. polecat.Manager still
	// recognizes this; checkpoint_dog must too rather than silently skip.
	tmp := t.TempDir()
	rig := "myrig"
	polecat := "bob"
	polecatsDir := filepath.Join(tmp, "polecats")
	worktree := filepath.Join(polecatsDir, polecat)
	if err := os.MkdirAll(filepath.Join(worktree, ".git"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got := resolveCheckpointWorkDir(polecatsDir, polecat, rig)
	if got != worktree {
		t.Errorf("got %q, want %q (legacy flat layout)", got, worktree)
	}
}

func TestResolveCheckpointWorkDir_NoGitNeitherLevel(t *testing.T) {
	// Critical regression case: polecat container exists but has no .git
	// at either level. Function MUST return "" so the caller skips, NOT
	// fall back to a parent dir (which would have the workspace's .git
	// and cause the wrong-branch checkpoint bug this code prevents).
	tmp := t.TempDir()
	rig := "myrig"
	polecat := "carol"
	polecatsDir := filepath.Join(tmp, "polecats")
	if err := os.MkdirAll(filepath.Join(polecatsDir, polecat, rig), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Simulate top-level workspace .git that git would walk up to find.
	// resolveCheckpointWorkDir must NOT return a path that lets git walk
	// to this — it should return "" so the caller skips entirely.
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
		t.Fatalf("setup parent .git: %v", err)
	}
	got := resolveCheckpointWorkDir(polecatsDir, polecat, rig)
	if got != "" {
		t.Errorf("got %q, want empty (skip — no polecat-level .git)", got)
	}
}

func TestResolveCheckpointWorkDir_PrefersNestedOverFlat(t *testing.T) {
	// If both levels have .git (transitional state during a migration),
	// prefer the nested (newer) layout.
	tmp := t.TempDir()
	rig := "myrig"
	polecat := "dave"
	polecatsDir := filepath.Join(tmp, "polecats")
	flat := filepath.Join(polecatsDir, polecat)
	nested := filepath.Join(flat, rig)
	for _, d := range []string{flat, nested} {
		if err := os.MkdirAll(filepath.Join(d, ".git"), 0o755); err != nil {
			t.Fatalf("setup %s: %v", d, err)
		}
	}
	got := resolveCheckpointWorkDir(polecatsDir, polecat, rig)
	if got != nested {
		t.Errorf("got %q, want nested %q", got, nested)
	}
}

func TestIsGitWorktree(t *testing.T) {
	tmp := t.TempDir()
	if isGitWorktree(tmp) {
		t.Error("empty dir should not be a worktree")
	}
	// .git as directory (full clone)
	dirGit := filepath.Join(tmp, "fullclone")
	if err := os.MkdirAll(filepath.Join(dirGit, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !isGitWorktree(dirGit) {
		t.Error(".git directory should count as worktree")
	}
	// .git as file (linked worktree — git uses a file pointing to commondir)
	fileGit := filepath.Join(tmp, "linked")
	if err := os.MkdirAll(fileGit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fileGit, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isGitWorktree(fileGit) {
		t.Error(".git file (linked worktree) should count as worktree")
	}
}
