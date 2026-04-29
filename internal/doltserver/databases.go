package doltserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// dbCache deduplicates and caches SHOW DATABASES results to prevent the
// "thundering herd" problem where multiple concurrent callers each spawn a
// dolt sql subprocess. See GH#2180.
var dbCache = struct {
	mu       sync.Mutex
	result   []string
	err      error
	updated  time.Time
	inflight chan struct{} // non-nil when a fetch is in progress
}{} //nolint:gochecknoglobals // process-level cache, intentional

const dbCacheTTL = 30 * time.Second

// InvalidateDBCache clears the cached ListDatabases result, forcing the next
// call to re-query. Use after operations that change the database set (e.g.,
// CREATE DATABASE, DROP DATABASE, InitRig).
func InvalidateDBCache() {
	dbCache.mu.Lock()
	dbCache.result = nil
	dbCache.err = nil
	dbCache.updated = time.Time{}
	dbCache.mu.Unlock()
}

// ListDatabases returns the list of available rig databases.
// For local servers, scans the data directory on disk.
// For remote servers, queries SHOW DATABASES via SQL.
//
// Results are cached for 30 seconds and concurrent callers share a single
// in-flight query to avoid overwhelming the Dolt server (GH#2180).
func ListDatabases(townRoot string) ([]string, error) {
	config := DefaultConfig(townRoot)

	if config.IsRemote() {
		return listDatabasesCached(config)
	}

	return listDatabasesLocal(config)
}

// listDatabasesLocal scans the filesystem for valid Dolt database directories.
func listDatabasesLocal(config *Config) ([]string, error) {
	entries, err := os.ReadDir(config.DataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var databases []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Check if this directory is a valid Dolt database.
		// A phantom/corrupted .dolt/ dir (e.g., from DROP + catalog re-materialization)
		// will have .dolt/ but no noms/manifest. Loading such a dir crashes the server.
		doltDir := filepath.Join(config.DataDir, entry.Name(), ".dolt")
		if _, err := os.Stat(doltDir); err != nil {
			continue
		}
		manifest := filepath.Join(doltDir, "noms", "manifest")
		if _, err := os.Stat(manifest); err != nil {
			// .dolt/ exists but no noms/manifest — corrupted/phantom database
			fmt.Fprintf(os.Stderr, "Warning: skipping corrupted database %q (missing noms/manifest)\n", entry.Name())
			continue
		}
		databases = append(databases, entry.Name())
	}

	return databases, nil
}

// listDatabasesCached returns cached SHOW DATABASES results for remote servers,
// deduplicating concurrent queries via a shared in-flight channel.
func listDatabasesCached(config *Config) ([]string, error) {
	dbCache.mu.Lock()

	// Return cached result if fresh.
	if dbCache.result != nil && time.Since(dbCache.updated) < dbCacheTTL {
		result := make([]string, len(dbCache.result))
		copy(result, dbCache.result)
		dbCache.mu.Unlock()
		return result, nil
	}

	// If another goroutine is already fetching, wait for it.
	if dbCache.inflight != nil {
		ch := dbCache.inflight
		dbCache.mu.Unlock()
		<-ch
		// Re-read the result the fetcher stored.
		dbCache.mu.Lock()
		result := make([]string, len(dbCache.result))
		copy(result, dbCache.result)
		err := dbCache.err
		dbCache.mu.Unlock()
		return result, err
	}

	// We're the fetcher. Mark in-flight.
	ch := make(chan struct{})
	dbCache.inflight = ch
	dbCache.mu.Unlock()

	// Execute the actual query.
	result, err := listDatabasesRemote(config)

	// Store result and wake waiters.
	dbCache.mu.Lock()
	dbCache.result = result
	dbCache.err = err
	if err == nil {
		dbCache.updated = time.Now()
	}
	dbCache.inflight = nil
	dbCache.mu.Unlock()
	close(ch)

	if err != nil {
		return nil, err
	}
	out := make([]string, len(result))
	copy(out, result)
	return out, nil
}

