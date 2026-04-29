package doltserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/atomicfile"
)

// metadataMu provides per-path mutexes for EnsureMetadata goroutine synchronization.
// flock is inter-process only and cannot reliably synchronize goroutines within the
// same process (the same process may acquire the same flock twice without blocking).
var metadataMu sync.Map // map[string]*sync.Mutex

// getMetadataMu returns a mutex for the given metadata file path, creating one if needed.
func getMetadataMu(path string) *sync.Mutex {
	mu, _ := metadataMu.LoadOrStore(path, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// State represents the Dolt server's runtime state.
type State struct {
	// Running indicates if the server is running.
	Running bool `json:"running"`

	// PID is the process ID of the server.
	PID int `json:"pid"`

	// Port is the port the server is listening on.
	Port int `json:"port"`

	// StartedAt is when the server started.
	StartedAt time.Time `json:"started_at"`

	// DataDir is the data directory containing all rig databases.
	DataDir string `json:"data_dir"`

	// Databases is the list of available databases (rig names).
	Databases []string `json:"databases,omitempty"`
}

// StateFile returns the path to the state file.
func StateFile(townRoot string) string {
	return filepath.Join(townRoot, "daemon", "dolt-state.json")
}

// LoadState loads Dolt server state from disk.
func LoadState(townRoot string) (*State, error) {
	stateFile := StateFile(townRoot)
	data, err := os.ReadFile(stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			return &State{}, nil
		}
		return nil, err
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// SaveState saves Dolt server state to disk using atomic write.
func SaveState(townRoot string, state *State) error {
	stateFile := StateFile(townRoot)

	// Ensure daemon directory exists
	if err := os.MkdirAll(filepath.Dir(stateFile), 0755); err != nil {
		return err
	}

	return atomicfile.WriteJSON(stateFile, state)
}

// countDoltDatabases counts the number of Dolt database directories in dataDir.
// Each subdirectory containing a .dolt directory is considered a database.
// Returns at least 1 so the caller never divides by zero.
func countDoltDatabases(dataDir string) int {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return 1
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// A Dolt database directory contains a .dolt subdirectory.
		if _, statErr := os.Stat(filepath.Join(dataDir, e.Name(), ".dolt")); statErr == nil {
			count++
		}
	}
	if count < 1 {
		return 1
	}
	return count
}
