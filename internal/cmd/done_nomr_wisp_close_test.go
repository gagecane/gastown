package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestCloseAttachedWispNoMR_ClosesWisp verifies that closeAttachedWispNoMR
// closes the molecule wisp bonded to a hooked bead. Regression test for
// gu-irou: without this cleanup, a later reopen of the bead leaves the wisp
// blocking redispatch.
func TestCloseAttachedWispNoMR_ClosesWisp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gastown"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")

	// bd stub:
	// - gt-base-456 has attached_molecule: gt-wisp-abc
	// - gt-wisp-abc exists (open, ephemeral)
	// - close logs ID per call
	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-base-456)
        echo '[{"id":"gt-base-456","title":"Base","status":"hooked","description":"attached_molecule: gt-wisp-abc"}]'
        ;;
      gt-wisp-abc)
        echo '[{"id":"gt-wisp-abc","title":"mol-polecat-work","status":"open","ephemeral":true}]'
        ;;
      *)
        echo '[]'
        ;;
    esac
    ;;
  list)
    echo '[]'
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
esac
exit 0
`, closesLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	bd := beads.New(filepath.Join(townRoot, "gastown"))
	closeAttachedWispNoMR(bd, "gt-base-456")

	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("expected wisp to be closed but closes.log missing: %v", err)
	}
	closes := string(closesBytes)
	if !strings.Contains(closes, "gt-wisp-abc") {
		t.Errorf("expected gt-wisp-abc to be closed, got:\n%s", closes)
	}
	// The base bead must NOT be closed by this helper — it only closes the wisp.
	// The caller (no-MR close path) handles the base bead via forceCloseWithRetry.
	if strings.Contains(closes, "gt-base-456") {
		t.Errorf("helper should NOT close the base bead, got:\n%s", closes)
	}
}

// TestCloseAttachedWispNoMR_NoMolecule verifies that the helper is a no-op
// when the hooked bead has no attached molecule (e.g., raw bead hook, or
// already-detached molecule).
func TestCloseAttachedWispNoMR_NoMolecule(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gastown"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")

	// gt-base-789 has no attached_molecule field in description
	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-base-789)
        echo '[{"id":"gt-base-789","title":"Base","status":"hooked","description":"plain description with no molecule"}]'
        ;;
      *)
        echo '[]'
        ;;
    esac
    ;;
  list)
    echo '[]'
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
esac
exit 0
`, closesLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	bd := beads.New(filepath.Join(townRoot, "gastown"))
	// Should be a clean no-op when no molecule is attached.
	closeAttachedWispNoMR(bd, "gt-base-789")

	if data, err := os.ReadFile(closesLog); err == nil && len(data) > 0 {
		t.Errorf("expected no closes when no molecule attached, got:\n%s", string(data))
	}
}

// TestCloseAttachedWispNoMR_ShowError verifies that the helper is a clean
// no-op when bd.Show fails (e.g., bead missing, transient db error). Closing
// the work bead is the primary goal of the no-MR path; a stuck wisp is a
// downstream cleanup the witness/reapers handle.
func TestCloseAttachedWispNoMR_ShowError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gastown"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")

	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    echo "bd: error" >&2
    exit 1
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
esac
exit 0
`, closesLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	bd := beads.New(filepath.Join(townRoot, "gastown"))
	// Must not panic, must not close anything.
	closeAttachedWispNoMR(bd, "gt-base-doesntexist")

	if data, err := os.ReadFile(closesLog); err == nil && len(data) > 0 {
		t.Errorf("expected no closes on bd.Show error, got:\n%s", string(data))
	}
}
