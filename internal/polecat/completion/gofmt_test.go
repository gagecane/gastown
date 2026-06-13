package completion

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/steveyegge/gastown/internal/git"
)

// fakeGofmtCommitter records CommitPaths calls so tests can assert on the
// fixup commit without standing up a real repo.
type fakeGofmtCommitter struct {
	workDir     string
	commitErr   error
	commitMsg   string
	commitPaths []string
	commitCalls int
}

func (f *fakeGofmtCommitter) WorkDir() string { return f.workDir }

func (f *fakeGofmtCommitter) CommitPaths(message string, paths ...string) error {
	f.commitCalls++
	f.commitMsg = message
	f.commitPaths = paths
	return f.commitErr
}

// scriptedGofmt returns a GofmtRunner that responds to `-l` (list) and `-w`
// (write) invocations from canned data, recording the args it was called with.
func scriptedGofmt(listOut string, listErr, writeErr error, calls *[][]string) GofmtRunner {
	return func(workDir string, args ...string) ([]byte, error) {
		*calls = append(*calls, args)
		if len(args) > 0 && args[0] == "-l" {
			return []byte(listOut), listErr
		}
		// "-w" path
		return nil, writeErr
	}
}

func TestAutoFormatGoFiles_CleanTreeNoOp(t *testing.T) {
	var calls [][]string
	g := &fakeGofmtCommitter{workDir: "/repo"}
	run := scriptedGofmt("", nil, nil, &calls)

	formatted, err := AutoFormatGoFiles(g, run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if formatted {
		t.Error("formatted = true on a clean tree, want false")
	}
	if g.commitCalls != 0 {
		t.Errorf("commit calls = %d on clean tree, want 0", g.commitCalls)
	}
	// Only the `gofmt -l .` detection should have run.
	if len(calls) != 1 || calls[0][0] != "-l" {
		t.Errorf("expected single `gofmt -l .` call, got %v", calls)
	}
}

func TestAutoFormatGoFiles_UnformattedAutoFixesAndCommits(t *testing.T) {
	var calls [][]string
	g := &fakeGofmtCommitter{workDir: "/repo"}
	run := scriptedGofmt("a.go\nb.go\n", nil, nil, &calls)

	formatted, err := AutoFormatGoFiles(g, run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !formatted {
		t.Fatal("formatted = false, want true (files were unformatted)")
	}
	if g.commitCalls != 1 {
		t.Fatalf("commit calls = %d, want 1", g.commitCalls)
	}
	if g.commitPaths[0] != "a.go" || g.commitPaths[1] != "b.go" {
		t.Errorf("committed paths = %v, want [a.go b.go]", g.commitPaths)
	}
	if !strings.Contains(g.commitMsg, "gofmt") {
		t.Errorf("commit message %q should mention gofmt", g.commitMsg)
	}
	// The `-w` invocation must carry exactly the unformatted files.
	var sawWrite bool
	for _, c := range calls {
		if c[0] == "-w" {
			sawWrite = true
			if c[1] != "a.go" || c[2] != "b.go" {
				t.Errorf("gofmt -w args = %v, want [-w a.go b.go]", c)
			}
		}
	}
	if !sawWrite {
		t.Error("expected a `gofmt -w` invocation")
	}
}

// A gofmt *detection* error must never strand submission — the pre-push hook
// and refinery gate remain the backstop.
func TestAutoFormatGoFiles_DetectionErrorSwallowed(t *testing.T) {
	var calls [][]string
	g := &fakeGofmtCommitter{workDir: "/repo"}
	run := scriptedGofmt("", errors.New("gofmt: not found"), nil, &calls)

	formatted, err := AutoFormatGoFiles(g, run)
	if err != nil {
		t.Errorf("detection error should be swallowed, got %v", err)
	}
	if formatted {
		t.Error("formatted should be false when detection fails")
	}
	if g.commitCalls != 0 {
		t.Errorf("no commit should fire on detection error, got %d", g.commitCalls)
	}
}

// A failed fixup commit IS a hard error — the caller logs it but does not
// block submission (verified at the call site, not here).
func TestAutoFormatGoFiles_CommitErrorReturned(t *testing.T) {
	var calls [][]string
	g := &fakeGofmtCommitter{workDir: "/repo", commitErr: errors.New("commit boom")}
	run := scriptedGofmt("a.go\n", nil, nil, &calls)

	formatted, err := AutoFormatGoFiles(g, run)
	if err == nil {
		t.Fatal("expected error from failed commit, got nil")
	}
	if formatted {
		t.Error("formatted should be false when the commit fails")
	}
}

// TestAutoFormatGoFiles_RealRepo is the bead's exit-criteria check: a branch
// with a deliberately-misformatted committed .go file gets auto-formatted and
// committed by gt done's pre-submit step, so it can no longer reach the
// refinery with a gofmt failure. Requires a real gofmt on PATH.
func TestAutoFormatGoFiles_RealRepo(t *testing.T) {
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt not on PATH")
	}

	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	testRunGit(t, tmp, "init", "--initial-branch", "main", repo)
	testRunGit(t, repo, "config", "user.email", "test@test.com")
	testRunGit(t, repo, "config", "user.name", "Test")

	// Commit a deliberately misformatted Go file (gofmt would re-indent the
	// body and drop the extra blank line / leading spaces).
	misformatted := "package foo\n\nfunc Bar()    int {\n        return  1\n\n\n}\n"
	writeRepoFile(t, repo, "bar.go", misformatted)
	testRunGit(t, repo, "add", ".")
	testRunGit(t, repo, "commit", "-m", "feat: add bar (unformatted)")

	g := gitpkg.NewGit(repo)
	formatted, err := AutoFormatGoFiles(g, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !formatted {
		t.Fatal("expected the misformatted file to be reformatted+committed")
	}

	// The working tree is now clean (the fix was committed, not left dirty).
	out, statErr := exec.Command("git", "-C", repo, "status", "--porcelain").CombinedOutput()
	if statErr != nil {
		t.Fatalf("git status: %v\n%s", statErr, out)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("working tree not clean after auto-format commit:\n%s", out)
	}

	// gofmt -l must now report nothing — the exact gate the refinery runs.
	gofmtOut, _ := execGofmt(repo, "-l", ".")
	if strings.TrimSpace(string(gofmtOut)) != "" {
		t.Errorf("gofmt -l still reports unformatted files after auto-format:\n%s", gofmtOut)
	}
}
