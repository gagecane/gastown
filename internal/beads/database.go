package beads

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var bdTargetEnvKeys = []string{
	"BEADS_DIR",
	"BEADS_DB",
	"BD_DB",
	"BEADS_DOLT_DATA_DIR",
	"BEADS_DOLT_HOST",
	"BEADS_DOLT_PORT",
	"BEADS_DOLT_SERVER_DATABASE",
	"BEADS_DOLT_SERVER_HOST",
	"BEADS_DOLT_SERVER_PORT",
	"BEADS_DOLT_SERVER_SOCKET",
	"BEADS_DOLT_SERVER_MODE",
	"BEADS_DOLT_SHARED_SERVER",
	"BEADS_SHARED_SERVER_DIR",
}

// DatabaseNameFromMetadata reads the dolt_database field from .beads/metadata.json.
// Returns empty string if metadata doesn't exist or has no database configured.
func DatabaseNameFromMetadata(beadsDir string) string {
	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		return ""
	}
	var meta struct {
		DoltDatabase string `json:"dolt_database"`
	}
	if json.Unmarshal(data, &meta) != nil {
		return ""
	}
	return meta.DoltDatabase
}

// DatabaseEnv returns the BEADS_DOLT_SERVER_DATABASE=<name> env var string
// for the given beadsDir, or empty string if no database is configured.
func DatabaseEnv(beadsDir string) string {
	db := DatabaseNameFromMetadata(beadsDir)
	if db == "" {
		return ""
	}
	return "BEADS_DOLT_SERVER_DATABASE=" + db
}

// StripBDTargetEnv removes inherited environment variables that can make a bd
// subprocess select a database/server other than the .beads directory chosen by
// Gas Town. It intentionally preserves BEADS_DOLT_AUTO_START so callers can keep
// the shared-server guardrail enabled.
func StripBDTargetEnv(env []string) []string {
	filtered := env
	for _, key := range bdTargetEnvKeys {
		filtered = stripEnvKey(filtered, key)
	}
	return filtered
}

// BuildPinnedBDEnv returns env for a bd subprocess pinned to beadsDir. BEADS_DIR
// is the authoritative database selector; inherited explicit database selectors
// are stripped because bd 1.0.x can resolve a different/stale view when both are
// present, making newly-created routed beads invisible to gt hook/sling.
func BuildPinnedBDEnv(base []string, beadsDir string) []string {
	env := SuppressBDSideEffects(StripBDTargetEnv(base))
	if beadsDir == "" {
		return PreventTestDoltLeak(addGTDerivedDoltTargetEnv(env), "")
	}
	env = append(env, "BEADS_DIR="+beadsDir)
	env = append(env, doltTargetEnvFromBeadsDir(beadsDir)...)
	return PreventTestDoltLeak(addGTDerivedDoltTargetEnv(env), beadsDir)
}

// BuildRoutingBDEnv returns env for a bd subprocess that intentionally relies on
// bd prefix routing. It strips stale target/database selectors, then re-adds only
// connection host/port from fallbackBeadsDir so routing can choose the database.
func BuildRoutingBDEnv(base []string, fallbackBeadsDir string) []string {
	env := SuppressBDSideEffects(StripBDTargetEnv(base))
	env = append(env, doltTargetEnvFromBeadsDir(fallbackBeadsDir)...)
	return PreventTestDoltLeak(addGTDerivedDoltTargetEnv(env), fallbackBeadsDir)
}

// BuildReadOnlyPinnedBDEnv returns env for a read-only bd subprocess pinned to
// beadsDir. It strips any inherited write/read mode before forcing read-only.
func BuildReadOnlyPinnedBDEnv(base []string, beadsDir string) []string {
	return forceBDReadOnly(BuildPinnedBDEnv(base, beadsDir))
}

// BuildReadOnlyRoutingBDEnv returns env for a read-only bd subprocess that uses
// bd prefix routing instead of pinning BEADS_DIR.
func BuildReadOnlyRoutingBDEnv(base []string, fallbackBeadsDir string) []string {
	return forceBDReadOnly(BuildRoutingBDEnv(base, fallbackBeadsDir))
}

// BuildMutationPinnedBDEnv returns env for a mutating bd subprocess pinned to
// beadsDir. It removes inherited read-only/auto-commit mode and forces commit-on
// so writes do not get stranded in a daemon or status-line subprocess context.
func BuildMutationPinnedBDEnv(base []string, beadsDir string) []string {
	return forceBDMutation(BuildPinnedBDEnv(base, beadsDir))
}

