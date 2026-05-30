package daemon

import (
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureRigsDoltAutoCommit verifies the gs-onu startup self-heal: every
// rig's resolved beads config (and the town config) gets dolt.auto-commit=on so
// ephemeral MR beads from `gt done` commit to shared main instead of stranding,
// while an operator's explicit value is preserved and missing dirs are skipped.
func TestEnsureRigsDoltAutoCommit(t *testing.T) {
	town := t.TempDir()

	// town-level beads config: no auto-commit key → should be added.
	writeBeadsConfig(t, filepath.Join(town, ".beads"), "storage.backend: dolt\n")

	// rig "fresh": resolved config (mayor/rig/.beads) missing the key → added.
	writeBeadsConfig(t, filepath.Join(town, "fresh", "mayor", "rig", ".beads"),
		"dolt.idle-timeout: \"0\"\n")

	// rig "explicit": operator set auto-commit=off → must be preserved.
	writeBeadsConfig(t, filepath.Join(town, "explicit", "mayor", "rig", ".beads"),
		"dolt.auto-commit: \"off\"\n")

	// rig "norig": no beads dir at all → must be skipped without error.

	d := &Daemon{
		config:              &Config{TownRoot: town},
		logger:              log.New(io.Discard, "", 0),
		knownRigsCache:      []string{"fresh", "explicit", "norig"},
		knownRigsCacheValid: true,
	}

	d.ensureRigsDoltAutoCommit()

	assertAutoCommit(t, filepath.Join(town, ".beads"), "on")
	assertAutoCommit(t, filepath.Join(town, "fresh", "mayor", "rig", ".beads"), "on")
	assertAutoCommit(t, filepath.Join(town, "explicit", "mayor", "rig", ".beads"), "off")

	// norig had no beads dir — nothing should have been created.
	if _, err := os.Stat(filepath.Join(town, "norig")); !os.IsNotExist(err) {
		t.Errorf("rig with no beads dir should be left untouched")
	}
}

func writeBeadsConfig(t *testing.T, beadsDir, content string) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", beadsDir, err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatalf("write config %s: %v", beadsDir, err)
	}
}

func assertAutoCommit(t *testing.T, beadsDir, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config %s: %v", beadsDir, err)
	}
	wantLine := "dolt.auto-commit: \"" + want + "\""
	if !strings.Contains(string(data), wantLine) {
		t.Errorf("config at %s: expected %q, got:\n%s", beadsDir, wantLine, data)
	}
}

// initRigGitRepo initializes <rigRoot> as a git repo on `main` with one
// initial commit so config.yaml can be tracked and committed by the
// daemon's self-heal path.
func initRigGitRepo(t *testing.T, rigRoot string) {
	t.Helper()
	if err := os.MkdirAll(rigRoot, 0755); err != nil {
		t.Fatalf("mkdir rig %s: %v", rigRoot, err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = rigRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, rigRoot, err, out)
		}
	}
	// Initial commit so the repo has a HEAD ref (CurrentBranchStrict
	// requires a non-detached HEAD).
	readme := filepath.Join(rigRoot, "README.md")
	if err := os.WriteFile(readme, []byte("# rig\n"), 0644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	for _, args := range [][]string{
		{"add", "README.md"},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = rigRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, rigRoot, err, out)
		}
	}
}

// rigHeadAndStatus returns the rig's HEAD ref short SHA and `git status
// --porcelain` output. Used by the test to assert the worktree is clean
// after the daemon's self-heal commit.
func rigHeadAndStatus(t *testing.T, rigRoot string) (head, status string) {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	cmd.Dir = rigRoot
	headOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse: %v\n%s", err, headOut)
	}
	cmd = exec.Command("git", "status", "--porcelain")
	cmd.Dir = rigRoot
	statusOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, statusOut)
	}
	return strings.TrimSpace(string(headOut)), strings.TrimSpace(string(statusOut))
}

