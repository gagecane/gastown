package tmux

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestPruneDeadTestSockets_RemovesDeadAndKeepsLive verifies that the janitor
// reaps gt-test-* socket files with no live listener (the leak from SIGKILLed
// test processes — gu-wb67v) while leaving sockets with a live server alone.
func TestPruneDeadTestSockets_RemovesDeadAndKeepsLive(t *testing.T) {
	dir := SocketDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}

	// Dead socket: a Unix listener we close immediately, leaving the file on
	// disk with nothing bound. A dial against it returns ECONNREFUSED — the
	// exact state of an orphaned gt-test-* socket.
	deadName := fmt.Sprintf("gt-test-prune-dead-%d", os.Getpid())
	deadPath := filepath.Join(dir, deadName)
	deadLn, err := net.Listen("unix", deadPath)
	if err != nil {
		t.Fatalf("create dead socket: %v", err)
	}
	// Keep the socket file on disk after Close (Go unlinks it by default), so
	// the file lingers with no listener — exactly an orphaned test socket.
	deadLn.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = deadLn.Close()
	t.Cleanup(func() { _ = os.Remove(deadPath) })
	if _, err := os.Stat(deadPath); err != nil {
		t.Fatalf("dead socket file should exist before prune: %v", err)
	}

	// Live socket: a listener we keep open for the duration of the test.
	liveName := fmt.Sprintf("gt-test-prune-live-%d", os.Getpid())
	livePath := filepath.Join(dir, liveName)
	liveLn, err := net.Listen("unix", livePath)
	if err != nil {
		t.Fatalf("create live socket: %v", err)
	}
	t.Cleanup(func() {
		_ = liveLn.Close()
		_ = os.Remove(livePath)
	})

	removed := PruneDeadTestSockets()
	if removed < 1 {
		t.Errorf("expected at least 1 socket reaped, got %d", removed)
	}

	// Dead socket file must be gone.
	if _, err := os.Stat(deadPath); !os.IsNotExist(err) {
		t.Errorf("dead socket %q should have been removed, stat err: %v", deadName, err)
	}
	// Live socket file must survive.
	if _, err := os.Stat(livePath); err != nil {
		t.Errorf("live socket %q should NOT have been removed, stat err: %v", liveName, err)
	}
}

// TestPruneDeadTestSockets_IgnoresNonTestSockets verifies the janitor only
// touches gt-test-* sockets, never the town socket or a user's personal one.
func TestPruneDeadTestSockets_IgnoresNonTestSockets(t *testing.T) {
	dir := SocketDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}

	// A dead, non-gt-test socket file. Must be left untouched.
	otherName := fmt.Sprintf("not-a-test-socket-%d", os.Getpid())
	otherPath := filepath.Join(dir, otherName)
	ln, err := net.Listen("unix", otherPath)
	if err != nil {
		t.Fatalf("create other socket: %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	t.Cleanup(func() { _ = os.Remove(otherPath) })

	PruneDeadTestSockets()

	if _, err := os.Stat(otherPath); err != nil {
		t.Errorf("non-test socket %q must not be reaped, stat err: %v", otherName, err)
	}
}

// TestPruneDeadTestSockets_KeepsLiveTmuxServer is an integration check: a real
// tmux server on a gt-test-* socket must survive a prune.
func TestPruneDeadTestSockets_KeepsLiveTmuxServer(t *testing.T) {
	if !hasTmux() {
		t.Skip("tmux not installed")
	}
	socket := fmt.Sprintf("gt-test-prune-tmux-%d", os.Getpid())
	if err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", "sentinel").Run(); err != nil {
		t.Fatalf("start tmux server: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
		_ = os.Remove(filepath.Join(SocketDir(), socket))
	})

	PruneDeadTestSockets()

	socketPath := filepath.Join(SocketDir(), socket)
	if _, err := os.Stat(socketPath); err != nil {
		t.Errorf("live tmux socket %q should survive prune, stat err: %v", socket, err)
	}
}
