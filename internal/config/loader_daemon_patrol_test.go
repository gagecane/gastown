package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaemonPatrolConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "mayor", "daemon.json")

	original := NewDaemonPatrolConfig()
	original.Patrols["custom"] = PatrolConfig{
		Enabled:  true,
		Interval: "10m",
		Agent:    "custom-agent",
	}

	if err := SaveDaemonPatrolConfig(path, original); err != nil {
		t.Fatalf("SaveDaemonPatrolConfig: %v", err)
	}

	loaded, err := LoadDaemonPatrolConfig(path)
	if err != nil {
		t.Fatalf("LoadDaemonPatrolConfig: %v", err)
	}

	if loaded.Type != "daemon-patrol-config" {
		t.Errorf("Type = %q, want 'daemon-patrol-config'", loaded.Type)
	}
	if loaded.Version != CurrentDaemonPatrolConfigVersion {
		t.Errorf("Version = %d, want %d", loaded.Version, CurrentDaemonPatrolConfigVersion)
	}
	if loaded.Heartbeat == nil || !loaded.Heartbeat.Enabled {
		t.Error("Heartbeat not preserved")
	}
	if len(loaded.Patrols) != 4 {
		t.Errorf("Patrols count = %d, want 4", len(loaded.Patrols))
	}
	if custom, ok := loaded.Patrols["custom"]; !ok || custom.Agent != "custom-agent" {
		t.Error("custom patrol not preserved")
	}
}

func TestDaemonPatrolConfigValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		config  *DaemonPatrolConfig
		wantErr bool
	}{
		{
			name:    "valid default config",
			config:  NewDaemonPatrolConfig(),
			wantErr: false,
		},
		{
			name: "valid minimal config",
			config: &DaemonPatrolConfig{
				Type:    "daemon-patrol-config",
				Version: 1,
			},
			wantErr: false,
		},
		{
			name: "wrong type",
			config: &DaemonPatrolConfig{
				Type:    "wrong",
				Version: 1,
			},
			wantErr: true,
		},
		{
			name: "future version rejected",
			config: &DaemonPatrolConfig{
				Type:    "daemon-patrol-config",
				Version: 999,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDaemonPatrolConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateDaemonPatrolConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadDaemonPatrolConfigNotFound(t *testing.T) {
	t.Parallel()
	_, err := LoadDaemonPatrolConfig("/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestDaemonPatrolConfigPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		townRoot string
		expected string
	}{
		{"/home/user/gt", "/home/user/gt/mayor/daemon.json"},
		{"/var/lib/gastown", "/var/lib/gastown/mayor/daemon.json"},
		{"/tmp/test-workspace", "/tmp/test-workspace/mayor/daemon.json"},
		{"~/gt", "~/gt/mayor/daemon.json"},
	}

	for _, tt := range tests {
		t.Run(tt.townRoot, func(t *testing.T) {
			path := DaemonPatrolConfigPath(tt.townRoot)
			if filepath.ToSlash(path) != filepath.ToSlash(tt.expected) {
				t.Errorf("DaemonPatrolConfigPath(%q) = %q, want %q", tt.townRoot, path, tt.expected)
			}
		})
	}
}

func TestEnsureDaemonPatrolConfig(t *testing.T) {
	t.Parallel()
	t.Run("creates config if missing", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "mayor"), 0755); err != nil {
			t.Fatalf("creating mayor dir: %v", err)
		}

		err := EnsureDaemonPatrolConfig(dir)
		if err != nil {
			t.Fatalf("EnsureDaemonPatrolConfig: %v", err)
		}

		path := DaemonPatrolConfigPath(dir)
		loaded, err := LoadDaemonPatrolConfig(path)
		if err != nil {
			t.Fatalf("LoadDaemonPatrolConfig: %v", err)
		}
		if loaded.Type != "daemon-patrol-config" {
			t.Errorf("Type = %q, want 'daemon-patrol-config'", loaded.Type)
		}
		if len(loaded.Patrols) != 3 {
			t.Errorf("Patrols count = %d, want 3 (deacon, witness, refinery)", len(loaded.Patrols))
		}
	})

	t.Run("preserves existing config", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "mayor", "daemon.json")

		existing := &DaemonPatrolConfig{
			Type:    "daemon-patrol-config",
			Version: 1,
			Patrols: map[string]PatrolConfig{
				"custom-only": {Enabled: true, Agent: "custom"},
			},
		}
		if err := SaveDaemonPatrolConfig(path, existing); err != nil {
			t.Fatalf("SaveDaemonPatrolConfig: %v", err)
		}

		err := EnsureDaemonPatrolConfig(dir)
		if err != nil {
			t.Fatalf("EnsureDaemonPatrolConfig: %v", err)
		}

		loaded, err := LoadDaemonPatrolConfig(path)
		if err != nil {
			t.Fatalf("LoadDaemonPatrolConfig: %v", err)
		}
		if len(loaded.Patrols) != 1 {
			t.Errorf("Patrols count = %d, want 1 (should preserve existing)", len(loaded.Patrols))
		}
		if _, ok := loaded.Patrols["custom-only"]; !ok {
			t.Error("existing custom patrol was overwritten")
		}
	})

}

