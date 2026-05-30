package witness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/lock"
	"github.com/steveyegge/gastown/internal/workspace"
)

// MaxBlankToolsAutoRestarts caps how many times a single bead may be
// auto-restarted in response to the Claude Code tool-output-capture failure
// ("blank-tools" / "session blind") before the witness stops looping and
// escalates to the mayor instead. A bead that keeps tripping the failure on
// every fresh spawn is not self-healing, so bound the auto-recovery. (gs-4lk)
const MaxBlankToolsAutoRestarts = 2

// blankToolsSignatures are the canonical phrases a blind polecat uses when it
// self-reports the tool-output-capture failure (gs-4lk). The session enters a
// state where ALL tool output returns empty, so the agent cannot read its
// bead, inspect files, or verify work, and self-escalates "tool-result channel
// broken / all tool output empty / session blind, recommend restart". Matched
// case-insensitively as substrings against the agent's self-reported heartbeat
// context.
var blankToolsSignatures = []string{
	"blank-tools",
	"blank tools",
	"tool-result channel broken",
	"tool result channel broken",
	"tool output empty",
	"tool output is empty",
	"tool output returns empty",
	"tool output returning empty",
	"all tool output empty",
	"empty tool output",
	"tool results empty",
	"tool results are empty",
	"empty tool results",
	"tools return empty",
	"tools returning empty",
	"session blind",
	"agent blind",
	"going blind",
}

// IsBlankToolsSignature reports whether text (typically a polecat's
// self-reported "stuck" heartbeat context) matches the blank-tools failure
// signature. The match is a case-insensitive substring test against the known
// phrases in blankToolsSignatures. (gs-4lk)
func IsBlankToolsSignature(text string) bool {
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	for _, sig := range blankToolsSignatures {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

// blankToolsMu serializes in-process access to the blank-tools restart state
// file. Cross-process serialization is handled by lock.FlockAcquire on a
// sibling .flock file, mirroring the bead-respawn counter (spawn_count.go).
var blankToolsMu sync.Mutex

// blankToolsRestartRecord tracks how many times a single bead has been
// auto-restarted in response to the blank-tools failure.
type blankToolsRestartRecord struct {
	BeadID      string    `json:"bead_id"`
	Count       int       `json:"count"`
	LastRestart time.Time `json:"last_restart"`
}

// blankToolsRestartState holds blank-tools restart counts for all tracked beads.
type blankToolsRestartState struct {
	Beads       map[string]*blankToolsRestartRecord `json:"beads"`
	LastUpdated time.Time                           `json:"last_updated"`
}

func blankToolsRestartStateFile(townRoot string) string {
	return filepath.Join(townRoot, "witness", "bead-blanktools-restart-counts.json")
}

func loadBlankToolsRestartState(townRoot string) *blankToolsRestartState {
	data, err := os.ReadFile(blankToolsRestartStateFile(townRoot)) //nolint:gosec // G304: path from trusted townRoot
	if err != nil {
		return &blankToolsRestartState{Beads: make(map[string]*blankToolsRestartRecord)}
	}
	var state blankToolsRestartState
	if err := json.Unmarshal(data, &state); err != nil {
		return &blankToolsRestartState{Beads: make(map[string]*blankToolsRestartRecord)}
	}
	if state.Beads == nil {
		state.Beads = make(map[string]*blankToolsRestartRecord)
	}
	return &state
}

func saveBlankToolsRestartState(townRoot string, state *blankToolsRestartState) error {
	stateFile := blankToolsRestartStateFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(stateFile), 0755); err != nil {
		return fmt.Errorf("creating witness dir: %w", err)
	}
	state.LastUpdated = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling blank-tools restart state: %w", err)
	}
	return os.WriteFile(stateFile, data, 0600)
}

// ShouldEscalateBlankTools returns true if the bead has already been
// auto-restarted MaxBlankToolsAutoRestarts times. When true, the witness should
// escalate to the mayor instead of auto-restarting again, so a bead that keeps
// tripping the blank-tools failure on every spawn doesn't loop forever. (gs-4lk)
func ShouldEscalateBlankTools(workDir, beadID string) bool {
	blankToolsMu.Lock()
	defer blankToolsMu.Unlock()

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}

	unlock, flockErr := lock.FlockAcquire(blankToolsRestartStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadBlankToolsRestartState(townRoot)
	rec, ok := state.Beads[beadID]
	if !ok {
		return false
	}
	return rec.Count >= MaxBlankToolsAutoRestarts
}

// RecordBlankToolsRestart increments the blank-tools auto-restart count for
// beadID and returns the new count. On state file errors the count is still
// incremented in memory and returned, so the caller can log/warn without
// blocking the restart itself.
//
// Serialized via blankToolsMu (in-process) and flock (cross-process) to prevent
// concurrent patrol cycles from racing on the load-modify-save cycle. (gs-4lk)
func RecordBlankToolsRestart(workDir, beadID string) int {
	blankToolsMu.Lock()
	defer blankToolsMu.Unlock()

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}

	unlock, flockErr := lock.FlockAcquire(blankToolsRestartStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadBlankToolsRestartState(townRoot)
	rec, ok := state.Beads[beadID]
	if !ok {
		rec = &blankToolsRestartRecord{BeadID: beadID}
		state.Beads[beadID] = rec
	}
	rec.Count++
	rec.LastRestart = time.Now().UTC()
	_ = saveBlankToolsRestartState(townRoot, state) // Non-fatal: tracking failure must not block restart
	return rec.Count
}
