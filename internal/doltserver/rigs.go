// rigs.go: Rig-level database management: InitRig, migration from beads,
// orphaned/broken workspace detection, and database removal.

package doltserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
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

// DatabaseExists checks whether a rig database exists in the centralized .dolt-data/ directory.
func DatabaseExists(townRoot, rigName string) bool {
	config := DefaultConfig(townRoot)
	doltDir := filepath.Join(config.DataDir, rigName, ".dolt")
	_, err := os.Stat(doltDir)
	return err == nil
}

// BrokenWorkspace represents a workspace whose metadata.json points to a
// nonexistent database on the Dolt server.
type BrokenWorkspace struct {
	// RigName is the rig whose database is missing.
	RigName string

	// BeadsDir is the path to the .beads directory with the broken metadata.
	BeadsDir string

	// ConfiguredDB is the dolt_database value from metadata.json.
	ConfiguredDB string

	// HasLocalData is true if .beads/dolt/<dbname> exists locally and can be migrated.
	HasLocalData bool

	// LocalDataPath is the path to local Dolt data, if present.
	LocalDataPath string

	// NotServed is true when the database exists on the filesystem but the
	// running Dolt server is not serving it. This typically means the server
	// needs a restart or was started from a different data directory.
	NotServed bool
}

// OrphanedDatabase represents a database in .dolt-data/ that is not referenced
// by any rig's metadata.json. These are leftover from partial setups, renames,
// or failed migrations.
type OrphanedDatabase struct {
	// Name is the database directory name in .dolt-data/.
	Name string

	// Path is the full path to the database directory.
	Path string

	// SizeBytes is the total size of the database directory.
	SizeBytes int64
}

// FindOrphanedDatabases scans .dolt-data/ for databases that are not referenced
// by any rig's metadata.json dolt_database field. These orphans consume disk space
// and are served by the Dolt server unnecessarily.
func FindOrphanedDatabases(townRoot string) ([]OrphanedDatabase, error) {
	databases, err := ListDatabases(townRoot)
	if err != nil {
		return nil, fmt.Errorf("listing databases: %w", err)
	}
	if len(databases) == 0 {
		return nil, nil
	}

	// Collect all referenced database names from metadata.json files
	referenced := collectReferencedDatabases(townRoot)

	// Find databases that exist on disk but aren't referenced
	config := DefaultConfig(townRoot)
	var orphans []OrphanedDatabase
	for _, dbName := range databases {
		if referenced[dbName] {
			continue
		}
		dbPath := filepath.Join(config.DataDir, dbName)
		size := dirSize(dbPath)
		orphans = append(orphans, OrphanedDatabase{
			Name:      dbName,
			Path:      dbPath,
			SizeBytes: size,
		})
	}

	return orphans, nil
}

// readExistingDoltDatabase reads the dolt_database field from an existing metadata.json.
// Returns empty string if the file doesn't exist or can't be read.
func readExistingDoltDatabase(beadsDir string) string {
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return ""
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(data, &meta); err != nil {
		return ""
	}
	if db, ok := meta["dolt_database"].(string); ok {
		return db
	}
	return ""
}