func TestNewDaemonPatrolConfig(t *testing.T) {
	t.Parallel()
	cfg := NewDaemonPatrolConfig()

	if cfg.Type != "daemon-patrol-config" {
		t.Errorf("Type = %q, want 'daemon-patrol-config'", cfg.Type)
	}
	if cfg.Version != CurrentDaemonPatrolConfigVersion {
		t.Errorf("Version = %d, want %d", cfg.Version, CurrentDaemonPatrolConfigVersion)
	}
	if cfg.Heartbeat == nil {
		t.Fatal("Heartbeat is nil")
	}
	if !cfg.Heartbeat.Enabled {
		t.Error("Heartbeat.Enabled should be true by default")
	}
	if cfg.Heartbeat.Interval != "3m" {
		t.Errorf("Heartbeat.Interval = %q, want '3m'", cfg.Heartbeat.Interval)
	}
	if len(cfg.Patrols) != 3 {
		t.Errorf("Patrols count = %d, want 3", len(cfg.Patrols))
	}

	for _, name := range []string{"deacon", "witness", "refinery"} {
		patrol, ok := cfg.Patrols[name]
		if !ok {
			t.Errorf("missing %s patrol", name)
			continue
		}
		if !patrol.Enabled {
			t.Errorf("%s patrol should be enabled by default", name)
		}
		if patrol.Agent != name {
			t.Errorf("%s patrol Agent = %q, want %q", name, patrol.Agent, name)
		}
	}
}

