// Package doltserver manages the Dolt SQL server for Gas Town.
//
// The Dolt server provides multi-client access to beads databases,
// avoiding the single-writer limitation of embedded Dolt mode.
//
// Server configuration:
//   - Port: 3307 (avoids conflict with MySQL on 3306)
//   - User: root (default Dolt user, no password for localhost)
//   - Data directory: ~/gt/.dolt-data/ (contains all rig databases)
//
// Each rig (hq, gastown, beads) has its own database subdirectory:
//
//	~/gt/.dolt-data/
//	├── hq/        # Town beads (hq-*)
//	├── gastown/   # Gastown rig (gt-*)
//	├── beads/     # Beads rig (bd-*)
//	└── ...        # Other rigs
//
// Usage:
//
//	gt dolt start           # Start the server
//	gt dolt stop            # Stop the server
//	gt dolt status          # Check server status
//	gt dolt logs            # View server logs
//	gt dolt sql             # Open SQL shell
//	gt dolt init-rig <name> # Initialize a new rig database
//
// The package is split across several files. This file owns the Dolt
// identity bootstrapper and the shared Config type. Sibling files:
//
//	state.go       State struct, persistence, countDoltDatabases
//	status.go      IsRunning / reachability / listener discovery
//	process.go     PID ownership verification, imposter detection
//	port.go        Port availability checks
//	lifecycle.go   Start / Stop / writeServerConfig / connection strings
//	databases.go   ListDatabases / VerifyDatabases / DatabaseExists / RemoveDatabase
//	init.go        InitRig
//	migrate.go     Migration discovery and legacy bd → .dolt-data moves
//	workspace.go   BrokenWorkspace / OrphanedDatabase / repair
//	metadata.go    metadata.json management (EnsureMetadata, beads-dir lookup)
//	health.go      Health metrics, read-only detection and recovery
//	sql.go         dolt sql wrappers (serverExecSQL, doltSQL*, retries)
//	fsutil.go      Low-level filesystem helpers (dirSize, formatBytes, moveDir)
//	dolthub.go     DoltHub sync (separate subsystem)
//	rollback.go    Snapshot rollback (separate subsystem)
//	sync.go        Peer-to-peer sync (separate subsystem)
//	wisps_migrate.go  Wisp schema migration (separate subsystem)
//	wl_*.go           Write-leader subsystem
package doltserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	"gopkg.in/yaml.v3"
)

// EnsureDoltIdentity configures dolt global identity (user.name, user.email)
// if not already set. Copies values from git config as a sensible default.
// This must run before InitRig and Start, since dolt init requires identity.
func EnsureDoltIdentity() error {
	// Check each field independently to avoid creating duplicates with --add.
	// Distinguish "key not found" (exit code 1, empty output) from dolt crashes.
	needName, err := doltConfigMissing("user.name")
	if err != nil {
		return fmt.Errorf("probing dolt user.name: %w", err)
	}
	needEmail, err := doltConfigMissing("user.email")
	if err != nil {
		return fmt.Errorf("probing dolt user.email: %w", err)
	}

	if !needName && !needEmail {
		return nil // already configured
	}

	// Copy missing fields from git global config.
	// We read --global only (not repo-local) to avoid silently persisting
	// a repo-scoped override into dolt's permanent global config.
	if needName {
		nameCmd := exec.Command("git", "config", "--global", "user.name")
		setProcessGroup(nameCmd)
		gitName, err := nameCmd.Output()
		if err != nil || len(bytes.TrimSpace(gitName)) == 0 {
			return fmt.Errorf("dolt identity not configured and git user.name not available; run: dolt config --global --add user.name \"Your Name\"")
		}
		if err := setDoltGlobalConfig("user.name", strings.TrimSpace(string(gitName))); err != nil {
			return fmt.Errorf("failed to set dolt user.name: %w", err)
		}
	}

	if needEmail {
		emailCmd := exec.Command("git", "config", "--global", "user.email")
		setProcessGroup(emailCmd)
		gitEmail, err := emailCmd.Output()
		if err != nil || len(bytes.TrimSpace(gitEmail)) == 0 {
			return fmt.Errorf("dolt identity not configured and git user.email not available; run: dolt config --global --add user.email \"you@example.com\"")
		}
		if err := setDoltGlobalConfig("user.email", strings.TrimSpace(string(gitEmail))); err != nil {
			return fmt.Errorf("failed to set dolt user.email: %w", err)
		}
	}

	return nil
}

