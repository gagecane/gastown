package witness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/lock"
	"github.com/steveyegge/gastown/internal/workspace"
)

// MaxStuckInDoneAutoRestarts caps how many times a single bead may be
// auto-restarted in response to the stuck-in-done failure (polecat hung in
// gt done past DoneIntentStuckTimeout) before the witness stops looping and
// escalates to the mayor instead. Restarting re-enters the SAME stalled
// gt-done flow (push/MR/dolt I/O blocked under host load), so the polecat
// re-sticks and the witness re-flags it every patrol cycle — the growing
// done-intent age churn in gu-5npkm. A bead that keeps re-sticking on every
// fresh spawn is not self-healing, so bound the auto-recovery. (gu-5npkm)
const MaxStuckInDoneAutoRestarts = 2

// stuckInDoneMu serializes in-process access to the stuck-in-done restart
// state file. Cross-process serialization is handled by lock.FlockAcquire on a
// sibling .flock file, mirroring the blank-tools counter (blanktools.go).
var stuckInDoneMu sync.Mutex

// stuckInDoneRestartRecord tracks how many times a single bead has been
// auto-restarted in response to the stuck-in-done failure.
type stuckInDoneRestartRecord struct {
	BeadID      string    `json:"bead_id"`
	Count       int       `json:"count"`
	LastRestart time.Time `json:"last_restart"`
}

// stuckInDoneRestartState holds stuck-in-done restart counts for all tracked beads.
type stuckInDoneRestartState struct {
	Beads       map[string]*stuckInDoneRestartRecord `json:"beads"`
	LastUpdated time.Time                            `json:"last_updated"`
}

func stuckInDoneRestartStateFile(townRoot string) string {
	return filepath.Join(townRoot, "witness", "bead-stuckindone-restart-counts.json")
}

func loadStuckInDoneRestartState(townRoot string) *stuckInDoneRestartState {
	data, err := os.ReadFile(stuckInDoneRestartStateFile(townRoot)) //nolint:gosec // G304: path from trusted townRoot
	if err != nil {
		return &stuckInDoneRestartState{Beads: make(map[string]*stuckInDoneRestartRecord)}
	}
	var state stuckInDoneRestartState
	if err := json.Unmarshal(data, &state); err != nil {
		return &stuckInDoneRestartState{Beads: make(map[string]*stuckInDoneRestartRecord)}
	}
	if state.Beads == nil {
		state.Beads = make(map[string]*stuckInDoneRestartRecord)
	}
	return &state
}

func saveStuckInDoneRestartState(townRoot string, state *stuckInDoneRestartState) error {
	stateFile := stuckInDoneRestartStateFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(stateFile), 0755); err != nil {
		return fmt.Errorf("creating witness dir: %w", err)
	}
	state.LastUpdated = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling stuck-in-done restart state: %w", err)
	}
	return os.WriteFile(stateFile, data, 0600)
}

// ShouldEscalateStuckInDone returns true if the bead has already been
// auto-restarted MaxStuckInDoneAutoRestarts times. When true, the witness
// should escalate to the mayor instead of auto-restarting again, so a bead
// that keeps re-sticking in gt done doesn't loop forever. (gu-5npkm)
func ShouldEscalateStuckInDone(workDir, beadID string) bool {
	stuckInDoneMu.Lock()
	defer stuckInDoneMu.Unlock()

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}

	unlock, flockErr := lock.FlockAcquire(stuckInDoneRestartStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadStuckInDoneRestartState(townRoot)
	rec, ok := state.Beads[beadID]
	if !ok {
		return false
	}
	return rec.Count >= MaxStuckInDoneAutoRestarts
}

// RecordStuckInDoneRestart increments the stuck-in-done auto-restart count for
// beadID and returns the new count. On state file errors the count is still
// incremented in memory and returned, so the caller can log/warn without
// blocking the restart itself.
//
// Serialized via stuckInDoneMu (in-process) and flock (cross-process) to
// prevent concurrent patrol cycles from racing on the load-modify-save cycle.
// (gu-5npkm)
func RecordStuckInDoneRestart(workDir, beadID string) int {
	stuckInDoneMu.Lock()
	defer stuckInDoneMu.Unlock()

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}

	unlock, flockErr := lock.FlockAcquire(stuckInDoneRestartStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadStuckInDoneRestartState(townRoot)
	rec, ok := state.Beads[beadID]
	if !ok {
		rec = &stuckInDoneRestartRecord{BeadID: beadID}
		state.Beads[beadID] = rec
	}
	rec.Count++
	rec.LastRestart = time.Now().UTC()
	_ = saveStuckInDoneRestartState(townRoot, state) // Non-fatal: tracking failure must not block restart
	return rec.Count
}
