package polecat

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
)

// TestRemoveTeardownPushSkipsSlowGate is the gu-zadrb regression guard: the
// best-effort push that RemoveWithOptions performs before discarding a worktree
// must skip the SLOW pre-push test gate (GT_SKIP_PREPUSH=1), not run it.
//
// Why this matters: the SLOW gate ('go test ./...') has been observed to hang
// mid-suite, orphaning a forking process tree (git-remote-https children) that
// PINS the worktree directory. A teardown push that fires that gate re-spawns
// the hang on every nuke retry → STUCK_NUKE loops + stranded work (3+ incidents
// in one session). A worktree about to be deleted must never run the full test
// suite on a push.
//
// The test installs a pre-push hook on the polecat worktree that REJECTS a
// plain push but ALLOWS one carrying GT_SKIP_PREPUSH=1 (mirroring the
// production scripts/pre-push-check.sh slow/fast split). It then makes an
// unpushed commit and calls RemoveWithOptions. If the teardown push used plain
// Push, the hook would reject it and the branch would never reach origin; the
// branch landing on origin proves the skip-prepush path was used.
func TestRemoveTeardownPushSkipsSlowGate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	installMockBd(t)

	root := t.TempDir()
	mayorRig := filepath.Join(root, "mayor", "rig")
	if err := os.MkdirAll(mayorRig, 0o755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Rig-level .beads redirect to mayor/rig/.beads (matches the canonical layout).
	rigBeads := filepath.Join(root, ".beads")
	if err := os.MkdirAll(rigBeads, 0o755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(mayorRig, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir mayor/rig/.beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigBeads, "redirect"), []byte("mayor/rig/.beads\n"), 0o644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}

	// A bare origin remote — the durable push target.
	origin := filepath.Join(root, "origin.git")
	runGit(t, root, "init", "-q", "--bare", origin)

	// Repo base (mayor/rig) with origin/main.
	runGit(t, mayorRig, "init", "-q", "-b", "main")
	runGit(t, mayorRig, "config", "commit.gpgsign", "false")
	runGit(t, mayorRig, "config", "user.email", "test@example.com")
	runGit(t, mayorRig, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(mayorRig, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, mayorRig, "add", "README.md")
	runGit(t, mayorRig, "commit", "-q", "-m", "init")
	runGit(t, mayorRig, "remote", "add", "origin", origin)
	runGit(t, mayorRig, "push", "-q", "origin", "main")
	runGit(t, mayorRig, "update-ref", "refs/remotes/origin/main", "HEAD")

	r := &rig.Rig{Name: "rig", Path: root}
	m := NewManager(r, git.NewGit(root), nil)

	p, err := m.AddWithOptions("TestAgent", AddOptions{})
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}
	if p.Branch == "" {
		t.Fatal("expected a polecat branch on the new worktree")
	}

	// Install a pre-push hook on the worktree that rejects plain pushes but
	// allows GT_SKIP_PREPUSH=1 — the production slow/fast split in miniature.
	hooksDir := filepath.Join(p.ClonePath, ".myhooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hook := "#!/bin/bash\n" +
		"if [ \"$GT_SKIP_PREPUSH\" = \"1\" ]; then exit 0; fi\n" +
		"echo 'SLOW-GATE-WOULD-HANG-HERE' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(hook), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	runGit(t, p.ClonePath, "config", "core.hooksPath", hooksDir)

	// Make an unpushed commit so the teardown push has work to deliver.
	if err := os.WriteFile(filepath.Join(p.ClonePath, "work.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("write work: %v", err)
	}
	runGit(t, p.ClonePath, "add", "work.txt")
	runGit(t, p.ClonePath, "commit", "-q", "-m", "unpushed work")
	wantSHA := runGit(t, p.ClonePath, "rev-parse", "HEAD")

	// Teardown. force=false exercises the best-effort push path (a --force nuke
	// skips the push entirely, which is a different code path).
	if err := m.RemoveWithOptions("TestAgent", false, false, false); err != nil {
		t.Fatalf("RemoveWithOptions: %v", err)
	}

	// The branch must have reached origin at the unpushed SHA. If the teardown
	// push used plain Push, the gating hook would have rejected it and this ref
	// would be absent — the gu-zadrb regression.
	got := runGit(t, origin, "rev-parse", "refs/heads/"+p.Branch)
	if got != wantSHA {
		t.Fatalf("teardown push did not deliver branch %s to origin at %s (got %q) — "+
			"the slow pre-push gate was not skipped (gu-zadrb regression)",
			p.Branch, wantSHA, got)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
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
	return trimNL(string(out))
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
