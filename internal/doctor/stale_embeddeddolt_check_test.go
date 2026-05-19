package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStaleEmbeddeddoltCheck(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test .beads directories
	beadsPath := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsPath, 0755)

	// Test case 1: No embeddeddolt directory (should pass)
	metadata := map[string]interface{}{
		"dolt_mode": "server",
	}
	metadataPath := filepath.Join(beadsPath, "metadata.json")
	data, _ := json.Marshal(metadata)
	os.WriteFile(metadataPath, data, 0644)

	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewStaleEmbeddeddoltCheck()
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("Expected OK status for no embeddeddolt dir, got %s", result.Status)
	}

	// Test case 2: Create embeddeddolt directory (should fail)
	embeddeddoltPath := filepath.Join(beadsPath, "embeddeddolt")
	os.MkdirAll(embeddeddoltPath, 0755)

	check = NewStaleEmbeddeddoltCheck()
	result = check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("Expected WARNING status for stale embeddeddolt dir, got %s", result.Status)
	}

	// Test case 3: Fix the issue (should remove embeddeddolt)
	fixErr := check.Fix(ctx)
	if fixErr != nil {
		t.Errorf("Fix failed: %v", fixErr)
	}

	if _, err := os.Stat(embeddeddoltPath); !os.IsNotExist(err) {
		t.Error("embeddeddolt directory was not removed by Fix()")
	}

	// Test case 4: Non-server mode metadata should not trigger warning
	os.MkdirAll(embeddeddoltPath, 0755)

	metadata = map[string]interface{}{
		"dolt_mode": "embedded",
	}
	newData, _ := json.Marshal(metadata)
	os.WriteFile(metadataPath, newData, 0644)

	check = NewStaleEmbeddeddoltCheck()
	result = check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("Expected OK status for embedded mode with embeddeddolt, got %s", result.Status)
	}
}

func TestStaleEmbeddeddoltCheck_RigMetadata(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a rig with metadata
	rigPath := filepath.Join(tmpDir, "test-rig")
	rigBeadsPath := filepath.Join(rigPath, ".beads")
	os.MkdirAll(rigBeadsPath, 0755)

	// Create rigs.json
	rigsData := map[string]interface{}{
		"rigs": map[string]interface{}{
			"test-rig": struct{}{},
		},
	}
	rigsJSON, _ := json.Marshal(rigsData)
	mayrorPath := filepath.Join(tmpDir, "mayor")
	os.MkdirAll(mayrorPath, 0755)
	os.WriteFile(filepath.Join(mayrorPath, "rigs.json"), rigsJSON, 0644)

	// Create server-mode metadata
	metadata := map[string]interface{}{
		"dolt_mode": "server",
	}
	metadataPath := filepath.Join(rigBeadsPath, "metadata.json")
	metadataJSON, _ := json.Marshal(metadata)
	os.WriteFile(metadataPath, metadataJSON, 0644)

	// Create embeddeddolt directory
	embeddeddoltPath := filepath.Join(rigBeadsPath, "embeddeddolt")
	os.MkdirAll(embeddeddoltPath, 0755)

	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewStaleEmbeddeddoltCheck()
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("Expected WARNING status for rig with stale embeddeddolt, got %s", result.Status)
	}
}
