package curio

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestProposalTargetGuard runs the curio-proposal-target-guard shell test suite
// (Curio P3 B6, air-gap layer 2) from inside `go test ./...`, so the proof that
// a curio.*-targeting proposal CR is REJECTED — and a non-Curio one PASSES — is
// exercised by the merge-queue gate, not only by `make test-makefile`.
//
// The guard itself is scripts/guards/curio-proposal-target-guard.sh; its
// behavioral assertions live in the adjacent _test.sh. This wrapper is the
// single line that pulls those assertions into the Go test graph that the
// Refinery and CI actually run.
func TestProposalTargetGuard(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available; guard shell test is exercised by `make test-makefile`")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available; guard shell test needs it to build fixture repos")
	}

	root, err := repoRootForTest()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	testScript := filepath.Join(root, "scripts", "guards", "curio-proposal-target-guard_test.sh")
	if _, err := os.Stat(testScript); err != nil {
		t.Fatalf("guard test script not found at %s: %v", testScript, err)
	}

	cmd := exec.Command(bash, testScript)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("curio-proposal-target-guard_test.sh failed: %v\n%s", err, out)
	}
}

// repoRootForTest walks up from the current directory to the repository root,
// identified by a .git entry (a dir in a normal clone, a file in a worktree).
func repoRootForTest() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no .git found from working directory")
		}
		dir = parent
	}
}
