package doltserver

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CheckPortAvailable verifies that a TCP port is free for use as a Dolt server.
// Returns a user-friendly error if the port is already in use.
func CheckPortAvailable(port int) error {
	return checkPortAvailable(port)
}

// PortHolder returns the PID and data directory of the process holding port.
// Returns (0, "") if the port is free or the holder cannot be identified.
// Note: data directory is only available when townRoot context is known;
// without it, returns PID only (ZFC fix: gt-utuk).
func PortHolder(port int) (pid int, dataDir string) {
	pid = findDoltServerOnPort(port)
	return pid, ""
}

// FindFreePort returns the first free TCP port at or above startFrom.
// Returns 0 if no free port is found within 100 attempts.
func FindFreePort(startFrom int) int {
	for port := startFrom; port < startFrom+100; port++ {
		if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			_ = ln.Close()
			return port
		}
	}
	return 0
}

func checkPortAvailable(port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		// Try to identify who holds the port
		detail := ""
		if pid := findDoltServerOnPort(port); pid > 0 {
			detail = fmt.Sprintf("\nPort is held by PID %d", pid)
		}
		return fmt.Errorf("port %d is already in use.%s\n"+
			"If you're running multiple Gas Town instances, each needs a unique Dolt port.\n"+
			"Set GT_DOLT_PORT in mayor/daemon.json env section:\n"+
			"  {\"env\": {\"GT_DOLT_PORT\": \"<port>\"}}", port, detail)
	}
	_ = ln.Close()
	return nil
}

// waitForPortRelease polls until the given port is free or the timeout expires.
// Used after killing an imposter to ensure the port is available before starting
// the canonical server, avoiding the race where a dying process still holds the port.
func waitForPortRelease(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_ = ln.Close()
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("port %d not released within %s", port, timeout)
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
