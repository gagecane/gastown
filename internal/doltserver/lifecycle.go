package doltserver

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// writeServerConfig writes a managed Dolt config.yaml from the Config struct.
// This ensures all required settings (especially connection timeouts) are always
// present when the server starts. The file is overwritten on each start to prevent
// configuration drift.
func writeServerConfig(config *Config, configPath string) error {
	// Build the listener host entry. Omit it when empty to use Dolt's default
	// (binds to all interfaces), which is the backward-compatible behavior.
	hostLine := ""
	if config.Host != "" {
		hostLine = fmt.Sprintf("\n  host: %s", config.Host)
	}

	// Build timeout entries. Omit when 0 to use Dolt's defaults (not recommended).
	readTimeoutLine := ""
	if config.ReadTimeoutMs > 0 {
		readTimeoutLine = fmt.Sprintf("\n  read_timeout_millis: %d", config.ReadTimeoutMs)
	}
	writeTimeoutLine := ""
	if config.WriteTimeoutMs > 0 {
		writeTimeoutLine = fmt.Sprintf("\n  write_timeout_millis: %d", config.WriteTimeoutMs)
	}

	maxConnLine := ""
	if config.MaxConnections > 0 {
		maxConnLine = fmt.Sprintf("\n  max_connections: %d", config.MaxConnections)
	}

	content := fmt.Sprintf(`# Dolt SQL server configuration — managed by Gas Town (gt dolt start)
# Do not edit manually; changes are overwritten on each server start.
# To customize, set Gas Town environment variables:
#   GT_DOLT_PORT, GT_DOLT_HOST, GT_DOLT_USER, GT_DOLT_PASSWORD, GT_DOLT_LOGLEVEL

log_level: %s

listener:
  port: %d%s%s%s%s

data_dir: "%s"

behavior:
  dolt_transaction_commit: false
  auto_gc_behavior:
    enable: true
    archive_level: 1
`,
		config.LogLevel,
		config.Port,
		hostLine,
		maxConnLine,
		readTimeoutLine,
		writeTimeoutLine,
		filepath.ToSlash(config.DataDir),
	)

	return os.WriteFile(configPath, []byte(content), 0600)
}

