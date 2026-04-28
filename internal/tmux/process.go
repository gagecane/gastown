package tmux

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// killSplitBrainSession kills a same-named session on the "default" tmux socket
// if this Tmux instance targets a different socket. This prevents split-brain
// where stale sessions on the wrong socket shadow the real ones, causing nudge
// and other session-discovery commands to fail.
//
// Best-effort: all errors are silently ignored. The stale session may not exist,
// the default server may not be running, etc. — none of these should block
// session creation on the correct socket.
func (t *Tmux) killSplitBrainSession(name string) {
	if t.socketName == "" || t.socketName == "default" || t.socketName == noTownSocket {
		return // Already on default or no town context — nothing to clean up
	}
	other := NewTmuxWithSocket("default")
	if running, _ := other.HasSession(name); running {
		_ = other.KillSessionWithProcesses(name)
	}
}

// collectReparentedGroupMembers returns process group members that have been
// reparented to init (PPID == 1) but are not in the known descendant set.
// These are processes that were likely children in our tree but outlived their
// parent and got reparented to init while keeping the original PGID.
//
// This is safer than killing the entire process group blindly with
// syscall.Kill(-pgid, ...), which could hit unrelated processes if the PGID
// is shared or has been reused after the group leader exited.
func collectReparentedGroupMembers(pgid string, knownPIDs map[string]bool) []string {
	members := getProcessGroupMembers(pgid)
	var reparented []string
	for _, member := range members {
		if knownPIDs[member] {
			continue // Already in descendant list, will be handled there
		}
		// Check if reparented to init — probably was our child
		ppid := getParentPID(member)
		if ppid == "1" {
			reparented = append(reparented, member)
		}
		// Otherwise skip — this process is not in our tree and not reparented,
		// so it's likely unrelated and should not be killed
	}
	return reparented
}

// getAllDescendants recursively finds all descendant PIDs of a process.
// Returns PIDs in deepest-first order so killing them doesn't orphan grandchildren.
func getAllDescendants(pid string) []string {
	var result []string

	// Get direct children using pgrep
	out, err := exec.Command("pgrep", "-P", pid).Output()
	if err != nil {
		return result
	}

	children := strings.Fields(strings.TrimSpace(string(out)))
	for _, child := range children {
		// First add grandchildren (recursively) - deepest first
		result = append(result, getAllDescendants(child)...)
		// Then add this child
		result = append(result, child)
	}

	return result
}

// KillPaneProcesses explicitly kills all processes associated with a tmux pane.
// This prevents orphan processes that survive pane respawn due to SIGHUP being ignored.
//
// Process:
// 1. Get the pane's main process PID and its process group ID (PGID)
// 2. Kill the entire process group (catches reparented processes)
// 3. Find all descendant processes recursively (catches any stragglers)
// 4. Send SIGTERM/SIGKILL to descendants
// 5. Kill the pane process itself
//
// This ensures Claude processes and all their children are properly terminated
// before respawning the pane.
func (t *Tmux) KillPaneProcesses(pane string) error {
	// Get the pane PID
	pid, err := t.GetPanePID(pane)
	if err != nil {
		return fmt.Errorf("getting pane PID: %w", err)
	}

	if pid == "" {
		return fmt.Errorf("pane PID is empty")
	}

	// Walk the process tree for all descendants (catches processes that
	// called setsid() and created their own process groups)
	descendants := getAllDescendants(pid)

	// Build known PID set for group membership verification
	knownPIDs := make(map[string]bool, len(descendants)+1)
	knownPIDs[pid] = true
	for _, d := range descendants {
		knownPIDs[d] = true
	}

	// Find reparented processes from our process group. Instead of killing
	// the entire group blindly with syscall.Kill(-pgid, ...) — which could
	// hit unrelated processes sharing the same PGID — we enumerate group
	// members and only include those reparented to init (PPID == 1).
	pgid := getProcessGroupID(pid)
	if pgid != "" && pgid != "0" && pgid != "1" {
		reparented := collectReparentedGroupMembers(pgid, knownPIDs)
		descendants = append(descendants, reparented...)
	}

	// Send SIGTERM to all descendants (deepest first to avoid orphaning)
	for _, dpid := range descendants {
		_ = exec.Command("kill", "-TERM", dpid).Run()
	}

	// Wait for graceful shutdown (2s gives processes time to clean up)
	time.Sleep(processKillGracePeriod)

	// Send SIGKILL to any remaining descendants
	for _, dpid := range descendants {
		_ = exec.Command("kill", "-KILL", dpid).Run()
	}

	// Kill the pane process itself (may have called setsid() and detached,
	// or may have no children like Claude Code)
	_ = exec.Command("kill", "-TERM", pid).Run()
	time.Sleep(processKillGracePeriod)
	_ = exec.Command("kill", "-KILL", pid).Run()

	return nil
}

// KillPaneProcessesExcluding is like KillPaneProcesses but excludes specified PIDs
// from being killed. This is essential for self-handoff scenarios where the calling
// process (e.g., gt handoff running inside Claude Code) needs to survive long enough
// to call RespawnPane. Without exclusion, the caller would be killed before completing.
//
// The excluded PIDs should include the calling process and any ancestors that must
// survive. After this function returns, RespawnPane's -k flag will send SIGHUP to
// clean up the remaining processes.
func (t *Tmux) KillPaneProcessesExcluding(pane string, excludePIDs []string) error {
	// Build exclusion set for O(1) lookup
	exclude := make(map[string]bool)
	for _, pid := range excludePIDs {
		exclude[pid] = true
	}

	// Get the pane PID
	pid, err := t.GetPanePID(pane)
	if err != nil {
		return fmt.Errorf("getting pane PID: %w", err)
	}

	if pid == "" {
		return fmt.Errorf("pane PID is empty")
	}

	// Get all descendant PIDs recursively (returns deepest-first order)
	descendants := getAllDescendants(pid)

	// Filter out excluded PIDs
	var filtered []string
	for _, dpid := range descendants {
		if !exclude[dpid] {
			filtered = append(filtered, dpid)
		}
	}

	// Send SIGTERM to all non-excluded descendants (deepest first to avoid orphaning)
	for _, dpid := range filtered {
		_ = exec.Command("kill", "-TERM", dpid).Run()
	}

	// Wait for graceful shutdown
	time.Sleep(100 * time.Millisecond)

	// Send SIGKILL to any remaining non-excluded descendants
	for _, dpid := range filtered {
		_ = exec.Command("kill", "-KILL", dpid).Run()
	}

	// Kill the pane process itself only if not excluded
	if !exclude[pid] {
		_ = exec.Command("kill", "-TERM", pid).Run()
		time.Sleep(100 * time.Millisecond)
		_ = exec.Command("kill", "-KILL", pid).Run()
	}

	return nil
}

// ServerPID returns the PID of the tmux server process.
// Returns 0 if the server is not running or the PID cannot be determined.
func (t *Tmux) ServerPID() int {
	out, err := t.run("display-message", "-p", "#{pid}")
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(out))
	return pid
}

// KillServer terminates the entire tmux server and all sessions.
func (t *Tmux) KillServer() error {
	_, err := t.run("kill-server")
	if errors.Is(err, ErrNoServer) {
		return nil // Already dead
	}
	return err
}
