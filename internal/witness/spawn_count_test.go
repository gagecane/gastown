package witness

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
)

// TestShouldBlockRespawn_StaleBlockDecays verifies hq-0qszq/hq-5em9k: an
// at-limit respawn block self-heals once no new respawn lands within the decay
// window (host-load fallout shouldn't permanently wedge a bead), while a fresh
// at-limit block still blocks.
//
// gu-iqji updates this contract: the live Count counter clears on decay so the
// soft block re-arms, but the cumulative Total counter is preserved so the
// permanent (chronic-failure) detector still observes every prior attempt.
func TestShouldBlockRespawn_StaleBlockDecays(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}
	max := config.LoadOperationalConfig(tmpDir).GetWitnessConfig().MaxBeadRespawnsV()
	if max < 1 {
		t.Skipf("unexpected max respawns %d", max)
	}

	for i := 0; i < max; i++ {
		RecordBeadRespawn(tmpDir, "bead-decay")
	}
	if !ShouldBlockRespawn(tmpDir, "bead-decay") {
		t.Fatalf("a fresh at-limit block must block")
	}

	// Age LastRespawn beyond the decay window — simulates a storm-induced block
	// long after load has recovered.
	st := loadBeadRespawnState(tmpDir)
	st.Beads["bead-decay"].LastRespawn = time.Now().UTC().Add(-respawnBlockDecayWindow - time.Minute)
	if err := saveBeadRespawnState(tmpDir, st); err != nil {
		t.Fatal(err)
	}

	if ShouldBlockRespawn(tmpDir, "bead-decay") {
		t.Errorf("a stale at-limit block (past decay window) must auto-clear")
	}
	rec := loadBeadRespawnState(tmpDir).Beads["bead-decay"]
	if rec == nil {
		t.Fatalf("decay must preserve the record so chronic-failure detection still sees prior attempts")
	}
	if rec.Count != 0 {
		t.Errorf("decayed live Count = %d, want 0 (re-armed)", rec.Count)
	}
	if rec.Total != max {
		t.Errorf("decayed Total = %d, want %d (lifetime counter must NOT decay)", rec.Total, max)
	}
}

func TestRecordBeadRespawn_Increments(t *testing.T) {
	tmpDir := t.TempDir()
	// Create the witness subdirectory so the state file path is valid.
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	count := RecordBeadRespawn(tmpDir, "bead-1")
	if count != 1 {
		t.Errorf("first RecordBeadRespawn = %d, want 1", count)
	}

	count = RecordBeadRespawn(tmpDir, "bead-1")
	if count != 2 {
		t.Errorf("second RecordBeadRespawn = %d, want 2", count)
	}
}

func TestShouldBlockRespawn_Threshold(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	// Below threshold.
	for i := 0; i < config.DefaultWitnessMaxBeadRespawns-1; i++ {
		RecordBeadRespawn(tmpDir, "bead-2")
	}
	if ShouldBlockRespawn(tmpDir, "bead-2") {
		t.Error("ShouldBlockRespawn = true before reaching threshold")
	}

	// At threshold.
	RecordBeadRespawn(tmpDir, "bead-2")
	if !ShouldBlockRespawn(tmpDir, "bead-2") {
		t.Error("ShouldBlockRespawn = false at threshold")
	}
}

func TestResetBeadRespawnCount(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	RecordBeadRespawn(tmpDir, "bead-3")
	RecordBeadRespawn(tmpDir, "bead-3")

	if err := ResetBeadRespawnCount(tmpDir, "bead-3"); err != nil {
		t.Fatalf("ResetBeadRespawnCount error: %v", err)
	}

	if ShouldBlockRespawn(tmpDir, "bead-3") {
		t.Error("ShouldBlockRespawn = true after reset")
	}

	// Re-increment should start from 1.
	count := RecordBeadRespawn(tmpDir, "bead-3")
	if count != 1 {
		t.Errorf("RecordBeadRespawn after reset = %d, want 1", count)
	}
}

func TestRecordBeadRespawn_ConcurrentSafe(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			RecordBeadRespawn(tmpDir, "bead-race")
		}()
	}
	wg.Wait()

	// After all goroutines, the count must equal the number of increments.
	state := loadBeadRespawnState(tmpDir)
	rec, ok := state.Beads["bead-race"]
	if !ok {
		t.Fatal("bead-race record not found")
	}
	if rec.Count != goroutines {
		t.Errorf("concurrent count = %d, want %d", rec.Count, goroutines)
	}
}

