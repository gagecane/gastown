package doltserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/steveyegge/gastown/internal/style"
)

// GetActiveConnectionCount queries the Dolt server to get the number of active connections.
// Uses `dolt sql` to query information_schema.PROCESSLIST, which avoids needing
// a MySQL driver dependency. Returns 0 if the server is unreachable or the query fails.
func GetActiveConnectionCount(townRoot string) (int, error) {
	config := DefaultConfig(townRoot)

	// Use dolt sql-client to query the server with a timeout to prevent
	// hanging indefinitely if the Dolt server is unresponsive.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Always connect as a TCP client to the running server, even for local servers.
	// Without explicit --host/--port, dolt sql runs in embedded mode which loads all
	// databases into memory — causing OOM kills on large data dirs.
	// Note: --host, --port, --user, --no-tls are dolt GLOBAL args and must come
	// BEFORE the "sql" subcommand.
	fullArgs := []string{
		"--host", config.EffectiveHost(),
		"--port", strconv.Itoa(config.Port),
		"--user", config.User,
		"--no-tls",
		"sql",
		"-r", "csv",
		"-q", "SELECT COUNT(*) AS cnt FROM information_schema.PROCESSLIST",
	}
	cmd := exec.CommandContext(ctx, "dolt", fullArgs...)
	// GH#2537: Set cmd.Dir to the server's data directory to prevent dolt from
	// creating stray .doltcfg/privileges.db files in the caller's CWD. Even in
	// TCP client mode, dolt may auto-create .doltcfg/ in the working directory.
	cmd.Dir = config.DataDir
	// Always set DOLT_CLI_PASSWORD to prevent interactive password prompt.
	// When empty, dolt connects without a password (which is the default for local servers).
	cmd.Env = append(os.Environ(), "DOLT_CLI_PASSWORD="+config.Password)
	setProcessGroup(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("querying connection count: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}

	// Parse CSV output: "cnt\n5\n"
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("unexpected output from connection count query: %s", string(output))
	}
	count, err := strconv.Atoi(strings.TrimSpace(lines[len(lines)-1]))
	if err != nil {
		return 0, fmt.Errorf("parsing connection count %q: %w", lines[len(lines)-1], err)
	}

	return count, nil
}

// HasConnectionCapacity checks whether the Dolt server has capacity for new connections.
// Returns true if the active connection count is below the threshold (80% of max_connections).
// Returns false with error if the connection count cannot be determined — fail closed
// to prevent connection storms that cause read-only mode (gt-lfc0d).
func HasConnectionCapacity(townRoot string) (bool, int, error) {
	config := DefaultConfig(townRoot)
	maxConn := config.MaxConnections
	if maxConn <= 0 {
		maxConn = 1000 // Dolt default
	}

	active, err := GetActiveConnectionCount(townRoot)
	if err != nil {
		// Fail closed: if we can't check, the server may be overloaded
		return false, 0, err
	}

	// Use 80% threshold to leave headroom for existing operations
	threshold := (maxConn * 80) / 100
	if threshold < 1 {
		threshold = 1
	}

	return active < threshold, active, nil
}

// HealthMetrics holds resource monitoring data for the Dolt server.
type HealthMetrics struct {
	// Connections is the number of active connections (from information_schema.PROCESSLIST).
	Connections int `json:"connections"`

	// MaxConnections is the configured maximum connections.
	MaxConnections int `json:"max_connections"`

	// ConnectionPct is the percentage of max connections in use.
	ConnectionPct float64 `json:"connection_pct"`

	// DiskUsageBytes is the total size of the .dolt-data/ directory.
	DiskUsageBytes int64 `json:"disk_usage_bytes"`

	// DiskUsageHuman is a human-readable disk usage string.
	DiskUsageHuman string `json:"disk_usage_human"`

	// QueryLatency is the time taken for a SELECT active_branch() round-trip.
	// Note: json.Marshal emits nanoseconds for time.Duration. Consumers should use
	// ServerHealth.LatencyMs (int64 milliseconds) for JSON output instead.
	QueryLatency time.Duration `json:"query_latency_ns"`

	// ReadOnly indicates whether the server is in read-only mode.
	// When true, the server accepts reads but rejects all writes.
	ReadOnly bool `json:"read_only"`

	// LastCommitAge is the time since the most recent Dolt commit across all databases.
	// A large gap (>1 hour) may indicate the server was down or writes are failing.
	// Note: json.Marshal emits nanoseconds for time.Duration. Consumers should use
	// ServerHealth.LastCommitAgeSec (float64 seconds) for JSON output instead.
	LastCommitAge time.Duration `json:"last_commit_age_ns"`

	// LastCommitDB is the database that had the most recent commit.
	LastCommitDB string `json:"last_commit_db,omitempty"`

	// Healthy indicates whether the server is within acceptable resource limits.
	Healthy bool `json:"healthy"`

	// Warnings contains any degradation warnings (non-fatal).
	Warnings []string `json:"warnings,omitempty"`
}

// GetHealthMetrics collects resource monitoring metrics from the Dolt server.
// Returns partial metrics if some checks fail — always returns what it can.
func GetHealthMetrics(townRoot string) *HealthMetrics {
	config := DefaultConfig(townRoot)
	metrics := &HealthMetrics{
		Healthy:        true,
		MaxConnections: config.MaxConnections,
	}
	if metrics.MaxConnections <= 0 {
		metrics.MaxConnections = 1000 // Dolt default
	}

	// 1. Query latency: time a SELECT active_branch()
	latency, err := MeasureQueryLatency(townRoot)
	if err == nil {
		metrics.QueryLatency = latency
		if latency > 1*time.Second {
			metrics.Warnings = append(metrics.Warnings,
				fmt.Sprintf("query latency %v exceeds 1s threshold — server may be under stress", latency.Round(time.Millisecond)))
		}
	}

	// 2. Connection count
	connCount, err := GetActiveConnectionCount(townRoot)
	if err == nil {
		metrics.Connections = connCount
		metrics.ConnectionPct = float64(connCount) / float64(metrics.MaxConnections) * 100
		if metrics.ConnectionPct >= 80 {
			metrics.Healthy = false
			metrics.Warnings = append(metrics.Warnings,
				fmt.Sprintf("connection count %d is %.0f%% of max %d — approaching limit",
					connCount, metrics.ConnectionPct, metrics.MaxConnections))
		}
	}

	// 3. Disk usage
	diskBytes := dirSize(config.DataDir)
	metrics.DiskUsageBytes = diskBytes
	metrics.DiskUsageHuman = formatBytes(diskBytes)

	// 4. Read-only probe: attempt a test write
	readOnly, _ := CheckReadOnly(townRoot)
	metrics.ReadOnly = readOnly
	if readOnly {
		metrics.Healthy = false
		metrics.Warnings = append(metrics.Warnings,
			"server is in READ-ONLY mode — requires restart to recover")
	}

	// 5. Commit freshness: check the most recent commit across all databases.
	// A gap >1 hour suggests writes are failing or the server was recently down.
	if commitAge, commitDB, err := GetLastCommitAge(townRoot); err == nil {
		metrics.LastCommitAge = commitAge
		metrics.LastCommitDB = commitDB
		if commitAge > 1*time.Hour {
			metrics.Warnings = append(metrics.Warnings,
				fmt.Sprintf("last Dolt commit was %v ago (db: %s) — possible commit gap",
					commitAge.Round(time.Minute), commitDB))
		}
	}

	return metrics
}

// CheckReadOnly probes the Dolt server to detect read-only state by attempting
// a test write. The server can enter read-only mode under concurrent write load
// ("cannot update manifest: database is read only") and will NOT self-recover.
// Returns (true, nil) if read-only, (false, nil) if writable, (false, err) on probe failure.
func CheckReadOnly(townRoot string) (bool, error) {
	config := DefaultConfig(townRoot)

	// Need a database to test writes against
	databases, err := ListDatabases(townRoot)
	if err != nil || len(databases) == 0 {
		return false, nil // Can't probe without a database
	}

	db := databases[0]
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Attempt a write operation: create a temp table, write a row, drop it.
	// If the server is in read-only mode, this will fail with a characteristic error.
	query := fmt.Sprintf(
		"USE `%s`; CREATE TABLE IF NOT EXISTS `__gt_health_probe` (v INT PRIMARY KEY); REPLACE INTO `__gt_health_probe` VALUES (1); DROP TABLE IF EXISTS `__gt_health_probe`",
		db,
	)
	cmd := buildDoltSQLCmd(ctx, config, "-q", query)

	output, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if IsReadOnlyError(msg) {
			return true, nil
		}
		return false, fmt.Errorf("write probe failed: %w (%s)", err, msg)
	}

	return false, nil
}