func TestAddRigToDaemonPatrols(t *testing.T) {
	t.Parallel()

	t.Run("adds rig to witness and refinery", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		mayorDir := filepath.Join(townRoot, "mayor")
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Write a daemon.json with existing rigs
		daemonJSON := `{
  "type": "daemon-patrol-config",
  "version": 1,
  "heartbeat": {"enabled": true, "interval": "3m"},
  "patrols": {
    "witness": {"enabled": true, "interval": "5m", "agent": "witness", "rigs": ["gastown"]},
    "refinery": {"enabled": true, "interval": "5m", "agent": "refinery", "rigs": ["gastown"]},
    "deacon": {"enabled": true, "interval": "5m", "agent": "deacon"}
  }
}`
		if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), []byte(daemonJSON), 0644); err != nil {
			t.Fatal(err)
		}

		if err := AddRigToDaemonPatrols(townRoot, "newrig"); err != nil {
			t.Fatalf("AddRigToDaemonPatrols: %v", err)
		}

		// Reload and verify
		cfg, err := LoadDaemonPatrolConfig(DaemonPatrolConfigPath(townRoot))
		if err != nil {
			t.Fatalf("LoadDaemonPatrolConfig: %v", err)
		}

		witness := cfg.Patrols["witness"]
		if len(witness.Rigs) != 2 || witness.Rigs[0] != "gastown" || witness.Rigs[1] != "newrig" {
			t.Errorf("witness rigs = %v, want [gastown newrig]", witness.Rigs)
		}

		refinery := cfg.Patrols["refinery"]
		if len(refinery.Rigs) != 2 || refinery.Rigs[0] != "gastown" || refinery.Rigs[1] != "newrig" {
			t.Errorf("refinery rigs = %v, want [gastown newrig]", refinery.Rigs)
		}

		// Deacon should be untouched
		deacon := cfg.Patrols["deacon"]
		if len(deacon.Rigs) != 0 {
			t.Errorf("deacon rigs = %v, want empty", deacon.Rigs)
		}
	})

	t.Run("skips duplicate rig", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		mayorDir := filepath.Join(townRoot, "mayor")
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			t.Fatal(err)
		}

		daemonJSON := `{
  "type": "daemon-patrol-config",
  "version": 1,
  "patrols": {
    "witness": {"enabled": true, "rigs": ["gastown", "beads"]},
    "refinery": {"enabled": true, "rigs": ["gastown", "beads"]}
  }
}`
		if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), []byte(daemonJSON), 0644); err != nil {
			t.Fatal(err)
		}

		if err := AddRigToDaemonPatrols(townRoot, "gastown"); err != nil {
			t.Fatalf("AddRigToDaemonPatrols: %v", err)
		}

		cfg, err := LoadDaemonPatrolConfig(DaemonPatrolConfigPath(townRoot))
		if err != nil {
			t.Fatalf("LoadDaemonPatrolConfig: %v", err)
		}

		witness := cfg.Patrols["witness"]
		if len(witness.Rigs) != 2 {
			t.Errorf("witness rigs = %v, want [gastown beads] (no duplicate)", witness.Rigs)
		}
	})

	t.Run("preserves dolt_server fields", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		mayorDir := filepath.Join(townRoot, "mayor")
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			t.Fatal(err)
		}

		daemonJSON := `{
  "type": "daemon-patrol-config",
  "version": 1,
  "patrols": {
    "dolt_server": {"enabled": true, "port": 3307, "host": "127.0.0.1", "data_dir": "/tmp/dolt"},
    "witness": {"enabled": true, "rigs": ["gastown"]},
    "refinery": {"enabled": true, "rigs": ["gastown"]}
  }
}`
		path := filepath.Join(mayorDir, "daemon.json")
		if err := os.WriteFile(path, []byte(daemonJSON), 0644); err != nil {
			t.Fatal(err)
		}

		if err := AddRigToDaemonPatrols(townRoot, "newrig"); err != nil {
			t.Fatalf("AddRigToDaemonPatrols: %v", err)
		}

		// Read raw JSON to verify dolt_server fields are preserved
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}

		var patrols map[string]json.RawMessage
		if err := json.Unmarshal(raw["patrols"], &patrols); err != nil {
			t.Fatal(err)
		}

		var doltServer map[string]interface{}
		if err := json.Unmarshal(patrols["dolt_server"], &doltServer); err != nil {
			t.Fatal(err)
		}

		if doltServer["port"].(float64) != 3307 {
			t.Errorf("dolt_server port = %v, want 3307", doltServer["port"])
		}
		if doltServer["host"].(string) != "127.0.0.1" {
			t.Errorf("dolt_server host = %v, want 127.0.0.1", doltServer["host"])
		}
		if doltServer["data_dir"].(string) != "/tmp/dolt" {
			t.Errorf("dolt_server data_dir = %v, want /tmp/dolt", doltServer["data_dir"])
		}
	})

	t.Run("no-op when daemon.json missing", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()

		if err := AddRigToDaemonPatrols(townRoot, "newrig"); err != nil {
			t.Fatalf("AddRigToDaemonPatrols: %v", err)
		}
		// Should not create the file
		if _, err := os.Stat(filepath.Join(townRoot, "mayor", "daemon.json")); !os.IsNotExist(err) {
			t.Error("expected daemon.json to not exist")
		}
	})

	t.Run("no-op when no patrols section", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		mayorDir := filepath.Join(townRoot, "mayor")
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			t.Fatal(err)
		}

		daemonJSON := `{"type": "daemon-patrol-config", "version": 1}`
		if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), []byte(daemonJSON), 0644); err != nil {
			t.Fatal(err)
		}

		if err := AddRigToDaemonPatrols(townRoot, "newrig"); err != nil {
			t.Fatalf("AddRigToDaemonPatrols: %v", err)
		}
	})
}

