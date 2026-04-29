package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/steveyegge/gastown/internal/constants"
)

// This file contains the daemon's lifecycle/shutdown surface: the internal
// graceful-shutdown sequence run from the Run loop, plus the exported
// helpers used by cmd/gt start/stop and by other packages (Boot, etc.)
// to detect whether a daemon is running or a shutdown is in progress.
// It also implements orphan daemon detection and cleanup, which is used
// by 'gt up' to recover after a hard crash.

// processLifecycleRequests checks for and processes lifecycle requests.
func (d *Daemon) processLifecycleRequests() {
	d.ProcessLifecycleRequests()
}

// shutdown performs graceful shutdown.
func (d *Daemon) shutdown(state *State) error { //nolint:unparam // error return kept for future use
	d.logger.Println("Daemon shutting down")

	// Stop feed curator
	if d.curator != nil {
		d.curator.Stop()
		d.logger.Println("Feed curator stopped")
	}

	// Stop convoy manager (also closes beads stores)
	if d.convoyManager != nil {
		d.convoyManager.Stop()
		d.logger.Println("Convoy manager stopped")
	}

	// Stop auto-dispatch watcher
	if d.autoDispatch != nil {
		d.autoDispatch.Stop()
		d.logger.Println("Auto-dispatch watcher stopped")
	}
	d.beadsStores = nil

	// Stop KRC pruner
	if d.krcPruner != nil {
		d.krcPruner.Stop()
		d.logger.Println("KRC pruner stopped")
	}

	// Push Dolt remotes before stopping the server (if patrol is enabled)
	d.pushDoltRemotes()

	// Stop Dolt server if we're managing it
	if d.doltServer != nil && d.doltServer.IsEnabled() && !d.doltServer.IsExternal() {
		if err := d.doltServer.Stop(); err != nil {
			d.logger.Printf("Warning: failed to stop Dolt server: %v", err)
		} else {
			d.logger.Println("Dolt server stopped")
		}
	}

	// Flush and stop OTel providers (5s deadline to avoid blocking shutdown).
	if d.otelProvider != nil {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := d.otelProvider.Shutdown(shutCtx); err != nil {
			d.logger.Printf("Warning: telemetry shutdown: %v", err)
		}
	}

	state.Running = false
	if err := SaveState(d.config.TownRoot, state); err != nil {
		d.logger.Printf("Warning: failed to save final state: %v", err)
	}

	d.logger.Println("Daemon stopped")
	return nil
}

// Stop signals the daemon to stop.
func (d *Daemon) Stop() {
	d.cancel()
}

// isShutdownInProgress checks if a shutdown is currently in progress.
// The shutdown.lock file is created by gt down before terminating sessions.
// This prevents the daemon from fighting shutdown by auto-restarting killed agents.
//
// Uses flock to check actual lock status rather than file existence, since
// the lock file persists after shutdown completes. The file is intentionally
// never removed: flock works on file descriptors, not paths, and removing
// the file while another process waits on the flock defeats mutual exclusion.
func (d *Daemon) isShutdownInProgress() bool {
	lockPath := filepath.Join(d.config.TownRoot, "daemon", "shutdown.lock")

	// If file doesn't exist, no shutdown in progress
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		return false
	}

	// Try non-blocking lock acquisition to check if shutdown holds the lock
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		// Error acquiring lock - assume shutdown in progress to be safe
		return true
	}

	if locked {
		// We acquired the lock, so no shutdown is holding it
		// Release immediately; leave the file in place so all
		// concurrent callers flock the same inode.
		_ = lock.Unlock()
		return false
	}

	// Could not acquire lock - shutdown is in progress
	return true
}

// IsShutdownInProgress checks if a shutdown is currently in progress for the given town.
// This is the exported version of isShutdownInProgress for use by other packages
// (e.g., Boot triage) that need to avoid restarting sessions during shutdown.
func IsShutdownInProgress(townRoot string) bool {
	lockPath := filepath.Join(townRoot, "daemon", "shutdown.lock")

	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		return false
	}

	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		return true
	}

	if locked {
		_ = lock.Unlock()
		return false
	}

	return true
}

// IsRunning checks if a daemon is running for the given town.
// Uses the daemon.lock flock as the authoritative signal — if the lock is held,
// the daemon is running. Falls back to PID file for the process ID.
// This avoids fragile ps string matching for process identity (ZFC fix: gt-utuk).
func IsRunning(townRoot string) (bool, int, error) {
	// Primary check: is the daemon lock held?
	lockPath := filepath.Join(townRoot, "daemon", "daemon.lock")
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		return false, 0, nil
	}

	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		// Can't check lock — fall back to PID file + signal check
		return isRunningFromPID(townRoot)
	}

	if locked {
		// We acquired the lock, so no daemon holds it
		_ = lock.Unlock()
		// Clean up stale PID file if present
		pidFile := filepath.Join(townRoot, "daemon", "daemon.pid")
		_ = os.Remove(pidFile)
		return false, 0, nil
	}

	// Lock is held — daemon is running. Read PID from file.
	// Use readPIDFile to handle the "PID\nNONCE" format introduced alongside
	// nonce-based ownership verification. A plain Atoi on the raw file content
	// fails when a nonce line is present, returning PID 0.
	pidFile := filepath.Join(townRoot, "daemon", "daemon.pid")
	pid, _, err := readPIDFile(pidFile)
	if err != nil {
		// Lock held but no readable PID file — daemon running, PID unknown
		return true, 0, nil
	}

	return true, pid, nil
}

