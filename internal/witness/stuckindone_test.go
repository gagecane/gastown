package witness

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
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
