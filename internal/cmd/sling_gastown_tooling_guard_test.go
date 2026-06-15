package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installGastownToolingGuardStubs wires a minimal `bd show` stub returning a
// single open task whose description is supplied by the caller, plus an
// injectable customer-repo decision. Returns the town root.
//
// The bd stub only needs to satisfy the bead-info read at the top of
// executeSling; the customer-repo decision is driven through the injected seam
// so the test never writes a real rig config layer.
func installGastownToolingGuardStubs(t *testing.T, description string, customerRepo bool) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	// JSON-escape the description for embedding in the stub's bd show output.
	desc := strings.ReplaceAll(description, `\`, `\\`)
	desc = strings.ReplaceAll(desc, `"`, `\"`)
	bdScript := `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"Some task","status":"open","assignee":"","description":"` + desc + `","issue_type":"task","labels":[]}]'
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	prev := isCustomerRepoRigFn
	isCustomerRepoRigFn = func(_, _ string) bool { return customerRepo }
	t.Cleanup(func() { isCustomerRepoRigFn = prev })

	return townRoot
}

// TestExecuteSling_GastownToolingIntoCustomerRig_Rejected verifies the gs-ilbt
// guard: a gastown-tooling bead targeting a customer-repo rig is refused
// pre-dispatch with a re-home instruction, never spawning a doomed polecat.
func TestExecuteSling_GastownToolingIntoCustomerRig_Rejected(t *testing.T) {
	townRoot := installGastownToolingGuardStubs(t,
		"This is gastown-level rig/worktree provisioning; rig-guards/ stays internal.",
		true /*customerRepo*/)

	result, err := executeSling(SlingParams{
		BeadID:   "lb-x3qn",
		RigName:  "lia_bac",
		TownRoot: townRoot,
	})
	if err == nil {
		t.Fatal("expected error slinging a gastown-tooling bead to a customer rig, got nil")
	}
	if result.ErrMsg != "gastown-tooling-in-customer-rig" {
		t.Errorf("expected ErrMsg=\"gastown-tooling-in-customer-rig\", got %q", result.ErrMsg)
	}
	if !strings.Contains(err.Error(), "re-home") {
		t.Errorf("error should instruct to re-home to gastown: %v", err)
	}
}

// TestExecuteSling_GastownToolingIntoCustomerRig_NotBypassedByForce verifies the
// guard is NOT bypassable by --force: a customer polecat cannot deliver gastown
// source regardless of dispatch intent.
func TestExecuteSling_GastownToolingIntoCustomerRig_NotBypassedByForce(t *testing.T) {
	townRoot := installGastownToolingGuardStubs(t,
		"change the gastown internal/cmd/done.go gt-source ship path",
		true /*customerRepo*/)

	result, err := executeSling(SlingParams{
		BeadID:   "lb-ax1v",
		RigName:  "lia_bac",
		TownRoot: townRoot,
		Force:    true,
	})
	if err == nil || result.ErrMsg != "gastown-tooling-in-customer-rig" {
		t.Fatalf("--force must NOT bypass the gastown-tooling guard; got err=%v ErrMsg=%q", err, result.ErrMsg)
	}
}

// TestExecuteSling_GastownToolingIntoGastownRig_NotRejected verifies the guard
// only fires for customer-repo targets. The same bead routed to gastown (the
// correct home, customer_repo=false) must fall through this guard.
func TestExecuteSling_GastownToolingIntoGastownRig_NotRejected(t *testing.T) {
	townRoot := installGastownToolingGuardStubs(t,
		"This is gastown-level provisioning work",
		false /*customerRepo*/)

	result, _ := executeSling(SlingParams{
		BeadID:   "gs-194x",
		RigName:  "gastown",
		TownRoot: townRoot,
	})
	if result != nil && result.ErrMsg == "gastown-tooling-in-customer-rig" {
		t.Errorf("guard must not fire for a gastown target; got ErrMsg=%q", result.ErrMsg)
	}
}

// TestExecuteSling_CustomerWorkIntoCustomerRig_NotRejected verifies genuine
// customer work routed to its own customer rig is untouched — no gastown-tooling
// markers, so the guard stays silent.
func TestExecuteSling_CustomerWorkIntoCustomerRig_NotRejected(t *testing.T) {
	townRoot := installGastownToolingGuardStubs(t,
		"Fix the payment handler 500 on empty cart in our checkout API",
		true /*customerRepo*/)

	result, _ := executeSling(SlingParams{
		BeadID:   "lb-real1",
		RigName:  "lia_bac",
		TownRoot: townRoot,
	})
	if result != nil && result.ErrMsg == "gastown-tooling-in-customer-rig" {
		t.Errorf("guard must not fire for genuine customer work; got ErrMsg=%q", result.ErrMsg)
	}
}