// collectReferencedDatabases returns a set of database names referenced by
// any rig's metadata.json dolt_database field. It checks multiple sources
// to avoid falsely flagging legitimate databases as orphans (gt-q8f6n):
//   - town-level .beads/metadata.json (HQ)
//   - all rigs from rigs.json
//   - all routes from routes.jsonl (catches rigs not yet in rigs.json)
//   - broad scan of metadata.json files under town root
func collectReferencedDatabases(townRoot string) map[string]bool {
	referenced := make(map[string]bool)

	// Check town-level beads (hq)
	townBeadsDir := filepath.Join(townRoot, ".beads")
	if db := readExistingDoltDatabase(townBeadsDir); db != "" {
		referenced[db] = true
	}

	// Check all rigs from rigs.json
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	data, err := os.ReadFile(rigsPath)
	if err == nil {
		var config struct {
			Rigs map[string]interface{} `json:"rigs"`
		}
		if err := json.Unmarshal(data, &config); err == nil {
			for rigName := range config.Rigs {
				beadsDir := FindRigBeadsDir(townRoot, rigName)
				if beadsDir == "" {
					continue
				}
				if db := readExistingDoltDatabase(beadsDir); db != "" {
					referenced[db] = true
				}
			}
		}
	}

	// Also check routes.jsonl — catches rigs that have routes but aren't in
	// rigs.json yet (e.g., hop before gt rig add). (gt-q8f6n fix)
	routesPath := filepath.Join(townRoot, ".beads", "routes.jsonl")
	if routesData, readErr := os.ReadFile(routesPath); readErr == nil {
		for _, line := range strings.Split(string(routesData), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var route struct {
				Path string `json:"path"`
			}
			if json.Unmarshal([]byte(line), &route) != nil || route.Path == "" {
				continue
			}
			// route.Path is relative to town root, e.g., "hop", "beads/mayor/rig"
			beadsDir := filepath.Join(townRoot, route.Path, ".beads")
			if db := readExistingDoltDatabase(beadsDir); db != "" {
				referenced[db] = true
			}
		}
	}

	// Scan top-level directories for any .beads/metadata.json with dolt_database.
	// This catches rigs that exist on disk but aren't in rigs.json or routes.jsonl.
	if entries, readErr := os.ReadDir(townRoot); readErr == nil {
		for _, entry := range entries {
			if !entry.IsDir() || entry.Name() == ".beads" || entry.Name() == "mayor" {
				continue
			}
			// Check <rig>/.beads/metadata.json
			if db := readExistingDoltDatabase(filepath.Join(townRoot, entry.Name(), ".beads")); db != "" {
				referenced[db] = true
			}
			// Check <rig>/mayor/rig/.beads/metadata.json
			if db := readExistingDoltDatabase(filepath.Join(townRoot, entry.Name(), "mayor", "rig", ".beads")); db != "" {
				referenced[db] = true
			}
		}
	}

	// Safety net: also mark all rig prefixes from rigs.json as referenced.
	// Some rigs use their prefix as the database name (e.g., "lc" for laneassist,
	// "gt" for gastown). If metadata.json is missing or corrupted, the prefix-named
	// DB would appear orphaned without this fallback. (gt-85w7)
	for _, prefix := range config.AllRigPrefixes(townRoot) {
		referenced[prefix] = true
	}

	return referenced
}

// CollectDatabaseOwners returns a map from database name to a human-readable
// owner description (e.g., "gastown rig beads", "town beads"). This is used by
// gt dolt status to annotate each database with its rig owner, preventing
// accidental drops of production databases. (GH#2252)
func CollectDatabaseOwners(townRoot string) map[string]string {
	owners := make(map[string]string)

	// Check town-level beads (hq)
	townBeadsDir := filepath.Join(townRoot, ".beads")
	if db := readExistingDoltDatabase(townBeadsDir); db != "" {
		owners[db] = "town beads"
	}

	// Check all rigs from rigs.json
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	data, err := os.ReadFile(rigsPath)
	if err == nil {
		var config struct {
			Rigs map[string]interface{} `json:"rigs"`
		}
		if err := json.Unmarshal(data, &config); err == nil {
			for rigName := range config.Rigs {
				beadsDir := FindRigBeadsDir(townRoot, rigName)
				if beadsDir == "" {
					continue
				}
				if db := readExistingDoltDatabase(beadsDir); db != "" {
					owners[db] = rigName + " rig beads"
				}
			}
		}
	}

	// Also check routes.jsonl
	routesPath := filepath.Join(townRoot, ".beads", "routes.jsonl")
	if routesData, readErr := os.ReadFile(routesPath); readErr == nil {
		for _, line := range strings.Split(string(routesData), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var route struct {
				Prefix string `json:"prefix"`
				Path   string `json:"path"`
			}
			if json.Unmarshal([]byte(line), &route) != nil || route.Path == "" {
				continue
			}
			beadsDir := filepath.Join(townRoot, route.Path, ".beads")
			if db := readExistingDoltDatabase(beadsDir); db != "" {
				if _, already := owners[db]; !already {
					// Derive a name from the route path
					parts := strings.Split(route.Path, "/")
					owners[db] = parts[0] + " rig beads"
				}
			}
		}
	}

	// Scan top-level directories for any .beads/metadata.json
	if entries, readErr := os.ReadDir(townRoot); readErr == nil {
		for _, entry := range entries {
			if !entry.IsDir() || entry.Name() == ".beads" || entry.Name() == "mayor" {
				continue
			}
			dirName := entry.Name()
			if db := readExistingDoltDatabase(filepath.Join(townRoot, dirName, ".beads")); db != "" {
				if _, already := owners[db]; !already {
					owners[db] = dirName + " rig beads"
				}
			}
			if db := readExistingDoltDatabase(filepath.Join(townRoot, dirName, "mayor", "rig", ".beads")); db != "" {
				if _, already := owners[db]; !already {
					owners[db] = dirName + " rig beads"
				}
			}
		}
	}

	return owners
}