// rigHeadCommitMessage returns the subject + body of the rig's HEAD commit.
func rigHeadCommitMessage(t *testing.T, rigRoot string) string {
	t.Helper()
	cmd := exec.Command("git", "log", "-1", "--format=%B", "HEAD")
	cmd.Dir = rigRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestEnsureRigsDoltAutoCommit_CommitsSelfHealInGitRepo verifies the
// gu-b7h5 fix: when the daemon's startup self-heal appends
// dolt.auto-commit=on to a tracked config.yaml, it follows up with a
// surgical commit so rebuild-gt's "skip if dirty" guard does not skip
// every cooldown forever. Mirrors the production layout
// (<TownRoot>/<rig>/mayor/rig is itself a git repo).
func TestEnsureRigsDoltAutoCommit_CommitsSelfHealInGitRepo(t *testing.T) {
	town := t.TempDir()

	// rig "tracked": the rig directory IS a git repo and its config.yaml is
	// tracked. Self-heal must (1) modify config.yaml and (2) commit it.
	rigRoot := filepath.Join(town, "tracked", "mayor", "rig")
	initRigGitRepo(t, rigRoot)
	beadsDir := filepath.Join(rigRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	preExisting := "prefix: tracked\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(preExisting), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// Add and commit config.yaml so the next modification is observable as a
	// dirty tracked file before self-heal runs.
	for _, args := range [][]string{
		{"add", ".beads/config.yaml"},
		{"commit", "-m", "track config.yaml"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = rigRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	preHead, preStatus := rigHeadAndStatus(t, rigRoot)
	if preStatus != "" {
		t.Fatalf("precondition: rig should be clean before self-heal, got: %q", preStatus)
	}

	d := &Daemon{
		config:              &Config{TownRoot: town},
		logger:              log.New(io.Discard, "", 0),
		knownRigsCache:      []string{"tracked"},
		knownRigsCacheValid: true,
	}

	d.ensureRigsDoltAutoCommit()

	// File now contains the auto-commit key.
	assertAutoCommit(t, beadsDir, "on")

	// Worktree is clean again — the daemon committed its own write (gu-b7h5).
	postHead, postStatus := rigHeadAndStatus(t, rigRoot)
	if postStatus != "" {
		t.Fatalf("post-self-heal: rig should be clean, got: %q", postStatus)
	}
	if postHead == preHead {
		t.Fatalf("expected new HEAD commit after self-heal, HEAD unchanged at %s", preHead)
	}

	// Commit message identifies the self-heal and the bead.
	msg := rigHeadCommitMessage(t, rigRoot)
	if !strings.Contains(msg, "self-heal dolt.auto-commit=on") {
		t.Errorf("commit message missing self-heal subject: %q", msg)
	}
	if !strings.Contains(msg, "gu-b7h5") {
		t.Errorf("commit message should reference gu-b7h5 for traceability: %q", msg)
	}
}

// TestEnsureRigsDoltAutoCommit_NoCommitWhenAlreadySet verifies that when
// dolt.auto-commit is already present (operator-set or already-fixed),
// the daemon performs no write and no commit. The `changed` return on
// EnsureDoltAutoCommitDefault is the gate: if it didn't write the file,
// no commit must fire.
func TestEnsureRigsDoltAutoCommit_NoCommitWhenAlreadySet(t *testing.T) {
	town := t.TempDir()
	rigRoot := filepath.Join(town, "explicit", "mayor", "rig")
	initRigGitRepo(t, rigRoot)
	beadsDir := filepath.Join(rigRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	preExisting := "prefix: explicit\ndolt.auto-commit: \"off\"\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(preExisting), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	for _, args := range [][]string{
		{"add", ".beads/config.yaml"},
		{"commit", "-m", "track config.yaml with explicit off"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = rigRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	preHead, _ := rigHeadAndStatus(t, rigRoot)

	d := &Daemon{
		config:              &Config{TownRoot: town},
		logger:              log.New(io.Discard, "", 0),
		knownRigsCache:      []string{"explicit"},
		knownRigsCacheValid: true,
	}
	d.ensureRigsDoltAutoCommit()

	// Operator's explicit "off" preserved; no new commit.
	assertAutoCommit(t, beadsDir, "off")
	postHead, postStatus := rigHeadAndStatus(t, rigRoot)
	if postStatus != "" {
		t.Errorf("expected clean tree, got: %q", postStatus)
	}
	if postHead != preHead {
		t.Errorf("expected no new commit when nothing was written; HEAD moved %s → %s", preHead, postHead)
	}
}

// TestEnsureRigsDoltAutoCommit_NotAGitRepoIsNonFatal verifies that a rig
// whose "mayor/rig" directory is NOT a git work tree still gets the
// config write — the commit step fails gracefully (logged, not fatal).
// This guards against a daemon-startup crash on a freshly-cloned rig
// or a workspace with non-standard layout.
func TestEnsureRigsDoltAutoCommit_NotAGitRepoIsNonFatal(t *testing.T) {
	town := t.TempDir()
	rigRoot := filepath.Join(town, "norepo", "mayor", "rig")
	beadsDir := filepath.Join(rigRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix: norepo\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var buf strings.Builder
	d := &Daemon{
		config:              &Config{TownRoot: town},
		logger:              log.New(&buf, "", 0),
		knownRigsCache:      []string{"norepo"},
		knownRigsCacheValid: true,
	}

	// Must not panic; must still write the config.
	d.ensureRigsDoltAutoCommit()

	assertAutoCommit(t, beadsDir, "on")
	if !strings.Contains(buf.String(), "could not commit dolt.auto-commit self-heal") {
		t.Errorf("expected warning log for non-git rig, got: %q", buf.String())
	}
}
