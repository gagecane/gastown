package curio

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestDispatchReconcilePlugin runs the curio-dispatch-reconcile plugin shell
// test suite (gu-l84k2 fix #3, parent bug gu-ac2bu) from inside `go test ./...`,
// so the proof that the reconcile closes the success-of-dispatch != proposal-filed
// gap — warning ONLY on an actionable-but-unfiled run, and never on a legitimately
// quiet night or a still-in-flight run — is exercised by the merge-queue gate,
// not only by `make test-makefile`.
//
// The plugin itself is plugins/curio-dispatch-reconcile/run.sh; its behavioral
// assertions live in the adjacent run_test.sh. This wrapper is the single line
// that pulls those assertions into the Go test graph the Refinery and CI run —
// mirroring TestProposalExpiryPlugin (B8) and TestProposalTargetGuard (B6).
func TestDispatchReconcilePlugin(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available; plugin shell test is exercised by `make test-makefile`")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available; the reconcile plugin's functional harness needs it")
	}

	root, err := repoRootForTest()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	testScript := filepath.Join(root, "plugins", "curio-dispatch-reconcile", "run_test.sh")
	if _, err := os.Stat(testScript); err != nil {
		t.Fatalf("plugin test script not found at %s: %v", testScript, err)
	}

	cmd := exec.Command(bash, testScript)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("curio-dispatch-reconcile/run_test.sh failed: %v\n%s", err, out)
	}
}
