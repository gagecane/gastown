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

// Polecat startup backoff (gu-mkz7 / mitigation 3 of gu-rq8i)
// ============================================================
//
// When a polecat's tmux session dies during startup (e.g. a kiro-cli panic
// at chat/mod.rs:1719 — the exact symptom gu-rq8i was filed for), the
// witness patrol previously called RestartPolecatSession on every heartbeat
// with no backoff. For a persistent per-rig failure — most commonly a broken
// agent runtime right after a daemon restart — this turns into a rapid-fire
// retry loop that floods the mayor with POLECAT_DIED mail and prevents
// automatic recovery once the underlying problem is resolved.
//
// This file implements the same backoff contract as daemon/dog_startup_backoff.go
// (gu-ro75), but for polecats. We cannot import the daemon's RestartTracker
// directly because daemon → witness is the existing dependency direction
// (the daemon imports the witness, not the other way around). Rather than
// refactor RestartTracker into a shared package as part of this bead, we
// reuse the proven file-backed + flock pattern already used by
// spawn_count.go and expose three helpers with the same contract as the
// dog equivalents:
//
//   isPolecatInStartupBackoff(workDir, rig, name) → (skip, reason)
//   recordPolecatStartFailure(workDir, rig, name)
//   recordPolecatStartSuccess(workDir, rig, name)
//
// Backoff schedule (identical to the dog equivalent so operators see the
// same timings across both agent classes):
//
//   attempt 1 → 30s    attempt 2 → 1m     attempt 3 → 2m
//   attempt 4 → 4m     attempt 5 → 8m     attempt 6+ → 10m (capped)
//
// Crash loop: 5 failed starts within 15 minutes mutes the polecat until
// `gt witness clear-polecat-backoff <rig>/<name>` is invoked. A muted
// polecat's RestartPolecatSession call is skipped — the witness patrol
// will continue to observe the dead session on later cycles but will not
// redispatch it.
//
// Stability reset: after 30 minutes without a recorded restart, the next
// recordSuccess clears the counter so a formerly-flaky polecat eventually
// returns to first-class status.
//
// State layout (single file, shared across the whole town so cross-rig
// patrol cycles observe the same counters):
//   <townRoot>/witness/polecat-startup-backoff.json
//   <townRoot>/witness/polecat-startup-backoff.json.flock (advisory)

// --- Tunables (package private so tests can override) -----------------------

// Keep these in sync with daemon.DefaultRestartTrackerConfig so polecats and
// dogs have the same failure-visibility semantics for operators.
var (
	polecatBackoffInitial    = 30 * time.Second
	polecatBackoffMax        = 10 * time.Minute
	polecatBackoffMultiplier = 2.0
	polecatCrashLoopWindow   = 15 * time.Minute
	polecatCrashLoopCount    = 5
	polecatStabilityPeriod   = 30 * time.Minute
)

// --- Persistence -----------------------------------------------------------

// polecatBackoffMu serializes in-process file access. Cross-process safety
// is handled by lock.FlockAcquire on the sibling .flock file — matching
// spawn_count.go.
var polecatBackoffMu sync.Mutex

// polecatBackoffRecord tracks startup-failure state for one polecat, keyed
// by polecatBackoffID(rig, name).
type polecatBackoffRecord struct {
	LastRestart    time.Time `json:"last_restart,omitempty"`
	RestartCount   int       `json:"restart_count,omitempty"`
	BackoffUntil   time.Time `json:"backoff_until,omitempty"`
	CrashLoopSince time.Time `json:"crash_loop_since,omitempty"`
}

// polecatBackoffState is the on-disk shape.
type polecatBackoffState struct {
	Polecats    map[string]*polecatBackoffRecord `json:"polecats"`
	LastUpdated time.Time                        `json:"last_updated"`
}

// polecatBackoffID is the map key / human-readable agent identifier used in
// log lines and the `clear-polecat-backoff` operator command. We include
// both rig and name so cross-rig collisions are impossible (two rigs can
// legitimately have polecats with the same name).
func polecatBackoffID(rigName, polecatName string) string {
	return "polecat:" + rigName + "/" + polecatName
}

func polecatBackoffStateFile(townRoot string) string {
	return filepath.Join(townRoot, "witness", "polecat-startup-backoff.json")
}

