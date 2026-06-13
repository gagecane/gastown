package curio

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestProposalExpiryPlugin runs the curio-proposal-expiry plugin shell test
// suite (Curio P3 B8, gu-5zf4t) from inside `go test ./...`, so the proof that
// expiry auto-closes stale proposals (stamping a curio-outcome:<code> label the
// B0b reconciler reads) and that the volume-breaker alert fires exactly once per
// trip is exercised by the merge-queue gate, not only by `make test-makefile`.
//
// The plugin itself is plugins/curio-proposal-expiry/run.sh; its behavioral
// assertions live in the adjacent run_test.sh. This wrapper is the single line
// that pulls those assertions into the Go test graph the Refinery and CI run —
// mirroring TestProposalTargetGuard (B6) for the proposal-target guard.
func TestProposalExpiryPlugin(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available; plugin shell test is exercised by `make test-makefile`")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available; the expiry plugin's functional harness needs it")
	}

	root, err := repoRootForTest()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	testScript := filepath.Join(root, "plugins", "curio-proposal-expiry", "run_test.sh")
	if _, err := os.Stat(testScript); err != nil {
		t.Fatalf("plugin test script not found at %s: %v", testScript, err)
	}

	cmd := exec.Command(bash, testScript)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("curio-proposal-expiry/run_test.sh failed: %v\n%s", err, out)
	}
}
