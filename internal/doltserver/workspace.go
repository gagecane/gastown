package doltserver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
)

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
	cfg := DefaultConfig(townRoot)
	var orphans []OrphanedDatabase
	for _, dbName := range databases {
		if referenced[dbName] {
			continue
		}
		dbPath := filepath.Join(cfg.DataDir, dbName)
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
		var cfg struct {
			Rigs map[string]interface{} `json:"rigs"`
		}
		if err := json.Unmarshal(data, &cfg); err == nil {
			for rigName := range cfg.Rigs {
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
		var cfg struct {
			Rigs map[string]interface{} `json:"rigs"`
		}
		if err := json.Unmarshal(data, &cfg); err == nil {
			for rigName := range cfg.Rigs {
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
	var cfg struct {
		Rigs map[string]interface{} `json:"rigs"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return broken, warning
	}

	for rigName := range cfg.Rigs {
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