func TestRemoveRigFromDaemonPatrols(t *testing.T) {
	t.Parallel()

	t.Run("removes rig from witness and refinery", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		mayorDir := filepath.Join(townRoot, "mayor")
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			t.Fatal(err)
		}

		daemonJSON := `{
  "type": "daemon-patrol-config",
  "version": 1,
  "patrols": {
    "witness": {"enabled": true, "rigs": ["gastown", "beads", "myrig"]},
    "refinery": {"enabled": true, "rigs": ["gastown", "beads", "myrig"]},
    "deacon": {"enabled": true}
  }
}`
		if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), []byte(daemonJSON), 0644); err != nil {
			t.Fatal(err)
		}

		if err := RemoveRigFromDaemonPatrols(townRoot, "beads"); err != nil {
			t.Fatalf("RemoveRigFromDaemonPatrols: %v", err)
		}

		cfg, err := LoadDaemonPatrolConfig(DaemonPatrolConfigPath(townRoot))
		if err != nil {
			t.Fatalf("LoadDaemonPatrolConfig: %v", err)
		}

		witness := cfg.Patrols["witness"]
		if len(witness.Rigs) != 2 || witness.Rigs[0] != "gastown" || witness.Rigs[1] != "myrig" {
			t.Errorf("witness rigs = %v, want [gastown myrig]", witness.Rigs)
		}

		refinery := cfg.Patrols["refinery"]
		if len(refinery.Rigs) != 2 || refinery.Rigs[0] != "gastown" || refinery.Rigs[1] != "myrig" {
			t.Errorf("refinery rigs = %v, want [gastown myrig]", refinery.Rigs)
		}
	})

	t.Run("no-op when rig not present", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		mayorDir := filepath.Join(townRoot, "mayor")
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			t.Fatal(err)
		}

		daemonJSON := `{
  "type": "daemon-patrol-config",
  "version": 1,
  "patrols": {
    "witness": {"enabled": true, "rigs": ["gastown"]},
    "refinery": {"enabled": true, "rigs": ["gastown"]}
  }
}`
		if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), []byte(daemonJSON), 0644); err != nil {
			t.Fatal(err)
		}

		if err := RemoveRigFromDaemonPatrols(townRoot, "nonexistent"); err != nil {
			t.Fatalf("RemoveRigFromDaemonPatrols: %v", err)
		}

		cfg, err := LoadDaemonPatrolConfig(DaemonPatrolConfigPath(townRoot))
		if err != nil {
			t.Fatalf("LoadDaemonPatrolConfig: %v", err)
		}

		witness := cfg.Patrols["witness"]
		if len(witness.Rigs) != 1 || witness.Rigs[0] != "gastown" {
			t.Errorf("witness rigs = %v, want [gastown]", witness.Rigs)
		}
	})

	t.Run("preserves dolt_server fields", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		mayorDir := filepath.Join(townRoot, "mayor")
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			t.Fatal(err)
		}

		daemonJSON := `{
  "type": "daemon-patrol-config",
  "version": 1,
  "patrols": {
    "dolt_server": {"enabled": true, "port": 3307, "host": "127.0.0.1"},
    "witness": {"enabled": true, "rigs": ["gastown", "beads"]},
    "refinery": {"enabled": true, "rigs": ["gastown", "beads"]}
  }
}`
		path := filepath.Join(mayorDir, "daemon.json")
		if err := os.WriteFile(path, []byte(daemonJSON), 0644); err != nil {
			t.Fatal(err)
		}

		if err := RemoveRigFromDaemonPatrols(townRoot, "beads"); err != nil {
			t.Fatalf("RemoveRigFromDaemonPatrols: %v", err)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}

		var patrols map[string]json.RawMessage
		if err := json.Unmarshal(raw["patrols"], &patrols); err != nil {
			t.Fatal(err)
		}

		var doltServer map[string]interface{}
		if err := json.Unmarshal(patrols["dolt_server"], &doltServer); err != nil {
			t.Fatal(err)
		}

		if doltServer["port"].(float64) != 3307 {
			t.Errorf("dolt_server port = %v, want 3307", doltServer["port"])
		}
	})

	t.Run("no-op when daemon.json missing", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()

		if err := RemoveRigFromDaemonPatrols(townRoot, "beads"); err != nil {
			t.Fatalf("RemoveRigFromDaemonPatrols: %v", err)
		}
	})

	t.Run("no-op when no patrols section", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		mayorDir := filepath.Join(townRoot, "mayor")
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			t.Fatal(err)
		}

		daemonJSON := `{"type": "daemon-patrol-config", "version": 1}`
		if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), []byte(daemonJSON), 0644); err != nil {
			t.Fatal(err)
		}

		if err := RemoveRigFromDaemonPatrols(townRoot, "beads"); err != nil {
			t.Fatalf("RemoveRigFromDaemonPatrols: %v", err)
		}
	})

	t.Run("roundtrip add then remove", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		mayorDir := filepath.Join(townRoot, "mayor")
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			t.Fatal(err)
		}

		daemonJSON := `{
  "type": "daemon-patrol-config",
  "version": 1,
  "patrols": {
    "witness": {"enabled": true, "rigs": ["gastown"]},
    "refinery": {"enabled": true, "rigs": ["gastown"]}
  }
}`
		if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), []byte(daemonJSON), 0644); err != nil {
			t.Fatal(err)
		}

		// Add a rig
		if err := AddRigToDaemonPatrols(townRoot, "newrig"); err != nil {
			t.Fatalf("AddRigToDaemonPatrols: %v", err)
		}

		// Remove it
		if err := RemoveRigFromDaemonPatrols(townRoot, "newrig"); err != nil {
			t.Fatalf("RemoveRigFromDaemonPatrols: %v", err)
		}

		cfg, err := LoadDaemonPatrolConfig(DaemonPatrolConfigPath(townRoot))
		if err != nil {
			t.Fatalf("LoadDaemonPatrolConfig: %v", err)
		}

		witness := cfg.Patrols["witness"]
		if len(witness.Rigs) != 1 || witness.Rigs[0] != "gastown" {
			t.Errorf("witness rigs = %v, want [gastown]", witness.Rigs)
		}

		refinery := cfg.Patrols["refinery"]
		if len(refinery.Rigs) != 1 || refinery.Rigs[0] != "gastown" {
			t.Errorf("refinery rigs = %v, want [gastown]", refinery.Rigs)
		}
	})
}