// IsReadOnlyError checks if an error message indicates a Dolt read-only state.
// The characteristic error is "cannot update manifest: database is read only".
func IsReadOnlyError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "read only") ||
		strings.Contains(lower, "read-only") ||
		strings.Contains(lower, "readonly")
}

// RecoverReadOnly detects a read-only Dolt server, restarts it, and verifies
// recovery. This is the gt-level counterpart to the daemon's auto-recovery:
// when a gt command (spawn, done, etc.) encounters persistent read-only errors,
// it can call this to attempt recovery without waiting for the daemon's 30s loop.
// Returns nil if recovery succeeded, an error if recovery failed or wasn't needed.
func RecoverReadOnly(townRoot string) error {
	readOnly, err := CheckReadOnly(townRoot)
	if err != nil {
		return fmt.Errorf("read-only probe failed: %w", err)
	}
	if !readOnly {
		return nil // Server is writable, no recovery needed
	}

	fmt.Printf("Dolt server is in read-only mode, attempting recovery...\n")

	// Stop the server
	if err := Stop(townRoot); err != nil {
		// Server might already be stopped or unreachable
		style.PrintWarning("stop returned error (proceeding with restart): %v", err)
	}

	// Brief pause for cleanup
	time.Sleep(1 * time.Second)

	// Restart the server
	if err := Start(townRoot); err != nil {
		return fmt.Errorf("failed to restart Dolt server: %w", err)
	}

	// Verify recovery with exponential backoff (server may need time to become writable)
	const maxAttempts = 5
	const baseBackoff = 500 * time.Millisecond
	const maxBackoff = 8 * time.Second

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		backoff := baseBackoff
		for i := 1; i < attempt; i++ {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
				break
			}
		}
		time.Sleep(backoff)

		readOnly, err = CheckReadOnly(townRoot)
		if err != nil {
			if attempt == maxAttempts {
				return fmt.Errorf("post-restart probe failed after %d attempts: %w", maxAttempts, err)
			}
			continue
		}
		if !readOnly {
			fmt.Printf("Dolt server recovered from read-only state\n")
			return nil
		}
	}

	return fmt.Errorf("Dolt server still read-only after restart (%d verification attempts)", maxAttempts)
}

