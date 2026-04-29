package doltserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// IsRunning checks if a Dolt server is running for the given town.

// Returns (running, pid, error).
// Checks both PID file AND port to detect externally-started servers.
// For remote servers, skips PID/port scan and just does TCP reachability.
func IsRunning(townRoot string) (bool, int, error) {
	config := DefaultConfig(townRoot)

	// Remote server: no local PID/process to check — just TCP reachability.
	if config.IsRemote() {
		conn, err := net.DialTimeout("tcp", config.HostPort(), 2*time.Second)
		if err != nil {
			return false, 0, nil
		}
		_ = conn.Close()
		return true, 0, nil
	}

	// First check PID file
	data, err := os.ReadFile(config.PidFile)
	if err == nil {
		pidStr := strings.TrimSpace(string(data))
		pid, err := strconv.Atoi(pidStr)
		if err == nil {
			// Check if process is alive
			if processIsAlive(pid) {
				// Verify it's actually serving on the expected port.
				// More reliable than ps string matching (ZFC fix: gt-utuk).
				if isDoltServerOnPort(config.Port) {
					if doltProcessMatchesTown(townRoot, pid, config) {
						return true, pid, nil
					}
					// Port served by a different town's Dolt — fall through to stale cleanup
				}
			}
		}
		// PID file is stale, clean it up
		_ = os.Remove(config.PidFile)
	}

	// No valid PID file - check if port is in use by dolt anyway.
	// This catches externally-started dolt servers.
	pid := findDoltServerOnPort(config.Port)
	if pid > 0 && doltProcessMatchesTown(townRoot, pid, config) {
		return true, pid, nil
	}

	// Last resort: TCP reachability check. This handles Docker containers,
	// externally-restarted servers (e.g., dolt restarted outside of gt),
	// and other setups where no local dolt process is visible via lsof/ss
	// (e.g., the port is forwarded by a Docker proxy).
	// We always check, even on the default port 3307, so that gt rig add
	// succeeds when dolt is live regardless of how it was started.
	conn, err := net.DialTimeout("tcp", config.HostPort(), 2*time.Second)
	if err == nil {
		_ = conn.Close()
		return true, 0, nil
	}

	return false, 0, nil
}

// CheckServerReachable verifies the Dolt server is actually accepting TCP connections.
// This catches the case where a process exists but the server hasn't finished starting,
// or the PID file is stale and the port is not actually listening.
// Returns nil if reachable, error describing the problem otherwise.
func CheckServerReachable(townRoot string) error {
	config := DefaultConfig(townRoot)
	addr := config.HostPort()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		hint := ""
		if !config.IsRemote() {
			hint = "\n\nStart with: gt dolt start"
		}
		return fmt.Errorf("Dolt server not reachable at %s: %w%s", addr, err, hint)
	}
	_ = conn.Close()
	return nil
}

// WaitForReady polls for the Dolt server to become reachable (TCP connection
// succeeds) within the given timeout. Returns nil if the server is reachable
// or if no server-mode metadata is configured (nothing to wait for).
// Returns an error if the timeout expires before the server is reachable.
//
// This is used by gt up to ensure the Dolt server is ready before starting
// agents (witnesses, refineries) that depend on beads database access.
// Without this, agents race the Dolt server startup and get "connection refused".
func WaitForReady(townRoot string, timeout time.Duration) error {
	// Check if any rig is configured for server mode.
	// If not, there's no Dolt server to wait for.
	if len(HasServerModeMetadata(townRoot)) == 0 {
		return nil
	}

	config := DefaultConfig(townRoot)
	addr := config.HostPort()
	deadline := time.Now().Add(timeout)
	interval := 100 * time.Millisecond

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		dialTimeout := 1 * time.Second
		if remaining < dialTimeout {
			dialTimeout = remaining
		}
		conn, err := net.DialTimeout("tcp", addr, dialTimeout)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		remaining = time.Until(deadline)
		if remaining <= 0 {
			break
		}
		if interval > remaining {
			interval = remaining
		}
		time.Sleep(interval)
		// Exponential backoff capped at 500ms
		if interval < 500*time.Millisecond {
			interval = interval * 2
			if interval > 500*time.Millisecond {
				interval = 500 * time.Millisecond
			}
		}
	}

	return fmt.Errorf("Dolt server not ready at %s after %v", addr, timeout)
}

// HasServerModeMetadata checks whether any rig has metadata.json configured for
// Dolt server mode. Returns the list of rig names configured for server mode.
// This is used to detect the split-brain risk: if metadata says "server" but
// the server isn't running, bd commands may silently create isolated databases.
func HasServerModeMetadata(townRoot string) []string {
	var serverRigs []string

	// Check town-level beads (hq)
	townBeadsDir := filepath.Join(townRoot, ".beads")
	if hasServerMode(townBeadsDir) {
		serverRigs = append(serverRigs, "hq")
	}

	// Check rig-level beads
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	data, err := os.ReadFile(rigsPath)
	if err != nil {
		return serverRigs
	}
	var config struct {
		Rigs map[string]interface{} `json:"rigs"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return serverRigs
	}

	for rigName := range config.Rigs {
		beadsDir := FindRigBeadsDir(townRoot, rigName)
		if beadsDir != "" && hasServerMode(beadsDir) {
			serverRigs = append(serverRigs, rigName)
		}
	}

	return serverRigs
}

// hasServerMode reads metadata.json and returns true if dolt_mode is "server".
func hasServerMode(beadsDir string) bool {
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return false
	}
	var metadata struct {
		DoltMode string `json:"dolt_mode"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return false
	}
	return metadata.DoltMode == "server"
}

