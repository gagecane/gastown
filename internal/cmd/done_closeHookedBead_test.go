package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// setupCloseHookedBeadTestEnv wires up a minimal town layout plus a bd stub
// script whose close behavior is controlled by the caller. Returns the path
// to the closes log and configures the test environment so a subsequent
// updateAgentStateOnDone call exercises only the hooked-bead close path.
//
// The base bead has NO attached_molecule so the test isolates the gu-z93z fix
// (the merged-path close) from the orthogonal molecule/descendant close paths.
func setupCloseHookedBeadTestEnv(t *testing.T, bdScriptBody string) (townRoot, closesLog string) {
	t.Helper()

	townRoot = t.TempDir()

	// Workspace marker
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}

	// .beads directory
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown"}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog = filepath.Join(townRoot, "closes.log")

	// Replace placeholders in the stub script.
	script := strings.ReplaceAll(bdScriptBody, "__CLOSES_LOG__", closesLog)
	script = strings.ReplaceAll(script, "__TOWN_ROOT__", townRoot)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_ROLE", "polecat")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "nitro")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	return townRoot, closesLog
}

// stubBdWithRetryableClose returns a bd stub script where close calls for a
// specified bead ID fail until the N-th attempt, then succeed. Close calls
// for OTHER bead IDs always succeed on the first try. Every close call
// (successful or failed) is logged to the closes log with its attempt
// number, plus a flag indicating whether --force and --reason were passed.
//
// This lets tests verify:
//   - the caller retries on transient failure (gu-z93z primary)
//   - the caller uses --force to bypass dep checks (gu-z93z secondary)
//   - the caller supplies --reason for audit attribution (gu-z93z tertiary)
func stubBdWithRetryableClose(failingBead string, failUntilAttempt int) string {
	return fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nitro)
        echo '[{"id":"gt-gastown-polecat-nitro","title":"Polecat nitro","status":"open","hook_bead":"gt-base-123","agent_state":"working"}]'
        ;;
      gt-base-123)
        # Hooked bead: non-terminal, no attached molecule — focuses the test on
        # the final hooked-bead close path (gu-z93z).
        echo '[{"id":"gt-base-123","title":"Base bead","status":"in_progress","description":"some unrelated description"}]'
        ;;
    esac
    ;;
  list)
    echo '[]'
    ;;
  close)
    has_force=0
    has_reason=0
    ids=""
    for arg in "$@"; do
      case "$arg" in
        --force) has_force=1; continue;;
        --reason=*) has_reason=1; continue;;
        --*) continue;;
      esac
      ids="$ids $arg"
    done
    for id in $ids; do
      cnt_file="%s/close-$id.cnt"
      if [ -f "$cnt_file" ]; then
        cnt=$(cat "$cnt_file")
      else
        cnt=0
      fi
      cnt=$((cnt + 1))
      echo "$cnt" > "$cnt_file"
      echo "$id attempt=$cnt force=$has_force reason=$has_reason" >> "%s"
      if [ "$id" = "%s" ] && [ "$cnt" -lt %d ]; then
        echo "simulated dolt lock contention on $id (attempt $cnt)" >&2
        exit 1
      fi
    done
    ;;
  agent|update|slot|comments)
    exit 0
    ;;
