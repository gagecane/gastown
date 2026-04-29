package doltserver

import (
	"fmt"
	"net"
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