// doltConfigMissing checks whether a dolt global config key is unset.
// Returns (true, nil) for missing keys, (false, nil) for present keys,
// and (false, error) when dolt itself fails unexpectedly.
func doltConfigMissing(key string) (bool, error) {
	cmd := exec.Command("dolt", "config", "--global", "--get", key)
	setProcessGroup(cmd)
	out, err := cmd.Output()
	if err == nil {
		// Command succeeded — key exists if output is non-empty
		return len(bytes.TrimSpace(out)) == 0, nil
	}
	// dolt config --get exits 1 for missing keys with no stderr.
	// Any other failure (crash, permission error) is unexpected.
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return true, nil // key not found — expected
	}
	return false, fmt.Errorf("dolt config --global --get %s: %w", key, err)
}

// setDoltGlobalConfig idempotently sets a dolt global config key.
// Uses --unset then --add to avoid duplicate entries from repeated calls.
func setDoltGlobalConfig(key, value string) error {
	// Remove existing value (ignore error — key may not exist yet)
	unsetCmd := exec.Command("dolt", "config", "--global", "--unset", key)
	setProcessGroup(unsetCmd)
	_ = unsetCmd.Run()
	addCmd := exec.Command("dolt", "config", "--global", "--add", key, value)
	setProcessGroup(addCmd)
	return addCmd.Run()
}

// Default configuration
const (
	DefaultPort           = 3307
	DefaultUser           = "root" // Default Dolt user (no password for local access)
	DefaultMaxConnections = 1000   // Dolt default; no reason to limit below (Tim Sehn confirmed 1k is fine)

	// DefaultReadTimeoutMs is the server-side timeout for reading a complete request from a client.
	// Controls how long Dolt waits for a client to send a query on an idle connection.
	// Prevents CLOSE_WAIT accumulation from abandoned connections: when a client times out
	// and closes its end, Dolt will detect the dead connection within this window.
	// 5 minutes matches the compactor GC timeout (compactorGCTimeout) so GC ops complete
	// before the connection is considered stale.
	DefaultReadTimeoutMs = 5 * 60 * 1000 // 5 minutes in milliseconds

	// DefaultWriteTimeoutMs is the server-side timeout for writing a response back to a client.
	// When a client closes its TCP connection while a query is running (e.g. compactor GC),
	// Dolt detects the dead connection within this timeout rather than holding CLOSE_WAIT
	// for Dolt's default 8 hours. Set to match compactor GC timeout.
	DefaultWriteTimeoutMs = 5 * 60 * 1000 // 5 minutes in milliseconds
)

// doltConfigYAML represents the subset of Dolt's config.yaml that we need to read.
type doltConfigYAML struct {
	Listener struct {
		Port int `yaml:"port"`
	} `yaml:"listener"`
}

// readPortFromConfigYAML reads the port from .dolt-data/config.yaml if it exists.
// Returns the configured port, or 0 if the file doesn't exist or doesn't specify a port.
func readPortFromConfigYAML(townRoot string) int {
	configPath := filepath.Join(townRoot, ".dolt-data", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return 0 // File doesn't exist or can't be read
	}

	var cfg doltConfigYAML
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return 0 // Invalid YAML or doesn't match structure
	}

	return cfg.Listener.Port // 0 if not specified
}