// Start starts the Dolt SQL server.
func Start(townRoot string) error {
	config := DefaultConfig(townRoot)

	// Ensure daemon directory exists
	daemonDir := filepath.Dir(config.LogFile)
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		return fmt.Errorf("creating daemon directory: %w", err)
	}

	// Acquire exclusive lock to prevent concurrent starts (same pattern as gt daemon).
	// If the lock is held, retry briefly — the holder may be finishing up. If still
	// held after retries, check if the holding process is alive. (gt-tosjp)
	lockFile := filepath.Join(daemonDir, "dolt.lock")
	fileLock := flock.New(lockFile)
	locked, err := fileLock.TryLock()
	if err != nil {
		// Lock file may be corrupted — remove and retry once
		_ = os.Remove(lockFile)
		locked, err = fileLock.TryLock()
		if err != nil {
			return fmt.Errorf("acquiring lock: %w", err)
		}
	}
	if !locked {
		// Scale the retry window by the number of databases: each database takes
		// ~5s to initialize. Clamp between 15s and 120s to handle both small and
		// large installs. (gt-nkn: fix thundering herd)
		numDBs := countDoltDatabases(config.DataDir)
		lockTimeout := time.Duration(numDBs) * 5 * time.Second
		if lockTimeout < 15*time.Second {
			lockTimeout = 15 * time.Second
		}
		if lockTimeout > 120*time.Second {
			lockTimeout = 120 * time.Second
		}
		interval := 500 * time.Millisecond
		deadline := time.Now().Add(lockTimeout)
		for time.Now().Before(deadline) {
			time.Sleep(interval)
			locked, err = fileLock.TryLock()
			if err == nil && locked {
				break
			}
		}
		if !locked {
			// Still locked after the full timeout. Before force-removing the lock,
			// check if Dolt is already running — the lock holder may have finished
			// starting Dolt successfully. If so, return nil instead of spawning a
			// duplicate server. (gt-nkn: fix thundering herd)
			if already, _, _ := IsRunning(townRoot); already {
				return nil
			}
			// POSIX flocks auto-release on process death. We timed out waiting,
			// so forcibly remove the stale lock and retry once. (gt-tosjp)
			fmt.Fprintf(os.Stderr, "Warning: dolt.lock held for >%s — removing stale lock\n", lockTimeout.Round(time.Second))
			_ = os.Remove(lockFile)
			fileLock = flock.New(lockFile)
			locked, err = fileLock.TryLock()
			if err != nil || !locked {
				return fmt.Errorf("another gt dolt start is in progress (lock held after recovery attempt)")
			}
		}
	}
	defer func() { _ = fileLock.Unlock() }()

	// Stop idle-monitor processes first. These background processes auto-spawn
	// rogue Dolt servers and will immediately respawn an imposter if we kill
	// one without stopping the monitors. (gt-restart-race fix)
	if stopped := StopIdleMonitors(townRoot); stopped > 0 {
		fmt.Fprintf(os.Stderr, "Stopped %d idle-monitor process(es)\n", stopped)
		// Brief pause to let spawned rogue processes settle
		time.Sleep(200 * time.Millisecond)
	}

	// Check if already running (checks both PID file AND port)
	running, pid, err := IsRunning(townRoot)
	if err != nil {
		return fmt.Errorf("checking server status: %w", err)
	}

	// If IsRunning returns false, the port may still be held by a dolt process
	// that doesn't match this town's ownership (e.g., a leftover from an old
	// town setup started with different flags). IsRunning's ownership check
	// correctly returns false, but we need to evict the squatter before we can
	// bind the port. (fix: start-kills-unowned-port-holder)
	if !running {
		if squatterPID := findDoltServerOnPort(config.Port); squatterPID > 0 {
			fmt.Fprintf(os.Stderr, "Warning: port %d held by unowned dolt process (PID %d) — killing before start\n", config.Port, squatterPID)
			if proc, findErr := os.FindProcess(squatterPID); findErr == nil {
				_ = proc.Kill()
				if err := waitForPortRelease(config.Port, 5*time.Second); err != nil {
					// Kill didn't work, try again
					_ = proc.Kill()
					if err := waitForPortRelease(config.Port, 3*time.Second); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: port %d still occupied after killing PID %d: %v\n", config.Port, squatterPID, err)
					}
				}
			}
		}
	}

	if running {
		// If data directory doesn't exist, this is an orphaned server (e.g., user
		// deleted ~/gt and re-ran gt install). Kill it so we can start fresh.
		if _, statErr := os.Stat(config.DataDir); os.IsNotExist(statErr) {
			fmt.Fprintf(os.Stderr, "Warning: Dolt server (PID %d) is running but data directory %s does not exist — stopping orphaned server\n", pid, config.DataDir)
			if stopErr := Stop(townRoot); stopErr != nil {
				if pid > 0 {
					if proc, findErr := os.FindProcess(pid); findErr == nil {
						_ = proc.Kill()
						time.Sleep(100 * time.Millisecond)
					}
				}
			}
			// Fall through to start a new server
		} else {
			// Server is running with valid data dir — check if it's an imposter
			// (e.g., bd launched its own dolt server from a different data directory).
			legitimate, verifyErr := VerifyServerDataDir(townRoot)
			if verifyErr == nil && !legitimate {
				fmt.Fprintf(os.Stderr, "Warning: running Dolt server (PID %d) is an imposter — killing and restarting\n", pid)
				if killErr := KillImposters(townRoot); killErr != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to kill imposter: %v\n", killErr)
				}
				// Wait for port to be released, with retry
				if err := waitForPortRelease(config.Port, 5*time.Second); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: port %d still occupied after imposter kill: %v\n", config.Port, err)
				}
				// Fall through to start a new server
			} else if verifyErr != nil && !legitimate {
				// Verification failed but server is suspicious — log and try to kill
				fmt.Fprintf(os.Stderr, "Warning: could not verify Dolt server identity: %v — killing and restarting\n", verifyErr)
				if killErr := KillImposters(townRoot); killErr != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to kill imposter: %v\n", killErr)
				}
				if err := waitForPortRelease(config.Port, 5*time.Second); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: port %d still occupied after imposter kill: %v\n", config.Port, err)
				}
			} else {
				// Server is legitimate — verify PID file is correct (gm-ouur fix)
				// If PID file is stale/missing but server is on port, update it
				pidFromFile := 0
				if data, err := os.ReadFile(config.PidFile); err == nil {
					pidFromFile, _ = strconv.Atoi(strings.TrimSpace(string(data)))
				}
				if pidFromFile != pid {
					// PID file is stale/wrong - update it
					fmt.Printf("Updating stale PID file (was %d, actual %d)\n", pidFromFile, pid)
					if err := os.WriteFile(config.PidFile, []byte(strconv.Itoa(pid)), 0644); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: could not update PID file: %v\n", err)
					}
					// Update state too
					state, _ := LoadState(townRoot)
					if state != nil && state.PID != pid {
						state.PID = pid
						state.Running = true
						_ = SaveState(townRoot, state)
					}
				}
				return nil // already running and legitimate — idempotent success
			}
		}
	}

	// Ensure data directory exists
	if err := os.MkdirAll(config.DataDir, 0755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}

	// Quarantine corrupted/phantom database dirs before server launch.
	// WARNING: DO NOT remove, delete, or modify files inside Dolt's .dolt/
	// directory — including noms/LOCK files. These are Dolt-internal files.
	// Removing them WILL cause unrecoverable data corruption and data loss.
	// Dolt manages these files itself; external interference is never safe.
	//
	// Previously this section quarantined/removed database dirs with missing
	// noms/manifest and cleaned up stale .dolt/noms/LOCK files. Both operations
	// manipulated Dolt-internal state and risked data corruption. Dolt handles
	// its own lock files and database integrity on startup.

	databases, _ := ListDatabases(townRoot)

	// Open log file
	logFile, err := os.OpenFile(config.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("opening log file: %w", err)
	}

	// Remove stale Unix socket left behind by a previous Dolt crash.
	// Dolt creates /tmp/mysql.sock by default; if not cleaned up, the
	// next start emits "unix socket set up failed: file already in use"
	// and falls back to TCP-only. (GH#2687)
	cleanStaleDoltSocket()

	// Validate port is available before starting (catches multi-town port conflicts)
	if err := checkPortAvailable(config.Port); err != nil {
		logFile.Close()
		return err
	}

	// Clean stale Unix socket from prior crash. Dolt creates /tmp/mysql.sock by
	// default (or a port-specific variant). If the server crashed, the socket file
	// persists and Dolt warns "unix socket set up failed: file already in use".
	// Safe to remove: if a Dolt server were actually running, IsRunning() above
	// would have detected it and we'd have returned already. (gh-2687)
	socketPath := "/tmp/mysql.sock"
	if config.Port != 3306 {
		socketPath = fmt.Sprintf("/tmp/mysql.%d.sock", config.Port)
	}
	if _, statErr := os.Stat(socketPath); statErr == nil {
		fmt.Fprintf(os.Stderr, "Removing stale Unix socket: %s\n", socketPath)
		_ = os.Remove(socketPath)
	}

	// Always write a managed config.yaml from the Config struct before starting.
	// This ensures critical settings (especially read/write timeouts) are always
	// present, preventing CLOSE_WAIT accumulation from abandoned connections.
	// The config file uses --config so all settings come from this file; CLI flags
	// are ignored by dolt when --config is used.
	configPath := filepath.Join(config.DataDir, "config.yaml")
	if err := writeServerConfig(config, configPath); err != nil {
		logFile.Close()
		return fmt.Errorf("writing Dolt config: %w", err)
	}
	args := []string{"sql-server", "--config", configPath}
	cmd := exec.Command("dolt", args...)
	cmd.Dir = config.DataDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	// Detach from terminal and put dolt in its own process group so that
	// signals sent to the parent process group (e.g. SIGHUP when the caller
	// calls syscall.Exec to become tmux) don't reach the dolt server.
	cmd.Stdin = nil
	setProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		if closeErr := logFile.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close dolt log file: %v\n", closeErr)
		}
		return fmt.Errorf("starting Dolt server: %w", err)
	}

	// Close log file in parent (child has its own handle)
	if closeErr := logFile.Close(); closeErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to close dolt log file: %v\n", closeErr)
	}

	// Write PID file
	if err := os.WriteFile(config.PidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0644); err != nil {
		// Try to kill the process we just started
		_ = cmd.Process.Kill()
		return fmt.Errorf("writing PID file: %w", err)
	}

	// Save state
	state := &State{
		Running:   true,
		PID:       cmd.Process.Pid,
		Port:      config.Port,
		StartedAt: time.Now(),
		DataDir:   config.DataDir,
		Databases: databases,
	}
	if err := SaveState(townRoot, state); err != nil {
		// Non-fatal - server is still running
		fmt.Fprintf(os.Stderr, "Warning: failed to save state: %v\n", err)
	}

	// Wait for the server to be accepting connections, not just alive.
	// We check process liveness directly via signal(0) rather than calling
	// IsRunning, because IsRunning removes the PID file when the process is
	// alive but not yet listening — treating a starting-up process as stale.
	// On systems with slow storage (CSI/NFS), dolt can take 1-2s to bind its
	// port, well past the first 500ms check. By using cmd.Process.Signal(0)
	// we detect true process death without the PID-file side effect.
	//
	// The number of attempts scales with the database count: each database
	// adds ~1s of startup overhead (LevelDB compaction, stats loading, etc.).
	// We allow 5s per database so that workspaces with many rigs don't time
	// out before Dolt finishes initializing.
	dbCount := len(databases)
	if dbCount < 1 {
		dbCount = 1
	}
	maxAttempts := dbCount * 10 // 10 × 500ms = 5s per database
	var lastErr error
	tcpReachable := false
	for attempt := 0; attempt < maxAttempts; attempt++ {
		time.Sleep(500 * time.Millisecond)

		// Check if the process we started is still alive.
		if !processIsAlive(cmd.Process.Pid) {
			return fmt.Errorf("Dolt server process died during startup (check logs with 'gt dolt logs')")
		}

		if !tcpReachable {
			if err := CheckServerReachable(townRoot); err != nil {
				lastErr = err
				continue
			}
			tcpReachable = true
		}

		// TCP listener is up. Verify that the expected on-disk databases are
		// actually being served before declaring success. Without this check
		// Start() can return on the first iteration where Dolt has bound its
		// port but is still discovering/loading databases — leaving callers
		// (and waiting agents) connected to a server that only exposes
		// information_schema and mysql. Symptom: gt down + gt up cycle leaves
		// SHOW DATABASES showing no rig databases until the user manually
		// runs gt dolt stop + gt dolt start. (gt-nq1)
		if len(databases) == 0 {
			return nil // Nothing to verify — fresh install or empty data dir
		}
		_, missing, verifyErr := VerifyDatabases(townRoot)
		if verifyErr != nil {
			lastErr = fmt.Errorf("verifying databases: %w", verifyErr)
			continue
		}
		if len(missing) == 0 {
			return nil // Server is up and serving every expected database
		}
		lastErr = fmt.Errorf("server is reachable but %d/%d databases not yet served (missing: %v)",
			len(missing), len(databases), missing)
	}

	totalTimeout := time.Duration(dbCount) * 5 * time.Second
	if !tcpReachable {
		return fmt.Errorf("Dolt server process started (PID %d) but not accepting connections after %v (%d databases × 5s): %w\nCheck logs with: gt dolt logs", cmd.Process.Pid, totalTimeout, dbCount, lastErr)
	}
	return fmt.Errorf("Dolt server process started (PID %d) and is reachable, but databases failed to load after %v (%d databases × 5s): %w\nRecovery: gt dolt stop && gt dolt start\nCheck logs with: gt dolt logs", cmd.Process.Pid, totalTimeout, dbCount, lastErr)
}