// doltSQLWithRecovery executes a SQL statement with retry logic and, if retries
// are exhausted due to read-only errors, attempts server restart before a final retry.
// This is the gt-level recovery path for polecat management operations (spawn, done).
func doltSQLWithRecovery(townRoot, rigDB, query string) error {
	err := doltSQLWithRetry(townRoot, rigDB, query)
	if err == nil {
		return nil
	}

	// If the final error is a read-only error, attempt recovery
	if !IsReadOnlyError(err.Error()) {
		return err
	}

	// Attempt server recovery
	if recoverErr := RecoverReadOnly(townRoot); recoverErr != nil {
		return fmt.Errorf("read-only recovery failed: %w (original: %v)", recoverErr, err)
	}

	// Retry the operation after recovery
	if retryErr := doltSQL(townRoot, rigDB, query); retryErr != nil {
		return fmt.Errorf("operation failed after read-only recovery: %w", retryErr)
	}

	return nil
}

// MeasureQueryLatency times a SELECT active_branch() query against the Dolt server.
// Per Tim Sehn (Dolt CEO): active_branch() is a lightweight probe that won't block
// behind queued queries, unlike SELECT 1 which goes through the full query executor.
// Uses a direct TCP connection via the Go MySQL driver to measure actual query
// latency, not subprocess startup time.
func MeasureQueryLatency(townRoot string) (time.Duration, error) {
	config := DefaultConfig(townRoot)

	dsn := fmt.Sprintf("%s@tcp(%s:%d)/", config.User, config.EffectiveHost(), config.Port)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return 0, fmt.Errorf("opening mysql connection: %w", err)
	}
	defer db.Close()

	db.SetConnMaxLifetime(5 * time.Second)
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	var branch string
	err = db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&branch)
	elapsed := time.Since(start)

	if err != nil {
		return 0, fmt.Errorf("SELECT active_branch() failed: %w", err)
	}

	return elapsed, nil
}