// Config holds Dolt server configuration.
type Config struct {
	// TownRoot is the Gas Town workspace root.
	TownRoot string

	// Host is the Dolt server hostname or IP.
	// Empty means localhost (backward-compatible default).
	Host string

	// Port is the MySQL protocol port.
	Port int

	// User is the MySQL user name.
	User string

	// Password is the MySQL password.
	// Empty means no password (backward-compatible default for local access).
	Password string

	// DataDir is the root directory containing all rig databases.
	// Each subdirectory is a separate database that will be served.
	DataDir string

	// LogFile is the path to the server log file.
	LogFile string

	// PidFile is the path to the PID file.
	PidFile string

	// MaxConnections is the maximum number of simultaneous connections the server will accept.
	// Set to 0 to use the Dolt default (1000). Gas Town defaults to 50 to prevent
	// connection storms during mass polecat slings.
	MaxConnections int

	// ReadTimeoutMs is the server-side read timeout in milliseconds.
	// Controls how long Dolt waits for a client to send a request on an idle connection.
	// Prevents abandoned connections from staying in CLOSE_WAIT indefinitely.
	// Set to 0 to use Dolt's default (28800000 = 8 hours — strongly discouraged).
	ReadTimeoutMs int

	// WriteTimeoutMs is the server-side write timeout in milliseconds.
	// Controls how long Dolt waits to write response data back to a client.
	// When a client closes its TCP connection while a query is running, Dolt
	// detects the dead connection within WriteTimeoutMs instead of holding it
	// open for up to 8 hours (Dolt default).
	// Must be >= the longest expected query (e.g., compactor GC at 5 minutes).
	// Set to 0 to use Dolt's default (28800000 = 8 hours — strongly discouraged).
	WriteTimeoutMs int

	// LogLevel is the Dolt server log level (trace, debug, info, warning, error, fatal).
	// Default is "warning" to suppress connection open/close noise. Override with
	// GT_DOLT_LOGLEVEL=info (or debug) for diagnostics.
	LogLevel string
}

// DefaultConfig returns the default Dolt server configuration.
//
// Port priority (highest to lowest):
//  1. .dolt-data/config.yaml listener.port (authoritative file-based config)
//  2. GT_DOLT_PORT environment variable (for overrides)
//  3. DefaultPort (3307)
//
// This ordering prevents stale environment variables in long-running sessions
// from overriding the intended configuration.
//
// Other environment variables:
//   - GT_DOLT_HOST → Host
//   - GT_DOLT_USER → User
//   - GT_DOLT_PASSWORD → Password
//   - GT_DOLT_LOGLEVEL → LogLevel (trace, debug, info, warning, error, fatal)
func DefaultConfig(townRoot string) *Config {
	daemonDir := filepath.Join(townRoot, "daemon")
	config := &Config{
		TownRoot:       townRoot,
		Port:           DefaultPort,
		User:           DefaultUser,
		DataDir:        filepath.Join(townRoot, ".dolt-data"),
		LogFile:        filepath.Join(daemonDir, "dolt.log"),
		PidFile:        filepath.Join(daemonDir, "dolt.pid"),
		MaxConnections: DefaultMaxConnections,
		ReadTimeoutMs:  DefaultReadTimeoutMs,
		WriteTimeoutMs: DefaultWriteTimeoutMs,
		LogLevel:       "warning",
	}

	if h := os.Getenv("GT_DOLT_HOST"); h != "" {
		config.Host = h
	}

	// Port precedence: config.yaml > env var > default
	// config.yaml takes precedence to prevent stale env var pollution
	if port := readPortFromConfigYAML(townRoot); port > 0 {
		config.Port = port
	} else if p := os.Getenv("GT_DOLT_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil {
			config.Port = port
		}
	}

	if u := os.Getenv("GT_DOLT_USER"); u != "" {
		config.User = u
	}
	if pw := os.Getenv("GT_DOLT_PASSWORD"); pw != "" {
		config.Password = pw
	}
	if ll := os.Getenv("GT_DOLT_LOGLEVEL"); ll != "" {
		config.LogLevel = ll
	} else if townRoot != "" {
		// Fallback: read GT_DOLT_LOGLEVEL from daemon/daemon.env so the log
		// level survives daemon-triggered Dolt restarts (gt-zb8). The daemon
		// process may not have GT_DOLT_LOGLEVEL in its own environment when it
		// was started before the manual env var was applied.
		if ll := readDaemonEnvVar(filepath.Join(townRoot, "daemon", "daemon.env"), "GT_DOLT_LOGLEVEL"); ll != "" {
			config.LogLevel = ll
		}
	}

	// Fallback: if GT_DOLT_PORT is not in the shell env, read it from
	// mayor/daemon.json. Commands like gt dolt status, gt dolt stop, etc.
	// are typically run without the daemon.json env vars exported to the
	// shell, so DefaultConfig would otherwise return the wrong port (3307)
	// when the town uses a custom port (e.g. GT_DOLT_PORT=3308).
	// We cannot import the daemon package here (circular: daemon→doltserver),
	// so we parse the minimal JSON structure directly.
	if os.Getenv("GT_DOLT_PORT") == "" && townRoot != "" {
		daemonJSONPath := filepath.Join(townRoot, "mayor", "daemon.json")
		if data, err := os.ReadFile(daemonJSONPath); err == nil {
			var daemonEnv struct {
				Env map[string]string `json:"env"`
			}
			if err := json.Unmarshal(data, &daemonEnv); err == nil {
				if v, ok := daemonEnv.Env["GT_DOLT_PORT"]; ok {
					if port, err := strconv.Atoi(v); err == nil {
						config.Port = port
					}
				}
			}
		}
	}

	// Default to warning logging. Use GT_DOLT_LOGLEVEL=info or =debug for diagnostics.
	if config.LogLevel == "" {
		config.LogLevel = "warning"
	}

	return config
}

