package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/session"
)

// resolveHeartbeatSession and the heartbeat/keepalive commands derive the
// session name when GT_SESSION is missing (gu-urr85). These tests run with
// TMUX unset so the tmux-pane branch is skipped and the role-derived fallback
// is exercised deterministically.

func TestResolveHeartbeatSession_EnvWins(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("GT_SESSION", "hq-deacon")
	t.Setenv("GT_ROLE", "deacon")

	name, source := resolveHeartbeatSession()
	if name != "hq-deacon" {
		t.Errorf("name = %q, want hq-deacon", name)
	}
	if source != "env" {
		t.Errorf("source = %q, want env", source)
	}
}

func TestResolveHeartbeatSession_RoleDerivedDeacon(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("GT_SESSION", "")
	t.Setenv("GT_ROLE", "deacon")

	name, source := resolveHeartbeatSession()
	if want := session.DeaconSessionName(); name != want {
		t.Errorf("name = %q, want %q", name, want)
	}
	if source != "role" {
		t.Errorf("source = %q, want role", source)
	}
}

func TestResolveHeartbeatSession_RoleDerivedWitness(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("GT_SESSION", "")
	t.Setenv("GT_ROLE", "gastown/witness")
	t.Setenv("GT_RIG", "gastown")

	name, source := resolveHeartbeatSession()
	if name == "" {
		t.Fatalf("expected a derived witness session name, got empty")
	}
	if !strings.HasSuffix(name, "-witness") {
		t.Errorf("name = %q, want a *-witness session name", name)
	}
	if source != "role" {
		t.Errorf("source = %q, want role", source)
	}
}

func TestResolveHeartbeatSession_NoSession(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("GT_SESSION", "")
	// A multi-instance role (crew) has no derivable singleton name, so the
	// resolver returns empty rather than guessing.
	t.Setenv("GT_ROLE", "gastown/crew/jane")
	t.Setenv("GT_RIG", "gastown")

	name, source := resolveHeartbeatSession()
	if name != "" {
		t.Errorf("crew should not derive a session name, got %q (source %q)", name, source)
	}
}

// TestRunHeartbeatKeepalive_DerivesWhenSessionMissing is the core gu-urr85
// regression: keepalive must refresh the runtime heartbeat (not silently
// no-op) when GT_SESSION is empty but the role is a derivable singleton.
func TestRunHeartbeatKeepalive_DerivesWhenSessionMissing(t *testing.T) {
	townRoot := setupTestTownForDeacon(t)
	t.Setenv("TMUX", "")
	t.Setenv("GT_SESSION", "")
	t.Setenv("GT_ROLE", "deacon")

	stderr := captureStderr(t, func() {
		if err := runHeartbeatKeepalive(&cobra.Command{}, nil); err != nil {
			t.Fatalf("runHeartbeatKeepalive: %v", err)
		}
	})

	want := session.DeaconSessionName()
	path := filepath.Join(townRoot, ".runtime", "heartbeats", want+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("keepalive should have written runtime heartbeat %s: %v", path, err)
	}
	if !strings.Contains(stderr, "gu-urr85") {
		t.Errorf("expected warning referencing gu-urr85, got: %q", stderr)
	}
	if !strings.Contains(stderr, want) {
		t.Errorf("warning should include derived session %q, got: %q", want, stderr)
	}
}

// TestRunHeartbeatKeepalive_NoOpWhenUnresolvable confirms the command still
// exits 0 (build wrappers must not break) and warns when no session can be
// derived at all.
func TestRunHeartbeatKeepalive_NoOpWhenUnresolvable(t *testing.T) {
	setupTestTownForDeacon(t)
	t.Setenv("TMUX", "")
	t.Setenv("GT_SESSION", "")
	t.Setenv("GT_ROLE", "gastown/crew/jane")
	t.Setenv("GT_RIG", "gastown")

	stderr := captureStderr(t, func() {
		if err := runHeartbeatKeepalive(&cobra.Command{}, nil); err != nil {
			t.Fatalf("runHeartbeatKeepalive should not error on unresolvable session: %v", err)
		}
	})

	if !strings.Contains(stderr, "no session could be derived") {
		t.Errorf("expected no-op warning, got: %q", stderr)
	}
}

// TestRunHeartbeat_DerivesWhenSessionMissing verifies the `--state` path also
// derives the session instead of erroring when GT_SESSION is empty.
func TestRunHeartbeat_DerivesWhenSessionMissing(t *testing.T) {
	townRoot := setupTestTownForDeacon(t)
	t.Setenv("TMUX", "")
	t.Setenv("GT_SESSION", "")
	t.Setenv("GT_ROLE", "deacon")
	heartbeatState = "working"

	stderr := captureStderr(t, func() {
		if err := runHeartbeat(&cobra.Command{}, []string{"ctx"}); err != nil {
			t.Fatalf("runHeartbeat should derive session, not error: %v", err)
		}
	})

	want := session.DeaconSessionName()
	hb, ok := readSessionHeartbeatRaw(t, townRoot, want)
	if !ok {
		t.Fatalf("runtime heartbeat not written under derived name %q", want)
	}
	if hb.State != polecat.HeartbeatWorking {
		t.Errorf("hb.State = %q, want %q", hb.State, polecat.HeartbeatWorking)
	}
	if !strings.Contains(stderr, "gu-urr85") {
		t.Errorf("expected warning referencing gu-urr85, got: %q", stderr)
	}
}
