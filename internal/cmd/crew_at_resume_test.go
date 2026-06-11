package cmd

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestResumeWorkspace_CleanOnDefault verifies --resume is a no-op (prints
// nothing) when the workspace is clean and already on the default branch.
func TestResumeWorkspace_CleanOnDefault(t *testing.T) {
	local := initRepoWithDefaultBranch(t, "main")

	out := captureStdout(t, func() {
		resumeWorkspace(local, "Crew workspace test/dave", "")
	})

	if strings.TrimSpace(out) != "" {
		t.Errorf("expected no-op (empty output) for clean default-branch workspace, got:\n%s", out)
	}
}

// TestResumeWorkspace_OffBranch verifies --resume announces resuming and does
// NOT switch branches when the workspace is on a feature branch.
func TestResumeWorkspace_OffBranch(t *testing.T) {
	local := initRepoWithDefaultBranch(t, "main")
	gitRun(t, local, "checkout", "-b", "crew/dave/feature")

	out := captureStdout(t, func() {
		resumeWorkspace(local, "Crew workspace test/dave", "")
	})

	if !strings.Contains(out, "Resuming") {
		t.Errorf("expected resume message, got:\n%s", out)
	}
	if !strings.Contains(out, "crew/dave/feature") {
		t.Errorf("expected branch name in output, got:\n%s", out)
	}

	// Must NOT have reset the branch — WIP preservation is the whole point.
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = local
	branch, _ := cmd.Output()
	if strings.TrimSpace(string(branch)) != "crew/dave/feature" {
		t.Errorf("resumeWorkspace must not switch branches, now on %q", strings.TrimSpace(string(branch)))
	}
}

// TestResumeWorkspace_DirtyOnDefault verifies --resume reports in-flight work
// even when on the default branch (uncommitted changes are still WIP to keep).
func TestResumeWorkspace_DirtyOnDefault(t *testing.T) {
	local := initRepoWithDefaultBranch(t, "main")
	if err := os.WriteFile(local+"/wip.txt", []byte("in flight"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		resumeWorkspace(local, "Crew workspace test/dave", "")
	})

	if !strings.Contains(out, "Resuming") {
		t.Errorf("expected resume message for dirty workspace, got:\n%s", out)
	}
	if !strings.Contains(out, "in-flight work") {
		t.Errorf("expected in-flight work note, got:\n%s", out)
	}
}

// TestWarnIfNotDefaultBranch_SuggestsResume verifies the off-branch warning
// suggests --resume alongside --reset (acceptance criterion).
func TestWarnIfNotDefaultBranch_SuggestsResume(t *testing.T) {
	local := initRepoWithDefaultBranch(t, "main")
	gitRun(t, local, "checkout", "-b", "crew/dave/feature")

	out := captureStdout(t, func() {
		warnIfNotDefaultBranch(local, "Crew workspace test/dave", "")
	})

	if !strings.Contains(out, "--resume") {
		t.Errorf("expected warning to suggest --resume, got:\n%s", out)
	}
	if !strings.Contains(out, "--reset") {
		t.Errorf("expected warning to still mention --reset, got:\n%s", out)
	}
}

// TestWarnIfNotDefaultBranch_DirtyRecommendsResumeFirst verifies that when
// in-flight work is present, the warning leads with the non-destructive
// --resume recommendation.
func TestWarnIfNotDefaultBranch_DirtyRecommendsResumeFirst(t *testing.T) {
	local := initRepoWithDefaultBranch(t, "main")
	gitRun(t, local, "checkout", "-b", "crew/dave/feature")
	if err := os.WriteFile(local+"/wip.txt", []byte("in flight"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		warnIfNotDefaultBranch(local, "Crew workspace test/dave", "")
	})

	if !strings.Contains(out, "In-flight work detected") {
		t.Errorf("expected in-flight work detection, got:\n%s", out)
	}
	if !strings.Contains(out, "--resume to continue") {
		t.Errorf("expected --resume recommendation, got:\n%s", out)
	}
}