// readDaemonEnvVar reads a single key=value variable from a simple env file.
// Handles blank lines and # comments; returns "" if not found or on error.
func readDaemonEnvVar(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	prefix := key + "="
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

// IsRemote returns true when the config points to a non-local Dolt server.
// Empty host, "127.0.0.1", "localhost", "::1", and "[::1]" are all considered local.
// Hostnames that resolve to a loopback address are also treated as local.
func (c *Config) IsRemote() bool {
	switch strings.ToLower(c.Host) {
	case "", "127.0.0.1", "localhost", "::1", "[::1]":
		return false
	}
	// Resolve hostname and check if it points to loopback.
	addrs, err := net.LookupHost(c.Host)
	if err != nil {
		return true
	}
	for _, addr := range addrs {
		if ip := net.ParseIP(addr); ip != nil && ip.IsLoopback() {
			return false
		}
	}
	return true
}

// SQLArgs returns the dolt CLI flags needed to connect to a remote server.
// Returns nil for local servers (dolt auto-detects the running local server).
func (c *Config) SQLArgs() []string {
	if !c.IsRemote() {
		return nil
	}
	return []string{
		"--host", c.Host,
		"--port", strconv.Itoa(c.Port),
		"--user", c.User,
		"--no-tls",
	}
}

// userDSN returns the user[:password] portion of a MySQL DSN.
func (c *Config) userDSN() string {
	if c.Password != "" {
		return c.User + ":" + c.Password
	}
	return c.User
}

// EffectiveHost returns the configured host, defaulting to "127.0.0.1" when empty.
func (c *Config) EffectiveHost() string {
	if c.Host == "" {
		return "127.0.0.1"
	}
	return c.Host
}

// HostPort returns "host:port", defaulting host to "127.0.0.1" when empty.
func (c *Config) HostPort() string {
	host := c.Host
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("%s:%d", host, c.Port)
}

// buildDoltSQLCmd constructs a non-interactive dolt sql command that always
// talks to the running SQL server over TCP.
//
// For local servers, this avoids embedded-mode auto-discovery, which can load
// databases relative to cmd.Dir instead of querying the live shared server.
func buildDoltSQLCmd(ctx context.Context, config *Config, args ...string) *exec.Cmd {
	fullArgs := make([]string, 0, 8+len(args))
	fullArgs = append(fullArgs,
		"--host", config.EffectiveHost(),
		"--port", strconv.Itoa(config.Port),
		"--user", config.User,
		"--no-tls",
		"sql",
	)
	fullArgs = append(fullArgs, args...)

	cmd := exec.CommandContext(ctx, "dolt", fullArgs...)

	// GH#2537: Always set cmd.Dir to prevent dolt from creating stray
	// .doltcfg/privileges.db files in the caller's CWD. Even TCP client
	// connections can trigger .doltcfg creation if CWD is uncontrolled.
	cmd.Dir = config.DataDir
	setProcessGroup(cmd)

	// Always set DOLT_CLI_PASSWORD to suppress interactive prompts.
	// When empty, dolt connects without a password, which is the local default.
	cmd.Env = append(os.Environ(), "DOLT_CLI_PASSWORD="+config.Password)

	return cmd
}

// RigDatabaseDir returns the database directory for a specific rig.
func RigDatabaseDir(townRoot, rigName string) string {
	config := DefaultConfig(townRoot)
	return filepath.Join(config.DataDir, rigName)
}