// BuildMutationRoutingBDEnv returns env for a mutating bd subprocess that uses
// bd prefix routing instead of pinning BEADS_DIR.
func BuildMutationRoutingBDEnv(base []string, fallbackBeadsDir string) []string {
	return forceBDMutation(BuildRoutingBDEnv(base, fallbackBeadsDir))
}

// BuildMutationNeutralBDEnv returns env for a mutating bd subprocess whose argv
// contains an explicit native target such as --repo=<path>. It strips inherited
// Gas Town target selectors and suppresses side effects without adding BEADS_DIR
// or town Dolt connection metadata that could change native bd path semantics.
func BuildMutationNeutralBDEnv(base []string) []string {
	return forceBDMutation(SuppressBDSideEffects(StripBDTargetEnv(base)))
}

// ArgsAreReadOnly classifies bd CLI arguments for env policy. Unknown commands
// are treated as mutations so they cannot accidentally inherit read-only mode.
func ArgsAreReadOnly(args []string) bool {
	args = stripBDGlobalFlags(args)
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "show", "list", "ready", "blocked", "stats", "stale", "orphans", "activity", "query", "version":
		return true
	case "dep":
		return len(args) > 1 && args[1] == "list"
	case "mol":
		return len(args) > 2 && args[1] == "wisp" && args[2] == "list"
	case "sql":
		query := strings.ToLower(strings.Join(stripBDCommandFlags(args[1:]), " "))
		return strings.HasPrefix(strings.TrimSpace(query), "select")
	case "config":
		return len(args) > 1 && args[1] == "get"
	default:
		return false
	}
}

func stripBDGlobalFlags(args []string) []string {
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		args = args[1:]
	}
	return args
}

func stripBDCommandFlags(args []string) []string {
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		args = args[1:]
	}
	return args
}

// SuppressBDSideEffects disables Beads JSONL export/backup/push side effects for
// Gas Town-managed subprocesses. The authoritative data plane is Dolt; exporting
// JSONL from high-frequency gt callers re-invalidates Beads' import freshness
// checks and can create a self-feeding Dolt load loop.
func SuppressBDSideEffects(env []string) []string {
	for _, key := range []string{
		"BEADS_NO_AUTO_IMPORT",
		"BD_EXPORT_AUTO",
		"BD_BACKUP_ENABLED",
		"BD_DOLT_AUTO_PUSH",
		"BD_NO_PUSH",
		"BD_EXPORT_GIT_ADD",
		"BD_NO_GIT_OPS",
	} {
		env = stripEnvKey(env, key)
	}
	return append(env,
		"BEADS_NO_AUTO_IMPORT=1",
		"BD_EXPORT_AUTO=false",
		"BD_BACKUP_ENABLED=false",
		"BD_DOLT_AUTO_PUSH=false",
		"BD_NO_PUSH=true",
		"BD_EXPORT_GIT_ADD=false",
		"BD_NO_GIT_OPS=true",
	)
}

func forceBDReadOnly(env []string) []string {
	env = stripEnvKey(env, "BD_DOLT_AUTO_COMMIT")
	env = stripEnvKey(env, "BD_READONLY")
	return append(env, "BD_DOLT_AUTO_COMMIT=off", "BD_READONLY=true")
}

func forceBDMutation(env []string) []string {
	env = stripEnvKey(env, "BD_DOLT_AUTO_COMMIT")
	env = stripEnvKey(env, "BD_READONLY")
	return append(env, "BD_DOLT_AUTO_COMMIT=on")
}

func doltTargetEnvFromBeadsDir(beadsDir string) []string {
	if beadsDir == "" {
		return nil
	}
	meta := readDoltMetadata(beadsDir)
	var env []string
	// Only set BEADS_DOLT_DATA_DIR when:
	//   - the rig is NOT configured for server mode (no Host/Port in metadata), AND
	//   - the data dir actually exists on disk.
	// bd v1.0.3+ honors BEADS_DOLT_DATA_DIR even when server mode is selected,
	// and uses it as a database lookup root that overrides the server-side
	// DB. With our shared-server topology, the town-level .dolt-data either
	// doesn't exist or maps to a stale embedded DB — pointing bd at it makes
	// rig-prefixed bead lookups fail with "not found" (gu-6a68).
	if meta.Host == "" && meta.Port == "" {
		if townRoot := FindTownRoot(filepath.Dir(beadsDir)); townRoot != "" {
			dataDir := filepath.Join(townRoot, ".dolt-data")
			if _, err := os.Stat(dataDir); err == nil {
				env = append(env, "BEADS_DOLT_DATA_DIR="+dataDir)
			}
		}
	}
	if meta.Host != "" {
		env = append(env, "BEADS_DOLT_SERVER_HOST="+meta.Host)
	}
	if meta.Port != "" {
		env = append(env, "BEADS_DOLT_SERVER_PORT="+meta.Port)
		env = append(env, "BEADS_DOLT_PORT="+meta.Port)
	}
	return env
}

