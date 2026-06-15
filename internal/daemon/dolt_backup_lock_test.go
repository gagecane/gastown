package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestDoltBackupStoreLockPlugin runs the dolt-backup plugin shell test suite
// (gu-p02zy, root cause of gu-rhvsn) from inside `go test ./...`, so the proof
// that a backup defers (rather than fails or corrupts) when a Dolt restart holds
// the store lock — and that the FAILED escalation is deduped — is exercised by
// the Refinery merge-queue gate, not only by a manual shell run.
//
// The guard's two halves are: the Go restart paths take the advisory store lock
// (internal/doltserver.WithStoreLock, wrapping doltserver.Stop/Start and the
// daemon manager's stopLocked/startLocked), and plugins/dolt-backup/run.sh takes
// the SAME flock per-DB. This wrapper pulls the run.sh behavioral assertions into
// the Go test graph; the path-parity assertion (same lock file on both sides) is
// covered by TestStoreLockPath_* in internal/doltserver.
func TestDoltBackupStoreLockPlugin(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available; plugin shell test is exercised manually")
	}
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock(1) not available; the store-lock guard harness needs it")
	}

	root, err := repoRootFromTest(t)
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	testScript := filepath.Join(root, "plugins", "dolt-backup", "run_test.sh")
	if _, err := os.Stat(testScript); err != nil {
		t.Fatalf("plugin test script not found at %s: %v", testScript, err)
	}

	cmd := exec.Command(bash, testScript)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dolt-backup/run_test.sh failed: %v\n%s", err, out)
	}
}

// repoRootFromTest walks up from the current test's working directory to the
// repository root (the dir containing go.mod). Self-contained so this test does
// not couple to helpers in other packages.
func repoRootFromTest(t *testing.T) (string, error) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
