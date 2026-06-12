package beads

import (
	"os"
	"path/filepath"
	"testing"
)

// TestForceAutoStartOffWhenServerTargeted_ForcesOffOnServerTarget verifies that
// any resolved env carrying a managed-server target gets BEADS_DOLT_AUTO_START=0,
// so a bd subprocess cannot auto-spawn a rogue embedded server that hijacks the
// shared port (gu-u7mcl; RCA hq:gc-o4yt68).
func TestForceAutoStartOffWhenServerTargeted_ForcesOffOnServerTarget(t *testing.T) {
	cases := []struct {
		name string
		key  string
		val  string
	}{
		{"GT_DOLT_PORT", "GT_DOLT_PORT", "3307"},
		{"BEADS_DOLT_PORT", "BEADS_DOLT_PORT", "3307"},
		{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_SERVER_PORT", "3307"},
		{"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_HOST", "127.0.0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := forceAutoStartOffWhenServerTargeted([]string{"PATH=/usr/bin", tc.key + "=" + tc.val})
			if got := envValue(env, "BEADS_DOLT_AUTO_START"); got != "0" {
				t.Fatalf("BEADS_DOLT_AUTO_START = %q, want 0 when %s is set; env=%v", got, tc.key, env)
			}
		})
	}
}

// TestForceAutoStartOffWhenServerTargeted_OverridesInheritedOn verifies that an
// inherited BEADS_DOLT_AUTO_START=1 is overridden (not duplicated) to 0 when a
// server target is present.
func TestForceAutoStartOffWhenServerTargeted_OverridesInheritedOn(t *testing.T) {
	env := forceAutoStartOffWhenServerTargeted([]string{
		"PATH=/usr/bin",
		"GT_DOLT_PORT=3307",
		"BEADS_DOLT_AUTO_START=1",
	})
	if got := envValue(env, "BEADS_DOLT_AUTO_START"); got != "0" {
		t.Fatalf("BEADS_DOLT_AUTO_START = %q, want 0 (inherited =1 must be overridden); env=%v", got, env)
	}
	var count int
	for _, e := range env {
		if len(e) >= len("BEADS_DOLT_AUTO_START=") && e[:len("BEADS_DOLT_AUTO_START=")] == "BEADS_DOLT_AUTO_START=" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("BEADS_DOLT_AUTO_START appears %d times, want exactly 1; env=%v", count, env)
	}
}

// TestForceAutoStartOffWhenServerTargeted_LeavesEmbeddedUntouched verifies that
// when no managed-server target is present (bare embedded/local use), the
// auto-start guard is NOT injected — legitimate embedded bd must still auto-start.
func TestForceAutoStartOffWhenServerTargeted_LeavesEmbeddedUntouched(t *testing.T) {
	env := forceAutoStartOffWhenServerTargeted([]string{
		"PATH=/usr/bin",
		"BEADS_DOLT_DATA_DIR=/tmp/embedded/.dolt-data",
	})
	if _, ok := envMap(env)["BEADS_DOLT_AUTO_START"]; ok {
		t.Fatalf("BEADS_DOLT_AUTO_START should be absent for embedded (no server target); env=%v", env)
	}
}

// TestBuildPinnedBDEnv_ForcesAutoStartOffForServerRig verifies the end-to-end
// guard: a rig pinned to a server-mode metadata.json yields an env with
// BEADS_DOLT_AUTO_START=0 even though the caller never set it. This is the
// chokepoint that stops a stray bd from auto-creating a rogue empty store on the
// shared port (gu-u7mcl).
func TestBuildPinnedBDEnv_ForcesAutoStartOffForServerRig(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"dolt_database":"rigdb","dolt_server_host":"127.0.0.1","dolt_server_port":3307}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0o644); err != nil {
		t.Fatal(err)
	}

	env := BuildPinnedBDEnv([]string{"PATH=/usr/bin"}, beadsDir)
	if got := envValue(env, "BEADS_DOLT_AUTO_START"); got != "0" {
		t.Fatalf("BuildPinnedBDEnv did not force auto-start off for a server-mode rig; got %q in %v", got, env)
	}
}

// TestBuildRoutingBDEnv_ForcesAutoStartOffWithGTDoltPort verifies the routing env
// builder also forces the guard when the daemon-exported GT_DOLT_PORT is present.
func TestBuildRoutingBDEnv_ForcesAutoStartOffWithGTDoltPort(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	env := BuildRoutingBDEnv([]string{"PATH=/usr/bin", "GT_DOLT_PORT=3307"}, beadsDir)
	if got := envValue(env, "BEADS_DOLT_AUTO_START"); got != "0" {
		t.Fatalf("BuildRoutingBDEnv did not force auto-start off when GT_DOLT_PORT is set; got %q in %v", got, env)
	}
}