func TestShouldBlockRespawn_UnknownBead(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	if ShouldBlockRespawn(tmpDir, "nonexistent") {
		t.Error("ShouldBlockRespawn = true for unknown bead")
	}
}

// TestShouldPermanentlyBlockRespawn_TripsAtMultiplier verifies the chronic-fail
// permanent block (gu-iqji): once a bead's lifetime Total crosses
// PermanentBlockMultiplier × MaxBeadRespawns, ShouldPermanentlyBlockRespawn
// returns true regardless of the live Count or decay state.
func TestShouldPermanentlyBlockRespawn_TripsAtMultiplier(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}
	max := config.LoadOperationalConfig(tmpDir).GetWitnessConfig().MaxBeadRespawnsV()
	limit := PermanentBlockMultiplier * max

	// Below the permanent threshold — even after the soft block trips,
	// permanent block does not fire.
	for i := 0; i < max; i++ {
		RecordBeadRespawn(tmpDir, "chronic")
	}
	if ShouldPermanentlyBlockRespawn(tmpDir, "chronic") {
		t.Fatalf("permanent block tripped at Total=%d (want >= %d)", max, limit)
	}

	// Walk Total all the way to the multiplier threshold by simulating the
	// "decay-then-retry" cycle: clear live Count via decay, attempt again.
	for total := max; total < limit; total++ {
		st := loadBeadRespawnState(tmpDir)
		st.Beads["chronic"].LastRespawn = time.Now().UTC().Add(-respawnBlockDecayWindow - time.Minute)
		if err := saveBeadRespawnState(tmpDir, st); err != nil {
			t.Fatal(err)
		}
		// Stale-decay clears Count but preserves Total
		_ = ShouldBlockRespawn(tmpDir, "chronic")
		RecordBeadRespawn(tmpDir, "chronic")
	}

	if !ShouldPermanentlyBlockRespawn(tmpDir, "chronic") {
		rec := loadBeadRespawnState(tmpDir).Beads["chronic"]
		t.Fatalf("permanent block did not trip: Total=%d, want >= %d", rec.Total, limit)
	}
	// And ShouldBlockRespawn (the umbrella check used by the main path) must
	// also return true — even if the live Count is below the soft limit.
	if !ShouldBlockRespawn(tmpDir, "chronic") {
		t.Errorf("ShouldBlockRespawn must return true once permanent threshold is crossed")
	}

	// A permanent block must NOT auto-clear via decay window.
	st := loadBeadRespawnState(tmpDir)
	st.Beads["chronic"].LastRespawn = time.Now().UTC().Add(-respawnBlockDecayWindow - time.Hour)
	if err := saveBeadRespawnState(tmpDir, st); err != nil {
		t.Fatal(err)
	}
	if !ShouldPermanentlyBlockRespawn(tmpDir, "chronic") {
		t.Error("permanent block must not decay")
	}
	if !ShouldBlockRespawn(tmpDir, "chronic") {
		t.Error("ShouldBlockRespawn must stay true past decay once Total crosses permanent threshold")
	}

	// Reset clears both counters.
	if err := ResetBeadRespawnCount(tmpDir, "chronic"); err != nil {
		t.Fatalf("ResetBeadRespawnCount: %v", err)
	}
	if ShouldPermanentlyBlockRespawn(tmpDir, "chronic") {
		t.Error("permanent block must clear after respawn-reset")
	}
}

// TestRecordBeadRespawn_TracksTotal verifies the cumulative counter is
// monotonically incremented on every respawn — including respawns that arrive
// after a decay window cleared the live Count.
func TestRecordBeadRespawn_TracksTotal(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	RecordBeadRespawn(tmpDir, "tally")
	RecordBeadRespawn(tmpDir, "tally")
	if rec := loadBeadRespawnState(tmpDir).Beads["tally"]; rec.Total != 2 {
		t.Errorf("Total after 2 respawns = %d, want 2", rec.Total)
	}

	// Simulate decay: live Count drops, Total must persist.
	st := loadBeadRespawnState(tmpDir)
	st.Beads["tally"].Count = 0
	if err := saveBeadRespawnState(tmpDir, st); err != nil {
		t.Fatal(err)
	}
	RecordBeadRespawn(tmpDir, "tally")
	rec := loadBeadRespawnState(tmpDir).Beads["tally"]
	if rec.Count != 1 {
		t.Errorf("Count after decay+1 respawn = %d, want 1", rec.Count)
	}
	if rec.Total != 3 {
		t.Errorf("Total after 3 cumulative respawns = %d, want 3", rec.Total)
	}
}
