package doltserver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// serverExecSQL executes a SQL statement against the Dolt server without targeting
// a specific database. Used for server-level commands like CREATE DATABASE.
//
// Always connects via explicit --host/--port flags to ensure the command goes
// through the running sql-server process. Without these flags, `dolt sql` runs
// in embedded mode (even from the data directory), which creates databases on
// disk but does NOT register them with the live server catalog. This caused
// "database not found" errors during gt rig add.
func serverExecSQL(townRoot, query string) error {
	config := DefaultConfig(townRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := buildServerSQLCmd(ctx, config, "-q", query)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w (output: %s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// buildServerSQLCmd constructs a dolt sql command that always connects to the
// running sql-server via explicit --host/--port flags. Unlike buildDoltSQLCmd,
// which omits connection flags for local servers (relying on dolt auto-detection),
// this function ensures the command goes through the live server process.
// This is critical for DDL operations (CREATE/DROP DATABASE) that must modify
// the server's in-memory catalog, not just the filesystem.
//
// Dolt requires --host, --port, --user, --no-tls as global flags (before the
// subcommand), not as subcommand flags. The order is:
//   dolt --host=H --port=P --user=U --no-tls sql -q "..."
func buildServerSQLCmd(ctx context.Context, config *Config, args ...string) *exec.Cmd {
	// Global connection flags must come before the "sql" subcommand.
	// Always pass --password to prevent dolt from prompting on stdin
	// (which fails with "inappropriate ioctl" in non-TTY environments).
	password := config.Password
	fullArgs := []string{
		"--host", config.EffectiveHost(),
		"--port", strconv.Itoa(config.Port),
		"--user", config.User,
		"--password", password,
		"--no-tls",
		"sql",
	}
	fullArgs = append(fullArgs, args...)

	cmd := exec.CommandContext(ctx, "dolt", fullArgs...)
	cmd.Dir = config.DataDir
	setProcessGroup(cmd)

	return cmd
}

// waitForCatalog polls the Dolt server until the named database is visible in the
// in-memory catalog. This bridges the race between CREATE DATABASE returning and the
// catalog being updated — without this, immediate USE/query operations can fail with
// "Unknown database". Uses exponential backoff: 100ms, 200ms, 400ms, 800ms, 1.6s.
// Only retries on catalog-race errors ("Unknown database"); returns immediately for
// other failures (e.g., server crash, binary missing).
func waitForCatalog(townRoot, dbName string) error {
	const maxAttempts = 5
	const baseBackoff = 100 * time.Millisecond
	const maxBackoff = 2 * time.Second

	query := fmt.Sprintf("USE %s", dbName)
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := serverExecSQL(townRoot, query); err != nil {
			lastErr = err
			// Only retry catalog-race errors; fail fast on other errors
			// (connection refused, binary missing, etc.)
			errStr := err.Error()
			if !strings.Contains(errStr, "Unknown database") && !strings.Contains(errStr, "database not found") {
				return fmt.Errorf("database %q probe failed (non-retryable): %w", dbName, err)
			}
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
		return nil
	}
	return fmt.Errorf("database %q not visible after %d attempts: %w", dbName, maxAttempts, lastErr)
}

// doltSQL executes a SQL statement against a specific rig database on the Dolt server.
// Uses explicit --host/--port flags to connect to the running server (same rationale
// as serverExecSQL — embedded mode doesn't share the server's catalog).
// The USE prefix selects the database since --use-db is not available on all dolt versions.
func doltSQL(townRoot, rigDB, query string) error {
	config := DefaultConfig(townRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Prepend USE <db> to select the target database.
	fullQuery := fmt.Sprintf("USE %s; %s", rigDB, query)
	cmd := buildServerSQLCmd(ctx, config, "-q", fullQuery)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w (output: %s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// doltSQLWithRetry executes a SQL statement with exponential backoff on transient errors.
func doltSQLWithRetry(townRoot, rigDB, query string) error {
	const maxRetries = 5
	const baseBackoff = 500 * time.Millisecond
	const maxBackoff = 15 * time.Second

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := doltSQL(townRoot, rigDB, query); err != nil {
			lastErr = err
			if !isDoltRetryableError(err) {
				return err
			}
			if attempt < maxRetries {
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
		return nil
	}
	return fmt.Errorf("after %d retries: %w", maxRetries, lastErr)
}

// isDoltRetryableError returns true if the error is a transient Dolt failure worth retrying.
// Covers manifest lock contention, read-only mode, optimistic lock failures, timeouts,
// and catalog propagation delays after CREATE DATABASE.
func isDoltRetryableError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "database is read only") ||
		strings.Contains(msg, "cannot update manifest") ||
		strings.Contains(msg, "optimistic lock") ||
		strings.Contains(msg, "serialization failure") ||
		strings.Contains(msg, "lock wait timeout") ||
		strings.Contains(msg, "try restarting transaction") ||
		strings.Contains(msg, "Unknown database")
}

// CommitServerWorkingSet stages all pending changes and commits them on the current branch via SQL.
// This flushes the Dolt working set to HEAD so that DOLT_BRANCH (which forks from
// HEAD, not the working set) will include all recent writes. Critical for the sling
// flow where BD_DOLT_AUTO_COMMIT=off leaves writes in working set only.
//
// NOTE: This flushes ALL pending working set changes on the target branch, not just
// those from a specific polecat. In batch sling, polecat B's flush may capture
// polecat A's writes. This is benign because beads are keyed by unique ID, so
// duplicate data across branches merges cleanly.
func CommitServerWorkingSet(townRoot, rigDB, message string) error {
	if err := doltSQLWithRecovery(townRoot, rigDB, "CALL DOLT_ADD('-A')"); err != nil {
		return fmt.Errorf("staging working set in %s: %w", rigDB, err)
	}
	escaped := strings.ReplaceAll(message, "'", "''")
	query := fmt.Sprintf("CALL DOLT_COMMIT('--allow-empty', '-m', '%s')", escaped)
	if err := doltSQLWithRecovery(townRoot, rigDB, query); err != nil {
		return fmt.Errorf("committing working set in %s: %w", rigDB, err)
	}
	return nil
}

// doltSQLScript executes a multi-statement SQL script via a temp file.
// Uses `dolt sql --file` for reliable multi-statement execution within a
// single connection, preserving DOLT_CHECKOUT state across statements.
func doltSQLScript(townRoot, script string) error {
	config := DefaultConfig(townRoot)

	tmpFile, err := os.CreateTemp("", "dolt-script-*.sql")
	if err != nil {
		return fmt.Errorf("creating temp SQL file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(script); err != nil {
		tmpFile.Close()
		return fmt.Errorf("writing SQL script: %w", err)
	}
	tmpFile.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := buildDoltSQLCmd(ctx, config, "--file", tmpFile.Name())
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w (output: %s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// doltSQLScriptWithRetry executes a SQL script with exponential backoff on transient errors.
// Callers must ensure scripts are idempotent, as partial execution may have occurred
// before the retry. Uses the same retry classification as doltSQLWithRetry but with
// fewer retries and shorter backoff since multi-statement scripts are more expensive.
func doltSQLScriptWithRetry(townRoot, script string) error {
	const maxRetries = 3
	const baseBackoff = 500 * time.Millisecond
	const maxBackoff = 8 * time.Second

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := doltSQLScript(townRoot, script); err != nil {
			lastErr = err
			if !isDoltRetryableError(err) {
				return err
			}
			if attempt < maxRetries {
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
		return nil
	}
	return fmt.Errorf("after %d retries: %w", maxRetries, lastErr)
}
