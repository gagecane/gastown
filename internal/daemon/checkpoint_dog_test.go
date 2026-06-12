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

// --- gu-lxrbv: node_modules must be unstaged by the runtime exclude list ----
//
// casc_webapp polecat worktrees do not gitignore node_modules, and it commonly
// appears as a gitlink or symlink rather than a real directory. The auto-WIP
// checkpoint was committing a node_modules-only entry — pure junk that tripped
// the DIRTY recovery predicate. These tests assert the configured exclude list
// unstages node_modules in all three on-disk forms.
//
// applyRuntimeExcludes replicates the reset loop checkpointWorktree runs after
// `git add -A`, driving it off the production runtimeExcludeDirs list so the
// test fails if node_modules is dropped or reverts to a trailing-slash form
// (which does NOT unstage a symlink — the original bug).
func applyRuntimeExcludes(t *testing.T, repo string) {
	t.Helper()
	for _, dir := range runtimeExcludeDirs {
		gitRun(t, repo, "reset", "HEAD", "--", dir)
	}
}

func TestRuntimeExcludes_UnstagesNodeModulesSymlink(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	gitRun(t, "", "init", "-b", "main", repo)
	gitRun(t, repo, "commit", "--allow-empty", "-m", "init")

	if err := os.Symlink("/tmp/elsewhere", filepath.Join(repo, "node_modules")); err != nil {
		t.Fatalf("symlink node_modules: %v", err)
	}
	gitRun(t, repo, "add", "-A")
	if staged := gitRun(t, repo, "diff", "--cached", "--name-only"); staged == "" {
		t.Fatalf("expected node_modules symlink to be staged before exclusion")
	}

	applyRuntimeExcludes(t, repo)

	if staged := gitRun(t, repo, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("expected node_modules symlink unstaged, still staged: %q", staged)
	}
}

func TestRuntimeExcludes_UnstagesNodeModulesGitlink(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	gitRun(t, "", "init", "-b", "main", repo)
	gitRun(t, repo, "commit", "--allow-empty", "-m", "init")

	// Seed an inner repo and stage it as a gitlink (mode 160000), the form
	// observed in the bead's recurring junk commits.
	sub := filepath.Join(repo, "sub")
	gitRun(t, "", "init", "-b", "main", sub)
	gitRun(t, sub, "commit", "--allow-empty", "-m", "subinit")
	sha := gitRun(t, sub, "rev-parse", "HEAD")
	gitRun(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+sha+",node_modules")
	if staged := gitRun(t, repo, "diff", "--cached", "--name-only"); staged == "" {
		t.Fatalf("expected node_modules gitlink to be staged before exclusion")
	}

	applyRuntimeExcludes(t, repo)

	if staged := gitRun(t, repo, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("expected node_modules gitlink unstaged, still staged: %q", staged)
	}
}

func TestRuntimeExcludes_UnstagesNodeModulesRealDir(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	gitRun(t, "", "init", "-b", "main", repo)
	gitRun(t, repo, "commit", "--allow-empty", "-m", "init")

	nm := filepath.Join(repo, "node_modules")
	if err := os.MkdirAll(nm, 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nm, "pkg.js"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write pkg.js: %v", err)
	}
	gitRun(t, repo, "add", "-A")

	applyRuntimeExcludes(t, repo)

	if staged := gitRun(t, repo, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("expected node_modules dir contents unstaged, still staged: %q", staged)
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