func loadPolecatBackoffState(townRoot string) *polecatBackoffState {
	data, err := os.ReadFile(polecatBackoffStateFile(townRoot)) //nolint:gosec // G304: path from trusted townRoot
	if err != nil {
		return &polecatBackoffState{Polecats: make(map[string]*polecatBackoffRecord)}
	}
	var state polecatBackoffState
	if err := json.Unmarshal(data, &state); err != nil {
		return &polecatBackoffState{Polecats: make(map[string]*polecatBackoffRecord)}
	}
	if state.Polecats == nil {
		state.Polecats = make(map[string]*polecatBackoffRecord)
	}
	return &state
}

func savePolecatBackoffState(townRoot string, state *polecatBackoffState) error {
	stateFile := polecatBackoffStateFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
		return fmt.Errorf("creating witness dir: %w", err)
	}
	state.LastUpdated = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling polecat backoff state: %w", err)
	}
	return os.WriteFile(stateFile, data, 0o600)
}

// resolveTownRootOrSelf resolves workDir to a town root; on failure it
// returns workDir itself so the backoff helpers remain usable in tests
// that pass an ad-hoc tmpdir. Mirrors the pattern in spawn_count.go.
func resolveTownRootOrSelf(workDir string) string {
	if root, err := workspace.Find(workDir); err == nil && root != "" {
		return root
	}
	return workDir
}

// --- Public API -----------------------------------------------------------

