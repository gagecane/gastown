package rig

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestRunGuardsInstall_NoGuards verifies that rigs without a rig-guards/
// install.sh are a silent no-op rather than an error.
func TestRunGuardsInstall_NoGuards(t *testing.T) {
	rigPath := t.TempDir()
	worktree := t.TempDir()
	if err := RunGuardsInstall(rigPath, worktree); err != nil {
		t.Fatalf("expected no-op for rig without guards, got: %v", err)
	}
}

// TestRunGuardsInstall_Runs verifies that an executable rig-guards/install.sh
// is invoked with the worktree as its working directory and the documented
// environment variables set.
func TestRunGuardsInstall_Runs(t *testing.T) {
	rigPath := t.TempDir()
	worktree := t.TempDir()

	guardsDir := filepath.Join(rigPath, "rig-guards")
	if err := os.MkdirAll(guardsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(worktree, "guard-ran")
	script := filepath.Join(guardsDir, "install.sh")
	// The script records its cwd and the injected env vars so the test can
	// assert install.sh ran in the worktree with the expected context.
	content := "#!/usr/bin/env bash\nset -euo pipefail\n" +
		"printf '%s\\n%s\\n%s\\n' \"$PWD\" \"$GT_WORKTREE_PATH\" \"$GT_RIG_PATH\" > \"" + marker + "\"\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := RunGuardsInstall(rigPath, worktree); err != nil {
		t.Fatalf("RunGuardsInstall failed: %v", err)
	}

	out, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("guard marker not written (script did not run): %v", err)
	}
	// macOS resolves /var -> /private/var; compare via filepath.EvalSymlinks.
	gotCwd := firstLine(string(out))
	wantCwd, _ := filepath.EvalSymlinks(worktree)
	gotCwdResolved, _ := filepath.EvalSymlinks(gotCwd)
	if gotCwdResolved != wantCwd {
		t.Errorf("install.sh cwd = %q, want %q", gotCwd, worktree)
	}
}

// TestRunGuardsInstall_NonExecutable verifies a non-executable install.sh is
// skipped (warning) rather than failing provisioning.
func TestRunGuardsInstall_NonExecutable(t *testing.T) {
	rigPath := t.TempDir()
	worktree := t.TempDir()

	guardsDir := filepath.Join(rigPath, "rig-guards")
	if err := os.MkdirAll(guardsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guardsDir, "install.sh"), []byte("#!/usr/bin/env bash\nexit 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := RunGuardsInstall(rigPath, worktree); err != nil {
		t.Fatalf("expected non-executable guard to be skipped, got: %v", err)
	}
}

// TestRunGuardsInstall_PropagatesFailure verifies a failing install.sh surfaces
// the error to the caller (which logs it as a non-fatal warning).
func TestRunGuardsInstall_PropagatesFailure(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	rigPath := t.TempDir()
	worktree := t.TempDir()

	guardsDir := filepath.Join(rigPath, "rig-guards")
	if err := os.MkdirAll(guardsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guardsDir, "install.sh"), []byte("#!/usr/bin/env bash\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := RunGuardsInstall(rigPath, worktree); err == nil {
		t.Fatal("expected error from failing install.sh, got nil")
	}
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}
