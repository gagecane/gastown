package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

// installMockBdForReuseGuard places a fake bd on PATH that reports the polecat's
// agent bead as an idle polecat with clean cleanup_status. This lets
// AddWithOptions/ReuseIdlePolecat run without a real bd installation. Mirrors
// internal/polecat/manager_test.go:installMockBd (which is unexported there).
func installMockBdForReuseGuard(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mock bd shell script not supported on Windows")
	}
	binDir := t.TempDir()
	script := `#!/bin/sh
# Mock bd for the reuse-guard test.
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done
case "$cmd" in
  init|config|update|slot|reopen|migrate)
    exit 0
    ;;
  create)
    bead_id="mock-1"
    for arg in "$@"; do
      case "$arg" in
        --id=*) bead_id="${arg#--id=}" ;;
      esac
    done
    echo "{\"id\":\"$bead_id\",\"status\":\"open\",\"created_at\":\"2025-01-01T00:00:00Z\"}"
    exit 0
    ;;
  show)
    id=""
    seen_show=0
    for arg in "$@"; do
      if [ "$seen_show" = 0 ]; then
        [ "$arg" = "show" ] && seen_show=1
        continue
      fi
      case "$arg" in --*) continue ;; esac
      id="$arg"
      break
    done
    printf '[{"id":"%s","title":"agent","issue_type":"agent","description":"agent\\n\\nrole_type: polecat\\nagent_state: idle\\nhook_bead: null\\ncleanup_status: clean"}]\n' "$id"
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// setupReuseGuardRig builds a minimal rig with a bare-repo-equivalent mayor/rig
// and a working polecat Manager. Mirrors the canonical harness used by the
// polecat-package reuse tests.
func setupReuseGuardRig(t *testing.T) (*polecat.Manager, *rig.Rig, string) {
	t.Helper()
	installMockBdForReuseGuard(t)

	root := t.TempDir()
	mayorRig := filepath.Join(root, "mayor", "rig")
	if err := os.MkdirAll(mayorRig, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}
	rigBeads := filepath.Join(root, ".beads")
	if err := os.MkdirAll(rigBeads, 0755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}
	mayorBeads := filepath.Join(mayorRig, ".beads")
	if err := os.MkdirAll(mayorBeads, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig/.beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigBeads, "redirect"), []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}

	runGit := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	runGit(mayorRig, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(mayorRig, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	mayorGit := git.NewGit(mayorRig)
	if err := mayorGit.Add("README.md"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := mayorGit.Commit("Initial commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	runGit(mayorRig, "remote", "add", "origin", mayorRig)
	runGit(mayorRig, "update-ref", "refs/remotes/origin/main", "HEAD")

	r := &rig.Rig{Name: "rig", Path: root}
	mgr := polecat.NewManager(r, git.NewGit(root), tmux.NewTmux())
	return mgr, r, mayorRig
}

// commitUnpushedWork creates a local commit on the polecat worktree that is NOT
// on any remote — the "completed-but-unpushed deliverable" that gu-q2vnb is
// guarding against losing.
func commitUnpushedWork(t *testing.T, worktreePath string) string {
	t.Helper()
	g := git.NewGit(worktreePath)
	if err := g.CheckoutNewBranch("polecat/toast/deliverable", "HEAD"); err != nil {
		t.Fatalf("checkout deliverable branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "deliverable.md"), []byte("audit findings\n"), 0644); err != nil {
		t.Fatalf("write deliverable: %v", err)
	}
	if err := g.Add("deliverable.md"); err != nil {
		t.Fatalf("git add deliverable: %v", err)
	}
	if err := g.Commit("Documentation coverage audit"); err != nil {
		t.Fatalf("git commit deliverable: %v", err)
	}
	sha, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("resolve deliverable HEAD: %v", err)
	}
	return sha
}

// TestTryReuseIdlePolecat_RecoveryNeededSlotNotForceRepaired is the gu-q2vnb
// guard: when an idle polecat carries an unpushed deliverable commit,
// ReuseIdlePolecat refuses reuse with ErrPolecatNeedsRecovery. The spawn path
// must NOT escalate to a forced worktree repair (which would raw-remove the
// worktree and destroy the unpushed commit before it lands via MR or is
// preserved). It must skip the slot (return false) and leave the worktree —
// and its unpushed commit — intact for recovery.
func TestTryReuseIdlePolecat_RecoveryNeededSlotNotForceRepaired(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux not supported on Windows")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	mgr, r, _ := setupReuseGuardRig(t)

	reg := session.NewPrefixRegistry()
	reg.Register("gt", r.Name)
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })

	pc, err := mgr.AddWithOptions("toast", polecat.AddOptions{})
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	// Sanity: reuse must refuse this slot for recovery (unpushed commit present).
	wantSHA := commitUnpushedWork(t, pc.ClonePath)
	if _, err := mgr.ReuseIdlePolecat("toast", polecat.AddOptions{HookBead: "gt-next"}); err == nil {
		t.Fatal("precondition: ReuseIdlePolecat should refuse a slot with an unpushed commit")
	}

	// Re-create the unpushed commit if the failed reuse touched the branch, then
	// exercise the spawn-level guard.
	if cur, _ := git.NewGit(pc.ClonePath).Rev("HEAD"); cur != wantSHA {
		wantSHA = commitUnpushedWork(t, pc.ClonePath)
	}

	info, reused := tryReuseIdlePolecat(mgr, r, tmux.NewTmux(), "toast", r.Name, SlingSpawnOptions{HookBead: "gt-next"})
	if reused {
		t.Fatalf("tryReuseIdlePolecat should refuse a recovery-needed slot, got reused=true (info=%+v)", info)
	}

	// The worktree must still exist (NOT force-repaired away).
	if _, err := os.Stat(pc.ClonePath); err != nil {
		t.Fatalf("worktree should survive a refused reuse, but stat failed: %v", err)
	}

	// The unpushed deliverable commit must still be on HEAD — proving the guard
	// prevented the destructive force-repair that loses unpushed work.
	gotSHA, err := git.NewGit(pc.ClonePath).Rev("HEAD")
	if err != nil {
		t.Fatalf("resolve worktree HEAD after refused reuse: %v", err)
	}
	if gotSHA != wantSHA {
		t.Fatalf("unpushed deliverable commit lost: HEAD = %s, want %s", gotSHA, wantSHA)
	}
}
