package witness

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/tmux"
)

func TestRecordStuckInDoneRestart_Increments(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	if count := RecordStuckInDoneRestart(tmpDir, "bead-1"); count != 1 {
		t.Errorf("first RecordStuckInDoneRestart = %d, want 1", count)
	}
	if count := RecordStuckInDoneRestart(tmpDir, "bead-1"); count != 2 {
		t.Errorf("second RecordStuckInDoneRestart = %d, want 2", count)
	}
	// A different bead is tracked independently.
	if count := RecordStuckInDoneRestart(tmpDir, "bead-2"); count != 1 {
		t.Errorf("RecordStuckInDoneRestart(bead-2) = %d, want 1", count)
	}
}

func TestShouldEscalateStuckInDone_Threshold(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	// Unknown bead: never escalate.
	if ShouldEscalateStuckInDone(tmpDir, "unknown") {
		t.Error("ShouldEscalateStuckInDone = true for unknown bead")
	}

	// Below the cap: do not escalate.
	for i := 0; i < MaxStuckInDoneAutoRestarts-1; i++ {
		RecordStuckInDoneRestart(tmpDir, "bead-3")
	}
	if ShouldEscalateStuckInDone(tmpDir, "bead-3") {
		t.Errorf("ShouldEscalateStuckInDone = true below cap (%d restarts)", MaxStuckInDoneAutoRestarts-1)
	}

	// At the cap: escalate.
	RecordStuckInDoneRestart(tmpDir, "bead-3")
	if !ShouldEscalateStuckInDone(tmpDir, "bead-3") {
		t.Errorf("ShouldEscalateStuckInDone = false at cap (%d restarts)", MaxStuckInDoneAutoRestarts)
	}
}

func TestRecordStuckInDoneRestart_ConcurrentSafe(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	const n = 20
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			RecordStuckInDoneRestart(tmpDir, "bead-concurrent")
		}()
	}
	wg.Wait()

	// The final count must reflect all n increments with no lost updates.
	if got := RecordStuckInDoneRestart(tmpDir, "bead-concurrent"); got != n+1 {
		t.Errorf("after %d concurrent increments + 1, count = %d, want %d", n, got, n+1)
	}
}

// TestStuckInDoneCappedImpliesActiveWork guards that the capped classification
// still routes to the active-work verdict (and thus mayor notification), the
// same as the uncapped stuck-in-done case. (gu-5npkm)
func TestStuckInDoneCappedImpliesActiveWork(t *testing.T) {
	if !ZombieStuckInDoneCapped.ImpliesActiveWork() {
		t.Error("ZombieStuckInDoneCapped.ImpliesActiveWork() = false, want true")
	}
}

// TestReleaseCappedStuckInDone_ResetsBead is the regression guard for gu-lrfqn:
// when a live polecat is capped on stuck-in-done and has NO pending MR, the
// witness must release the strand — reset the hooked bead for re-dispatch
// (RECOVERED_BEAD) instead of only escalating and leaving the bead HOOKED on a
// wedged session.
func TestReleaseCappedStuckInDone_ResetsBead(t *testing.T) {
	// Not parallel: overrides package-level verifyCommitOnMain.
	oldVerify := verifyCommitOnMain
	verifyCommitOnMain = func(workDir, rigName, polecatName string) (bool, error) {
		return false, nil // work NOT on main → reset for re-dispatch
	}
	t.Cleanup(func() { verifyCommitOnMain = oldVerify })

	resetRedispatchLimitersForTest()
	t.Cleanup(resetRedispatchLimitersForTest)

	// list (cleanup-wisp lookup) → [] (no pending MR); show → hooked bead.
	bd, mock := mockBd(
		func(args []string) (string, error) {
			if len(args) >= 1 {
				switch args[0] {
				case "list":
					return "[]", nil
				case "show":
					return `[{"status":"hooked"}]`, nil
				}
			}
			return "", nil
		},
		func(args []string) error { return nil },
	)

	zombie, found := releaseCappedStuckInDonePolecat(
		bd, t.TempDir(), "testrig", "thunder", "no-such-session",
		tmux.NewTmux(), "working", "gu-work1", 30*time.Minute, &agentBeadSnapshot{HookBead: "gu-work1"}, nil,
	)
	if !found {
		t.Fatal("releaseCappedStuckInDonePolecat: found = false, want true")
	}
	if zombie.Classification != ZombieStuckInDoneCapped {
		t.Errorf("classification = %q, want %q", zombie.Classification, ZombieStuckInDoneCapped)
	}
	if !zombie.BeadRecovered {
		t.Errorf("BeadRecovered = false, want true; action=%q", zombie.Action)
	}

	var foundReset bool
	for _, call := range mock.calls {
		if strings.Contains(call, "update") && strings.Contains(call, "--status=open") {
			foundReset = true
		}
	}
	if !foundReset {
		t.Errorf("expected bead reset (update --status=open), got calls: %v", mock.calls)
	}
}

// TestReleaseCappedStuckInDone_PreservesPendingMR guards the pending-MR safety
// gate: a capped stuck-in-done polecat whose work is validly queued in the
// refinery must NOT have its bead reset (which would re-dispatch already-
// submitted work and orphan the queued branch). It escalates and preserves.
func TestReleaseCappedStuckInDone_PreservesPendingMR(t *testing.T) {
	// A non-empty cleanup-wisp list makes hasPendingMRFromSnapshot return true.
	bd, mock := mockBd(
		func(args []string) (string, error) {
			if len(args) >= 1 && args[0] == "list" {
				return `[{"id":"gu-cleanup-1"}]`, nil
			}
			return "", nil
		},
		func(args []string) error { return nil },
	)

	zombie, found := releaseCappedStuckInDonePolecat(
		bd, t.TempDir(), "testrig", "thunder", "no-such-session",
		tmux.NewTmux(), "working", "gu-work1", 30*time.Minute, &agentBeadSnapshot{HookBead: "gu-work1"}, nil,
	)
	if !found {
		t.Fatal("releaseCappedStuckInDonePolecat: found = false, want true")
	}
	if zombie.BeadRecovered {
		t.Error("BeadRecovered = true with pending MR, want false (work preserved)")
	}
	if !strings.Contains(zombie.Action, "pending-mr") {
		t.Errorf("action = %q, want it to mention pending-mr", zombie.Action)
	}

	for _, call := range mock.calls {
		if strings.Contains(call, "update") && strings.Contains(call, "--status=open") {
			t.Errorf("bead must NOT be reset when MR is pending; calls: %v", mock.calls)
		}
	}
}