// listDatabasesRemote queries SHOW DATABASES on a remote Dolt server.
func listDatabasesRemote(config *Config) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := buildDoltSQLCmd(ctx, config, "-r", "json", "-q", "SHOW DATABASES")

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("querying remote SHOW DATABASES: %w (stderr: %s)", err, strings.TrimSpace(stderrBuf.String()))
	}

	var result struct {
		Rows []struct {
			Database string `json:"Database"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("parsing SHOW DATABASES JSON: %w", err)
	}

	var databases []string
	for _, row := range result.Rows {
		db := row.Database
		if !IsSystemDatabase(db) {
			databases = append(databases, db)
		}
	}
	return databases, nil
}

// VerifyDatabases queries the running Dolt SQL server for SHOW DATABASES and
// compares the result against the filesystem-discovered databases from
// ListDatabases. Returns the list of databases the server is actually serving
// and any that exist on disk but are missing from the server.
//
// This catches the silent failure mode where Dolt skips databases with stale
// manifests after migration — the filesystem says they exist, but the server
// doesn't serve them.
func VerifyDatabases(townRoot string) (served, missing []string, err error) {
	return verifyDatabasesWithRetry(townRoot, 1)
}

// VerifyExpectedDatabasesAtConfig queries SHOW DATABASES on the exact server
// described by config and reports which expected database names are missing.
// Unlike VerifyDatabases, this helper does not inspect the filesystem; it is
// intended for health checks that must validate a specific server address from
// metadata rather than the town's default local Dolt config.
func VerifyExpectedDatabasesAtConfig(config *Config, expected []string) (served, missing []string, err error) {
	const baseBackoff = 1 * time.Second
	const maxBackoff = 8 * time.Second
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := buildServerSQLCmd(ctx, config, "-r", "json", "-q", "SHOW DATABASES")
		var stderrBuf bytes.Buffer
		cmd.Stderr = &stderrBuf
		output, queryErr := cmd.Output()
		cancel()
		if queryErr != nil {
			stderrMsg := strings.TrimSpace(stderrBuf.String())
			errDetail := strings.TrimSpace(string(output))
			if stderrMsg != "" {
				errDetail = errDetail + " (stderr: " + stderrMsg + ")"
			}
			lastErr = fmt.Errorf("querying SHOW DATABASES: %w (output: %s)", queryErr, errDetail)
			if attempt < 3 {
				backoff := baseBackoff
				for i := 1; i < attempt; i++ {
					backoff *= 2
					if backoff > maxBackoff {
						backoff = maxBackoff
						break
					}
				}
				time.Sleep(backoff)
			}
			continue
		}

		served, err = parseShowDatabases(output)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing SHOW DATABASES output: %w", err)
		}

		missing = findMissingDatabases(served, expected)
		return served, missing, nil
	}

	return nil, nil, lastErr
}

// VerifyDatabasesWithRetry is like VerifyDatabases but retries the SHOW DATABASES
// query with exponential backoff to handle the case where the server has just started
// and is still loading databases.
func VerifyDatabasesWithRetry(townRoot string, maxAttempts int) (served, missing []string, err error) {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	return verifyDatabasesWithRetry(townRoot, maxAttempts)
}

func verifyDatabasesWithRetry(townRoot string, maxAttempts int) (served, missing []string, err error) {
	config := DefaultConfig(townRoot)

	// Retry with backoff since the server may still be loading databases
	// after a recent start (Start() only waits 500ms + process-alive check).
	// Both reachability and query are inside the loop so transient startup
	// failures are retried.
	const baseBackoff = 1 * time.Second
	const maxBackoff = 8 * time.Second
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Check if the server is reachable (TCP-level).
		if reachErr := CheckServerReachable(townRoot); reachErr != nil {
			lastErr = fmt.Errorf("server not reachable: %w", reachErr)
			if attempt < maxAttempts {
				backoff := baseBackoff
				for i := 1; i < attempt; i++ {
					backoff *= 2
					if backoff > maxBackoff {
						backoff = maxBackoff
						break
					}
				}
				time.Sleep(backoff)
			}
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := buildDoltSQLCmd(ctx, config,
			"-r", "json",
			"-q", "SHOW DATABASES",
		)

		// Capture stderr separately so it doesn't corrupt JSON parsing.
		// Dolt commonly writes deprecation/manifest warnings to stderr.
		// See also daemon/dolt.go:listDatabases() which uses cmd.Output()
		// for the same reason.
		var stderrBuf bytes.Buffer
		cmd.Stderr = &stderrBuf
		output, queryErr := cmd.Output()
		cancel()
		if queryErr != nil {
			stderrMsg := strings.TrimSpace(stderrBuf.String())
			errDetail := strings.TrimSpace(string(output))
			if stderrMsg != "" {
				errDetail = errDetail + " (stderr: " + stderrMsg + ")"
			}
			lastErr = fmt.Errorf("querying SHOW DATABASES: %w (output: %s)", queryErr, errDetail)
			if attempt < maxAttempts {
				backoff := baseBackoff
				for i := 1; i < attempt; i++ {
					backoff *= 2
					if backoff > maxBackoff {
						backoff = maxBackoff
						break
					}
				}
				time.Sleep(backoff)
			}
			continue
		}

		var parseErr error
		served, parseErr = parseShowDatabases(output)
		if parseErr != nil {
			return nil, nil, fmt.Errorf("parsing SHOW DATABASES output: %w", parseErr)
		}

		// Compare against filesystem databases.
		fsDatabases, fsErr := ListDatabases(townRoot)
		if fsErr != nil {
			return served, nil, fmt.Errorf("listing filesystem databases: %w", fsErr)
		}

		missing = findMissingDatabases(served, fsDatabases)
		return served, missing, nil
	}
	return nil, nil, lastErr
}

// systemDatabases is the set of Dolt/MySQL internal databases that should be
// filtered from SHOW DATABASES results. These are not user rig databases:
//   - information_schema: MySQL standard metadata
//   - mysql: MySQL system database (privileges, users)
//   - dolt_cluster: Dolt clustering internal database (present when clustering is configured)
var systemDatabases = map[string]bool{
	"information_schema": true,
	"mysql":              true,
	"dolt_cluster":       true,
}

// IsSystemDatabase returns true if the given database name is a Dolt/MySQL
// internal database that should be excluded from user-facing database lists.
func IsSystemDatabase(name string) bool {
	return systemDatabases[strings.ToLower(name)]
}

// parseShowDatabases parses the output of SHOW DATABASES from dolt sql.
// It tries JSON parsing first, falling back to line-based parsing for
// plain-text output. Returns an error if the output format is unrecognized.
// Filters out system databases (information_schema, mysql, dolt_cluster).
func parseShowDatabases(output []byte) ([]string, error) {
	// Try JSON first. Use a raw map to detect schema presence.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(output, &raw); err != nil {
		// Check if the output looks like JSON that failed to parse —
		// don't fall through to line parsing with JSON-shaped text.
		trimmed := strings.TrimSpace(string(output))
		if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
			return nil, fmt.Errorf("output looks like JSON but failed to parse: %w", err)
		}

		// Fall back to line parsing for plain-text output.
		var databases []string
		for _, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && line != "Database" && !strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "|") {
				if !IsSystemDatabase(line) {
					databases = append(databases, line)
				}
			}
		}
		if len(databases) == 0 && len(trimmed) > 0 {
			return nil, fmt.Errorf("fallback parser returned zero databases from non-empty output (%d bytes); format may be unrecognized", len(trimmed))
		}
		return databases, nil
	}

	// JSON parsed — require the expected "rows" key.
	rowsRaw, hasRows := raw["rows"]
	if !hasRows {
		return nil, fmt.Errorf("JSON output missing expected 'rows' key (keys: %v); Dolt output schema may have changed", jsonKeys(raw))
	}

	var rows []struct {
		Database string `json:"Database"`
	}
	if err := json.Unmarshal(rowsRaw, &rows); err != nil {
		return nil, fmt.Errorf("JSON 'rows' field has unexpected type: %w", err)
	}

	var databases []string
	for _, row := range rows {
		if row.Database != "" && !IsSystemDatabase(row.Database) {
			databases = append(databases, row.Database)
		}
	}
	return databases, nil
}

// findMissingDatabases returns filesystem databases not present in the served list.
// Comparison is case-insensitive since Dolt database names are case-insensitive
// in SQL but case-preserving on the filesystem.
func findMissingDatabases(served, fsDatabases []string) []string {
	servedSet := make(map[string]bool, len(served))
	for _, db := range served {
		servedSet[strings.ToLower(db)] = true
	}
	var missing []string
	for _, db := range fsDatabases {
		if !servedSet[strings.ToLower(db)] {
			missing = append(missing, db)
		}
	}
	return missing
}

// jsonKeys returns the top-level keys from a JSON object map, for diagnostics.
func jsonKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// DatabaseExists checks whether a rig database exists in the centralized .dolt-data/ directory.
func DatabaseExists(townRoot, rigName string) bool {
	config := DefaultConfig(townRoot)
	doltDir := filepath.Join(config.DataDir, rigName, ".dolt")
	_, err := os.Stat(doltDir)
	return err == nil
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
