// state.go: State type and serialization, plus helpers for detecting whether
// a server is configured or running in server mode via filesystem metadata.

package doltserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/gastown/internal/atomicfile"
)

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

// HasServerModeMetadata checks whether any rig has metadata.json configured for
// Dolt server mode. Returns the list of rig names configured for server mode.
// This is used to detect the split-brain risk: if metadata says "server" but
// the server isn't running, bd commands may silently create isolated databases.
func HasServerModeMetadata(townRoot string) []string {
	var serverRigs []string

	// Check town-level beads (hq)
	townBeadsDir := filepath.Join(townRoot, ".beads")
	if hasServerMode(townBeadsDir) {
		serverRigs = append(serverRigs, "hq")
	}

	// Check rig-level beads
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	data, err := os.ReadFile(rigsPath)
	if err != nil {
		return serverRigs
	}
	var config struct {
		Rigs map[string]interface{} `json:"rigs"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return serverRigs
	}

	for rigName := range config.Rigs {
		beadsDir := FindRigBeadsDir(townRoot, rigName)
		if beadsDir != "" && hasServerMode(beadsDir) {
			serverRigs = append(serverRigs, rigName)
		}
	}

	return serverRigs
}

// hasServerMode reads metadata.json and returns true if dolt_mode is "server".
func hasServerMode(beadsDir string) bool {
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return false
	}
	var metadata struct {
		DoltMode string `json:"dolt_mode"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return false
	}
	return metadata.DoltMode == "server"
}