// CheckPortConflict checks if the configured port is occupied by another town's Dolt.
// Returns (conflicting PID, conflicting data-dir) if a foreign Dolt holds the port,
// or (0, "") if the port is free or used by this town's own Dolt.
func CheckPortConflict(townRoot string) (int, string) {
	cfg := DefaultConfig(townRoot)
	if cfg.IsRemote() {
		return 0, ""
	}
	pid := findDoltServerOnPort(cfg.Port)
	if pid <= 0 {
		return 0, ""
	}
	if doltProcessMatchesTown(townRoot, pid, cfg) {
		return 0, ""
	}
	return pid, doltProcessOwnerPath(townRoot, pid)
}

// findDoltServerOnPort finds a process listening on the given port.
// Returns the PID or 0 if not found.
// Does not verify process identity via ps string matching (ZFC fix: gt-utuk).
//
// Tries lsof first (macOS and most Linux), then ss (iproute2) as a fallback
// for Linux systems where lsof is not installed.
func findDoltServerOnPort(port int) int {
	// Try lsof — preferred when available (cross-platform).
	// Without -sTCP:LISTEN, lsof returns client PIDs (e.g., gt daemon) first,
	// which aren't dolt processes — causing false negatives.
	cmd := exec.Command("lsof", "-i", fmt.Sprintf(":%d", port), "-sTCP:LISTEN", "-t")
	setProcessGroup(cmd)
	if output, err := cmd.Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		if len(lines) > 0 && lines[0] != "" {
			if pid, err := strconv.Atoi(lines[0]); err == nil {
				return pid
			}
		}
	}

	// Fall back to ss (iproute2) — standard on modern Linux, no extra packages needed.
	// Example output line: LISTEN 0 128 *:3307 *:* users:(("dolt",pid=12345,fd=7))
	cmd = exec.Command("ss", "-tlnp", fmt.Sprintf("sport = :%d", port))
	setProcessGroup(cmd)
	if output, err := cmd.Output(); err == nil {
		for _, line := range strings.Split(string(output), "\n") {
			if idx := strings.Index(line, "pid="); idx >= 0 {
				rest := line[idx+4:]
				if end := strings.IndexAny(rest, ",)"); end > 0 {
					if pid, err := strconv.Atoi(rest[:end]); err == nil && pid > 0 {
						return pid
					}
				}
			}
		}
	}

	return 0
}

// DoltListener represents a Dolt process listening on a TCP port.
type DoltListener struct {
	PID  int
	Port int
}

// FindAllDoltListeners discovers all Dolt processes with TCP listeners using lsof.
// Uses process binary name matching (-c dolt) instead of command-line string matching
// (pgrep -f), avoiding fragile ps/pgrep pattern coupling (ZFC fix: gt-fj87).
// The -a flag is critical: without it, lsof ORs -c and -i selections, matching ANY
// process with TCP listeners (not just dolt). With -a, selections are ANDed (fix: gt-lzdp).
func FindAllDoltListeners() []DoltListener {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "lsof", "-a", "-c", "dolt", "-sTCP:LISTEN", "-i", "TCP", "-n", "-P", "-F", "pn")
	setProcessGroup(cmd)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	// Parse lsof -F output. Lines are field-prefixed:
	//   p<PID>     — process ID
	//   n<addr>    — network name (e.g., "*:3307" or "127.0.0.1:3307")
	var listeners []DoltListener
	var currentPID int
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if len(line) == 0 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, err := strconv.Atoi(line[1:])
			if err == nil {
				currentPID = pid
			}
		case 'n':
			if currentPID == 0 {
				continue
			}
			// Extract port from address like "*:3307" or "127.0.0.1:3307"
			addr := line[1:]
			if idx := strings.LastIndex(addr, ":"); idx >= 0 {
				port, err := strconv.Atoi(addr[idx+1:])
				if err == nil {
					// Deduplicate: same PID can have multiple FDs on same port
					dup := false
					for _, l := range listeners {
						if l.PID == currentPID && l.Port == port {
							dup = true
							break
						}
					}
					if !dup {
						listeners = append(listeners, DoltListener{PID: currentPID, Port: port})
					}
				}
			}
		}
	}
	return listeners
}

// isDoltServerOnPort checks if a dolt server is accepting connections on the given port.
// More reliable than ps string matching for process identity verification (ZFC fix: gt-utuk).
func isDoltServerOnPort(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
