package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/util"
)

// pidStartTimeFunc is overridden in tests. This package's tests must NOT use
// t.Parallel() because they mutate this package-level variable without synchronization.
var pidStartTimeFunc = processStartTime

type trackedPID struct {
	PID       int
	StartTime string
}

// pidsDir returns the directory for PID tracking files.
// All PID files live under <townRoot>/.runtime/pids/ since tmux session
// names are globally unique (they include the rig name).
func pidsDir(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "pids")
}

// pidFile returns the path to a PID file for a given session.
func pidFile(townRoot, sessionID string) string {
	return filepath.Join(pidsDir(townRoot), sessionID+".pid")
}

// pidLastFile returns the path to the rolling-history file for a session.
// Each call to TrackPID or UntrackPID appends a line with a timestamped
// event, giving operators a trail to debug stale-PID issues (gu-ytwg).
func pidLastFile(townRoot, sessionID string) string {
	return filepath.Join(pidsDir(townRoot), sessionID+".last")
}

// maxPidLastLines caps the size of the .last history file so it cannot grow
// unbounded across respawn cycles. Old entries are rotated out on each append.
const maxPidLastLines = 50

// appendPidHistory writes a best-effort audit entry to <session>.last.
// Errors are swallowed because history is debug-only and must never block a
// shutdown or spawn path.
func appendPidHistory(townRoot, sessionID, event string) {
	dir := pidsDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	path := pidLastFile(townRoot, sessionID)
	line := fmt.Sprintf("%s %s\n", time.Now().UTC().Format(time.RFC3339Nano), event)

	// Read existing lines, trim to maxPidLastLines-1, then append.
	var existing []string
	if data, err := os.ReadFile(path); err == nil {
		existing = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		if len(existing) == 1 && existing[0] == "" {
			existing = nil
		}
		if len(existing) > maxPidLastLines-1 {
			existing = existing[len(existing)-(maxPidLastLines-1):]
		}
	}
	var buf strings.Builder
	for _, e := range existing {
		buf.WriteString(e)
		buf.WriteByte('\n')
	}
	buf.WriteString(line)
	_ = os.WriteFile(path, []byte(buf.String()), 0644)
}

