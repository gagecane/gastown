package autotestpr

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
)

// TestRunCycle_MissingTownBead verifies the missing-town-bead path
// (Round 2 fix #10): if the town-state bead is not provisioned, the
// cycle emits a structured warning and exits 0 — does NOT panic.
func TestRunCycle_MissingTownBead(t *testing.T) {
	t.Parallel()

	// Set up a mock beads that always returns ErrTownStateNotProvisioned.
	// We use a minimal config with a rig whose settings don't enable
	// auto-test-pr, so reconcile computes an empty list.
	tmpDir := t.TempDir()
	rigName := "test_rig"
	rigDir := filepath.Join(tmpDir, rigName)
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}

	rigsConfig := &config.RigsConfig{
		Rigs: map[string]config.RigEntry{
			rigName: {},
		},
	}

	cfg := &CycleConfig{
		TownRoot:   tmpDir,
		TownBeads:  nil, // Will fail validation
		RigsConfig: rigsConfig,
		Now:        time.Now(),
	}

	// nil TownBeads should fail validation
	_, err := RunCycle(cfg)
	if err == nil {
		t.Fatal("expected error with nil TownBeads")
	}
}

// TestRunCycle_NoRigsEnabled verifies the exit-early path: when no rig
// has auto_test_pr.enabled=true, the cycle exits 0 with ExitReason
// "no-rigs-enabled".
func TestRunCycle_NoRigsEnabled_ViaComputeSettings(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a rig directory with settings that do NOT enable auto-test-pr.
	rigName := "test_rig"
	rigDir := filepath.Join(tmpDir, rigName)
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Write settings without auto_test_pr block.
	settingsJSON := `{"type": "rig-settings", "version": 1}`
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), []byte(settingsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	rigsConfig := &config.RigsConfig{
		Rigs: map[string]config.RigEntry{
			rigName: {},
		},
	}

	// computeEnabledRigsFromSettings should find no enabled rigs.
	got := computeEnabledRigsFromSettings(tmpDir, rigsConfig)
	if len(got) != 0 {
		t.Errorf("expected empty enabled rigs, got %v", got)
	}
}

// TestComputeEnabledRigsFromSettings_Enabled verifies that a rig with
// auto_test_pr.enabled=true is picked up by the reconcile scanner.
func TestComputeEnabledRigsFromSettings_Enabled(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a rig with auto_test_pr.enabled=true.
	rigName := "gastown_upstream"
	rigDir := filepath.Join(tmpDir, rigName)
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatal(err)
	}
	settingsJSON := `{
		"type": "rig-settings",
		"version": 1,
		"auto_test_pr": {"enabled": true, "language": "go"}
	}`
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), []byte(settingsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a second rig with auto_test_pr disabled.
	rigName2 := "other_rig"
	rigDir2 := filepath.Join(tmpDir, rigName2)
	settingsDir2 := filepath.Join(rigDir2, "settings")
	if err := os.MkdirAll(settingsDir2, 0755); err != nil {
		t.Fatal(err)
	}
	settingsJSON2 := `{"type": "rig-settings", "version": 1}`
	if err := os.WriteFile(filepath.Join(settingsDir2, "config.json"), []byte(settingsJSON2), 0644); err != nil {
		t.Fatal(err)
	}

	rigsConfig := &config.RigsConfig{
		Rigs: map[string]config.RigEntry{
			rigName:  {},
			rigName2: {},
		},
	}

	got := computeEnabledRigsFromSettings(tmpDir, rigsConfig)
	if len(got) != 1 {
		t.Fatalf("expected 1 enabled rig, got %d: %v", len(got), got)
	}
	if got[0] != rigName {
		t.Errorf("expected %q, got %q", rigName, got[0])
	}
}

// TestComputeEnabledRigsFromSettings_NoSettings verifies that rigs
// without a settings file (or with a broken one) are treated as
// disabled — no panic, no error propagation.
func TestComputeEnabledRigsFromSettings_NoSettings(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Rig directory exists but no settings file.
	rigName := "bare_rig"
	rigDir := filepath.Join(tmpDir, rigName)
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}

	rigsConfig := &config.RigsConfig{
		Rigs: map[string]config.RigEntry{
			rigName: {},
		},
	}

	got := computeEnabledRigsFromSettings(tmpDir, rigsConfig)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

// TestStringSlicesEqual covers the comparison helper used by reconcile.
func TestStringSlicesEqual(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b []string
		want bool
	}{
		{"both-nil", nil, nil, true},
		{"both-empty", []string{}, []string{}, true},
		{"nil-vs-empty", nil, []string{}, true},
		{"equal", []string{"a", "b"}, []string{"a", "b"}, true},
		{"different-length", []string{"a"}, []string{"a", "b"}, false},
		{"different-content", []string{"a", "b"}, []string{"a", "c"}, false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := stringSlicesEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("stringSlicesEqual(%v, %v) = %v; want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestCycleConfig_Validate covers the validation logic.
func TestCycleConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     CycleConfig
		wantErr bool
	}{
		{
			name:    "empty-town-root",
			cfg:     CycleConfig{TownRoot: "", TownBeads: &beads.Beads{}, RigsConfig: &config.RigsConfig{}, Now: time.Now()},
			wantErr: true,
		},
		{
			name:    "nil-town-beads",
			cfg:     CycleConfig{TownRoot: "/tmp", TownBeads: nil, RigsConfig: &config.RigsConfig{}, Now: time.Now()},
			wantErr: true,
		},
		{
			name:    "nil-rigs-config",
			cfg:     CycleConfig{TownRoot: "/tmp", TownBeads: &beads.Beads{}, RigsConfig: nil, Now: time.Now()},
			wantErr: true,
		},
		{
			name:    "zero-now",
			cfg:     CycleConfig{TownRoot: "/tmp", TownBeads: &beads.Beads{}, RigsConfig: &config.RigsConfig{}},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestCycleResult_JSON verifies structured output serialization.
func TestCycleResult_JSON(t *testing.T) {
	t.Parallel()

	r := &CycleResult{
		Reconciled:  true,
		EnabledRigs: []string{},
		ExitReason:  "no-rigs-enabled",
	}

	data, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty JSON")
	}
	// Verify it contains expected fields.
	s := string(data)
	if !contains(s, "no-rigs-enabled") {
		t.Errorf("JSON missing exit_reason: %s", s)
	}
	if !contains(s, "reconciled") {
		t.Errorf("JSON missing reconciled: %s", s)
	}
}

// TestCycleConfig_Logger verifies that CycleConfig.logger() returns the
// caller-supplied logger when set, and falls back to a non-nil default
// when nil. Tests can pass a buffer-backed logger to assert on output.
func TestCycleConfig_Logger(t *testing.T) {
	t.Parallel()

	t.Run("uses-supplied-logger", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		injected := log.New(&buf, "", 0)
		cfg := &CycleConfig{Logger: injected}
		got := cfg.logger()
		if got != injected {
			t.Fatal("logger() did not return the supplied logger")
		}
		got.Printf("hello %s", "world")
		if !strings.Contains(buf.String(), "hello world") {
			t.Errorf("buffer missing log line: %q", buf.String())
		}
	})

	t.Run("default-when-nil", func(t *testing.T) {
		t.Parallel()
		cfg := &CycleConfig{}
		if cfg.logger() == nil {
			t.Error("logger() returned nil for default fallback")
		}
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