func TestSaveTownSettings(t *testing.T) {
	t.Parallel()
	t.Run("saves valid town settings", func(t *testing.T) {
		tmpDir := t.TempDir()
		settingsPath := filepath.Join(tmpDir, "settings", "config.json")

		settings := &TownSettings{
			Type:         "town-settings",
			Version:      CurrentTownSettingsVersion,
			DefaultAgent: "gemini",
			Agents: map[string]*RuntimeConfig{
				"my-agent": {
					Command: "my-agent",
					Args:    []string{"--arg1", "--arg2"},
				},
			},
		}

		err := SaveTownSettings(settingsPath, settings)
		if err != nil {
			t.Fatalf("SaveTownSettings failed: %v", err)
		}

		// Verify file exists
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatalf("reading settings file: %v", err)
		}

		// Verify it contains expected content
		content := string(data)
		if !strings.Contains(content, `"type": "town-settings"`) {
			t.Errorf("missing type field")
		}
		if !strings.Contains(content, `"default_agent": "gemini"`) {
			t.Errorf("missing default_agent field")
		}
		if !strings.Contains(content, `"my-agent"`) {
			t.Errorf("missing custom agent")
		}
	})

	t.Run("creates parent directories", func(t *testing.T) {
		tmpDir := t.TempDir()
		settingsPath := filepath.Join(tmpDir, "deeply", "nested", "settings", "config.json")

		settings := NewTownSettings()

		err := SaveTownSettings(settingsPath, settings)
		if err != nil {
			t.Fatalf("SaveTownSettings failed: %v", err)
		}

		// Verify file exists
		if _, err := os.Stat(settingsPath); err != nil {
			t.Errorf("settings file not created: %v", err)
		}
	})

	t.Run("rejects invalid type", func(t *testing.T) {
		tmpDir := t.TempDir()
		settingsPath := filepath.Join(tmpDir, "config.json")

		settings := &TownSettings{
			Type:    "invalid-type",
			Version: CurrentTownSettingsVersion,
		}

		err := SaveTownSettings(settingsPath, settings)
		if err == nil {
			t.Error("expected error for invalid type")
		}
	})

	t.Run("rejects unsupported version", func(t *testing.T) {
		tmpDir := t.TempDir()
		settingsPath := filepath.Join(tmpDir, "config.json")

		settings := &TownSettings{
			Type:    "town-settings",
			Version: CurrentTownSettingsVersion + 100,
		}

		err := SaveTownSettings(settingsPath, settings)
		if err == nil {
			t.Error("expected error for unsupported version")
		}
	})

	t.Run("roundtrip save and load", func(t *testing.T) {
		tmpDir := t.TempDir()
		settingsPath := filepath.Join(tmpDir, "config.json")

		original := &TownSettings{
			Type:         "town-settings",
			Version:      CurrentTownSettingsVersion,
			DefaultAgent: "codex",
			Agents: map[string]*RuntimeConfig{
				"custom-1": {
					Command: "custom-agent",
					Args:    []string{"--flag"},
				},
			},
		}

		err := SaveTownSettings(settingsPath, original)
		if err != nil {
			t.Fatalf("SaveTownSettings failed: %v", err)
		}

		loaded, err := LoadOrCreateTownSettings(settingsPath)
		if err != nil {
			t.Fatalf("LoadOrCreateTownSettings failed: %v", err)
		}

		if loaded.Type != original.Type {
			t.Errorf("Type = %q, want %q", loaded.Type, original.Type)
		}
		if loaded.Version != original.Version {
			t.Errorf("Version = %d, want %d", loaded.Version, original.Version)
		}
		if loaded.DefaultAgent != original.DefaultAgent {
			t.Errorf("DefaultAgent = %q, want %q", loaded.DefaultAgent, original.DefaultAgent)
		}

		if len(loaded.Agents) != len(original.Agents) {
			t.Errorf("Agents count = %d, want %d", len(loaded.Agents), len(original.Agents))
		}
	})
}

