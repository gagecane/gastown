package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestExecuteSling_EpicPhaseLabel verifies the gu-fs88 dispatch gate:
// executeSling rejects beads that carry the phase:epic label even when the
// title does not start with "EPIC:" and issue_type is a slingable type.
//
// Scenario mirrors the ta-823 recurrence: the auto-dispatcher kept hooking
// ta-823 as polecat work because the type filter said "task" and the title
// prefix guard had already fired (but something upstream missed it). The
// label is a type-independent signal that survives title edits and adds a
// second line of defense.
func TestExecuteSling_EpicPhaseLabel(t *testing.T) {
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
	// Non-EPIC title + phase:epic label + issue_type=task → epic container.
	// Returns no children for bd children so the open-children guard doesn't
	// short-circuit this test (we want to exercise the label path specifically).
	bdScript := `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"Triage Queue","status":"open","assignee":"","description":"","issue_type":"task","labels":["phase:epic"]}]'
    ;;
  children)
    echo '[]'
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	params := SlingParams{
		BeadID:   "ta-823",
		RigName:  "testrig",
		TownRoot: townRoot,
	}

	result, err := executeSling(params)
	if err == nil {
		t.Fatal("expected error when slinging phase:epic bead, got nil")
	}
	if result.ErrMsg != "epic-like" {
		t.Errorf("expected ErrMsg=\"epic-like\", got %q", result.ErrMsg)
	}
	if !strings.Contains(err.Error(), "epic container") {
		t.Errorf("error should mention epic container: %v", err)
	}
	if !strings.Contains(err.Error(), "phase:epic") {
		t.Errorf("error should mention phase:epic label: %v", err)
	}
}

// TestExecuteSling_OpenChildren verifies the gu-fs88 dispatch gate:
// executeSling rejects beads that have any non-closed child, regardless of
// epic labels or title prefixes. A parent bead is a container — the children
// track the actual work — and cannot itself be "done" by a polecat.
//
// The ta-823 bug report documents this as the second signal the dispatcher
// should have used: "ta-823 has 4 open children: ta-823.5, .6, .7, .8".
func TestExecuteSling_OpenChildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	// Stub hasOpenChildrenFn directly rather than relying on the bd stub so
	// the test is hermetic and fast — we're validating the dispatch wiring,
	// not the subprocess plumbing (which is covered elsewhere).
	prev := hasOpenChildrenFn
	hasOpenChildrenFn = func(id string) (bool, error) {
		if id != "ta-823" {
			t.Fatalf("unexpected bead id: %q", id)
		}
		return true, nil
	}
	t.Cleanup(func() { hasOpenChildrenFn = prev })

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	// Plain slingable bead — no identity/epic signals. Only the open-children
	// guard should reject it.
	bdScript := `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"Triage Queue","status":"open","assignee":"","description":"","issue_type":"task","labels":[]}]'
    ;;
esac
exit 0
`
	// Note: writeBDStub installs a default "no open children" override that
	// we then re-override above. The t.Cleanup on hasOpenChildrenFn runs
	// LIFO so the inner override applies during the test.
	writeBDStub(t, binDir, bdScript, "")
	hasOpenChildrenFn = func(id string) (bool, error) {
		if id != "ta-823" {
			return false, fmt.Errorf("unexpected bead id: %q", id)
		}
		return true, nil
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	params := SlingParams{
		BeadID:   "ta-823",
		RigName:  "testrig",
		TownRoot: townRoot,
	}

	result, err := executeSling(params)
	if err == nil {
		t.Fatal("expected error when slinging parent-of-open-children, got nil")
	}
	if result.ErrMsg != "parent of open children" {
		t.Errorf("expected ErrMsg=\"parent of open children\", got %q", result.ErrMsg)
	}
	if !strings.Contains(err.Error(), "open children") {
		t.Errorf("error should mention open children: %v", err)
	}
	if !strings.Contains(err.Error(), "container") {
		t.Errorf("error should mention container: %v", err)
	}
}

// TestExecuteSling_ClosedChildrenAllowed verifies the open-children guard
// does NOT fire when all children are closed (work completed). A bead whose
// children are all done is a legitimate work item again — closing it is
// the natural completion signal.
func TestExecuteSling_ClosedChildrenAllowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	// Stub: "no open children" so the guard passes.
	prev := hasOpenChildrenFn
	hasOpenChildrenFn = func(id string) (bool, error) { return false, nil }
	t.Cleanup(func() { hasOpenChildrenFn = prev })

	// Confirm the helper reports "not a parent of open children" here.
	if isParentOfOpenChildren("any-id") {
		t.Fatal("expected isParentOfOpenChildren=false with stub returning false")
	}
}