// RemoveDatabase removes an orphaned database directory from .dolt-data/.
// The caller should verify the database is actually orphaned before calling this.
// If the Dolt server is running, it will DROP the database first.
// If force is false and the database has real user tables, it refuses to remove. (gt-q8f6n)
func RemoveDatabase(townRoot, dbName string, force bool) error {
	config := DefaultConfig(townRoot)
	dbPath := filepath.Join(config.DataDir, dbName)

	// Verify the directory exists
	if _, err := os.Stat(filepath.Join(dbPath, ".dolt")); err != nil {
		return fmt.Errorf("database %q not found at %s", dbName, dbPath)
	}

	// Safety check: if DB has real data and force is not set, refuse. (gt-q8f6n, gt-xvh)
	// This prevents destroying legitimate databases that happen to be unreferenced.
	running, _, _ := IsRunning(townRoot)
	if !force {
		if running {
			// Server is up — check via SQL for user tables
			if hasData, _ := databaseHasUserTables(townRoot, dbName); hasData {
				return fmt.Errorf("database %q has user tables — use --force to remove", dbName)
			}
		} else {
			// Server is down — check via filesystem size as a safety proxy. (gt-xvh)
			// Databases with >1MB of data are almost certainly not empty orphans.
			// Without the server, we can't query tables, so size is the best heuristic.
			size := dirSize(dbPath)
			const safeRemoveThreshold = 1 << 20 // 1MB
			if size > safeRemoveThreshold {
				return fmt.Errorf("database %q has %s of data (server offline, cannot verify contents) — start server or use --force to remove",
					dbName, formatBytes(size))
			}
		}
	}

	// If server is running, DROP the database first and clean up branch control entries.
	// In Dolt 1.81.x, DROP DATABASE does not automatically remove dolt_branch_control
	// entries for the dropped database. These stale entries cause the database directory
	// to be recreated when connections reference the database name (gt-zlv7l).
	if running {
		// Try to DROP — capture errors for read-only detection (gt-r1cyd)
		if dropErr := serverExecSQL(townRoot, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName)); dropErr != nil {
			if IsReadOnlyError(dropErr.Error()) {
				return fmt.Errorf("DROP put server into read-only mode: %w", dropErr)
			}
			// Other errors (DB not loaded, etc.) — continue with filesystem removal
		}
		// Explicitly clean up branch control entries to prevent the database from being
		// recreated on subsequent connections. `database` is a reserved word, so backtick-quote it.
		_ = serverExecSQL(townRoot, fmt.Sprintf("DELETE FROM dolt_branch_control WHERE `database` = '%s'", dbName))
	}

	InvalidateDBCache() // Database removed — bust the cache.

	// Remove the directory
	if err := os.RemoveAll(dbPath); err != nil {
		return fmt.Errorf("removing database directory: %w", err)
	}

	return nil
}

// databaseHasUserTables checks if a database has tables beyond Dolt system tables.
// Returns (true, nil) if user tables exist, (false, nil) if only system tables or empty.
func databaseHasUserTables(townRoot, dbName string) (bool, error) {
	config := DefaultConfig(townRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	query := fmt.Sprintf("USE `%s`; SHOW TABLES", dbName)
	cmd := buildDoltSQLCmd(ctx, config, "-r", "csv", "-q", query)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, err
	}

	// Parse output — each line is a table name. Skip Dolt system tables.
	for _, line := range strings.Split(string(output), "\n") {
		table := strings.TrimSpace(line)
		if table == "" || table == "Tables_in_"+dbName || table == "Table" {
			continue
		}
		// Dolt system tables start with "dolt_"
		if !strings.HasPrefix(table, "dolt_") {
			return true, nil
		}
	}
	return false, nil
}

