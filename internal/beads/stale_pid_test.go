package beads

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestCleanStaleDoltServerPID_NoPIDFile is a no-op when the PID file is
// missing.
func TestCleanStaleDoltServerPID_NoPIDFile(t *testing.T) {
	dir := t.TempDir()
	// No dolt/ subdirectory or dolt-server.pid — should silently succeed.
	CleanStaleDoltServerPID(dir)
}

// TestCleanStaleDoltServerPID_CorruptPIDFile removes a PID file with
// unparseable contents.
func TestCleanStaleDoltServerPID_CorruptPIDFile(t *testing.T) {
	dir := t.TempDir()
	doltDir := filepath.Join(dir, "dolt")
	if err := os.MkdirAll(doltDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pidPath := filepath.Join(doltDir, "dolt-server.pid")
	if err := os.WriteFile(pidPath, []byte("not-a-number"), 0600); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	CleanStaleDoltServerPID(dir)

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("corrupt PID file was not removed: stat err = %v", err)
	}
}

// TestCleanStaleDoltServerPID_NegativePID treats <= 0 PIDs as corrupt and
// removes the file.
func TestCleanStaleDoltServerPID_NegativePID(t *testing.T) {
	dir := t.TempDir()
	doltDir := filepath.Join(dir, "dolt")
	if err := os.MkdirAll(doltDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pidPath := filepath.Join(doltDir, "dolt-server.pid")
	if err := os.WriteFile(pidPath, []byte("-42\n"), 0600); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	CleanStaleDoltServerPID(dir)

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("negative PID file was not removed: stat err = %v", err)
	}
}

// TestCleanStaleDoltServerPID_ZeroPID treats a PID of 0 as corrupt.
func TestCleanStaleDoltServerPID_ZeroPID(t *testing.T) {
	dir := t.TempDir()
	doltDir := filepath.Join(dir, "dolt")
	if err := os.MkdirAll(doltDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pidPath := filepath.Join(doltDir, "dolt-server.pid")
	if err := os.WriteFile(pidPath, []byte("0"), 0600); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	CleanStaleDoltServerPID(dir)

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("zero PID file was not removed: stat err = %v", err)
	}
}

// TestCleanStaleDoltServerPID_StaleDeadProcess removes the PID file when the
// referenced process is dead. We pick a PID unlikely to be alive by looking
// for gaps in the PID space.
func TestCleanStaleDoltServerPID_StaleDeadProcess(t *testing.T) {
	dir := t.TempDir()
	doltDir := filepath.Join(dir, "dolt")
	if err := os.MkdirAll(doltDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pidPath := filepath.Join(doltDir, "dolt-server.pid")

	deadPID := findDeadPID(t)
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", deadPID)), 0600); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	CleanStaleDoltServerPID(dir)

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("stale PID file (dead process %d) was not removed: stat err = %v", deadPID, err)
	}
}

// TestCleanStaleDoltServerPID_LivePIDKept leaves the file alone when the PID
// belongs to a live process — this test uses the current process.
func TestCleanStaleDoltServerPID_LivePIDKept(t *testing.T) {
	dir := t.TempDir()
	doltDir := filepath.Join(dir, "dolt")
	if err := os.MkdirAll(doltDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pidPath := filepath.Join(doltDir, "dolt-server.pid")
	myPID := os.Getpid()
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", myPID)), 0600); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	CleanStaleDoltServerPID(dir)

	if _, err := os.Stat(pidPath); err != nil {
		t.Errorf("live PID file was incorrectly removed: %v", err)
	}
}

// TestCleanStaleDoltServerPID_WhitespaceHandling strips whitespace from PID
// content before parsing.
func TestCleanStaleDoltServerPID_WhitespaceHandling(t *testing.T) {
	dir := t.TempDir()
	doltDir := filepath.Join(dir, "dolt")
	if err := os.MkdirAll(doltDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pidPath := filepath.Join(doltDir, "dolt-server.pid")
	// Write our own PID with surrounding whitespace — should still be parsed
	// and detected as alive.
	content := fmt.Sprintf("   %d   \n\n", os.Getpid())
	if err := os.WriteFile(pidPath, []byte(content), 0600); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	CleanStaleDoltServerPID(dir)

	if _, err := os.Stat(pidPath); err != nil {
		t.Errorf("live PID file was incorrectly removed after whitespace trim: %v", err)
	}
}

// findDeadPID returns a PID value that is not currently alive. Iterates from a
// high value downward and checks liveness via signal 0.
func findDeadPID(t *testing.T) int {
	t.Helper()
	// Try a range of high-ish PIDs unlikely to be in use.
	for pid := 99990; pid > 10000; pid-- {
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			// Process doesn't exist — this is a dead PID we can use.
			return pid
		}
	}
	t.Skip("could not find a dead PID in the expected range; skipping")
	return 0
}
