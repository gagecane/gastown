package doltserver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/atomicfile"
)

// EnsureMetadata writes or updates the metadata.json for a rig's beads directory
// to include proper Dolt server configuration. This prevents the split-brain problem
// where bd falls back to local embedded databases instead of connecting to the
// centralized Dolt server.
//
// For the "hq" rig, it writes to <townRoot>/.beads/metadata.json.
// For other rigs, it writes to mayor/rig/.beads/metadata.json if that path exists,
// otherwise to <townRoot>/<rigName>/.beads/metadata.json.
// EnsureMetadata ensures that the .beads/metadata.json for a rig has correct
// Dolt server configuration.  rigName is the rig's directory name (e.g.
// "beads_el"). When dolt_database is absent the default is rigName, which is
// correct for rigs whose Dolt database name matches their directory name.
// Callers that know the rig uses a short DB prefix (e.g. "be" for "beads_el")
// should pass it as doltDatabase so metadata.json gets the right value.
func EnsureMetadata(townRoot, rigName string, doltDatabase ...string) error {
	// Determine the Dolt database name to write when the field is absent.
	// Default: rigName (correct when db-name == rig-dir-name, e.g. "gastown").
	// Callers from EnsureAllMetadata pass the actual DB prefix ("at", "be") so
	// that rigs with short prefixes get the correct database name, not the full
	// rig directory name.
	explicitDB := len(doltDatabase) > 0 && doltDatabase[0] != ""
	effectiveDB := rigName
	if explicitDB {
		effectiveDB = doltDatabase[0]
	}

	// Use FindOrCreateRigBeadsDir to atomically resolve and create the directory,
	// avoiding the TOCTOU race where the directory state changes between
	// FindRigBeadsDir's Stat check and our subsequent file operations.
	beadsDir, err := FindOrCreateRigBeadsDir(townRoot, rigName)
	if err != nil {
		return fmt.Errorf("resolving beads directory for rig %q: %w", rigName, err)
	}

	metadataPath := filepath.Join(beadsDir, "metadata.json")

	// Acquire per-path mutex for goroutine synchronization.
	// EnsureAllMetadata calls EnsureMetadata concurrently; flock (inter-process)
	// cannot reliably synchronize goroutines within the same process.
	mu := getMetadataMu(metadataPath)
	mu.Lock()
	defer mu.Unlock()

	// Load existing metadata if present (preserve any extra fields)
	existing := make(map[string]interface{})
	if data, err := os.ReadFile(metadataPath); err == nil {
		_ = json.Unmarshal(data, &existing) // best effort
	}

	// Resolve the authoritative server config (config.yaml > env > daemon.json > default).
	config := DefaultConfig(townRoot)

	// Patch dolt server fields. Only write when values actually change so tracked
	// metadata.json files in source repos stay clean.
	changed := false
	if existing["database"] != "dolt" {
		existing["database"] = "dolt"
		changed = true
	}
	if existing["backend"] != "dolt" {
		existing["backend"] = "dolt"
		changed = true
	}
	if existing["dolt_mode"] != "server" {
		existing["dolt_mode"] = "server"
		changed = true
	}
	// Fix wrong dolt_database values (not just empty). After a crash or rig
	// addition, metadata.json can end up pointing to the wrong database name
	// (e.g., "beads_gt" instead of "gastown"), causing PROJECT IDENTITY MISMATCH
	// errors that are hard to diagnose and recover from. (gas-tc4)
	if existing["dolt_database"] == nil || existing["dolt_database"] == "" {
		existing["dolt_database"] = effectiveDB
		changed = true
	} else if dbStr, ok := existing["dolt_database"].(string); ok && dbStr != effectiveDB {
		// The existing value differs from what we'd write. When the caller
		// provided an explicit dbName (from EnsureAllMetadata, which resolves
		// the canonical name from rigs.json), always correct. When no explicit
		// dbName was given (effectiveDB == rigName), only correct if the
		// existing value is not a real database — this prevents flip-flop
		// between "at" and "atomize" when two code paths disagree. (gt-9c4)
		if explicitDB || !DatabaseExists(townRoot, dbStr) {
			fmt.Fprintf(os.Stderr, "Warning: metadata.json dolt_database was %q, correcting to %q (identity mismatch repair)\n", dbStr, effectiveDB)
			existing["dolt_database"] = effectiveDB
			changed = true
		}
	}

	// Ensure server connection fields match the authoritative config.
	// bd reads dolt_server_host and dolt_server_port from metadata.json to
	// connect to the Dolt server. Stale values (e.g., port 13729 from a
	// previous bd init) cause "connection refused" errors.
	wantHost := config.EffectiveHost()
	wantPort := float64(config.Port) // JSON numbers are float64
	if existing["dolt_server_host"] != wantHost {
		existing["dolt_server_host"] = wantHost
		changed = true
	}
	if existing["dolt_server_port"] != wantPort {
		existing["dolt_server_port"] = wantPort
		changed = true
	}

	// Fast path: avoid rewriting metadata.json when already correct.
	if !changed {
		return nil
	}

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling metadata: %w", err)
	}

	if err := atomicfile.WriteFile(metadataPath, append(data, '\n'), 0600); err != nil {
		return fmt.Errorf("writing metadata.json: %w", err)
	}

	return nil
}

