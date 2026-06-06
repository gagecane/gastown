package capacity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// SchedulerState represents the runtime operational state of the capacity scheduler.
// Stored at <townRoot>/.runtime/scheduler-state.json.
// Follows the pattern of deacon/redispatch-state.json for daemon operational state.
type SchedulerState struct {
	Paused            bool   `json:"paused"`
	PausedBy          string `json:"paused_by,omitempty"`
	PausedAt          string `json:"paused_at,omitempty"`
	LastDispatchAt    string `json:"last_dispatch_at,omitempty"`
	LastDispatchCount int    `json:"last_dispatch_count,omitempty"`
}

// stateFile returns the path to the scheduler state file.
func stateFile(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "scheduler-state.json")
}

// legacyStateFile returns the path to the old queue state file for migration.
func legacyStateFile(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "queue-state.json")
}

// LoadState loads the scheduler runtime state, returning a zero-value state if the file
// doesn't exist. This is intentional: absence means "not paused, never dispatched."
// Falls back to reading the legacy queue-state.json if the new file doesn't exist.
func LoadState(townRoot string) (*SchedulerState, error) {
	path := stateFile(townRoot)
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed internally
	if err != nil {
		if os.IsNotExist(err) {
			// Try legacy path
			legacyPath := legacyStateFile(townRoot)
			data, err = os.ReadFile(legacyPath) //nolint:gosec // G304: path is constructed internally
			if err != nil {
				if os.IsNotExist(err) {
					return &SchedulerState{}, nil
				}
				return nil, err
			}
			// Fall through to parse legacy data
		} else {
			return nil, err
		}
	}

	var state SchedulerState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// SaveState writes the scheduler runtime state to disk atomically.
// Uses write-to-temp + rename to prevent corruption from concurrent writers
// (e.g., dispatch RecordDispatch racing with gt scheduler pause).
func SaveState(townRoot string, state *SchedulerState) error {
	path := stateFile(townRoot)
	dir := filepath.Dir(path)

	// Ensure .runtime directory exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	// Atomic write: temp file + rename
	tmp, err := os.CreateTemp(dir, ".scheduler-state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath) // clean up on rename failure
		return err
	}
	return nil
}

// SetPaused marks the scheduler as paused by the given actor.
func (s *SchedulerState) SetPaused(by string) {
	s.Paused = true
	s.PausedBy = by
	s.PausedAt = time.Now().UTC().Format(time.RFC3339)
}

// SetResumed marks the scheduler as resumed (not paused).
func (s *SchedulerState) SetResumed() {
	s.Paused = false
	s.PausedBy = ""
	s.PausedAt = ""
}

// RecordDispatch records a dispatch event.
func (s *SchedulerState) RecordDispatch(count int) {
	s.LastDispatchAt = time.Now().UTC().Format(time.RFC3339)
	s.LastDispatchCount = count
}

// RecordDispatchEvent stamps last_dispatch_at/count on the persisted scheduler
// state for a successful dispatch. It reloads the state fresh before stamping so
// it never clobbers a concurrent `gt scheduler pause` (mirrors the
// read-modify-write pattern dispatch has always used).
//
// This is the single place every dispatch path records the timestamp. Calling it
// from the universal dispatch chokepoint (executeSling) keeps last_dispatch_at
// honest across ALL dispatch paths — fresh spawn, idle-polecat reuse, convoy /
// epic / batch dispatch, and manual sling — not just the scheduler's batch loop
// (gu-rzv7v: the field was frozen for hours because only one path stamped it).
func RecordDispatchEvent(townRoot string, count int) error {
	state, err := LoadState(townRoot)
	if err != nil {
		return err
	}
	state.RecordDispatch(count)
	return SaveState(townRoot, state)
}