// WARNING: DO NOT remove, delete, or modify files inside Dolt's .dolt/
// directory — including noms/LOCK files. These are Dolt-internal files.
// Removing them WILL cause unrecoverable data corruption and data loss.
// Dolt manages these files itself; external interference is never safe.
//
// cleanupStaleDoltLock previously removed stale .dolt/noms/LOCK files.
// This was unsafe — Dolt manages its own lock files on startup.

// DefaultDoltSocketPath is the default Unix socket Dolt creates.
const DefaultDoltSocketPath = "/tmp/mysql.sock"

// cleanStaleDoltSocket removes the default Unix socket file that Dolt creates
// at /tmp/mysql.sock. After a crash, this file lingers and prevents the next
// server start from binding the Unix socket, causing a warning and TCP-only
// fallback.
func cleanStaleDoltSocket() {
	cleanStaleSocket(DefaultDoltSocketPath)
}

// cleanStaleSocket removes a Unix socket file if it exists and no process
// currently holds it open.
func cleanStaleSocket(socketPath string) {
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return
	}

	// Check if any process holds the socket open
	cmd := exec.Command("lsof", socketPath)
	setProcessGroup(cmd)
	if err := cmd.Run(); err != nil {
		// lsof exit code 1 = no process holds it → stale, safe to remove
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			_ = os.Remove(socketPath)
		}
	}
	// If lsof succeeds (exit 0), a process is using it — leave it alone.
}

