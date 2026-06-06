package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installBlockedGuardStubs wires a minimal `bd show` stub (an open, plain
// slingable task with no blocking signals other than the injected seams) plus
// the two injectable guard seams. Returns the town root.
//
// The bd stub only needs to satisfy the bead-info read at the top of
// executeSling; the blocked-ness and dispatch-mode decisions are driven through
// the injected seams so the test never shells out to `bd blocked` or reads a
// town settings file.
func installBlockedGuardStubs(t *testing.T, blocked, deferred bool) string {
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
	bdScript := `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"Wave-2 step","status":"open","assignee":"","description":"","issue_type":"task","labels":[]}]'
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	prevBlocked := isBeadBlockedByOpenDepsFn
	prevDefer := shouldDeferDispatchFn
	isBeadBlockedByOpenDepsFn = func(_, _ string) bool { return blocked }
	shouldDeferDispatchFn = func() (bool, error) { return deferred, nil }
	t.Cleanup(func() {
		isBeadBlockedByOpenDepsFn = prevBlocked
		shouldDeferDispatchFn = prevDefer
	})

	return townRoot
}

// TestExecuteSling_BlockedInDirectMode_Rejected verifies the gu-gzng2 guard:
// in direct-dispatch mode a bead blocked by open dependencies is REFUSED with a
// clear message instead of being silently dropped from the scheduler.
func TestExecuteSling_BlockedInDirectMode_Rejected(t *testing.T) {
	townRoot := installBlockedGuardStubs(t, true /*blocked*/, false /*deferred*/)

	result, err := executeSling(SlingParams{
		BeadID:   "gu-wfs-vyaf2",
		RigName:  "testrig",
		TownRoot: townRoot,
	})
	if err == nil {
		t.Fatal("expected error when slinging a blocked bead in direct mode, got nil")
	}
	if result.ErrMsg != "blocked by open dependencies" {
		t.Errorf("expected ErrMsg=\"blocked by open dependencies\", got %q", result.ErrMsg)
	}
	if !strings.Contains(err.Error(), "blocked by open dependencies") {
		t.Errorf("error should explain the bead is blocked: %v", err)
	}
	if !strings.Contains(err.Error(), "scheduler.max_polecats") {
		t.Errorf("error should point at deferred dispatch as the held alternative: %v", err)
	}
}

// TestExecuteSling_BlockedInDeferredMode_NotRejected verifies the guard is
// scoped to direct mode only. In deferred mode the dispatcher holds blocked
// beads upstream, so the guard must NOT fire — a blocked bead reaching
// executeSling here should fall through (and fail later for an unrelated reason,
// never with the blocked-deps message).
func TestExecuteSling_BlockedInDeferredMode_NotRejected(t *testing.T) {
	townRoot := installBlockedGuardStubs(t, true /*blocked*/, true /*deferred*/)

	result, _ := executeSling(SlingParams{
		BeadID:   "gu-wfs-vyaf2",
		RigName:  "testrig",
		TownRoot: townRoot,
	})
	if result != nil && result.ErrMsg == "blocked by open dependencies" {
		t.Errorf("guard must not fire in deferred mode; got ErrMsg=%q", result.ErrMsg)
	}
}

// TestExecuteSling_BlockedWithForce_NotRejected verifies --force bypasses the
// blocked-deps guard for the operator who knowingly pre-spawns a polecat on
// soon-to-clear work.
func TestExecuteSling_BlockedWithForce_NotRejected(t *testing.T) {
	townRoot := installBlockedGuardStubs(t, true /*blocked*/, false /*deferred*/)

	result, _ := executeSling(SlingParams{
		BeadID:   "gu-wfs-vyaf2",
		RigName:  "testrig",
		TownRoot: townRoot,
		Force:    true,
	})
	if result != nil && result.ErrMsg == "blocked by open dependencies" {
		t.Errorf("--force must bypass the blocked-deps guard; got ErrMsg=%q", result.ErrMsg)
	}
}

// TestExecuteSling_UnblockedInDirectMode_NotRejected verifies the guard leaves
// ordinary unblocked work untouched in direct mode — the common path.
func TestExecuteSling_UnblockedInDirectMode_NotRejected(t *testing.T) {
	townRoot := installBlockedGuardStubs(t, false /*blocked*/, false /*deferred*/)

	result, _ := executeSling(SlingParams{
		BeadID:   "gu-wfs-vyaf2",
		RigName:  "testrig",
		TownRoot: townRoot,
	})
	if result != nil && result.ErrMsg == "blocked by open dependencies" {
		t.Errorf("guard must not fire for an unblocked bead; got ErrMsg=%q", result.ErrMsg)
	}
}

// TestIsBeadBlockedByOpenDeps_FailsOpen verifies the safety contract: when the
// underlying `bd blocked` query fails (here: every dir errors), the helper
// returns false (not blocked) rather than turning a transient query failure into
// a dispatch refusal that a plain `gt sling` would not otherwise hit.
func TestIsBeadBlockedByOpenDeps_FailsOpen(t *testing.T) {
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
	// bd stub that fails the `blocked` subcommand so listBlockedWorkBeadIDsWithError
	// returns an error for every dir.
	bdScript := `#!/bin/sh
case "$1" in
  blocked) echo "bd: blocked query failed" >&2; exit 1 ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if isBeadBlockedByOpenDeps(townRoot, "gu-wfs-vyaf2") {
		t.Error("isBeadBlockedByOpenDeps must fail open (return false) when bd blocked errors")
	}
}
