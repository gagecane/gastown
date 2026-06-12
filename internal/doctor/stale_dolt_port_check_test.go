package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestStaleDoltPortCheck_ConsistentPorts verifies the check passes when all ports are consistent.
func TestStaleDoltPortCheck_ConsistentPorts(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .dolt-data/config.yaml with port 3307
	doltDataDir := filepath.Join(tmpDir, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	configYaml := `log_level: warning
listener:
  port: 3307
`
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"), []byte(configYaml), 0644); err != nil {
		t.Fatal(err)
	}

	// Create town .beads/metadata.json with consistent port
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]interface{}{
		"dolt_mode":        "server",
		"dolt_server_host": "127.0.0.1",
		"dolt_server_port": 3307,
		"dolt_database":    "hq",
	}
	metadataBytes, _ := json.MarshalIndent(metadata, "", "  ")
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadataBytes, 0644); err != nil {
		t.Fatal(err)
	}

	// Create dolt-server.port file with consistent port
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte("3307"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewStaleDoltPortCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK for consistent ports, got %v: %s", result.Status, result.Message)
	}
}

// TestStaleDoltPortCheck_InconsistentMetadata verifies the check detects wrong port in metadata.json.
func TestStaleDoltPortCheck_InconsistentMetadata(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .dolt-data/config.yaml with port 3307
	doltDataDir := filepath.Join(tmpDir, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	configYaml := `log_level: warning
listener:
  port: 3307
`
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"), []byte(configYaml), 0644); err != nil {
		t.Fatal(err)
	}

	// Create town .beads/metadata.json with WRONG port (3306 instead of 3307)
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]interface{}{
		"dolt_mode":        "server",
		"dolt_server_host": "127.0.0.1",
		"dolt_server_port": 3306, // Wrong port!
		"dolt_database":    "hq",
	}
	metadataBytes, _ := json.MarshalIndent(metadata, "", "  ")
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadataBytes, 0644); err != nil {
		t.Fatal(err)
	}

	check := NewStaleDoltPortCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning for inconsistent port, got %v: %s", result.Status, result.Message)
	}

	// Should mention the wrong port in details
	if len(result.Details) == 0 {
		t.Error("expected details to contain port mismatch info")
	}

	t.Logf("Result: status=%v, message=%s, details=%v", result.Status, result.Message, result.Details)
}

// writeDoltDataConfig writes a minimal .dolt-data/config.yaml declaring the
// shared server port, so DefaultConfig resolves to that port.
func writeDoltDataConfig(t *testing.T, townRoot string, port int) {
	t.Helper()
	doltDataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	configYaml := "log_level: warning\nlistener:\n  port: " + strconv.Itoa(port) + "\n"
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"), []byte(configYaml), 0644); err != nil {
		t.Fatal(err)
	}
}

// writeEmbeddedDoltConfig writes a .beads/dolt/config.yaml under beadsParent
// declaring the given listener port (pass 0 to omit the port directive).
func writeEmbeddedDoltConfig(t *testing.T, beadsParent string, port int) {
	t.Helper()
	doltDir := filepath.Join(beadsParent, ".beads", "dolt")
	if err := os.MkdirAll(doltDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "log_level: warning\n"
	if port > 0 {
		content += "listener:\n  port: " + strconv.Itoa(port) + "\n"
	}
	if err := os.WriteFile(filepath.Join(doltDir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestStaleDoltPortCheck_RigEmbeddedConfigHardcodesSharedPort reproduces gu-msz5t:
// a rig-level .beads/dolt/config.yaml hardcoding the shared port (3307) is the
// imposter launchpad. It must be flagged even though it matches the correct port.
func TestStaleDoltPortCheck_RigEmbeddedConfigHardcodesSharedPort(t *testing.T) {
	tmpDir := t.TempDir()
	writeDoltDataConfig(t, tmpDir, 3307)
	writeRigsJSON(t, tmpDir, "talon_cdk")

	// Rig embedded dolt config hardcoding the shared port — the exact hazard.
	writeEmbeddedDoltConfig(t, filepath.Join(tmpDir, "talon_cdk"), 3307)

	check := NewStaleDoltPortCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir})

	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning for rig embedded config on shared port, got %v: %s", result.Status, result.Message)
	}
	foundHazard := false
	for _, d := range result.Details {
		if strings.Contains(d, "talon_cdk") && strings.Contains(d, "imposter hazard") {
			foundHazard = true
		}
	}
	if !foundHazard {
		t.Errorf("expected imposter-hazard detail mentioning talon_cdk, got: %v", result.Details)
	}
}

// TestStaleDoltPortCheck_NoEmbeddedConfig verifies a clean town (no .beads/dolt
// config anywhere) passes.
func TestStaleDoltPortCheck_NoEmbeddedConfig(t *testing.T) {
	tmpDir := t.TempDir()
	writeDoltDataConfig(t, tmpDir, 3307)
	writeRigsJSON(t, tmpDir, "talon_cdk")

	check := NewStaleDoltPortCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir})

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK for town with no embedded dolt configs, got %v: %s", result.Status, result.Message)
	}
}

