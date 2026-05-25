//go:build integration

package cmd

// Integration-only test helpers for scheduler tests. These require the
// "integration" build tag because they are only called from
// scheduler_integration_test.go (which also has that tag). They were
// mistakenly removed in b40fbb58 by staticcheck U1000 which did not see
// their callers behind the build tag.

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/testutil"
)

// --- Environment helpers ---

// cleanSchedulerTestEnv returns os.Environ() with GT_*/BD_* variables removed
// (except GT_DOLT_PORT and BEADS_DOLT_PORT) and HOME overridden to tmpHome.
// This isolates gt/bd processes from the host while preserving test Dolt routing.
func cleanSchedulerTestEnv(tmpHome string) []string {
	return testutil.CleanGTEnv("HOME=" + tmpHome)
}

// --- gt command helpers ---

// runGTCmdOutput runs a gt command and returns stdout only.
// Fails the test if the command exits non-zero.
func runGTCmdOutput(t *testing.T, binary, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("gt %v failed: %v\nstdout:\n%s\nstderr:\n%s", args, err, out, stderr.String())
	}
	return string(out)
}

// runGTCmdMayFail runs a gt command and returns combined output and any error.
// Does NOT fail the test on non-zero exit.
func runGTCmdMayFail(t *testing.T, binary, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// --- Scheduler query helpers ---

// getSchedulerStatus runs `gt scheduler status --json` and returns the parsed output.
func getSchedulerStatus(t *testing.T, gtBinary, dir string, env []string) map[string]interface{} {
	t.Helper()
	out := runGTCmdOutput(t, gtBinary, dir, env, "scheduler", "status", "--json")
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parse scheduler status JSON: %v\nraw: %s", err, out)
	}
	return result
}

// getSchedulerList runs `gt scheduler list --json` and returns the parsed output.
func getSchedulerList(t *testing.T, gtBinary, dir string, env []string) []map[string]interface{} {
	t.Helper()
	out := runGTCmdOutput(t, gtBinary, dir, env, "scheduler", "list", "--json")
	var result []map[string]interface{}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parse scheduler list JSON: %v\nraw: %s", err, out)
	}
	return result
}

// --- Bead helpers ---

// createTestBead creates a bead with the given title using bd create and returns
// the auto-generated bead ID.
func createTestBead(t *testing.T, dir, title string) string {
	t.Helper()
	args := []string{"create", "--title=" + title, "--type=task",
		"--description=Integration test bead", "--json"}
	cmd := exec.Command("bd", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		// Capture stderr for diagnostics
		cmd2 := exec.Command("bd", args...)
		cmd2.Dir = dir
		combined, _ := cmd2.CombinedOutput()
		t.Fatalf("bd create failed: %v\n%s", err, combined)
	}
	var issue struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &issue); err != nil {
		t.Fatalf("parse bd create output: %v\nraw: %s", err, out)
	}
	if issue.ID == "" {
		t.Fatalf("bd create returned empty ID\nraw: %s", out)
	}
	return issue.ID
}

// createTestBeadOfType creates a bead with the given title and issue type (e.g.,
// "epic", "convoy", "task") and returns the auto-generated bead ID.
func createTestBeadOfType(t *testing.T, dir, title, issueType string) string {
	t.Helper()
	args := []string{"create", "--title=" + title, "--type=" + issueType,
		"--description=Integration test bead", "--json"}
	cmd := exec.Command("bd", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		cmd2 := exec.Command("bd", args...)
		cmd2.Dir = dir
		combined, _ := cmd2.CombinedOutput()
		t.Fatalf("bd create --type=%s failed: %v\n%s", issueType, err, combined)
	}
	var issue struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &issue); err != nil {
		t.Fatalf("parse bd create output: %v\nraw: %s", err, out)
	}
	if issue.ID == "" {
		t.Fatalf("bd create returned empty ID\nraw: %s", out)
	}
	return issue.ID
}

// beadHasLabel checks whether a bead has the specified label.
// Runs bd show --json from dir and inspects the labels array.
func beadHasLabel(t *testing.T, beadID, label, dir string) bool {
	t.Helper()
	args := beads.MaybePrependAllowStale([]string{"show", beadID, "--json"})
	cmd := exec.Command("bd", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bd show %s failed: %v", beadID, err)
	}
	var issues []struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(out, &issues); err != nil {
		t.Fatalf("parse bd show %s: %v", beadID, err)
	}
	if len(issues) == 0 {
		t.Fatalf("bd show %s returned no results", beadID)
	}
	for _, l := range issues[0].Labels {
		if l == label {
			return true
		}
	}
	return false
}

// getBeadDescription returns the description of a bead via bd show --json.
func getBeadDescription(t *testing.T, beadID, dir string) string {
	t.Helper()
	args := beads.MaybePrependAllowStale([]string{"show", beadID, "--json"})
	cmd := exec.Command("bd", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("bd show %s failed: %v\nstderr: %s", beadID, err, exitErr.Stderr)
		}
		t.Fatalf("bd show %s failed: %v", beadID, err)
	}
	var issues []struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(out, &issues); err != nil {
		t.Fatalf("parse bd show %s: %v", beadID, err)
	}
	if len(issues) == 0 {
		t.Fatalf("bd show %s returned no results", beadID)
	}
	return issues[0].Description
}

// addBeadDependency adds a blocking dependency: blocker blocks blocked.
func addBeadDependency(t *testing.T, blocked, blocker, dir string) {
	t.Helper()
	cmd := exec.Command("bd", "dep", "add", blocked, blocker)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd dep add %s %s failed: %v\n%s", blocked, blocker, err, out)
	}
}

// addBeadDependencyOfType adds a dependency with a specific type (e.g., "tracks",
// "depends_on"). The from bead must exist in the local DB at dir; the to bead can
// be in a different DB if routes.jsonl is present in dir's .beads/.
func addBeadDependencyOfType(t *testing.T, from, to, depType, dir string) {
	t.Helper()
	cmd := exec.Command("bd", "dep", "add", from, to, "--type="+depType)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd dep add %s %s --type=%s failed: %v\n%s", from, to, depType, err, out)
	}
}

// slingToScheduler runs `gt sling <bead> <rig> --hook-raw-bead` in deferred mode.
// The test setup (configureScheduler) sets max_polecats > 0, so gt sling
// automatically defers dispatch without a --scheduler flag.
// Uses --hook-raw-bead to skip formula cooking (no formula infrastructure
// in integration tests).
func slingToScheduler(t *testing.T, gtBinary, dir string, env []string, beadID, rig string, extraFlags ...string) string {
	t.Helper()
	args := []string{"sling", beadID, rig, "--hook-raw-bead"}
	args = append(args, extraFlags...)
	return runGTCmdOutput(t, gtBinary, dir, env, args...)
}
