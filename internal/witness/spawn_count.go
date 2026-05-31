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
//
// Two counters live here on purpose (gu-iqji):
//   - Count is the live counter that participates in the soft block + decay
//     window. It can clear when the bead recovers from a load storm so a
//     transient host-pressure event doesn't permanently wedge the bead.
//   - Total is the cumulative lifetime counter, monotonically incremented on
//     every respawn and never decayed. Once Total crosses
//     PermanentBlockMultiplier × MaxBeadRespawns, the bead is permanently
//     blocked from any further dispatch — including paths that pass --force —
//     until an operator runs `gt sling respawn-reset`. This is the chronic-fail
//     circuit breaker for beads that bounce between attempt 1..N → block →
//     decay → attempt 1..N → block → decay forever (gu-ttc2 / cait-oc7 /
//     cadk-w8k pattern).
type beadRespawnRecord struct {
	BeadID      string    `json:"bead_id"`
	Count       int       `json:"count"`
	Total       int       `json:"total,omitempty"`
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

// PermanentBlockMultiplier is the multiplier applied to MaxBeadRespawns to
// derive the permanent (non-decaying, non-bypassable) circuit-breaker
// threshold. Once a bead's lifetime respawn total crosses this threshold, no
// dispatch path — including --force — can re-arm it without an explicit
// `gt sling respawn-reset`. (gu-iqji: chronic-fail beads like gu-ttc2 /
// cait-oc7 / cadk-w8k were cycling forever because each 30m decay window
// re-armed them for another N attempts.)
const PermanentBlockMultiplier = 2

// ShouldBlockRespawn returns true if the bead has already been respawned
// MaxBeadRespawns times (from operational config) AND the block is still fresh.
// When true, the caller should escalate to mayor instead of sending
// RECOVERED_BEAD to deacon for re-dispatch. This is the primary circuit breaker
// for spawn storms (clown show #22). A block older than respawnBlockDecayWindow
// is cleared so a load-storm-induced wedge self-heals once conditions recover.
//
// A bead whose cumulative lifetime Total has crossed
// PermanentBlockMultiplier × MaxBeadRespawns is treated as permanently broken
// and is blocked unconditionally — see ShouldPermanentlyBlockRespawn — but
// callers can short-circuit on this function alone for the common path.
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
	if !ok {
		return false
	}

	// Permanent block (gu-iqji): cumulative Total never decays. Once a bead
	// has churned through 2x MaxBeadRespawns lifetime attempts, it stays
	// blocked until an operator runs `gt sling respawn-reset`. This catches
	// chronic-fail beads that cycle through Count → block → 30m decay →
	// Count → block → ... and never converge.
	if rec.Total >= PermanentBlockMultiplier*maxRespawns {
		return true
	}

	if rec.Count < maxRespawns {
		return false
	}

	// hq-0qszq/hq-5em9k: a stale block (no new respawn within the decay window)
	// is most likely host-load fallout, not a live loop — clear the live Count
	// so the bead auto-recovers instead of staying wedged until a manual
	// respawn-reset. Total is preserved so chronic-failure detection still
	// trips at the permanent threshold.
	if !rec.LastRespawn.IsZero() && time.Since(rec.LastRespawn) > respawnBlockDecayWindow {
		rec.Count = 0
		_ = saveBeadRespawnState(townRoot, state)
		return false
	}
	return true
}

// ShouldPermanentlyBlockRespawn reports whether the bead has crossed the
// chronic-failure threshold (PermanentBlockMultiplier × MaxBeadRespawns
// cumulative attempts). Permanent blocks must NOT be bypassed by --force —
// the bead has demonstrably failed to converge across multiple decay windows,
// and re-dispatching it is known to waste polecat slots and reset the
// counter spiral. The only way out is `gt sling respawn-reset` after an
// operator investigates.
func ShouldPermanentlyBlockRespawn(workDir, beadID string) bool {
	respawnMu.Lock()
	defer respawnMu.Unlock()

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}
	maxRespawns := config.LoadOperationalConfig(townRoot).GetWitnessConfig().MaxBeadRespawnsV()

	unlock, flockErr := lock.FlockAcquire(beadRespawnStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadBeadRespawnState(townRoot)
	rec, ok := state.Beads[beadID]
	if !ok {
		return false
	}
	return rec.Total >= PermanentBlockMultiplier*maxRespawns
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
	rec.Total++ // gu-iqji: lifetime counter, never decays.
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
