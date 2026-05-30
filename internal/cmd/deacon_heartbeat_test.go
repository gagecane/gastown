package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/session"
)

// setupTestTownForDeacon creates a minimal Gas Town workspace and chdirs into
// it so workspace.FindFromCwdOrError resolves. Returns the town root.
func setupTestTownForDeacon(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	// `mayor/` directory is enough to satisfy workspace.IsWorkspace.
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir town: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWd) })
	return townRoot
}

// readSessionHeartbeatRaw reads the runtime heartbeat file directly so the
// test can assert on its on-disk shape.
func readSessionHeartbeatRaw(t *testing.T, townRoot, sessionName string) (polecat.SessionHeartbeat, bool) {
	t.Helper()
	path := filepath.Join(townRoot, ".runtime", "heartbeats", sessionName+".json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return polecat.SessionHeartbeat{}, false
	}
	if err != nil {
		t.Fatalf("read heartbeat %s: %v", path, err)
	}
	var hb polecat.SessionHeartbeat
	if err := json.Unmarshal(data, &hb); err != nil {
		t.Fatalf("unmarshal heartbeat: %v", err)
	}
	return hb, true
}

// TestRunDeaconHeartbeat_GTSessionPresent verifies that when GT_SESSION is
// set, the runtime heartbeat is written under that session name with no
// fallback warning emitted.
func TestRunDeaconHeartbeat_GTSessionPresent(t *testing.T) {
	townRoot := setupTestTownForDeacon(t)
	t.Setenv("GT_SESSION", "hq-deacon")
	t.Setenv("GT_ROLE", "deacon")

	stderr := captureStderr(t, func() {
		if err := runDeaconHeartbeat(&cobra.Command{}, []string{"test-action"}); err != nil {
			t.Fatalf("runDeaconHeartbeat: %v", err)
		}
	})

	if strings.Contains(stderr, "GT_SESSION not set") {
		t.Errorf("unexpected fallback warning when GT_SESSION is set: %q", stderr)
	}

	hb, ok := readSessionHeartbeatRaw(t, townRoot, "hq-deacon")
	if !ok {
		t.Fatalf("runtime heartbeat file not written for hq-deacon")
	}
	if hb.State != polecat.HeartbeatWorking {
		t.Errorf("hb.State = %q, want %q", hb.State, polecat.HeartbeatWorking)
	}
	if hb.Context != "test-action" {
		t.Errorf("hb.Context = %q, want %q", hb.Context, "test-action")
	}
	if time.Since(hb.Timestamp) > 5*time.Second {
		t.Errorf("hb.Timestamp too old: %v", hb.Timestamp)
	}
}

// TestRunDeaconHeartbeat_GTSessionMissing covers the gu-em89 fix: even when
// GT_SESSION is missing from the invoking process, the runtime heartbeat
// still refreshes because we fall back to the deterministic deacon session
// name. A warning is emitted to stderr so the env loss is visible.
func TestRunDeaconHeartbeat_GTSessionMissing(t *testing.T) {
	townRoot := setupTestTownForDeacon(t)
	t.Setenv("GT_SESSION", "")
	t.Setenv("GT_ROLE", "deacon")

	stderr := captureStderr(t, func() {
		if err := runDeaconHeartbeat(&cobra.Command{}, nil); err != nil {
			t.Fatalf("runDeaconHeartbeat: %v", err)
		}
	})

	want := session.DeaconSessionName()
	hb, ok := readSessionHeartbeatRaw(t, townRoot, want)
	if !ok {
		t.Fatalf("runtime heartbeat file not written under fallback name %q", want)
	}
	if hb.State != polecat.HeartbeatWorking {
		t.Errorf("hb.State = %q, want %q", hb.State, polecat.HeartbeatWorking)
	}
	if !strings.Contains(stderr, "GT_SESSION not set") {
		t.Errorf("expected fallback warning, got: %q", stderr)
	}
	if !strings.Contains(stderr, want) {
		t.Errorf("warning should include fallback name %q, got: %q", want, stderr)
	}
	if !strings.Contains(stderr, "gu-em89") {
		t.Errorf("warning should reference gu-em89 for traceability, got: %q", stderr)
	}
}

// TestRunDeaconHeartbeat_AlsoUpdatesDeaconHeartbeat verifies that the deacon
// patrol heartbeat file (deacon/heartbeat.json) is still written by the same
// command — confirming the gu-em89 change is additive and didn't regress the
// pre-existing behavior the daemon relies on for stuck-deacon detection.
func TestRunDeaconHeartbeat_AlsoUpdatesDeaconHeartbeat(t *testing.T) {
	townRoot := setupTestTownForDeacon(t)
	t.Setenv("GT_SESSION", "hq-deacon")

	if err := runDeaconHeartbeat(&cobra.Command{}, []string{"action"}); err != nil {
		t.Fatalf("runDeaconHeartbeat: %v", err)
	}

	deaconHB := filepath.Join(townRoot, "deacon", "heartbeat.json")
	if _, err := os.Stat(deaconHB); err != nil {
		t.Errorf("deacon/heartbeat.json should be written: %v", err)
	}
}
