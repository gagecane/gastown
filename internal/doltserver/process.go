// process.go: Process inspection helpers. Examines running Dolt processes to
// determine their data directories, config paths, and ownership relative to
// the current town root (for imposter detection and process verification).

package doltserver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// getServerDataDir returns the data directory for the Dolt server associated with townRoot.
// Reads from the persisted state file instead of parsing ps command output
// (ZFC fix: gt-utuk — eliminates fragile ps string matching).
// Returns empty string if the state file is missing or the PID doesn't match.
func getServerDataDir(townRoot string, pid int) string {
	state, err := LoadState(townRoot)
	if err != nil {
		return ""
	}
	// Only trust the state if the PID matches or we don't know the PID
	if state.PID == pid || pid == 0 {
		return state.DataDir
	}
	// PID mismatch — state is stale or belongs to a different server
	return ""
}

func getDoltFlagFromArgs(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
		prefix := flag + "="
		if strings.HasPrefix(arg, prefix) {
			return strings.TrimPrefix(arg, prefix)
		}
	}
	return ""
}

func getProcessArgs(pid int) []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "args=")
	setProcessGroup(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return strings.Fields(strings.TrimSpace(string(out)))
}

func getProcessCWD(pid int) string {
	switch runtime.GOOS {
	case "linux":
		cwd, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "cwd"))
		if err == nil {
			return cwd
		}
	case "darwin":
		cmd := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn")
		setProcessGroup(cmd)
		out, err := cmd.Output()
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if strings.HasPrefix(line, "n") {
					return strings.TrimPrefix(line, "n")
				}
			}
		}
	}
	return ""
}

func resolveProcessPath(pid int, path string) string {
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if cwd := getProcessCWD(pid); cwd != "" {
		return filepath.Clean(filepath.Join(cwd, path))
	}
	return filepath.Clean(path)
}

// GetDoltDataDirFromProcess reads the --data-dir flag value from the running
// process's command-line arguments. This is structural (reading a well-defined
// CLI flag), not heuristic string matching. Used as a tiebreaker when the
// state-file based check is inconclusive (e.g. PID reuse across towns).
//
// Supported on macOS and Linux via POSIX ps. Returns empty string on Windows
// (not supported) or on any error.
func GetDoltDataDirFromProcess(pid int) string {
	return resolveProcessPath(pid, getDoltFlagFromArgs(getProcessArgs(pid), "--data-dir"))
}

// getDoltConfigPathFromProcess reads the --config flag value from the running
// process's command-line arguments. Gas Town starts Dolt via --config, so this
// is the primary ownership signal when --data-dir is absent.
func getDoltConfigPathFromProcess(pid int) string {
	return resolveProcessPath(pid, getDoltFlagFromArgs(getProcessArgs(pid), "--config"))
}

