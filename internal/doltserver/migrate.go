package doltserver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// Migration represents a database migration from old to new location.
type Migration struct {
	RigName    string
	SourcePath string
	TargetPath string
}

// findLocalDoltDB scans beadsDir/dolt/ for a subdirectory containing a .dolt
// directory (an embedded Dolt database). Returns the full path to the database
// directory, or "" if none found.
//
// bd names the subdirectory based on internal conventions (e.g., beads_hq,
// beads_gt) that have changed across versions. Scanning avoids hardcoding
// assumptions about the naming scheme.
//
// If multiple databases are found, returns "" and logs a warning to stderr.
// Callers should not silently pick one — ambiguity requires manual resolution.
func findLocalDoltDB(beadsDir string) string {
	doltParent := filepath.Join(beadsDir, "dolt")
	entries, err := os.ReadDir(doltParent)
	if err != nil {
		return ""
	}
	var candidates []string
	for _, e := range entries {
		// Resolve symlinks: DirEntry.IsDir() returns false for symlinks-to-directories
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(filepath.Join(doltParent, e.Name()))
			if err != nil {
				continue
			}
			fi, err := os.Stat(resolved)
			if err != nil || !fi.IsDir() {
				continue
			}
		} else if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(doltParent, e.Name())
		if _, err := os.Stat(filepath.Join(candidate, ".dolt")); err == nil {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		if len(entries) > 0 {
			fmt.Fprintf(os.Stderr, "[doltserver] Warning: %s exists but contains no valid dolt database\n", doltParent)
		}
		return ""
	}
	if len(candidates) > 1 {
		fmt.Fprintf(os.Stderr, "[doltserver] Warning: multiple dolt databases found in %s: %v — manual resolution required\n", doltParent, candidates)
		return ""
	}
	return candidates[0]
}

// FindMigratableDatabases finds existing dolt databases that can be migrated.
func FindMigratableDatabases(townRoot string) []Migration {
	var migrations []Migration
	config := DefaultConfig(townRoot)

	// Check town-level beads database -> .dolt-data/hq
	townBeadsDir := beads.ResolveBeadsDir(townRoot)
	townSource := findLocalDoltDB(townBeadsDir)
	if townSource != "" {
		// Check target doesn't already have data
		targetDir := filepath.Join(config.DataDir, "hq")
		if _, err := os.Stat(filepath.Join(targetDir, ".dolt")); os.IsNotExist(err) {
			migrations = append(migrations, Migration{
				RigName:    "hq",
				SourcePath: townSource,
				TargetPath: targetDir,
			})
		}
	}

	// Check rig-level beads databases
	// Look for directories in townRoot, following .beads/redirect if present
	entries, err := os.ReadDir(townRoot)
	if err != nil {
		return migrations
	}

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		rigName := entry.Name()
		resolvedBeadsDir := beads.ResolveBeadsDir(filepath.Join(townRoot, rigName))
		rigSource := findLocalDoltDB(resolvedBeadsDir)

		if rigSource != "" {
			// Check target doesn't already have data
			targetDir := filepath.Join(config.DataDir, rigName)
			if _, err := os.Stat(filepath.Join(targetDir, ".dolt")); os.IsNotExist(err) {
				migrations = append(migrations, Migration{
					RigName:    rigName,
					SourcePath: rigSource,
					TargetPath: targetDir,
				})
			}
		}
	}

	return migrations
}

// MigrateRigFromBeads migrates an existing beads Dolt database to the data directory.
// This is used to migrate from the old per-rig .beads/dolt/<db_name> layout to the new
// centralized .dolt-data/<rigname> layout.
func MigrateRigFromBeads(townRoot, rigName, sourcePath string) error {
	config := DefaultConfig(townRoot)

	targetDir := filepath.Join(config.DataDir, rigName)

	// Check if target already exists
	if _, err := os.Stat(filepath.Join(targetDir, ".dolt")); err == nil {
		return fmt.Errorf("rig database %q already exists at %s", rigName, targetDir)
	}

	// Check if source exists
	if _, err := os.Stat(filepath.Join(sourcePath, ".dolt")); os.IsNotExist(err) {
		return fmt.Errorf("source database not found at %s", sourcePath)
	}

	// Ensure data directory exists
	if err := os.MkdirAll(config.DataDir, 0755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}

	// Move the database directory (with cross-filesystem fallback)
	if err := moveDir(sourcePath, targetDir); err != nil {
		return fmt.Errorf("moving database: %w", err)
	}

	// Update metadata.json to point to the server
	if err := EnsureMetadata(townRoot, rigName); err != nil {
		// Non-fatal: migration succeeded, metadata update failed
		fmt.Fprintf(os.Stderr, "Warning: database migrated but metadata.json update failed: %v\n", err)
	}

	return nil
}
