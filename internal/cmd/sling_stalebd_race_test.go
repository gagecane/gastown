package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestBdShowBeadOutputFresh_OmitsAllowStale verifies the fresh post-mutation
// helper does NOT pass --allow-stale to bd. This matters because in the
// dispatch flow `bd show <id>` runs immediately after `bd mol bond
// --auto-commit` mints a child issue (e.g. cadk-hc5.1). bd's stale-read
// path serves from an in-memory snapshot taken before the bond's Dolt
// commit was visible — producing spurious `'<id>' not found (not an issue
// ID or formula name)` errors and triggering wasted dispatch retries.
//
// The two stale-read offenders identified by mayor's trace (gc-wisp-pmav)
// are:
//   - storeFieldsInBead — reads the just-bonded bead before merging fields
//   - hookBeadWithRetry verify — re-reads the bead it just hooked to
//     confirm status/assignee stuck
//
// Both must use the fresh helpers introduced in gu-hxjx.
func TestBdShowBeadOutputFresh_OmitsAllowStale(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bd stub uses POSIX shell; skipping on Windows")
	}
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0o755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	flagLog := filepath.Join(townRoot, "flag.log")

	// Stub bd that records whether --allow-stale was present in argv on the
	// show subcommand. The probe call (bd show --allow-stale --help) used
	// for capability detection is allowed; we only record real show calls.
	bdScript := `#!/bin/sh
set -e
allow_stale=no
have_show=no
saw_help=no
for a in "$@"; do
  case "$a" in
    --allow-stale) allow_stale=yes ;;
    show) have_show=yes ;;
    --help|-h) saw_help=yes ;;
  esac
done
case "$1" in
  version)
    echo "bd 0.1.0"
    exit 0
    ;;
esac
if [ "$have_show" = yes ] && [ "$saw_help" != yes ]; then
  echo "$allow_stale" >> "` + flagLog + `"
  echo '[{"id":"cadk-hc5.1","title":"child","status":"open","assignee":""}]'
fi
exit 0
`
	_ = writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Fresh helper — must NOT use --allow-stale.
	if _, err := bdShowBeadOutputFresh("cadk-hc5.1"); err != nil {
		t.Fatalf("bdShowBeadOutputFresh: %v", err)
	}
	data, err := os.ReadFile(flagLog)
	if err != nil {
		t.Fatalf("read flag log: %v", err)
	}
	for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "yes" {
			t.Fatalf("bdShowBeadOutputFresh call %d included --allow-stale; gu-hxjx requires fresh reads", i)
		}
	}

	// And the from-townRoot variant.
	if err := os.Truncate(flagLog, 0); err != nil {
		t.Fatalf("truncate flag log: %v", err)
	}
	if _, err := bdShowBeadOutputFreshFromTownRoot(townRoot, "cadk-hc5.1"); err != nil {
		t.Fatalf("bdShowBeadOutputFreshFromTownRoot: %v", err)
	}
	data, err = os.ReadFile(flagLog)
	if err != nil {
		t.Fatalf("read flag log: %v", err)
	}
	for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "yes" {
			t.Fatalf("bdShowBeadOutputFreshFromTownRoot call %d included --allow-stale", i)
		}
	}
}

// TestBdShowBeadOutput_RetainsAllowStale verifies the OLD helper still
// passes --allow-stale, so cold/observational read paths (scheduler
// scans, status views) continue to enjoy the snapshot-read perf benefit.
// The fix is scoped to post-mutation reads only.
func TestBdShowBeadOutput_RetainsAllowStale(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bd stub uses POSIX shell; skipping on Windows")
	}
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0o755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	flagLog := filepath.Join(townRoot, "flag.log")
	bdScript := `#!/bin/sh
set -e
allow_stale=no
have_show=no
saw_help=no
for a in "$@"; do
  case "$a" in
    --allow-stale) allow_stale=yes ;;
    show) have_show=yes ;;
    --help|-h) saw_help=yes ;;
  esac
done
case "$1" in
  version)
    echo "bd 0.1.0"
    exit 0
    ;;
esac
if [ "$have_show" = yes ] && [ "$saw_help" != yes ]; then
  echo "$allow_stale" >> "` + flagLog + `"
  echo '[{"id":"gu-cold","title":"cold read","status":"open","assignee":""}]'
fi
exit 0
`
	_ = writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	if _, err := bdShowBeadOutput("gu-cold"); err != nil {
		t.Fatalf("bdShowBeadOutput: %v", err)
	}
	data, err := os.ReadFile(flagLog)
	if err != nil {
		t.Fatalf("read flag log: %v", err)
	}
	if !strings.Contains(string(data), "yes") {
		t.Fatalf("bdShowBeadOutput stopped sending --allow-stale; cold-read perf would regress. Log: %q", string(data))
	}
}

// TestBdShowBeadOutputFresh_RecoversChildAfterBondCommit simulates the
// dispatch-flow race: bd's stale-read snapshot is from BEFORE the bond
// row was committed, so the first show with --allow-stale returns "not
// found" while a fresh (live) read finds the bead. This is the exact
// failure mode mayor traced in gc-wisp-pmav with cadk-hc5.1.
//
// The stub returns "[]" (empty) when --allow-stale is requested and the
// real bead JSON otherwise, modelling the snapshot/live divergence.
func TestBdShowBeadOutputFresh_RecoversChildAfterBondCommit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bd stub uses POSIX shell; skipping on Windows")
	}
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0o755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
set -e
allow_stale=no
have_show=no
saw_help=no
for a in "$@"; do
  case "$a" in
    --allow-stale) allow_stale=yes ;;
    show) have_show=yes ;;
    --help|-h) saw_help=yes ;;
  esac
done
case "$1" in
  version)
    echo "bd 0.1.0"
    exit 0
    ;;
esac
if [ "$have_show" = yes ] && [ "$saw_help" != yes ]; then
  if [ "$allow_stale" = yes ]; then
    # Pre-bond snapshot: child does not exist.
    echo '[]'
  else
    # Fresh read: child has been committed.
    echo '[{"id":"cadk-hc5.1","title":"freshly-bonded child","status":"open","assignee":""}]'
  fi
fi
exit 0
`
	_ = writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Stale read returns "[]" — would fail the post-mutation lookup that
	// triggers the spurious 'not found' error mayor traced.
	staleOut, _ := bdShowBeadOutput("cadk-hc5.1")
	if strings.TrimSpace(string(staleOut)) != "[]" {
		t.Fatalf("stale path baseline: got %q, want [] (would-be pre-bond snapshot)", string(staleOut))
	}

	// Fresh read sees the committed row on the FIRST attempt.
	freshOut, freshErr := bdShowBeadOutputFresh("cadk-hc5.1")
	if freshErr != nil {
		t.Fatalf("bdShowBeadOutputFresh: %v", freshErr)
	}
	if !strings.Contains(string(freshOut), "cadk-hc5.1") {
		t.Fatalf("bdShowBeadOutputFresh = %q, want JSON containing cadk-hc5.1", string(freshOut))
	}
}