// canonicalizePath returns the canonical absolute form of a path with symlinks
// resolved. Falls back to filepath.Abs when EvalSymlinks fails (e.g. path does
// not exist, permission denied). Returns "" only if the input is empty.
//
// This is the primary tool for path equality comparisons in imposter detection:
// on systems where /home/<user> is a symlink to /local/home/<user> (Amazon
// CloudDesktop), a process reporting its data-dir via /proc/<pid>/cmdline may
// surface the physical path while the expected path uses the symlink path (or
// vice versa). Plain string comparison of filepath.Abs output would mis-classify
// the legitimate server as an imposter. See gu-qhyv.
func canonicalizePath(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	// EvalSymlinks requires the path to exist. When it does, we get the
	// canonical physical path; when it doesn't, we keep the absolute form so
	// two non-existent paths still compare equal when they're spelled the same.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

func doltProcessMatchesTownPaths(expectedDataDir, actualDataDir, actualConfigPath, actualCWD, stateDataDir string) bool {
	expectedDir := canonicalizePath(expectedDataDir)
	if actualDataDir != "" {
		return canonicalizePath(actualDataDir) == expectedDir
	}
	if actualConfigPath != "" {
		// Join config.yaml onto the UNRESOLVED expectedDataDir (so the join
		// does not accidentally cross a symlink), then canonicalize.
		expectedConfig := canonicalizePath(filepath.Join(expectedDataDir, "config.yaml"))
		return canonicalizePath(actualConfigPath) == expectedConfig
	}
	if actualCWD != "" {
		absCWD := canonicalizePath(actualCWD)
		return absCWD == expectedDir || absCWD == filepath.Dir(expectedDir)
	}
	if stateDataDir != "" {
		return canonicalizePath(stateDataDir) == expectedDir
	}
	return false
}

func doltProcessMatchesTown(townRoot string, pid int, config *Config) bool {
	return doltProcessMatchesTownPaths(
		config.DataDir,
		GetDoltDataDirFromProcess(pid),
		getDoltConfigPathFromProcess(pid),
		getProcessCWD(pid),
		getServerDataDir(townRoot, pid),
	)
}

func doltProcessOwnerPathFromEvidence(actualDataDir, actualConfigPath, actualCWD, stateDataDir string) string {
	switch {
	case actualDataDir != "":
		return actualDataDir
	case actualConfigPath != "":
		return actualConfigPath
	case actualCWD != "":
		return actualCWD
	default:
		return stateDataDir
	}
}

func doltProcessOwnerPath(townRoot string, pid int) string {
	return doltProcessOwnerPathFromEvidence(
		GetDoltDataDirFromProcess(pid),
		getDoltConfigPathFromProcess(pid),
		getProcessCWD(pid),
		getServerDataDir(townRoot, pid),
	)
}

// VerifyServerDataDir checks whether the running Dolt server is serving the
// expected databases from the correct data directory. Returns true if the server
// is legitimate (serving databases from config.DataDir), false if it's an imposter
// (e.g., started from a different data directory with different/empty databases).
func VerifyServerDataDir(townRoot string) (bool, error) {
	config := DefaultConfig(townRoot)

	// First check: inspect the state file for data-dir (ZFC fix: gt-utuk).
	running, pid, err := IsRunning(townRoot)
	if err != nil || !running {
		return false, fmt.Errorf("server not running")
	}

	ownerPath := doltProcessOwnerPath(townRoot, pid)
	if ownerPath != "" {
		expectedDir, _ := filepath.Abs(config.DataDir)
		if !doltProcessMatchesTown(townRoot, pid, config) {
			return false, fmt.Errorf("server ownership mismatch: expected %s, got %s (PID %d)", expectedDir, ownerPath, pid)
		}
		return true, nil
	}

	// No state file or PID mismatch — check served databases
	fsDatabases, fsErr := ListDatabases(townRoot)
	if fsErr != nil || len(fsDatabases) == 0 {
		// Can't verify if no databases expected
		return true, nil
	}

	served, _, verifyErr := VerifyDatabases(townRoot)
	if verifyErr != nil {
		return false, fmt.Errorf("could not query server databases: %w", verifyErr)
	}

	// If the server is serving none of our expected databases, it's an imposter
	servedSet := make(map[string]bool, len(served))
	for _, db := range served {
		servedSet[strings.ToLower(db)] = true
	}
	matchCount := 0
	for _, db := range fsDatabases {
		if servedSet[strings.ToLower(db)] {
			matchCount++
		}
	}
	if matchCount == 0 && len(fsDatabases) > 0 {
		return false, fmt.Errorf("server serves none of the expected %d databases — likely an imposter", len(fsDatabases))
	}

	return true, nil
}

// KillImposters finds and kills any dolt sql-server process on the configured
// port that is NOT serving from the expected data directory. This handles the
// case where another tool (e.g., bd) launched its own embedded Dolt server
// from a different directory, hijacking the port.
func KillImposters(townRoot string) error {
	config := DefaultConfig(townRoot)
	pid := findDoltServerOnPort(config.Port)
	if pid == 0 {
		return nil // No server on port
	}

	if doltProcessMatchesTown(townRoot, pid, config) {
		return nil
	}

	owner := doltProcessOwnerPath(townRoot, pid)
	expectedDir, _ := filepath.Abs(config.DataDir)
	fmt.Fprintf(os.Stderr, "Killing imposter dolt sql-server (PID %d, data-dir: %q, expected: %s)\n",
		pid, owner, expectedDir)

	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding imposter process %d: %w", pid, err)
	}
	if process == nil {
		// Defensive: os.FindProcess is documented as always returning a valid
		// Process on Unix, but returns nil on error paths on some platforms.
		// If it's nil, we can't signal — but the kernel confirmed earlier that
		// no process held the port, so there's nothing to do.
		return nil
	}

	// If the process exited between the port check and now, there's nothing
	// to kill. Detect that here so we don't hit "os: process not initialized"
	// from a stale handle.
	if !processIsAlive(pid) {
		_ = os.Remove(config.PidFile)
		return nil
	}

	// Graceful termination first (SIGTERM on Unix, Kill on Windows)
	if err := gracefulTerminate(process); err != nil {
		// Race: process exited between processIsAlive and gracefulTerminate.
		// That's fine — the imposter is gone.
		if !processIsAlive(pid) {
			_ = os.Remove(config.PidFile)
			return nil
		}
		return fmt.Errorf("sending termination signal to imposter PID %d: %w", pid, err)
	}

	// Wait for graceful shutdown
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		if !processIsAlive(pid) {
			// Clean up PID file if it pointed to the imposter
			_ = os.Remove(config.PidFile)
			return nil
		}
	}

	// Force kill (ignore error — process may have just exited)
	if processIsAlive(pid) {
		_ = process.Kill()
	}
	time.Sleep(100 * time.Millisecond)
	_ = os.Remove(config.PidFile)

	return nil
}

