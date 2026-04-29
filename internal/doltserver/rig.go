package doltserver

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// InitRig initializes a new rig database in the data directory.
// If the Dolt server is running, it executes CREATE DATABASE to register the
// database with the live server (avoiding the need for a restart).
// Returns (serverWasRunning, created, err). created is false when the database
// already existed on disk (idempotent no-op).
func InitRig(townRoot, rigName string) (serverWasRunning bool, created bool, err error) {
	if rigName == "" {
		return false, false, fmt.Errorf("rig name cannot be empty")
	}

	config := DefaultConfig(townRoot)

	// Validate rig name (simple alphanumeric + underscore/dash)
	for _, r := range rigName {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return false, false, fmt.Errorf("invalid rig name %q: must contain only alphanumeric, underscore, or dash", rigName)
		}
	}

	rigDir := filepath.Join(config.DataDir, rigName)

	// Check if already exists on disk — idempotent for callers like gt install.
	// Still run EnsureMetadata to repair missing/corrupt metadata.json.
	if _, err := os.Stat(filepath.Join(rigDir, ".dolt")); err == nil {
		running, _, _ := IsRunning(townRoot)
		if err := EnsureMetadata(townRoot, rigName); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: metadata.json update failed for existing database %q: %v\n", rigName, err)
		}
		return running, false, nil
	}

	// Check if server is running
	running, runningPID, _ := IsRunning(townRoot)

	if running {
		// If the data directory doesn't exist, the server is orphaned (e.g., user
		// deleted ~/gt and re-ran gt install while an old server was still running).
		// Stop the orphaned server and fall through to the offline init path.
		if _, err := os.Stat(config.DataDir); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Warning: Dolt server (PID %d) is running but data directory %s does not exist — stopping orphaned server\n", runningPID, config.DataDir)
			if stopErr := Stop(townRoot); stopErr != nil {
				// Force-kill if graceful stop fails (no PID file for orphaned server)
				if runningPID > 0 {
					if proc, err := os.FindProcess(runningPID); err == nil {
						_ = proc.Kill()
					}
				}
			}
			running = false
		}
	}

	if running {
		// Server is running: use CREATE DATABASE which both creates the
		// directory and registers the database with the live server.
		if err := serverExecSQL(townRoot, fmt.Sprintf("CREATE DATABASE `%s`", rigName)); err != nil {
			return true, false, fmt.Errorf("creating database on running server: %w", err)
		}
		// Wait for the new database to appear in the server's in-memory catalog.
		// CREATE DATABASE returns before the catalog is fully updated, so
		// subsequent USE/query operations can fail with "Unknown database".
		// Non-fatal: the database was created, so we log a warning and continue
		// to EnsureMetadata. The retry wrappers (doltSQLWithRetry) will handle
		// any residual catalog propagation delays in subsequent operations.
		if err := waitForCatalog(townRoot, rigName); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: catalog visibility wait timed out (will retry on use): %v\n", err)
		}
	} else {
		// Server not running: create directory and init manually.
		// The database will be picked up when the server starts.
		if err := os.MkdirAll(rigDir, 0755); err != nil {
			return false, false, fmt.Errorf("creating rig directory: %w", err)
		}

		cmd := exec.Command("dolt", "init")
		cmd.Dir = rigDir
		setProcessGroup(cmd)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return false, false, fmt.Errorf("initializing Dolt database: %w\n%s", err, output)
		}
	}

	InvalidateDBCache() // New database created — bust the cache.

	// Update metadata.json to point to the server
	if err := EnsureMetadata(townRoot, rigName); err != nil {
		// Non-fatal: init succeeded, metadata update failed
		fmt.Fprintf(os.Stderr, "Warning: database initialized but metadata.json update failed: %v\n", err)
	}

	return running, true, nil
}