// GetLastCommitAge returns the age and database name of the most recent Dolt commit
// across all databases. This detects commit gaps — periods where no writes persisted.
//
// Uses database/sql (like MeasureQueryLatency) rather than dolt subprocess to avoid
// subprocess startup overhead dominating the measurement.
func GetLastCommitAge(townRoot string) (time.Duration, string, error) {
	config := DefaultConfig(townRoot)

	dsn := fmt.Sprintf("%s@tcp(%s:%d)/", config.User, config.EffectiveHost(), config.Port)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return 0, "", fmt.Errorf("opening mysql connection: %w", err)
	}
	defer db.Close()

	db.SetConnMaxLifetime(5 * time.Second)
	db.SetMaxOpenConns(1)

	databases, err := ListDatabases(townRoot)
	if err != nil || len(databases) == 0 {
		return 0, "", fmt.Errorf("listing databases: %w", err)
	}

	var mostRecent time.Time
	var mostRecentDB string

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	for _, dbName := range databases {
		var dateStr string
		query := fmt.Sprintf("SELECT MAX(date) FROM `%s`.dolt_log LIMIT 1", dbName)
		if err := db.QueryRowContext(ctx, query).Scan(&dateStr); err != nil {
			continue // Skip databases that fail (e.g., no dolt_log)
		}
		// Dolt's dolt_log.date is DATETIME(6) (microsecond precision). Without
		// parseTime=true in the DSN, the Go MySQL driver returns this as a string
		// like "2025-03-28 12:34:56.123456". Go's ".999" fractional format accepts
		// any number of trailing digits (1-9), correctly parsing both millisecond
		// and microsecond timestamps. RFC3339 fallback handles version differences.
		t, err := time.Parse("2006-01-02 15:04:05.999", dateStr)
		if err != nil {
			t, err = time.Parse(time.RFC3339, dateStr)
			if err != nil {
				continue
			}
		}
		if t.After(mostRecent) {
			mostRecent = t
			mostRecentDB = dbName
		}
	}

	if mostRecent.IsZero() {
		return 0, "", fmt.Errorf("no commits found in any database")
	}

	return time.Since(mostRecent), mostRecentDB, nil
}

// dirSize returns the total size of a directory tree in bytes.
func dirSize(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// formatBytes returns a human-readable size string.
func formatBytes(b int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// moveDir moves a directory from src to dest. It first tries os.Rename for
// efficiency, but falls back to copy+delete if src and dest are on different
// filesystems (which causes EXDEV error on rename).
func moveDir(src, dest string) error {
	if err := os.Rename(src, dest); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}

	// Cross-filesystem: copy then delete source
	if runtime.GOOS == "windows" {
		cmd := exec.Command("robocopy", src, dest, "/E", "/MOVE", "/R:1", "/W:1")
		setProcessGroup(cmd)
		if err := cmd.Run(); err != nil {
			// robocopy returns 1 for success with copies
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() <= 7 {
				return nil
			}
			return fmt.Errorf("robocopy: %w", err)
		}
		return nil
	}
	cmd := exec.Command("cp", "-a", src, dest)
	setProcessGroup(cmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("copying directory: %w", err)
	}
	if err := os.RemoveAll(src); err != nil {
		return fmt.Errorf("removing source after copy: %w", err)
	}
	return nil
}
