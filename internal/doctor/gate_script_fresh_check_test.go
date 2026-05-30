package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gateRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gateWrite(t *testing.T, clonePath, rel, content string) {
	t.Helper()
	dst := filepath.Join(clonePath, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// setupGateClone builds townRoot/myrig/crew/worker as a git clone whose origin
// default branch carries the gate files at the given content.
func setupGateClone(t *testing.T) (townRoot, clonePath string) {
	t.Helper()
	base := t.TempDir()
	townRoot = filepath.Join(base, "town")
	origin := filepath.Join(base, "origin.git")
	clonePath = filepath.Join(townRoot, "myrig", "crew", "worker")
	if err := os.MkdirAll(clonePath, 0o755); err != nil {
		t.Fatal(err)
	}

	gateRun(t, base, "git", "init", "--bare", "-b", "main", origin)
	gateRun(t, clonePath, "git", "init", "-b", "main")
	gateRun(t, clonePath, "git", "config", "user.email", "t@t")
	gateRun(t, clonePath, "git", "config", "user.name", "t")
	gateRun(t, clonePath, "git", "remote", "add", "origin", origin)

	gateWrite(t, clonePath, ".githooks/pre-push", "#prepush v2\n")
	gateWrite(t, clonePath, "scripts/pre-push-check.sh", "#gate v2\n")

	gateRun(t, clonePath, "git", "add", "-A")
	gateRun(t, clonePath, "git", "commit", "-m", "gate v2")
	gateRun(t, clonePath, "git", "push", "-u", "origin", "main")
	gateRun(t, clonePath, "git", "remote", "set-head", "origin", "main")
	return townRoot, clonePath
}

func TestGateScriptFresh_OKWhenMatchingBranch(t *testing.T) {
	townRoot, _ := setupGateClone(t)
	res := NewGateScriptFreshAllRigsCheck().Run(&CheckContext{TownRoot: townRoot})
	if res.Status != StatusOK {
		t.Fatalf("status = %v (%s); want OK", res.Status, res.Message)
	}
}

func TestGateScriptFresh_FlagsAndFixesStale(t *testing.T) {
	townRoot, clonePath := setupGateClone(t)

	// Simulate a working tree lagging the branch tip: overwrite the checked-out
	// gate script with old content (as a frozen worktree would have).
	gateWrite(t, clonePath, "scripts/pre-push-check.sh", "#gate v1 (stale)\n")

	check := NewGateScriptFreshAllRigsCheck()
	res := check.Run(&CheckContext{TownRoot: townRoot})
	if res.Status != StatusWarning {
		t.Fatalf("status = %v (%s); want Warning", res.Status, res.Message)
	}
	if len(check.staleFixes) != 1 || len(check.staleFixes[0].files) != 1 ||
		check.staleFixes[0].files[0] != "scripts/pre-push-check.sh" {
		t.Fatalf("staleFixes = %+v; want one clone with the stale gate script", check.staleFixes)
	}

	if err := check.Fix(&CheckContext{TownRoot: townRoot}); err != nil {
		t.Fatalf("Fix: %v", err)
	}

	// After Fix the working-tree file matches the branch tip again...
	got, err := os.ReadFile(filepath.Join(clonePath, "scripts/pre-push-check.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "#gate v2\n" {
		t.Errorf("after Fix, content = %q; want refreshed branch content", got)
	}
	// ...with the executable bit preserved.
	if fi, err := os.Stat(filepath.Join(clonePath, "scripts/pre-push-check.sh")); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("gate script lost its executable bit: %v", fi.Mode())
	}

	// ...and a re-run reports clean.
	if res := check.Run(&CheckContext{TownRoot: townRoot}); res.Status != StatusOK {
		t.Errorf("after Fix, status = %v (%s); want OK", res.Status, res.Message)
	}
}

func TestRemoteDefaultBranch(t *testing.T) {
	_, clonePath := setupGateClone(t)
	if got := remoteDefaultBranch(clonePath); got != "main" {
		t.Errorf("remoteDefaultBranch = %q; want main", got)
	}
}
