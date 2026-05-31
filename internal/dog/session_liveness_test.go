package dog

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/tmux"
)

// tmuxBinAvailable reports whether the tmux binary is on PATH.
func tmuxBinAvailable() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// TestIsRunningDetectsZombieSession verifies that SessionManager.IsRunning
// distinguishes a healthy agent session from a zombie (tmux alive, agent dead),
// while SessionExists still reports the underlying tmux session.
//
// This is the gs-49s regression guard: before the fix, IsRunning only checked
// tmux session existence, so the daemon treated a zombie dog session as a live
// worker and assigned it a hook/mail that no agent ever consumed. A pane
// running a bare `sleep` (no claude/node) is exactly such a zombie.
func TestIsRunningDetectsZombieSession(t *testing.T) {
	if !tmuxBinAvailable() {
		t.Skip("tmux not available, skipping liveness integration test")
	}

	socket := fmt.Sprintf("gt-test-dog-liveness-%d", os.Getpid())
	tmx := tmux.NewTmuxWithSocket(socket)
	t.Cleanup(func() {
		_ = tmx.KillServer()
	})
	mgr := NewManager(t.TempDir(), &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}})
	sm := NewSessionManager(tmx, t.TempDir(), mgr)

	// Spin up a zombie session: tmux pane alive but running `sleep`, not an agent.
	zombieSession := sm.SessionName("zombie")
	if err := tmx.NewSessionWithCommand(zombieSession, "", "sleep 300"); err != nil {
		t.Fatalf("creating zombie session: %v", err)
	}

	// Wait for the session to actually appear.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if has, _ := tmx.HasSession(zombieSession); has {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// SessionExists must report the tmux session as present.
	exists, err := sm.SessionExists("zombie")
	if err != nil {
		t.Fatalf("SessionExists error: %v", err)
	}
	if !exists {
		t.Fatal("SessionExists(zombie) = false, want true (tmux session is alive)")
	}

	// IsRunning must report the zombie as NOT running — no live agent inside.
	running, err := sm.IsRunning("zombie")
	if err != nil {
		t.Fatalf("IsRunning error: %v", err)
	}
	if running {
		t.Error("IsRunning(zombie) = true, want false (no live agent in pane)")
	}

	// A dog with no session at all: both checks false.
	if exists, _ := sm.SessionExists("ghost"); exists {
		t.Error("SessionExists(ghost) = true, want false (no session)")
	}
	if running, _ := sm.IsRunning("ghost"); running {
		t.Error("IsRunning(ghost) = true, want false (no session)")
	}
}