// buildRigPrefixMap reads rigs.json and returns a map from Dolt database name
// (beads prefix without the trailing hyphen) to the rig directory name.
// Example: {"be": "beads_el", "sw": "sooper_whisper"}.
// Rigs where the database name equals the directory name are not included.
func buildRigPrefixMap(townRoot string) map[string]string {
	result := make(map[string]string)
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	data, err := os.ReadFile(rigsPath)
	if err != nil {
		return result
	}
	var parsed struct {
		Rigs map[string]struct {
			Beads struct {
				Prefix string `json:"prefix"`
			} `json:"beads"`
		} `json:"rigs"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return result
	}
	for rigName, info := range parsed.Rigs {
		prefix := strings.TrimSuffix(info.Beads.Prefix, "-")
		if prefix != "" && prefix != rigName {
			result[prefix] = rigName
		}
	}
	return result
}

// pickDBForRig selects which database name to use for a rig when multiple
// candidates exist. Prefers the value already in metadata.json to avoid
// oscillating corrections between two valid aliases for the same rig.
func pickDBForRig(townRoot, rigName string, candidates []string) string {
	beadsDir := FindRigBeadsDir(townRoot, rigName)
	if beadsDir != "" {
		if data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json")); err == nil {
			var meta map[string]interface{}
			if json.Unmarshal(data, &meta) == nil {
				if existingDB, _ := meta["dolt_database"].(string); existingDB != "" {
					for _, c := range candidates {
						if c == existingDB {
							return c // Already correct — no repair needed
						}
					}
				}
			}
		}
	}
	return candidates[0] // Default: first (alphabetical from os.ReadDir)
}

// buildDatabaseToRigMap loads routes.jsonl and builds a map from database name
// (prefix without hyphen) to rig name (first component of the path).
// For example: "bd" -> "beads", "gt" -> "gastown", "sw" -> "sallaWork"
func buildDatabaseToRigMap(townRoot string) map[string]string {
	result := make(map[string]string)
	beadsDir := filepath.Join(townRoot, ".beads")
	routes, err := beads.LoadRoutes(beadsDir)
	if err != nil {
		return result // Return empty map on error
	}
	for _, route := range routes {
		// Extract rig name from path (first component before "/")
		// e.g., "beads/mayor/rig" -> "beads", "gastown/mayor/rig" -> "gastown"
		prefix := strings.TrimSuffix(route.Prefix, "-")
		parts := strings.Split(route.Path, "/")
		if len(parts) > 0 && parts[0] != "" && parts[0] != "." {
			result[prefix] = parts[0]
		}
	}
	return result
}

// FindRigBeadsDir returns the .beads directory path for a rig (read-only lookup).
// For "hq", returns <townRoot>/.beads.
// For other rigs, returns <townRoot>/<rigName>/mayor/rig/.beads if it exists,
// otherwise <townRoot>/<rigName>/.beads if it exists,
// otherwise <townRoot>/<rigName>/mayor/rig/.beads (for creation by caller).
//
// WARNING: This function has a TOCTOU race — the returned directory may change
// state between the Stat check and the caller's operation. For write operations
// that need the directory to exist, use FindOrCreateRigBeadsDir instead.
// For read-only operations, handle errors on the returned path gracefully.
func FindRigBeadsDir(townRoot, rigName string) string {
	if townRoot == "" || rigName == "" {
		return ""
	}
	if rigName == "hq" {
		return filepath.Join(townRoot, ".beads")
	}

	// Prefer mayor/rig/.beads (canonical location for tracked beads)
	mayorBeads := filepath.Join(townRoot, rigName, "mayor", "rig", ".beads")
	if _, err := os.Stat(mayorBeads); err == nil {
		return mayorBeads
	}

	// Fall back to rig-root .beads
	rigBeads := filepath.Join(townRoot, rigName, ".beads")
	if _, err := os.Stat(rigBeads); err == nil {
		return rigBeads
	}

	// Neither exists; return rig-root path (consistent with FindOrCreateRigBeadsDir)
	return rigBeads
}

// FindOrCreateRigBeadsDir atomically resolves and ensures the .beads directory
// exists for a rig. Unlike FindRigBeadsDir, this combines directory resolution
// with creation to avoid TOCTOU races where the directory state changes between
// the existence check and the caller's write operation.
//
// Use this for write operations (EnsureMetadata, etc.) where the directory must
// exist. Use FindRigBeadsDir for read-only lookups where graceful failure on
// missing directories is acceptable.
func FindOrCreateRigBeadsDir(townRoot, rigName string) (string, error) {
	if townRoot == "" {
		return "", fmt.Errorf("townRoot cannot be empty")
	}
	if rigName == "" {
		return "", fmt.Errorf("rigName cannot be empty")
	}
	if rigName == "hq" {
		dir := filepath.Join(townRoot, ".beads")
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("creating HQ beads dir: %w", err)
		}
		return dir, nil
	}

	// Check mayor/rig/.beads first (canonical location).
	// Use MkdirAll as an idempotent existence check+create to close the
	// TOCTOU window between os.Stat and the caller's file operations.
	mayorBeads := filepath.Join(townRoot, rigName, "mayor", "rig", ".beads")
	if _, err := os.Stat(mayorBeads); err == nil {
		// Ensure it still exists (no-op if present, recreates if deleted)
		if err := os.MkdirAll(mayorBeads, 0755); err != nil {
			return "", fmt.Errorf("ensuring mayor beads dir: %w", err)
		}
		return mayorBeads, nil
	}

	// Check rig-root .beads
	rigBeads := filepath.Join(townRoot, rigName, ".beads")
	if _, err := os.Stat(rigBeads); err == nil {
		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			return "", fmt.Errorf("ensuring rig beads dir: %w", err)
		}
		return rigBeads, nil
	}

	// Neither exists — create rig-root .beads (NOT mayor path).
	// The mayor/rig/.beads path should only be used when the source repo
	// has tracked beads (checked out via git clone). Creating it here would
	// cause InitBeads to misdetect an untracked repo as having tracked beads,
	// taking the redirect early-return and skipping config.yaml creation
	// (see rig/manager.go InitBeads).
	if err := os.MkdirAll(rigBeads, 0755); err != nil {
		return "", fmt.Errorf("creating beads dir: %w", err)
	}

	return rigBeads, nil
}