// TrackSessionPID captures the pane PID of a tmux session and writes it
// to a PID tracking file. This is defense-in-depth: if a session dies
// unexpectedly and KillSessionWithProcesses can't find the tmux pane,
// we still have the PID on disk for cleanup.
//
// This is best-effort — errors are returned but callers should treat them
// as non-fatal since the primary kill mechanism (KillSessionWithProcesses)
// doesn't depend on PID files.
func TrackSessionPID(townRoot, sessionID string, t *tmux.Tmux) error {
	pidStr, err := t.GetPanePID(sessionID)
	if err != nil {
		return fmt.Errorf("getting pane PID: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(pidStr))
	if err != nil {
		return fmt.Errorf("parsing PID %q: %w", pidStr, err)
	}

	return TrackPID(townRoot, sessionID, pid)
}

// TrackPID writes a PID to a tracking file for later cleanup.
//
// On every call this function OVERWRITES the session's .pid file with the
// supplied PID (and optional start-time). This is intentional: when an agent
// is respawned in the same tmux pane, the previous PID becomes stale and must
// be replaced — otherwise KillTrackedPIDs and other consumers may signal the
// wrong process or falsely conclude the session is dead (gu-ytwg / gt-scrl4).
//
// An audit entry is also appended to <session>.last for debugging.
func TrackPID(townRoot, sessionID string, pid int) error {
	dir := pidsDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating pids directory: %w", err)
	}

	path := pidFile(townRoot, sessionID)
	record := strconv.Itoa(pid)
	if start, err := pidStartTimeFunc(pid); err == nil && start != "" {
		record = fmt.Sprintf("%d|%s", pid, start)
	}
	if err := os.WriteFile(path, []byte(record+"\n"), 0644); err != nil {
		return err
	}
	appendPidHistory(townRoot, sessionID, "track "+record)
	return nil
}

// UntrackPID removes the PID tracking file for a session.
//
// Call this from every shutdown / teardown path that kills a tracked tmux
// session so the PID file does not outlive the process it references. Without
// this, stale entries accumulate and any consumer that trusts the file (e.g.
// doctor, heartbeat, KillTrackedPIDs) may signal the wrong PID (gu-ytwg).
//
// Safe to call even if the file does not exist; errors are intentionally
// swallowed because this is a best-effort cleanup.
func UntrackPID(townRoot, sessionID string) {
	_ = os.Remove(pidFile(townRoot, sessionID))
	appendPidHistory(townRoot, sessionID, "untrack")
}

// KillTrackedPIDs reads all PID files and kills any processes that are
// still running. Returns the number of processes killed and any session
// names that had errors.
//
// This is designed for the shutdown orphan-cleanup phase: after all
// sessions have been killed through normal means, this catches any
// processes that survived (e.g., reparented to init after SIGHUP).
func KillTrackedPIDs(townRoot string) (killed int, errSessions []string) {
	dir := pidsDir(townRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, []string{fmt.Sprintf("read pids dir: %v", err)}
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pid") {
			continue
		}

		sessionID := strings.TrimSuffix(entry.Name(), ".pid")
		path := filepath.Join(dir, entry.Name())

		data, err := os.ReadFile(path)
		if err != nil {
			errSessions = append(errSessions, fmt.Sprintf("%s: read error: %v", sessionID, err))
			continue
		}

		record, err := parseTrackedPID(strings.TrimSpace(string(data)))
		if err != nil {
			// Corrupt PID file — remove it
			_ = os.Remove(path)
			continue
		}
		pid := record.PID

		// Check if process is still alive
		proc, err := os.FindProcess(pid)
		if err != nil {
			_ = os.Remove(path)
			continue
		}

		// Signal 0 checks existence without killing
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			// Process is already dead — clean up PID file
			_ = os.Remove(path)
			continue
		}

		// If we have process birth info, verify this is still the same process.
		// If PID was reused, skip killing to avoid terminating an active unrelated process.
		if record.StartTime != "" {
			currentStart, startErr := pidStartTimeFunc(pid)
			if startErr != nil {
				// Cannot verify process identity — leave the PID file so a
				// future cleanup attempt can retry once ps is available again.
				errSessions = append(errSessions, fmt.Sprintf("%s (PID %d): cannot verify start time: %v — skipping kill, preserving tracking file", sessionID, pid, startErr))
				continue
			}
			if currentStart != record.StartTime {
				// Confirmed PID reuse — safe to remove tracking file.
				_ = os.Remove(path)
				continue
			}
		}

		// Process is alive — kill it
		if err := proc.Signal(syscall.SIGTERM); err != nil {
			errSessions = append(errSessions, fmt.Sprintf("%s (PID %d): SIGTERM failed: %v", sessionID, pid, err))
		} else {
			killed++
		}

		// Clean up PID file regardless
		_ = os.Remove(path)
	}

	return killed, errSessions
}

func parseTrackedPID(value string) (trackedPID, error) {
	if value == "" {
		return trackedPID{}, fmt.Errorf("empty pid record")
	}
	parts := strings.SplitN(value, "|", 2)
	pid, err := strconv.Atoi(parts[0])
	if err != nil {
		return trackedPID{}, err
	}
	record := trackedPID{PID: pid}
	if len(parts) == 2 {
		record.StartTime = parts[1]
	}
	return record, nil
}

// processStartTime returns the start time of a process via ps(1).
// This works on Linux and macOS. On Windows (or minimal containers without ps),
// the call will fail and callers degrade gracefully to PID-only tracking.
func processStartTime(pid int) (string, error) {
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	util.SetDetachedProcessGroup(cmd)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