// FindBrokenWorkspaces scans all rig metadata.json files for Dolt server
// configuration where the referenced database doesn't exist in .dolt-data/
// or exists on disk but isn't served by the running Dolt server.
// These workspaces are broken: bd commands will fail or silently create
// isolated local databases instead of connecting to the centralized server.
func FindBrokenWorkspaces(townRoot string) ([]BrokenWorkspace, string) {
	var broken []BrokenWorkspace
	var warning string

	// Query the running server once for all served databases.
	// If the server isn't running, servedDBs will be nil and we
	// fall back to filesystem-only checks (previous behavior).
	var servedDBs map[string]bool
	if running, _, _ := IsRunning(townRoot); running {
		if served, _, err := VerifyDatabasesWithRetry(townRoot, 3); err == nil {
			servedDBs = make(map[string]bool, len(served))
			for _, db := range served {
				servedDBs[strings.ToLower(db)] = true
			}
		} else {
			warning = fmt.Sprintf("Warning: Dolt server is running but could not verify databases: %v\n"+
				"Server-aware checks are disabled; only filesystem checks will be performed.", err)
		}
	}

	// Check town-level beads (hq)
	townBeadsDir := filepath.Join(townRoot, ".beads")
	if ws := checkWorkspace(townRoot, "hq", townBeadsDir, servedDBs); ws != nil {
		broken = append(broken, *ws)
	}

	// Check rig-level beads via rigs.json
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	data, err := os.ReadFile(rigsPath)
	if err != nil {
		return broken, warning
	}
	var config struct {
		Rigs map[string]interface{} `json:"rigs"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return broken, warning
	}

	for rigName := range config.Rigs {
		beadsDir := FindRigBeadsDir(townRoot, rigName)
		if beadsDir == "" {
			continue
		}
		if ws := checkWorkspace(townRoot, rigName, beadsDir, servedDBs); ws != nil {
			broken = append(broken, *ws)
		}
	}

	return broken, warning
}

// checkWorkspace checks a single rig's metadata.json for broken Dolt configuration.
// Returns nil if the workspace is healthy or not configured for Dolt server mode.
func checkWorkspace(townRoot, rigName, beadsDir string, servedDBs map[string]bool) *BrokenWorkspace {
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil
	}

	var metadata struct {
		DoltMode     string `json:"dolt_mode"`
		DoltDatabase string `json:"dolt_database"`
		Backend      string `json:"backend"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil
	}

	// Only check workspaces configured for Dolt server mode
	if metadata.DoltMode != "server" || metadata.Backend != "dolt" {
		return nil
	}

	dbName := metadata.DoltDatabase
	if dbName == "" {
		dbName = rigName
	}

	existsOnDisk := DatabaseExists(townRoot, dbName)

	// If the server is running (servedDBs != nil), also check that the
	// database is actually being served. A database can exist on disk but
	// not be served if the server was started from a different data
	// directory or needs a restart after migration.
	if existsOnDisk {
		if servedDBs != nil && !servedDBs[strings.ToLower(dbName)] {
			return &BrokenWorkspace{
				RigName:      rigName,
				BeadsDir:     beadsDir,
				ConfiguredDB: dbName,
				NotServed:    true,
			}
		}
		return nil // healthy: exists on disk and (served or server not checked)
	}

	ws := &BrokenWorkspace{
		RigName:      rigName,
		BeadsDir:     beadsDir,
		ConfiguredDB: dbName,
	}

	// Check for local data that could be migrated
	localDoltPath := findLocalDoltDB(beadsDir)
	if localDoltPath != "" {
		ws.HasLocalData = true
		ws.LocalDataPath = localDoltPath
	}

	return ws
}

// RepairWorkspace fixes a broken workspace by creating the missing database
// or migrating local data if present. Returns a description of what was done.
func RepairWorkspace(townRoot string, ws BrokenWorkspace) (string, error) {
	if ws.HasLocalData {
		// Migrate local data to centralized location
		if err := MigrateRigFromBeads(townRoot, ws.ConfiguredDB, ws.LocalDataPath); err != nil {
			return "", fmt.Errorf("migrating local data for %s: %w", ws.RigName, err)
		}
		return fmt.Sprintf("migrated local data from %s", ws.LocalDataPath), nil
	}

	// No local data — create a fresh database
	_, created, err := InitRig(townRoot, ws.ConfiguredDB)
	if err != nil {
		return "", fmt.Errorf("creating database for %s: %w", ws.RigName, err)
	}
	if !created {
		return "database already exists (no-op)", nil
	}
	return "created new database", nil
}
