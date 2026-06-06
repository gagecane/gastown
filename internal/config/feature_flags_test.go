// Tests for the feature_flags block of RigSettings (Phase 2 task 19, gu-vvl4y).
//
// Acceptance criteria:
//
//	a. absent feature_flags block → all flag accessors return false (defaults).
//	b. well-formed block with auto_test_pr_revision_routing=true → accessor returns true.
//	c. nil RigSettings → accessor returns false (nil-safe).

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRigSettings_FeatureFlagsAbsent_AllDefaultsFalse(t *testing.T) {
	// A settings JSON with no feature_flags block must load successfully
	// and all flag accessors must return their default (false).
	body := `{"type": "rig-settings", "version": 1}`
	path := writeFeatureFlagsSettingsFile(t, body)

	settings, err := LoadRigSettings(path)
	if err != nil {
		t.Fatalf("LoadRigSettings: %v", err)
	}

	if settings.FeatureFlags != nil {
		t.Errorf("expected nil FeatureFlags (block absent), got %+v", settings.FeatureFlags)
	}

	ff := settings.GetFeatureFlags()
	if ff != nil {
		t.Errorf("GetFeatureFlags() on absent block should return nil, got %+v", ff)
	}

	// Nil-safe accessor must return false.
	if ff.IsAutoTestPRRevisionRouting() {
		t.Error("IsAutoTestPRRevisionRouting() on nil config should return false")
	}
}

func TestLoadRigSettings_FeatureFlagsPresent_ReturnsConfigured(t *testing.T) {
	body := `{
		"type": "rig-settings",
		"version": 1,
		"feature_flags": {
			"auto_test_pr_revision_routing": true
		}
	}`
	path := writeFeatureFlagsSettingsFile(t, body)

	settings, err := LoadRigSettings(path)
	if err != nil {
		t.Fatalf("LoadRigSettings: %v", err)
	}

	if settings.FeatureFlags == nil {
		t.Fatal("expected non-nil FeatureFlags")
	}

	if !settings.FeatureFlags.IsAutoTestPRRevisionRouting() {
		t.Error("IsAutoTestPRRevisionRouting() should be true when configured as true")
	}
}

func TestLoadRigSettings_FeatureFlagsExplicitFalse(t *testing.T) {
	body := `{
		"type": "rig-settings",
		"version": 1,
		"feature_flags": {
			"auto_test_pr_revision_routing": false
		}
	}`
	path := writeFeatureFlagsSettingsFile(t, body)

	settings, err := LoadRigSettings(path)
	if err != nil {
		t.Fatalf("LoadRigSettings: %v", err)
	}

	if settings.FeatureFlags == nil {
		t.Fatal("expected non-nil FeatureFlags (block present with explicit false)")
	}

	if settings.FeatureFlags.IsAutoTestPRRevisionRouting() {
		t.Error("IsAutoTestPRRevisionRouting() should be false when configured as false")
	}
}

func TestNilRigSettings_FeatureFlags_NilSafe(t *testing.T) {
	var s *RigSettings
	ff := s.GetFeatureFlags()
	if ff != nil {
		t.Errorf("GetFeatureFlags() on nil RigSettings should return nil, got %+v", ff)
	}
	if ff.IsAutoTestPRRevisionRouting() {
		t.Error("IsAutoTestPRRevisionRouting() on nil should return false")
	}
}

// writeFeatureFlagsSettingsFile writes a JSON document to a temp config.json.
func writeFeatureFlagsSettingsFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return path
}