type doltMetadata struct {
	Host string
	Port string
}

func readDoltMetadata(beadsDir string) doltMetadata {
	var meta doltMetadata
	if data, err := os.ReadFile(filepath.Join(beadsDir, "dolt-server.port")); err == nil {
		meta.Port = strings.TrimSpace(string(data))
	}
	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		return meta
	}
	var raw struct {
		DoltServerHost string `json:"dolt_server_host"`
		DoltServerPort int    `json:"dolt_server_port"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return meta
	}
	meta.Host = strings.TrimSpace(raw.DoltServerHost)
	if meta.Port == "" && raw.DoltServerPort > 0 {
		meta.Port = strconv.Itoa(raw.DoltServerPort)
	}
	return meta
}

func stripEnvKey(env []string, key string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func addGTDerivedDoltTargetEnv(env []string) []string {
	gtHost := envValue(env, "GT_DOLT_HOST")
	gtPort := envValue(env, "GT_DOLT_PORT")
	if gtHost != "" && envValue(env, "BEADS_DOLT_SERVER_HOST") == "" {
		env = append(env, "BEADS_DOLT_SERVER_HOST="+gtHost)
	}
	if gtPort != "" {
		if envValue(env, "BEADS_DOLT_SERVER_PORT") == "" {
			env = append(env, "BEADS_DOLT_SERVER_PORT="+gtPort)
		}
		if envValue(env, "BEADS_DOLT_PORT") == "" {
			env = append(env, "BEADS_DOLT_PORT="+gtPort)
		}
	}
	return env
}

// PreventTestDoltLeak stops a bd subprocess from polluting the shared
// production Dolt server during `go test`. When bd has no Dolt target at all, it
// falls back to its built-in default: connect to its default server and use a
// non-namespaced database literally named "beads". A test fixture built against
// a bare temp dir (no metadata, no server selector) therefore leaks an orphan
// "beads" database onto whatever server bd reaches — the production 3307 server
// when an agent runs the test suite as a gate — recurring every run and tripping
// deacon's safety gate (gs-7v3). Dozens of unit tests across ~10 packages
// construct beads wrappers against bare temp dirs, so guarding at this single env
// boundary covers them all rather than re-plumbing each (mirrors the
// testing.Testing() boundary fix in internal/tmux/sendkeys.go, gu-l8zp).
//
// This only fires under testing.Testing() (false in production binaries), and
// only when bd has no database AND no server/data-dir target — i.e. a bare test
// fixture. A configured database (real rigs always record dolt_database,
// preserving gu-6a68), an explicitly selected server, or a container-backed
// integration test (CI sets GT_DOLT_PORT to the container port) are all left
// untouched. When it fires it points bd at an isolated embedded data dir under
// beadsDir (within the test's temp tree, discarded with it) so bd uses local
// storage instead of the default server.
func PreventTestDoltLeak(env []string, beadsDir string) []string {
	// Only a non-empty beadsDir is a candidate for the bare-fixture leak. An
	// empty beadsDir means bd routing by bead-id prefix (e.g. mail delivery),
	// which legitimately needs the configured server and routes by prefix rather
	// than the default "beads" database — leave it alone.
	if !testing.Testing() || beadsDir == "" {
		return env
	}
	// A real database resolves from metadata — respect the selected server and
	// database; bd will not fall back to the default "beads" database.
	if DatabaseNameFromMetadata(beadsDir) != "" {
		return env
	}
	// A Dolt target is already selected — an explicit metadata server, an
	// isolated test server/container (CI sets GT_DOLT_PORT to the container
	// port), or an embedded data dir. Respect it; the leak only occurs when bd
	// has no target at all and falls back to its built-in default. Critically,
	// this leaves container-backed integration tests untouched — their port is
	// indistinguishable from a live town's, so we must not strip it.
	for _, key := range []string{
		"BEADS_DOLT_SERVER_HOST",
		"BEADS_DOLT_SERVER_PORT",
		"BEADS_DOLT_PORT",
		"GT_DOLT_PORT",
		"BEADS_DOLT_DATA_DIR",
	} {
		if envValue(env, key) != "" {
			return env
		}
	}
	// No target at all: bd would connect to its built-in default server with the
	// default "beads" database, leaking an orphan onto whatever server it reaches
	// (the production 3307 server when an agent runs `go test` as a gate). Pin an
	// isolated embedded data dir under the fixture's temp tree instead.
	return append(env, "BEADS_DOLT_DATA_DIR="+filepath.Join(beadsDir, ".dolt-data"))
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}
