package daemon

import (
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"

	"github.com/steveyegge/gastown/internal/estop"
)

// testStartupDispatchDaemon builds a minimal Daemon rooted at townRoot with a
// discarded logger, sufficient for exercising shouldDispatchOnStartup.
func testStartupDispatchDaemon(townRoot string) *Daemon {
	return &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(os.Stderr, "test: ", log.LstdFlags),
	}
}

// TestShouldDispatchOnStartup_CleanTown verifies the immediate startup dispatch
// is allowed on a clean town (no shutdown, no E-stop, pressure disabled by
// default). This is the common bounce case the fix targets (gu-n3u77).
func TestShouldDispatchOnStartup_CleanTown(t *testing.T) {
	d := testStartupDispatchDaemon(t.TempDir())
	if !d.shouldDispatchOnStartup() {
		t.Error("expected startup dispatch to be allowed on a clean town")
	}
}

// TestShouldDispatchOnStartup_ShutdownInProgress verifies the startup dispatch
// is suppressed while a shutdown holds the shutdown.lock, mirroring the
// heartbeat's own isShutdownInProgress gate.
func TestShouldDispatchOnStartup_ShutdownInProgress(t *testing.T) {
	tmpDir := t.TempDir()
	lockDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(lockDir, "shutdown.lock")

	// Hold the shutdown lock to simulate an active shutdown.
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		t.Fatalf("failed to acquire shutdown lock: %v", err)
	}
	if !locked {
		t.Fatal("expected to acquire shutdown lock")
	}
	defer func() { _ = lock.Unlock() }()

	d := testStartupDispatchDaemon(tmpDir)
	if d.shouldDispatchOnStartup() {
		t.Error("expected startup dispatch to be suppressed while shutdown is in progress")
	}
}

// TestShouldDispatchOnStartup_EStopActive verifies the startup dispatch is
// suppressed while E-stop is active, mirroring the heartbeat's estop gate so a
// restart does not spawn work that was intentionally frozen.
func TestShouldDispatchOnStartup_EStopActive(t *testing.T) {
	tmpDir := t.TempDir()
	if err := estop.Activate(tmpDir, "test", "verifying startup dispatch respects E-stop"); err != nil {
		t.Fatalf("failed to activate E-stop: %v", err)
	}

	d := testStartupDispatchDaemon(tmpDir)
	if d.shouldDispatchOnStartup() {
		t.Error("expected startup dispatch to be suppressed while E-stop is active")
	}
}
