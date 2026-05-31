package witness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/lock"
	"github.com/steveyegge/gastown/internal/workspace"
)

// respawnMu serializes in-process access to the respawn state file.
// Cross-process serialization is handled by lock.FlockAcquire on a
// sibling .flock file (see RecordBeadRespawn, ShouldBlockRespawn, etc.).
var respawnMu sync.Mutex

// beadRespawnRecord tracks how many times a single bead has been reset for re-dispatch.
type beadRespawnRecord struct {
	BeadID      string    `json:"bead_id"`
	Count       int       `json:"count"`
	LastRespawn time.Time `json:"last_respawn"`
}

// beadRespawnState holds respawn counts for all tracked beads.
type beadRespawnState struct {
	Beads       map[string]*beadRespawnRecord `json:"beads"`
	LastUpdated time.Time                     `json:"last_updated"`
}

func beadRespawnStateFile(townRoot string) string {
	return filepath.Join(townRoot, "witness", "bead-respawn-counts.json")
}

func loadBeadRespawnState(townRoot string) *beadRespawnState {
	data, err := os.ReadFile(beadRespawnStateFile(townRoot)) //nolint:gosec // G304: path from trusted townRoot
	if err != nil {
		return &beadRespawnState{Beads: make(map[string]*beadRespawnRecord)}
	}
	var state beadRespawnState
	if err := json.Unmarshal(data, &state); err != nil {
		return &beadRespawnState{Beads: make(map[string]*beadRespawnRecord)}
	}
	if state.Beads == nil {
		state.Beads = make(map[string]*beadRespawnRecord)
	}
	return &state
}

func saveBeadRespawnState(townRoot string, state *beadRespawnState) error {
	stateFile := beadRespawnStateFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(stateFile), 0755); err != nil {
		return fmt.Errorf("creating witness dir: %w", err)
	}
	state.LastUpdated = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling respawn state: %w", err)
	}
	return os.WriteFile(stateFile, data, 0600)
}

// respawnBlockDecayWindow bounds how long a respawn block lasts. The respawn
// limit guards against fast witness→deacon→sling feedback loops (seconds to a
// few minutes between attempts). A genuinely-looping bead keeps re-triggering
// well within this window, so it stays blocked. But a bead that hit the limit
// because its spawns/gates were SIGKILLed under host OOM/swap pressure (hq-0qszq
// load storm) would otherwise stay PERMANENTLY wedged long after load recovered,
// requiring a manual `gt sling respawn-reset`. Once no new respawn has been
// recorded for this window, the block is treated as stale and cleared so the
// bead auto-recovers on the next dispatch (hq-5em9k / hq-q943s class: a load
// storm must not permanently wedge work).
const respawnBlockDecayWindow = 30 * time.Minute

// ShouldBlockRespawn returns true if the bead has already been respawned
// MaxBeadRespawns times (from operational config) AND the block is still fresh.
// When true, the caller should escalate to mayor instead of sending
// RECOVERED_BEAD to deacon for re-dispatch. This is the primary circuit breaker
// for spawn storms (clown show #22). A block older than respawnBlockDecayWindow
// is cleared so a load-storm-induced wedge self-heals once conditions recover.
func ShouldBlockRespawn(workDir, beadID string) bool {
	respawnMu.Lock()
	defer respawnMu.Unlock()

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}
	maxRespawns := config.LoadOperationalConfig(townRoot).GetWitnessConfig().MaxBeadRespawnsV()

	// Cross-process flock to serialize with other witness instances.
	unlock, flockErr := lock.FlockAcquire(beadRespawnStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadBeadRespawnState(townRoot)
	rec, ok := state.Beads[beadID]
	if !ok || rec.Count < maxRespawns {
		return false
	}

	// hq-0qszq/hq-5em9k: a stale block (no new respawn within the decay window)
	// is most likely host-load fallout, not a live loop — clear it so the bead
	// auto-recovers instead of staying wedged until a manual respawn-reset.
	if !rec.LastRespawn.IsZero() && time.Since(rec.LastRespawn) > respawnBlockDecayWindow {
		delete(state.Beads, beadID)
		_ = saveBeadRespawnState(townRoot, state)
		return false
	}
	return true
}

// RecordBeadRespawn increments the respawn count for beadID and returns the new count.
// workDir is the rig path; townRoot is resolved internally via workspace.Find.
// On state file errors the count is still incremented in memory and returned, so the
// caller can log/warn without blocking the respawn itself.
//
// Serialized via respawnMu (in-process) and flock (cross-process) to prevent
// concurrent patrol cycles from racing on the load-modify-save cycle.
func RecordBeadRespawn(workDir, beadID string) int {
	respawnMu.Lock()
	defer respawnMu.Unlock()

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}

	// Cross-process flock to serialize with other witness instances.
	unlock, flockErr := lock.FlockAcquire(beadRespawnStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadBeadRespawnState(townRoot)
	rec, ok := state.Beads[beadID]
	if !ok {
		rec = &beadRespawnRecord{BeadID: beadID}
		state.Beads[beadID] = rec
	}
	rec.Count++
	rec.LastRespawn = time.Now().UTC()
	_ = saveBeadRespawnState(townRoot, state) // Non-fatal: tracking failure must not block respawn
	return rec.Count
}

// ResetBeadRespawnCount resets the respawn counter for beadID to zero.
// Used by `gt sling respawn-reset` to allow re-dispatch after investigation.
func ResetBeadRespawnCount(workDir, beadID string) error {
	respawnMu.Lock()
	defer respawnMu.Unlock()

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}

	// Cross-process flock to serialize with other witness instances.
	unlock, flockErr := lock.FlockAcquire(beadRespawnStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadBeadRespawnState(townRoot)
	delete(state.Beads, beadID)
	return saveBeadRespawnState(townRoot, state)
}
