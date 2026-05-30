package nudge

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/util"
)

func TestPollerPidFile(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-gastown-crew-bear"

	pidFile := pollerPidFile(townRoot, session)
	expected := filepath.Join(townRoot, ".runtime", "nudge_poller", session+".pid")
	if pidFile != expected {
		t.Errorf("pollerPidFile() = %q, want %q", pidFile, expected)
	}
}

func TestPollerPidFile_SlashSanitized(t *testing.T) {
	townRoot := t.TempDir()
	session := "some/session"

	pidFile := pollerPidFile(townRoot, session)
	// Slashes should be replaced with underscores
	expected := filepath.Join(townRoot, ".runtime", "nudge_poller", "some_session.pid")
	if pidFile != expected {
		t.Errorf("pollerPidFile() = %q, want %q", pidFile, expected)
	}
}

func TestPollerAlive_NoPidFile(t *testing.T) {
	townRoot := t.TempDir()
	_, alive := pollerAlive(townRoot, "nonexistent-session")
	if alive {
		t.Error("pollerAlive() returned true for nonexistent PID file")
	}
}

func TestPollerAlive_StalePid(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-gastown-crew-test"

	// Write a PID file with an invalid PID (process doesn't exist).
	pidDir := pollerPidDir(townRoot)
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	}
	pidPath := pollerPidFile(townRoot, session)
	// Use a very high PID that's almost certainly not running.
	if err := os.WriteFile(pidPath, []byte("999999999"), 0644); err != nil {
		t.Fatal(err)
	}

	_, alive := pollerAlive(townRoot, session)
	if alive {
		t.Error("pollerAlive() returned true for dead PID")
	}

	// Stale PID file should be cleaned up.
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("stale PID file was not cleaned up")
	}
}

func TestPollerAlive_CorruptPidFile(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-gastown-crew-test"

	pidDir := pollerPidDir(townRoot)
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	}
	pidPath := pollerPidFile(townRoot, session)
	if err := os.WriteFile(pidPath, []byte("not-a-number"), 0644); err != nil {
		t.Fatal(err)
	}

	_, alive := pollerAlive(townRoot, session)
	if alive {
		t.Error("pollerAlive() returned true for corrupt PID file")
	}
}

func TestStopPoller_NoPidFile(t *testing.T) {
	townRoot := t.TempDir()
	// Should be a no-op, no error.
	if err := StopPoller(townRoot, "nonexistent"); err != nil {
		t.Errorf("StopPoller() unexpected error: %v", err)
	}
}

func TestStopPoller_StalePid(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-gastown-crew-test"

	// Write a stale PID file.
	pidDir := pollerPidDir(townRoot)
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	}
	pidPath := pollerPidFile(townRoot, session)
	if err := os.WriteFile(pidPath, []byte("999999999"), 0644); err != nil {
		t.Fatal(err)
	}

	// Should succeed and clean up the stale PID file.
	if err := StopPoller(townRoot, session); err != nil {
		t.Errorf("StopPoller() unexpected error: %v", err)
	}

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("StopPoller did not clean up stale PID file")
	}
}

func TestPollerAlive_LiveProcess(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-gastown-crew-test"

	// Write our own PID — we're definitely alive.
	pidDir := pollerPidDir(townRoot)
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	}
	pidPath := pollerPidFile(townRoot, session)
	myPid := os.Getpid()
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(myPid)), 0644); err != nil {
		t.Fatal(err)
	}

	pid, alive := pollerAlive(townRoot, session)
	if !alive {
		t.Error("pollerAlive() returned false for live process")
	}
	if pid != myPid {
		t.Errorf("pollerAlive() pid = %d, want %d", pid, myPid)
	}
}

func TestWritePIDFile_RefreshesContent(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-gastown-crew-bear"

	// First write — creates the runtime dir and the file.
	if err := WritePIDFile(townRoot, session, 1234); err != nil {
		t.Fatalf("WritePIDFile() first write: %v", err)
	}
	pidPath := pollerPidFile(townRoot, session)
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("reading pid file: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "1234" {
		t.Errorf("pid file = %q, want %q", got, "1234")
	}

	// Second write with a new PID must overwrite (refresh on restart).
	if err := WritePIDFile(townRoot, session, 5678); err != nil {
		t.Fatalf("WritePIDFile() refresh: %v", err)
	}
	data, err = os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("reading pid file after refresh: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "5678" {
		t.Errorf("refreshed pid file = %q, want %q", got, "5678")
	}
}

func TestReleaseOwnPIDFile_RemovesOwnEntry(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-gastown-crew-bear"

	if err := WritePIDFile(townRoot, session, 4242); err != nil {
		t.Fatalf("WritePIDFile(): %v", err)
	}
	if err := ReleaseOwnPIDFile(townRoot, session, 4242); err != nil {
		t.Fatalf("ReleaseOwnPIDFile(): %v", err)
	}
	if _, err := os.Stat(pollerPidFile(townRoot, session)); !os.IsNotExist(err) {
		t.Error("ReleaseOwnPIDFile did not remove our own PID file")
	}
}

func TestReleaseOwnPIDFile_LeavesSuccessorEntry(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-gastown-crew-bear"

	// A successor poller has already claimed the file with its own PID.
	if err := WritePIDFile(townRoot, session, 9999); err != nil {
		t.Fatalf("WritePIDFile(): %v", err)
	}
	// The departing poller (pid 1111) must NOT delete the successor's entry.
	if err := ReleaseOwnPIDFile(townRoot, session, 1111); err != nil {
		t.Fatalf("ReleaseOwnPIDFile(): %v", err)
	}
	data, err := os.ReadFile(pollerPidFile(townRoot, session))
	if err != nil {
		t.Fatalf("successor pid file should survive: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "9999" {
		t.Errorf("successor pid file = %q, want %q", got, "9999")
	}
}

func TestReleaseOwnPIDFile_MissingFileIsNoError(t *testing.T) {
	townRoot := t.TempDir()
	if err := ReleaseOwnPIDFile(townRoot, "no-such-session", 1); err != nil {
		t.Errorf("ReleaseOwnPIDFile() on missing file: unexpected error %v", err)
	}
}

func TestBuildPollerCommand_UsesDetachedProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process group management is not supported on Windows")
	}
	townRoot := t.TempDir()
	cmd := buildPollerCommand("/tmp/fake-gt", townRoot, "gt-gastown-crew-bear")

	if got, want := cmd.Dir, townRoot; got != want {
		t.Fatalf("cmd.Dir = %q, want %q", got, want)
	}
	if got, want := cmd.Path, "/tmp/fake-gt"; got != want {
		t.Fatalf("cmd.Path = %q, want %q", got, want)
	}
	if len(cmd.Args) != 3 || cmd.Args[1] != "nudge-poller" || cmd.Args[2] != "gt-gastown-crew-bear" {
		t.Fatalf("cmd.Args = %#v, want poller invocation", cmd.Args)
	}
	if cmd.Cancel != nil {
		t.Fatal("buildPollerCommand() installed cmd.Cancel; detached pollers must leave it nil")
	}
	if cmd.Stdout != nil || cmd.Stderr != nil {
		t.Fatal("buildPollerCommand() should discard stdout/stderr")
	}
	if cmd.SysProcAttr == nil {
		t.Fatal("buildPollerCommand() did not configure SysProcAttr")
	}
}

func TestSetProcessGroup_InstallsCancelHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SetProcessGroup is a no-op on Windows")
	}
	cmd := exec.Command("true")
	util.SetProcessGroup(cmd)

	if cmd.Cancel == nil {
		t.Fatal("SetProcessGroup() should install a cancel hook")
	}
}