// IsPolecatInStartupBackoff reports whether a polecat is currently in startup
// backoff or crash-loop and the witness patrol should skip its restart this
// cycle. Returns (skip, reason); when skip is true, reason is a human-readable
// sentence suitable for a log line / ZombieResult.Action field.
//
// A polecat with no prior failure is never in backoff — the first restart
// attempt always goes through, matching the dog contract.
func IsPolecatInStartupBackoff(workDir, rigName, polecatName string) (bool, string) {
	polecatBackoffMu.Lock()
	defer polecatBackoffMu.Unlock()

	townRoot := resolveTownRootOrSelf(workDir)

	unlock, flockErr := lock.FlockAcquire(polecatBackoffStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadPolecatBackoffState(townRoot)
	id := polecatBackoffID(rigName, polecatName)
	info, ok := state.Polecats[id]
	if !ok {
		return false, ""
	}

	if !info.CrashLoopSince.IsZero() {
		return true, fmt.Sprintf(
			"polecat %s/%s in startup crash loop (use: gt witness clear-polecat-backoff %s/%s)",
			rigName, polecatName, rigName, polecatName,
		)
	}

	remaining := time.Until(info.BackoffUntil)
	if remaining > 0 {
		return true, fmt.Sprintf(
			"polecat %s/%s in startup backoff, %s remaining",
			rigName, polecatName, remaining.Round(time.Second),
		)
	}

	return false, ""
}

// RecordPolecatStartFailure records a failed startup attempt for a polecat
// and bumps the exponential backoff. Called from the witness zombie-detector
// callsites immediately after RestartPolecatSession returns an error.
//
// Also called when the zombie detector decides the preceding restart did
// not in fact heal the polecat — in other words, whenever the witness
// concludes this polecat is still broken after its restart attempt.
//
// Returns the new restart count and remaining backoff, useful for test
// assertions and diagnostic log lines.
func RecordPolecatStartFailure(workDir, rigName, polecatName string) (int, time.Duration) {
	polecatBackoffMu.Lock()
	defer polecatBackoffMu.Unlock()

	townRoot := resolveTownRootOrSelf(workDir)

	unlock, flockErr := lock.FlockAcquire(polecatBackoffStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadPolecatBackoffState(townRoot)
	id := polecatBackoffID(rigName, polecatName)
	now := time.Now().UTC()

	info, exists := state.Polecats[id]
	if !exists {
		info = &polecatBackoffRecord{}
		state.Polecats[id] = info
	}

	// Stability reset: if the previous failure was long enough ago that the
	// polecat is presumed to have been healthy in between, start fresh.
	if !info.LastRestart.IsZero() && now.Sub(info.LastRestart) > polecatStabilityPeriod {
		info.RestartCount = 0
		info.CrashLoopSince = time.Time{}
	}

	info.LastRestart = now
	info.RestartCount++

	// Exponential backoff, capped.
	backoff := polecatBackoffInitial
	for i := 1; i < info.RestartCount && backoff < polecatBackoffMax; i++ {
		backoff = time.Duration(float64(backoff) * polecatBackoffMultiplier)
	}
	if backoff > polecatBackoffMax {
		backoff = polecatBackoffMax
	}
	info.BackoffUntil = now.Add(backoff)

	// Crash-loop detection: N failures within the crash-loop window.
	if info.RestartCount >= polecatCrashLoopCount {
		windowStart := now.Add(-polecatCrashLoopWindow)
		if info.LastRestart.After(windowStart) {
			info.CrashLoopSince = now
		}
	}

	_ = savePolecatBackoffState(townRoot, state) // Non-fatal; tracking must not block the patrol.
	return info.RestartCount, backoff
}

// RecordPolecatStartSuccess records that a polecat's session has been
// observed healthy for at least the stability period. When called on a
// polecat that has been stable, this clears its counter so repeated
// benign restarts over a long timeline don't eventually trip the crash
// loop cap.
//
// Callers that cannot determine stability (most witness callsites) should
// NOT invoke this on every successful RestartPolecatSession, because a
// polecat that dies shortly after a successful start is still unhealthy
// — we want its previous backoff state intact. Instead, the witness
// patrol should call this when it observes a fresh heartbeat after a
// recent restart (see detectZombieActiveSession healthy path).
func RecordPolecatStartSuccess(workDir, rigName, polecatName string) {
	polecatBackoffMu.Lock()
	defer polecatBackoffMu.Unlock()

	townRoot := resolveTownRootOrSelf(workDir)

	unlock, flockErr := lock.FlockAcquire(polecatBackoffStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadPolecatBackoffState(townRoot)
	id := polecatBackoffID(rigName, polecatName)
	info, exists := state.Polecats[id]
	if !exists {
		return
	}

	// Only clear if the polecat has been stable for the stability period.
	// This preserves counters for a polecat that dies again shortly after
	// a single successful start, matching the daemon.RestartTracker contract.
	if time.Since(info.LastRestart) < polecatStabilityPeriod {
		return
	}

	info.RestartCount = 0
	info.CrashLoopSince = time.Time{}
	info.BackoffUntil = time.Time{}
	_ = savePolecatBackoffState(townRoot, state)
}

// ClearPolecatBackoff manually resets the crash-loop and backoff state for a
// single polecat. Exposed for the operator-facing `gt witness
// clear-polecat-backoff` command; wiring of that CLI is out of scope for
// this bead — the function is available for the next bead that adds it.
func ClearPolecatBackoff(workDir, rigName, polecatName string) error {
	polecatBackoffMu.Lock()
	defer polecatBackoffMu.Unlock()

	townRoot := resolveTownRootOrSelf(workDir)

	unlock, flockErr := lock.FlockAcquire(polecatBackoffStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadPolecatBackoffState(townRoot)
	id := polecatBackoffID(rigName, polecatName)
	delete(state.Polecats, id)
	return savePolecatBackoffState(townRoot, state)
}

// GetPolecatBackoffRemaining returns how long until the named polecat may
// be restarted again. Zero when not in backoff. Used by tests; also useful
// for diagnostic CLI surfaces.
func GetPolecatBackoffRemaining(workDir, rigName, polecatName string) time.Duration {
	polecatBackoffMu.Lock()
	defer polecatBackoffMu.Unlock()

	townRoot := resolveTownRootOrSelf(workDir)

	unlock, flockErr := lock.FlockAcquire(polecatBackoffStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadPolecatBackoffState(townRoot)
	id := polecatBackoffID(rigName, polecatName)
	info, ok := state.Polecats[id]
	if !ok {
		return 0
	}
	remaining := time.Until(info.BackoffUntil)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// IsPolecatInCrashLoop returns true when the polecat has been flagged as
// crash-looping (muted until cleared). Exposed for tests and for a future
// operator diagnostic.
func IsPolecatInCrashLoop(workDir, rigName, polecatName string) bool {
	polecatBackoffMu.Lock()
	defer polecatBackoffMu.Unlock()

	townRoot := resolveTownRootOrSelf(workDir)

	unlock, flockErr := lock.FlockAcquire(polecatBackoffStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadPolecatBackoffState(townRoot)
	id := polecatBackoffID(rigName, polecatName)
	info, ok := state.Polecats[id]
	if !ok {
		return false
	}
	return !info.CrashLoopSince.IsZero()
}