// TestParseListenerPort verifies port parsing honors comments and inline comments.
func TestParseListenerPort(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    int
	}{
		{"active port", "listener:\n  port: 3307\n", 3307},
		{"commented port", "listener:\n  # port: 3307\n", 0},
		{"inline comment", "listener:\n  port: 44985 # default\n", 44985},
		{"no port", "log_level: warning\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseListenerPort(tc.content); got != tc.want {
				t.Errorf("parseListenerPort(%q) = %d, want %d", tc.content, got, tc.want)
			}
		})
	}
}

// TestStaleDoltPortCheck_DaemonJSONPortNoConfigYAML reproduces gu-nid89.38:
// a town whose real Dolt port comes from mayor/daemon.json (e.g. 3308) with NO
// config.yaml port line. The old getCorrectPort read only config.yaml, returned
// 0, and Run() fell back to 3307 — flagging every correct 3308 port/metadata as
// stale (false positive) and, under --fix, corrupting them to 3307. The canonical
// DefaultConfig resolver honors daemon.json, so the check must report StatusOK.
func TestStaleDoltPortCheck_DaemonJSONPortNoConfigYAML(t *testing.T) {
	tmpDir := t.TempDir()

	// Clear GT_DOLT_PORT so the daemon.json fallback is exercised (env would
	// otherwise take precedence and mask the resolution path under test).
	t.Setenv("GT_DOLT_PORT", "")

	// mayor/daemon.json declares GT_DOLT_PORT=3308 (no .dolt-data/config.yaml).
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	daemonJSON := map[string]interface{}{
		"env": map[string]string{"GT_DOLT_PORT": "3308"},
	}
	daemonBytes, _ := json.MarshalIndent(daemonJSON, "", "  ")
	if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), daemonBytes, 0644); err != nil {
		t.Fatal(err)
	}

	// Port file and metadata.json both carry the CORRECT custom port 3308.
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte("3308"), 0644); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]interface{}{
		"dolt_mode":        "server",
		"dolt_server_host": "127.0.0.1",
		"dolt_server_port": 3308,
		"dolt_database":    "hq",
	}
	metadataBytes, _ := json.MarshalIndent(metadata, "", "  ")
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadataBytes, 0644); err != nil {
		t.Fatal(err)
	}

	check := NewStaleDoltPortCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir})

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK (no false positive) for daemon.json port 3308, got %v: %s (details=%v)",
			result.Status, result.Message, result.Details)
	}
}

// TestStaleDoltPortCheck_FixUpdatesMetadata verifies that Fix() updates metadata.json with correct port.
func TestStaleDoltPortCheck_FixUpdatesMetadata(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .dolt-data/config.yaml with port 3307
	doltDataDir := filepath.Join(tmpDir, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	configYaml := `log_level: warning
listener:
  port: 3307
`
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"), []byte(configYaml), 0644); err != nil {
		t.Fatal(err)
	}

	// Create town .beads/metadata.json with WRONG port
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]interface{}{
		"dolt_mode":        "server",
		"dolt_server_host": "127.0.0.1",
		"dolt_server_port": 3306, // Wrong port!
		"dolt_database":    "hq",
	}
	metadataBytes, _ := json.MarshalIndent(metadata, "", "  ")
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadataBytes, 0644); err != nil {
		t.Fatal(err)
	}

	check := NewStaleDoltPortCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Run check to detect the issue
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning before fix, got %v", result.Status)
	}

	// Run fix
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() failed: %v", err)
	}

	// Verify metadata.json was updated
	updatedBytes, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}

	var updatedMetadata map[string]interface{}
	if err := json.Unmarshal(updatedBytes, &updatedMetadata); err != nil {
		t.Fatal(err)
	}

	port := int(updatedMetadata["dolt_server_port"].(float64))
	if port != 3307 {
		t.Errorf("expected port 3307 after fix, got %d", port)
	}

	// Run check again - should pass now
	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK after fix, got %v: %s", result.Status, result.Message)
	}
}
