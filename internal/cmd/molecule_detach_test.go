package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestRunMoleculeDetach_RouteToWrongRigFallsBackToScan verifies the fix for
// gu-vkg3: when a pinned bead has a prefix that routes to a different rig
// than where it actually lives (legacy/misclassified pin), `gt mol detach`
// falls back to a .beads directory scan and still succeeds.
//
// Layout under test:
//   - townRoot/.beads         (owns "gt-" prefix per routes.jsonl)
//   - townRoot/legacy/.beads  (holds the pinned bead gt-stale-1)
//
// Without the fallback, Show("gt-stale-1") routes to the town DB and returns
// ErrNotFound, surfacing as "Error: checking attachment: issue not found".
// With the fallback, we scan rigs and find the bead in legacy/.
func TestRunMoleculeDetach_RouteToWrongRigFallsBackToScan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()

	// Workspace markers.
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0o755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}

	// Two .beads directories: town-level and a rig-level that holds the bead.
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(townBeads, "locks"), 0o755); err != nil {
		t.Fatalf("mkdir town beads: %v", err)
	}
	legacyRig := filepath.Join(townRoot, "legacy")
	legacyBeads := filepath.Join(legacyRig, ".beads")
	if err := os.MkdirAll(filepath.Join(legacyBeads, "locks"), 0o755); err != nil {
		t.Fatalf("mkdir legacy beads: %v", err)
	}

	// routes.jsonl mapping "gt-" → town root (so prefix routing sends us there).
	routes := `{"prefix":"gt-","path":"."}` + "\n"
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routes), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	// Stub bd binary. It returns ErrNotFound in the town DB for gt-stale-1
	// and returns the pinned bead (with attachment) in the legacy DB.
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	updateLog := filepath.Join(townRoot, "updates.log")
	bdScript := fmt.Sprintf(`#!/bin/sh
# Log invocation context for debugging
echo "PWD=$PWD BEADS_DIR=$BEADS_DIR args: $*" >> "%s.trace"

# Strip --allow-stale (not relevant here)
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"; shift

# Use BEADS_DIR (set explicitly by the caller) OR cwd to decide which DB
# we are pretending to be. BEADS_DIR points to a .beads dir; its parent
# is the rig path.
location=""
if [ -n "$BEADS_DIR" ]; then
    location="$BEADS_DIR"
else
    location="$PWD/.beads"
fi

case "$cmd" in
  show)
    # Show returns the pinned bead only in the legacy DB.
    case "$location" in
      *legacy*)
        # bd show --json prints an array
        cat <<'JSON'
[{"id":"gt-stale-1","title":"witness Handoff","status":"pinned","description":"attached_molecule: mol-dead-1\nattached_at: 2025-01-01T00:00:00Z"}]
JSON
        ;;
      *)
        echo "Issue not found: gt-stale-1" >&2
        exit 1
        ;;
    esac
    ;;
  update)
    # Record where updates land so the test can verify they hit the correct DB.
    echo "location=$location args=$*" >> "%s"
    ;;
  *)
    # Any other command: succeed silently.
    ;;
esac
exit 0
`, updateLog, updateLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Ensure the runner doesn't accidentally re-use an inherited BEADS_DIR.
	t.Setenv("BEADS_DIR", "")
	t.Setenv(EnvGTRole, "")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_RIG", "")
	t.Setenv("TMUX_PANE", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	// Run from townRoot so findLocalBeadsDir() lands on the town DB —
	// exactly the failure mode reported in gu-vkg3 (bead lives in a rig
	// but tool is invoked from the town-level dir).
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	err = runMoleculeDetach(nil, []string{"gt-stale-1"})
	if err != nil {
		t.Fatalf("runMoleculeDetach: expected success via fallback scan, got error: %v", err)
	}

	// Verify the update landed in the legacy rig DB, not the town DB.
	data, err := os.ReadFile(updateLog)
	if err != nil {
		t.Fatalf("no updates recorded — DetachMoleculeWithAuditLocal did not run: %v", err)
	}
	if !strings.Contains(string(data), "legacy") {
		t.Errorf("update did not target legacy rig DB.\nupdates.log:\n%s", data)
	}
	// The update should clear attached_molecule: make sure the new
	// description does NOT contain "mol-dead-1".
	if strings.Contains(string(data), "mol-dead-1") {
		t.Errorf("update still contains dead molecule reference.\nupdates.log:\n%s", data)
	}
}

// TestRunMoleculeDetach_BeadMissingEverywhere verifies that when the pinned
// bead genuinely does not exist in any rig DB, runMoleculeDetach surfaces
// a clear "issue not found" error rather than succeeding silently.
func TestRunMoleculeDetach_BeadMissingEverywhere(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0o755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(townBeads, "locks"), 0o755); err != nil {
		t.Fatalf("mkdir town beads: %v", err)
	}
	// Include a rig so the fallback scan has at least one candidate.
	rigBeads := filepath.Join(townRoot, "rig1", ".beads")
	if err := os.MkdirAll(filepath.Join(rigBeads, "locks"), 0o755); err != nil {
		t.Fatalf("mkdir rig beads: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	// Stub bd always returns "not found" for show.
	bdScript := `#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"; shift
case "$cmd" in
  show)
    echo "Issue not found: $1" >&2
    exit 1
    ;;
esac
exit 0
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BEADS_DIR", "")
	t.Setenv(EnvGTRole, "")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_RIG", "")
	t.Setenv("TMUX_PANE", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	err = runMoleculeDetach(nil, []string{"ghost-1"})
	if err == nil {
		t.Fatalf("expected error for non-existent bead, got nil")
	}
	if !strings.Contains(err.Error(), "checking attachment") {
		t.Errorf("error does not wrap the expected context: %v", err)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error does not indicate not-found: %v", err)
	}
}

// TestDiscoverRigBeadsDirs verifies that the helper picks up both the
// town-level .beads and every first-level rig .beads directory, which is
// what the fallback scan relies on to find misclassified pinned beads.
func TestDiscoverRigBeadsDirs(t *testing.T) {
	townRoot := t.TempDir()

	// Town-level .beads.
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0o755); err != nil {
		t.Fatalf("mkdir town beads: %v", err)
	}

	// Two rigs with .beads, plus one directory without .beads.
	for _, rig := range []string{"rig_a", "rig_b"} {
		if err := os.MkdirAll(filepath.Join(townRoot, rig, ".beads"), 0o755); err != nil {
			t.Fatalf("mkdir rig %s: %v", rig, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "not_a_rig"), 0o755); err != nil {
		t.Fatalf("mkdir not_a_rig: %v", err)
	}

	// Create a file (not directory) to make sure we don't mis-ID it.
	if err := os.WriteFile(filepath.Join(townRoot, "some_file"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	got := discoverRigBeadsDirs(townRoot)

	want := map[string]bool{
		filepath.Join(townRoot, ".beads"):         true,
		filepath.Join(townRoot, "rig_a", ".beads"): true,
		filepath.Join(townRoot, "rig_b", ".beads"): true,
	}
	if len(got) != len(want) {
		t.Errorf("discoverRigBeadsDirs returned %d dirs, want %d: %v", len(got), len(want), got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("discoverRigBeadsDirs returned unexpected dir %q", g)
		}
		delete(want, g)
	}
	for missing := range want {
		t.Errorf("discoverRigBeadsDirs missed %q", missing)
	}
}