// isRunningFromPID is the fallback when flock check fails. Uses PID file + signal.
func isRunningFromPID(townRoot string) (bool, int, error) {
	pidFile := filepath.Join(townRoot, "daemon", "daemon.pid")

	pid, alive, err := verifyPIDOwnership(pidFile)
	if err != nil {
		return false, 0, fmt.Errorf("checking PID file: %w", err)
	}

	if pid == 0 {
		// No PID file
		return false, 0, nil
	}

	if !alive {
		// Process not running, clean up stale PID file.
		// This is a successful recovery, not an error — the caller can
		// proceed as if no daemon is running (fixes #2107).
		os.Remove(pidFile) // best-effort cleanup
		return false, 0, nil
	}

	return true, pid, nil
}

// StopDaemon stops the running daemon for the given town.
// Note: The file lock in Run() prevents multiple daemons per town, so we only
// need to kill the process from the PID file.
func StopDaemon(townRoot string) error {
	running, pid, err := IsRunning(townRoot)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("daemon is not running")
	}

	if pid <= 0 {
		// Lock is held but PID is unknown (race: daemon starting, or stale lock).
		// Clean up the lock file so the next gt up can start fresh.
		lockPath := filepath.Join(townRoot, "daemon", "daemon.lock")
		_ = os.Remove(lockPath)
		pidFile := filepath.Join(townRoot, "daemon", "daemon.pid")
		_ = os.Remove(pidFile)
		return nil
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding process: %w", err)
	}

	// Send termination signal for graceful shutdown
	if err := sendTermSignal(process); err != nil {
		return fmt.Errorf("sending termination signal: %w", err)
	}

	// Wait a bit for graceful shutdown
	time.Sleep(constants.ShutdownNotifyDelay)

	// Check if still running
	if isProcessAlive(process) {
		// Still running, force kill
		_ = sendKillSignal(process)
	}

	// Clean up PID file
	pidFile := filepath.Join(townRoot, "daemon", "daemon.pid")
	_ = os.Remove(pidFile)

	return nil
}

// FindOrphanedDaemons detects daemon processes not tracked by the PID file.
// Uses flock on daemon.lock to detect running daemons without relying on
// pgrep or ps string matching (ZFC fix: gt-utuk).
//
// With flock-based daemon management, only one daemon can hold the lock.
// An "orphan" is detected when the lock is held but the PID file is stale
// (process dead) or missing. Returns the stale PID if available.
func FindOrphanedDaemons(townRoot string) ([]int, error) {
	lockPath := filepath.Join(townRoot, "daemon", "daemon.lock")
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		return nil, nil // No lock file — no daemon has ever run
	}

	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		return nil, nil // Can't check lock — assume no orphans
	}

	if locked {
		// We acquired the lock — no daemon holds it, no orphans possible
		_ = lock.Unlock()
		return nil, nil
	}

	// Lock is held — a daemon is running. Check if it's tracked.
	pidFile := filepath.Join(townRoot, "daemon", "daemon.pid")
	trackedPID, _, err := readPIDFile(pidFile)
	if err != nil {
		// Lock held but no/invalid PID file — daemon is running but untracked.
		// We can't determine its PID without ps/pgrep, so return empty.
		// The caller (start.go) should use IsRunning() which handles this case.
		return nil, nil
	}

	// Check if the tracked PID is actually alive
	process, findErr := os.FindProcess(trackedPID)
	if findErr != nil {
		return nil, nil
	}
	if !isProcessAlive(process) {
		// PID file exists but process is dead — stale PID file with held lock.
		// This shouldn't happen (lock should release on process death), but
		// report the stale PID for cleanup.
		return []int{trackedPID}, nil
	}

	// Lock held, PID alive, PID tracked — daemon is properly running, not orphaned.
	return nil, nil
}

// KillOrphanedDaemons finds and kills any orphaned gt daemon processes.
// Returns number of processes killed.
func KillOrphanedDaemons(townRoot string) (int, error) {
	pids, err := FindOrphanedDaemons(townRoot)
	if err != nil {
		return 0, err
	}

	killed := 0
	for _, pid := range pids {
		process, err := os.FindProcess(pid)
		if err != nil {
			continue
		}

		// Try termination signal first
		if err := sendTermSignal(process); err != nil {
			continue
		}

		// Wait for graceful shutdown
		time.Sleep(200 * time.Millisecond)

		// Check if still alive
		if isProcessAlive(process) {
			// Still alive, force kill
			_ = sendKillSignal(process)
		}

		killed++
	}

	return killed, nil
}
