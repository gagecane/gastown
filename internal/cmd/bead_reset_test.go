package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestBeadResetClosesMoleculeAndContext verifies that `gt bead reset` runs the
// full cleanup sequence on a stuck bead: detach + force-close molecule wisp,
// remove dep bond, close orphan sling-context, and flip the bead to open.
//
// This is the gu-8f7u acceptance test: after gu-koi7 cleared the description
// pointer, the wisp + dep bond + sling-context still leaked. The reset command
// must touch all four.
func TestBeadResetClosesMoleculeAndContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()

	// Workspace marker: mayor/ directory (SecondaryMarker)
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	bdLog := filepath.Join(townRoot, "bd.log")

	// bd stub:
	//   - bd show on the work bead returns description with attached_molecule + dep on wisp
	//   - bd list --label=gt:sling-context --status=open returns a context tracking the bead
	//   - bd update / bd close / bd dep remove are recorded for assertions
	bdScript := fmt.Sprintf(`#!/bin/sh
# Log everything the test cares about
echo "$@" >> %q
# Strip --allow-stale anywhere in the args
args=""
for a in "$@"; do
  if [ "$a" != "--allow-stale" ]; then
    args="$args $a"
  fi
done
set -- $args
cmd="$1"; shift
case "$cmd" in
  show)
    bead="$1"
    case "$bead" in
      gt-work-1)
        # work bead with attached molecule (description) + dep on the wisp
        cat <<'JSON'
[{"id":"gt-work-1","title":"work bead","status":"in_progress","assignee":"rig/polecats/dead","description":"attached_molecule: gt-wisp-mol1\nattached_at: 2026-05-15T00:00:00Z","dependencies":[{"id":"gt-wisp-mol1","status":"open"}]}]
JSON
        ;;
      *) echo '[]' ;;
    esac
    ;;
  list)
    # ListOpenSlingContexts: --label=gt:sling-context --status=open --json --limit=0
    if echo "$*" | grep -q "label=gt:sling-context"; then
      cat <<'JSON'
[{"id":"gt-ctx-1","title":"sling-context: work bead","status":"open","description":"{\"version\":1,\"work_bead_id\":\"gt-work-1\",\"target_rig\":\"rig\",\"enqueued_at\":\"2026-05-15T00:00:00Z\"}"}]
JSON
    else
      # closeDescendants etc — leaf wisp, no children
      echo '[]'
    fi
    ;;
  update)
    exit 0
    ;;
  dep)
    # dep remove gt-work-1 gt-wisp-mol1
    exit 0
    ;;
  close)
    exit 0
    ;;
esac
exit 0
`, bdLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BEADS_DIR", "")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_RIG", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv(EnvGTRole, "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Save and restore global flag state
	prevReason := beadResetReason
	prevDry := beadResetDryRun
	prevKeep := beadResetKeepOpen
	t.Cleanup(func() {
		beadResetReason = prevReason
		beadResetDryRun = prevDry
		beadResetKeepOpen = prevKeep
	})
	beadResetReason = "test reset"
	beadResetDryRun = false
	beadResetKeepOpen = false

	cmd := &cobra.Command{}
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)

	if err := runBeadReset(cmd, []string{"gt-work-1"}); err != nil {
		t.Fatalf("runBeadReset: %v", err)
	}

	logBytes, err := os.ReadFile(bdLog)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	log := string(logBytes)

	// 1. The wisp must have been force-closed.
	if !strings.Contains(log, "close gt-wisp-mol1") {
		t.Errorf("expected `bd close gt-wisp-mol1` (wisp force-close), got:\n%s", log)
	}

	// 2. The dep bond between work bead and wisp must have been removed.
	if !strings.Contains(log, "dep remove gt-work-1 gt-wisp-mol1") {
		t.Errorf("expected `bd dep remove gt-work-1 gt-wisp-mol1`, got:\n%s", log)
	}

	// 3. The sling-context must have been closed.
	if !strings.Contains(log, "close gt-ctx-1") {
		t.Errorf("expected `bd close gt-ctx-1` (sling-context close), got:\n%s", log)
	}

	// 4. The work bead must have been flipped to status=open with cleared assignee.
	if !strings.Contains(log, "update gt-work-1 --status=open --assignee=") {
		t.Errorf("expected `bd update gt-work-1 --status=open --assignee=` (release), got:\n%s", log)
	}
}

// TestBeadResetDryRun verifies --dry-run takes no destructive actions: no
// close, no dep remove, no status flip. Only `bd show` and `bd list`
// (read-only discovery) are allowed.
func TestBeadResetDryRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads", "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	bdLog := filepath.Join(townRoot, "bd.log")

	bdScript := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
args=""
for a in "$@"; do
  if [ "$a" != "--allow-stale" ]; then
    args="$args $a"
  fi
done
set -- $args
cmd="$1"; shift
case "$cmd" in
  show)
    cat <<'JSON'
[{"id":"gt-work-1","title":"work","status":"in_progress","assignee":"rig/polecats/dead","description":"attached_molecule: gt-wisp-mol1","dependencies":[{"id":"gt-wisp-mol1","status":"open"}]}]
JSON
    ;;
  list)
    echo '[]'
    ;;
esac
exit 0
`, bdLog)

	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BEADS_DIR", "")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_RIG", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv(EnvGTRole, "")

	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	prevDry := beadResetDryRun
	t.Cleanup(func() { beadResetDryRun = prevDry })
	beadResetDryRun = true

	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})

	if err := runBeadReset(cmd, []string{"gt-work-1"}); err != nil {
		t.Fatalf("runBeadReset: %v", err)
	}

	logBytes, _ := os.ReadFile(bdLog)
	log := string(logBytes)

	for _, forbidden := range []string{"close ", "dep remove", "update gt-work-1"} {
		if strings.Contains(log, forbidden) {
			t.Errorf("dry-run must not invoke %q, log:\n%s", forbidden, log)
		}
	}
}

// TestBeadResetRefusesClosedBead verifies that resetting a closed bead is
// rejected — reopening completed work would mask the closure.
func TestBeadResetRefusesClosedBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads", "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	bdScript := `#!/bin/sh
args=""
for a in "$@"; do
  if [ "$a" != "--allow-stale" ]; then
    args="$args $a"
  fi
done
set -- $args
cmd="$1"; shift
case "$cmd" in
  show)
    echo '[{"id":"gt-work-1","title":"work","status":"closed","assignee":"","description":""}]'
    ;;
  *) echo '[]' ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BEADS_DIR", "")
	t.Setenv(EnvGTRole, "")

	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})

	err := runBeadReset(cmd, []string{"gt-work-1"})
	if err == nil {
		t.Fatal("expected error resetting closed bead, got nil")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Errorf("expected error to mention 'closed', got: %v", err)
	}
}