func TestGetDefaultFormula(t *testing.T) {
	t.Parallel()
	t.Run("returns empty string for nonexistent rig", func(t *testing.T) {
		result := GetDefaultFormula("/nonexistent/path")
		if result != "" {
			t.Errorf("GetDefaultFormula() = %q, want empty string", result)
		}
	})

	t.Run("returns empty string when no workflow config", func(t *testing.T) {
		dir := t.TempDir()
		settings := NewRigSettings()
		if err := SaveRigSettings(RigSettingsPath(dir), settings); err != nil {
			t.Fatalf("SaveRigSettings: %v", err)
		}

		result := GetDefaultFormula(dir)
		if result != "" {
			t.Errorf("GetDefaultFormula() = %q, want empty string", result)
		}
	})

	t.Run("returns default formula when configured", func(t *testing.T) {
		dir := t.TempDir()
		settings := NewRigSettings()
		settings.Workflow = &WorkflowConfig{
			DefaultFormula: "shiny",
		}
		if err := SaveRigSettings(RigSettingsPath(dir), settings); err != nil {
			t.Fatalf("SaveRigSettings: %v", err)
		}

		result := GetDefaultFormula(dir)
		if result != "shiny" {
			t.Errorf("GetDefaultFormula() = %q, want %q", result, "shiny")
		}
	})
}
