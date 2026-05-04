// poller_supervisor.go exposes helpers that let the daemon supervise
// nudge-poller processes: listing live PID files, verifying liveness, and
// cleaning up stale entries. The actual supervisor loop lives in the
// daemon package (poller_dog) so this file stays focused on read-only
// introspection plus minimal cleanup.
//
// See gu-23z4 — without a supervisor, a poller that dies mid-session
// leaves nudges stranded forever.

package nudge

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PollerEntry describes a nudge-poller tracked via its PID file.
type PollerEntry struct {
	// Session is the tmux session name the poller serves. This is recovered
	// from the PID filename, which was produced by pollerPidFile() with '/'
	// replaced by '_'. The daemon treats it as opaque and passes it straight
	// back to StartPoller / tmux.HasSession, so round-tripping is not
	// required.
	Session string

	// PIDFile is the absolute path to the PID file.
	PIDFile string

	// PID is the PID read from the file. Zero when the file is missing or
	// unreadable.
	PID int

	// Alive reports whether the OS still has a process with this PID.
	// False when PID is zero or the process has exited.
	Alive bool
}

// ListPollers enumerates every PID file under the poller runtime directory.
// Corrupt or unreadable entries are returned with PID=0, Alive=false so the
// caller can decide whether to unlink them.
func ListPollers(townRoot string) ([]PollerEntry, error) {
	dir := pollerPidDir(townRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	out := make([]PollerEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".pid") {
			continue
		}

		session := strings.TrimSuffix(name, ".pid")
		pidPath := filepath.Join(dir, name)

		pe := PollerEntry{
			Session: session,
			PIDFile: pidPath,
		}

		data, err := os.ReadFile(pidPath) //nolint:gosec // G304: path constructed from our own dir listing
		if err != nil {
			out = append(out, pe)
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			// Corrupt PID file: reported as not-alive so caller can unlink.
			out = append(out, pe)
			continue
		}
		pe.PID = pid
		pe.Alive = pollerProcessAlive(pid)
		out = append(out, pe)
	}
	return out, nil
}

// RemoveStalePIDFile deletes the PID file for a session. The returned error
// is nil when the file was already gone. This exists so the supervisor can
// clean up stale entries without reimplementing path construction.
func RemoveStalePIDFile(townRoot, session string) error {
	path := pollerPidFile(townRoot, session)
	err := os.Remove(path)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}