esac
exit 0
`, "__TOWN_ROOT__", "__CLOSES_LOG__", failingBead, failUntilAttempt)
}

// withFastCloseBackoff disables the real time.Sleep between close retries so
// tests don't wait 6+ seconds. Restores the original at test end.
func withFastCloseBackoff(t *testing.T) {
	t.Helper()
	orig := closeHookedBeadBackoff
	closeHookedBeadBackoff = func(time.Duration) {} // skip sleeps
	t.Cleanup(func() { closeHookedBeadBackoff = orig })
}

// TestDoneCloseHookedBead_RetriesTransientFailure verifies the gu-z93z primary
// fix: when bd close fails transiently (dolt lock contention, subprocess
// glitch), updateAgentStateOnDone retries and eventually closes the bead.
// Before the fix, a single failure left the bead stuck IN_PROGRESS forever
// because the merged-path close used plain bd.Close with no retry.
func TestDoneCloseHookedBead_RetriesTransientFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}
	withFastCloseBackoff(t)

	script := stubBdWithRetryableClose("gt-base-123", 2) // first attempt fails
	townRoot, closesLog := setupCloseHookedBeadTestEnv(t, script)

	updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitCompleted, "gt-base-123")

	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("no closes log: %v", err)
	}
	closes := string(closesBytes)
	lines := strings.Split(strings.TrimSpace(closes), "\n")

	// Expect at least two close attempts recorded for gt-base-123:
	// attempt=1 failed, attempt=2 succeeded.
	var attempts []string
	for _, l := range lines {
		if strings.HasPrefix(l, "gt-base-123 ") {
			attempts = append(attempts, l)
		}
	}
	if len(attempts) < 2 {
		t.Fatalf("expected at least 2 close attempts for gt-base-123, got %d\nCloses log:\n%s", len(attempts), closes)
	}
	if !strings.Contains(attempts[0], "attempt=1") {
		t.Errorf("first attempt for gt-base-123 should be attempt=1, got %q", attempts[0])
	}
	// The second successful attempt must have completed — otherwise the bead
	// is stuck, which is exactly the gu-z93z failure mode.
	foundSuccess := false
	for _, a := range attempts[1:] {
		if strings.Contains(a, "attempt=2") || strings.Contains(a, "attempt=3") {
			foundSuccess = true
			break
		}
	}
	if !foundSuccess {
		t.Errorf("retry never occurred after transient failure — bead would be stuck IN_PROGRESS\nattempts:\n%s", strings.Join(attempts, "\n"))
	}
}

// TestDoneCloseHookedBead_UsesForceAndReason verifies the gu-z93z secondary
// fixes: the hooked-bead close is a --force close (bypassing dependency checks
// that could transiently block plain bd.Close) and carries an explicit
// --reason so the audit log records who closed the bead and why.
//
// Before gu-z93z the merged path called plain bd.Close with no --force and no
// --reason — making transient dependency failures silent and the close
// attribution invisible.
func TestDoneCloseHookedBead_UsesForceAndReason(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}
	withFastCloseBackoff(t)

	// failUntilAttempt=0 → every attempt succeeds on first try.
	script := stubBdWithRetryableClose("gt-never-fails", 0)
	townRoot, closesLog := setupCloseHookedBeadTestEnv(t, script)

	updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitCompleted, "gt-base-123")

	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("no closes log: %v", err)
	}
	closes := string(closesBytes)
	if !strings.Contains(closes, "gt-base-123") {
		t.Fatalf("gt-base-123 was not closed\nCloses log:\n%s", closes)
	}

	var baseLine string
	for _, l := range strings.Split(closes, "\n") {
		if strings.HasPrefix(l, "gt-base-123 ") {
			baseLine = l
			break
		}
	}
	if baseLine == "" {
		t.Fatalf("no close line recorded for gt-base-123")
	}
	if !strings.Contains(baseLine, "force=1") {
		t.Errorf("hooked bead close must use --force (bypass dep checks) — got %q", baseLine)
	}
	if !strings.Contains(baseLine, "reason=1") {
		t.Errorf("hooked bead close must include --reason (audit attribution) — got %q", baseLine)
	}
}

// TestDoneCloseHookedBead_GivesUpAfterMaxAttempts verifies that after 3
// attempts the close helper returns rather than retrying forever. The bead
// may still be left stuck (refinery is the backstop when gt done can't close),
// but gt done itself must not hang or loop.
func TestDoneCloseHookedBead_GivesUpAfterMaxAttempts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}
	withFastCloseBackoff(t)

	// failUntilAttempt=999 → no attempt will ever succeed.
	script := stubBdWithRetryableClose("gt-base-123", 999)
	townRoot, closesLog := setupCloseHookedBeadTestEnv(t, script)

	done := make(chan struct{})
	go func() {
		updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitCompleted, "gt-base-123")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("updateAgentStateOnDone hung on repeated close failures")
	}

	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("no closes log: %v", err)
	}
	closes := string(closesBytes)

	// Exactly 3 attempts should have been made (the helper's max) — not more,
	// not fewer.
	count := strings.Count(closes, "gt-base-123 ")
	if count != 3 {
		t.Errorf("expected exactly 3 close attempts for gt-base-123, got %d\nCloses log:\n%s", count, closes)
	}
}
