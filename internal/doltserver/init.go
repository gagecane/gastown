package doltserver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
