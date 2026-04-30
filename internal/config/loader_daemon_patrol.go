package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/constants"
)

// DaemonPatrolConfigPath returns the path to the daemon patrol config file.
func DaemonPatrolConfigPath(townRoot string) string {
	return filepath.Join(townRoot, constants.DirMayor, DaemonPatrolConfigFileName)
}

// LoadDaemonPatrolConfig loads and validates a daemon patrol config file.
func LoadDaemonPatrolConfig(path string) (*DaemonPatrolConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed internally
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("reading daemon patrol config: %w", err)
	}

	var config DaemonPatrolConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing daemon patrol config: %w", err)
	}

	if err := validateDaemonPatrolConfig(&config); err != nil {
		return nil, err
	}

	return &config, nil
}

// SaveDaemonPatrolConfig saves a daemon patrol config to a file.
func SaveDaemonPatrolConfig(path string, config *DaemonPatrolConfig) error {
	if err := validateDaemonPatrolConfig(config); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding daemon patrol config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil { //nolint:gosec // G306: config files don't contain secrets
		return fmt.Errorf("writing daemon patrol config: %w", err)
	}

	return nil
}

func validateDaemonPatrolConfig(c *DaemonPatrolConfig) error {
	if c.Type != "daemon-patrol-config" && c.Type != "" {
		return fmt.Errorf("%w: expected type 'daemon-patrol-config', got '%s'", ErrInvalidType, c.Type)
	}
	if c.Version > CurrentDaemonPatrolConfigVersion {
		return fmt.Errorf("%w: got %d, max supported %d", ErrInvalidVersion, c.Version, CurrentDaemonPatrolConfigVersion)
	}
	return nil
}

// EnsureDaemonPatrolConfig creates the daemon patrol config if it doesn't exist.
func EnsureDaemonPatrolConfig(townRoot string) error {
	path := DaemonPatrolConfigPath(townRoot)
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("checking daemon patrol config: %w", err)
		}
		return SaveDaemonPatrolConfig(path, NewDaemonPatrolConfig())
	}
	return nil
}

// AddRigToDaemonPatrols adds a rig to the witness and refinery patrol rigs arrays
// in daemon.json. Uses raw JSON manipulation to preserve fields not in PatrolConfig
// (e.g., dolt_server config). If daemon.json doesn't exist, this is a no-op.
func AddRigToDaemonPatrols(townRoot string, rigName string) error {
	path := DaemonPatrolConfigPath(townRoot)
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed internally
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No daemon.json yet, nothing to update
		}
		return fmt.Errorf("reading daemon config: %w", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing daemon config: %w", err)
	}

	patrolsRaw, ok := raw["patrols"]
	if !ok {
		return nil // No patrols section
	}

	var patrols map[string]json.RawMessage
	if err := json.Unmarshal(patrolsRaw, &patrols); err != nil {
		return fmt.Errorf("parsing patrols: %w", err)
	}

	modified := false
	for _, patrolName := range []string{"witness", "refinery"} {
		pRaw, ok := patrols[patrolName]
		if !ok {
			continue
		}

		var patrol map[string]json.RawMessage
		if err := json.Unmarshal(pRaw, &patrol); err != nil {
			continue
		}

		// Parse existing rigs array
		var rigs []string
		if rigsRaw, ok := patrol["rigs"]; ok {
			if err := json.Unmarshal(rigsRaw, &rigs); err != nil {
				rigs = nil
			}
		}

		// Check if already present
		found := false
		for _, r := range rigs {
			if r == rigName {
				found = true
				break
			}
		}
		if found {
			continue
		}

		// Append and update
		rigs = append(rigs, rigName)
		rigsJSON, err := json.Marshal(rigs)
		if err != nil {
			return fmt.Errorf("encoding rigs: %w", err)
		}
		patrol["rigs"] = rigsJSON

		patrolJSON, err := json.Marshal(patrol)
		if err != nil {
			return fmt.Errorf("encoding patrol %s: %w", patrolName, err)
		}
		patrols[patrolName] = patrolJSON
		modified = true
	}

	if !modified {
		return nil
	}

	patrolsJSON, err := json.Marshal(patrols)
	if err != nil {
		return fmt.Errorf("encoding patrols: %w", err)
	}
	raw["patrols"] = patrolsJSON

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding daemon config: %w", err)
	}

	if err := os.WriteFile(path, append(out, '\n'), 0644); err != nil { //nolint:gosec // G306: config file
		return fmt.Errorf("writing daemon config: %w", err)
	}

	return nil
}

// RemoveRigFromDaemonPatrols removes a rig from the witness and refinery patrol rigs arrays
// in daemon.json. Uses raw JSON manipulation to preserve fields not in PatrolConfig
// (e.g., dolt_server config). If daemon.json doesn't exist, this is a no-op.
func RemoveRigFromDaemonPatrols(townRoot string, rigName string) error {
	path := DaemonPatrolConfigPath(townRoot)
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed internally
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No daemon.json yet, nothing to update
		}
		return fmt.Errorf("reading daemon config: %w", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing daemon config: %w", err)
	}

	patrolsRaw, ok := raw["patrols"]
	if !ok {
		return nil // No patrols section
	}

	var patrols map[string]json.RawMessage
	if err := json.Unmarshal(patrolsRaw, &patrols); err != nil {
		return fmt.Errorf("parsing patrols: %w", err)
	}

	modified := false
	for _, patrolName := range []string{"witness", "refinery"} {
		pRaw, ok := patrols[patrolName]
		if !ok {
			continue
		}

		var patrol map[string]json.RawMessage
		if err := json.Unmarshal(pRaw, &patrol); err != nil {
			continue
		}

		// Parse existing rigs array
		var rigs []string
		if rigsRaw, ok := patrol["rigs"]; ok {
			if err := json.Unmarshal(rigsRaw, &rigs); err != nil {
				rigs = nil
			}
		}

		// Filter out the rig
		var filtered []string
		for _, r := range rigs {
			if r != rigName {
				filtered = append(filtered, r)
			}
		}

		if len(filtered) == len(rigs) {
			continue // Rig wasn't present
		}

		// Update with filtered list
		rigsJSON, err := json.Marshal(filtered)
		if err != nil {
			return fmt.Errorf("encoding rigs: %w", err)
		}
		patrol["rigs"] = rigsJSON

		patrolJSON, err := json.Marshal(patrol)
		if err != nil {
			return fmt.Errorf("encoding patrol %s: %w", patrolName, err)
		}
		patrols[patrolName] = patrolJSON
		modified = true
	}

	if !modified {
		return nil
	}

	patrolsJSON, err := json.Marshal(patrols)
	if err != nil {
		return fmt.Errorf("encoding patrols: %w", err)
	}
	raw["patrols"] = patrolsJSON

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding daemon config: %w", err)
	}

	if err := os.WriteFile(path, append(out, '\n'), 0644); err != nil { //nolint:gosec // G306: config file
		return fmt.Errorf("writing daemon config: %w", err)
	}

	return nil
}