// EnsureAllMetadata updates metadata.json for all rig databases known to the
// Dolt server. This is the fix for the split-brain problem where worktrees
// each have their own isolated database.
//
// For rigs that use a short DB prefix (e.g. database "be" for the "beads_el"
// rig), EnsureAllMetadata resolves the rig name from rigs.json and writes the
// correct dolt_database value ("be") so that convoy event polling connects to
// the right database instead of a non-existent "beads_el" database.
func EnsureAllMetadata(townRoot string) (updated []string, errs []error) {
	databases, err := ListDatabases(townRoot)
	if err != nil {
		return nil, []error{fmt.Errorf("listing databases: %w", err)}
	}

	// Map from DB prefix to rig directory name, e.g. "be" -> "beads_el".
	// Merge routes.jsonl (routes) and rigs.json (prefixes); rigs.json wins on
	// conflict. Rigs where db-name == rig-dir-name are not in this map and fall
	// through to the default behavior (rigName = dbName).
	dbToRig := buildDatabaseToRigMap(townRoot)
	for k, v := range buildRigPrefixMap(townRoot) {
		dbToRig[k] = v
	}

	// Group candidate database names by rig. When routes.jsonl and rigs.json
	// use different prefixes for the same rig (e.g. "gas" vs "gt" both map to
	// "gastown"), multiple databases may exist for the same rig. Processing
	// them all causes oscillation: each one overwrites the other's
	// dolt_database correction on every startup. (gas-ar0)
	rigCandidates := make(map[string][]string) // rig -> candidate db names
	for _, dbName := range databases {
		rigName := dbName
		if mapped, ok := dbToRig[dbName]; ok {
			rigName = mapped
		}
		if dbName == "hq" {
			rigName = "hq"
		}
		rigCandidates[rigName] = append(rigCandidates[rigName], dbName)
	}

	for rigName, candidates := range rigCandidates {
		// When multiple databases map to the same rig, choose one effective
		// DB name: prefer whatever is already in metadata.json (if it's among
		// the valid candidates) to avoid spurious mismatch warnings. Fall back
		// to the first candidate (alphabetical, from os.ReadDir ordering).
		dbName := candidates[0]
		if len(candidates) > 1 {
			dbName = pickDBForRig(townRoot, rigName, candidates)
		}
		// Pass dbName explicitly so EnsureMetadata writes the correct
		// dolt_database value ("be") rather than the rig dir name ("beads_el").
		if err := EnsureMetadata(townRoot, rigName, dbName); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", dbName, err))
		} else {
			updated = append(updated, dbName)
		}
	}

	return updated, errs
}
