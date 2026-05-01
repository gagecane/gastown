package cmd

import (
	"errors"
	"strings"
	"testing"
)

// TestStartSession_EmptyHookPreCondition_Refuses verifies that StartSession
// refuses to launch a tmux session when no work bead is hooked to the polecat.
// This is the gu-56ik guard: the scheduler must spawn a polecat AND attach
// work to it atomically. If the hook is empty when StartSession is called,
// something upstream silently skipped or failed the sling step — starting the
// session would produce a polecat that primes, finds no work, and escalates.
func TestStartSession_EmptyHookPreCondition_Refuses(t *testing.T) {
	orig := verifyHookedWorkForAgent
	t.Cleanup(func() { verifyHookedWorkForAgent = orig })

	sentinel := errors.New("no hooked or in-progress work bead found for agent test-agent (stub)")
	verifyHookedWorkForAgent = func(agentID, beadsDir string) error {
		if agentID != "testrig/polecats/toast" {
			t.Fatalf("unexpected agentID passed to guard: %q", agentID)
		}
		return sentinel
	}

	info := &SpawnedPolecatInfo{
		RigName:     "testrig",
		PolecatName: "toast",
	}

	_, err := info.StartSession()
	if err == nil {
		t.Fatal("expected StartSession to fail when guard reports no hooked work, got nil")
	}
	if !strings.Contains(err.Error(), "refusing to start session") {
		t.Errorf("error should mention 'refusing to start session', got: %v", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error should wrap the guard's error via %%w, got: %v", err)
	}
	if info.Pane != "" {
		t.Errorf("Pane should remain empty when start is refused, got %q", info.Pane)
	}
}

// TestStartSession_AlreadyStarted_SkipsGuard verifies that StartSession is a
// no-op when the session has already been started, regardless of guard state.
// The guard only applies to the initial launch; a cached Pane indicates the
// session is already running and any work-attachment invariants were enforced
// on the original call.
func TestStartSession_AlreadyStarted_SkipsGuard(t *testing.T) {
	orig := verifyHookedWorkForAgent
	t.Cleanup(func() { verifyHookedWorkForAgent = orig })

	guardCalled := false
	verifyHookedWorkForAgent = func(agentID, beadsDir string) error {
		guardCalled = true
		return errors.New("guard must not run when session already started")
	}

	info := &SpawnedPolecatInfo{
		RigName:     "testrig",
		PolecatName: "toast",
		Pane:        "existing-pane-id",
	}

	pane, err := info.StartSession()
	if err != nil {
		t.Fatalf("StartSession on already-started session should return nil error, got: %v", err)
	}
	if pane != "existing-pane-id" {
		t.Errorf("expected cached pane 'existing-pane-id', got %q", pane)
	}
	if guardCalled {
		t.Error("guard should not be invoked when session is already started")
	}
}