// drainConnectionsBeforeStop waits for active queries to complete before SIGTERM,
// reducing the nbs_manifest race window in Dolt's NomsBlockStore.Close() (gt-9bxzs).
//
// Dolt panics (Fatalf) when SIGTERM arrives while a goroutine is mid-write on an
// nbs_manifest temp file. By waiting until no queries are in-flight, we shrink
// the window where SIGTERM hits live storage I/O. Non-fatal: if the drain times
// out or the server is unreachable, we proceed with SIGTERM anyway.
func drainConnectionsBeforeStop(config *Config) {
	dsn := fmt.Sprintf("%s@tcp(%s:%d)/?timeout=3s&readTimeout=5s&writeTimeout=5s",
		config.User, config.EffectiveHost(), config.Port)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return
	}
	defer db.Close()
	db.SetConnMaxLifetime(5 * time.Second)
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Poll until only 1 connection remains (ours) or the drain window expires.
	// INFORMATION_SCHEMA.PROCESSLIST counts all server connections including ours.
	for {
		select {
		case <-ctx.Done():
			return // Drain window expired — proceed with SIGTERM
		default:
		}
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM INFORMATION_SCHEMA.PROCESSLIST").Scan(&count); err != nil {
			return // Server unreachable — proceed with SIGTERM
		}
		if count <= 1 {
			// Only our drain connection remains — safe to send SIGTERM
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Stop stops the Dolt SQL server.
// Works for both servers started via gt dolt start AND externally-started servers.
func Stop(townRoot string) error {
	config := DefaultConfig(townRoot)

	running, pid, err := IsRunning(townRoot)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("Dolt server is not running")
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding process: %w", err)
	}

	// Drain active connections before stopping to reduce the nbs_manifest
	// race window inside Dolt's NomsBlockStore.Close(). Non-fatal: proceeds even
	// if drain times out (10s max). Skipped for remote servers (no local PID).
	if !config.IsRemote() {
		drainConnectionsBeforeStop(config)
	}

	// Send termination signal for graceful shutdown (SIGTERM on Unix, Kill on Windows)
	if err := gracefulTerminate(process); err != nil {
		return fmt.Errorf("sending termination signal: %w", err)
	}

	// Wait for graceful shutdown (dolt needs more time)
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		if !processIsAlive(pid) {
			break
		}
	}

	// Check if still running
	if processIsAlive(pid) {
		// Still running, force kill
		_ = process.Kill()
		time.Sleep(100 * time.Millisecond)
	}

	// Clean up PID file
	_ = os.Remove(config.PidFile)

	// Update state - preserve historical info
	state, _ := LoadState(townRoot)
	if state == nil {
		state = &State{}
	}
	state.Running = false
	state.PID = 0
	_ = SaveState(townRoot, state)

	return nil
}

// GetConnectionString returns the MySQL connection string for the server.
// Use GetConnectionStringForRig for a specific database.
func GetConnectionString(townRoot string) string {
	config := DefaultConfig(townRoot)
	return fmt.Sprintf("%s@tcp(%s)/", config.displayDSN(), config.HostPort())
}

// GetConnectionStringForRig returns the MySQL connection string for a specific rig database.
func GetConnectionStringForRig(townRoot, rigName string) string {
	config := DefaultConfig(townRoot)
	return fmt.Sprintf("%s@tcp(%s)/%s", config.displayDSN(), config.HostPort(), rigName)
}

// displayDSN returns the user[:password] portion for display, masking any password.
func (c *Config) displayDSN() string {
	if c.Password != "" {
		return c.User + ":****"
	}
	return c.User
}