// containsPathBoundary checks whether line contains path as a complete path
// (not a prefix of a longer path). The character after the match must be a
// path separator, whitespace, or end-of-string.
func containsPathBoundary(line, path string) bool {
	if path == "" {
		return false
	}
	for start := 0; start < len(line); {
		idx := strings.Index(line[start:], path)
		if idx < 0 {
			return false
		}
		end := start + idx + len(path)
		if end >= len(line) {
			return true
		}
		c := line[end]
		if c == filepath.Separator || c == ' ' || c == '\t' {
			return true
		}
		start = start + idx + 1
	}
	return false
}

// StopIdleMonitors finds and terminates "bd dolt idle-monitor" processes
// associated with this town. These background processes auto-spawn rogue
// Dolt servers from per-rig .beads/dolt/ directories when the canonical
// server is unreachable, creating a race condition during restart.
func StopIdleMonitors(townRoot string) int {
	absRoot, _ := filepath.Abs(townRoot)
	if absRoot == "" {
		return 0
	}

	psCmd := exec.Command("ps", "-eo", "pid,args")
	setProcessGroup(psCmd)
	output, err := psCmd.Output()
	if err != nil {
		return 0
	}

	config := DefaultConfig(townRoot)
	portStr := strconv.Itoa(config.Port)

	stopped := 0
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "idle-monitor") {
			continue
		}
		if !strings.Contains(line, "dolt") {
			continue
		}

		// Scope to this town: match by path in args using path-boundary check
		// to avoid false matches on sibling paths (e.g., /tmp/gt matching /tmp/gt-old)
		matchesTown := containsPathBoundary(line, absRoot) || containsPathBoundary(line, townRoot)
		if !matchesTown {
			// Check for --port <portStr> as a discrete argument to avoid
			// false matches on PIDs or other numeric substrings
			args := strings.Fields(line)
			for i, arg := range args {
				if (arg == "--port" || arg == "-p") && i+1 < len(args) && args[i+1] == portStr {
					matchesTown = true
					break
				}
				if arg == "--port="+portStr {
					matchesTown = true
					break
				}
			}
		}
		if !matchesTown {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}

		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if err := proc.Kill(); err != nil {
			continue
		}

		// Wait briefly for termination
		for i := 0; i < 5; i++ {
			time.Sleep(100 * time.Millisecond)
			if !processIsAlive(pid) {
				break // Process exited
			}
			if i == 4 {
				_ = proc.Kill()
			}
		}
		stopped++
	}

	return stopped
}
