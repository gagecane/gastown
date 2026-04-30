package config

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestRigSettingsWithCustomMergeQueue(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	original := &RigSettings{
		Type:    "rig-settings",
		Version: 1,
		MergeQueue: &MergeQueueConfig{
			Enabled:                          true,
			IntegrationBranchPolecatEnabled:  boolPtr(false),
			IntegrationBranchRefineryEnabled: boolPtr(false),
			OnConflict:                       OnConflictAutoRebase,
			RunTests:                         boolPtr(true),
			TestCommand:                      "make test",
			DeleteMergedBranches:             boolPtr(false),
			RetryFlakyTests:                  3,
			PollInterval:                     "1m",
			MaxConcurrent:                    2,
		},
	}

	if err := SaveRigSettings(path, original); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	loaded, err := LoadRigSettings(path)
	if err != nil {
		t.Fatalf("LoadRigSettings: %v", err)
	}

	mq := loaded.MergeQueue
	if mq.OnConflict != OnConflictAutoRebase {
		t.Errorf("OnConflict = %q, want %q", mq.OnConflict, OnConflictAutoRebase)
	}
	if mq.TestCommand != "make test" {
		t.Errorf("TestCommand = %q, want 'make test'", mq.TestCommand)
	}
	if mq.RetryFlakyTests != 3 {
		t.Errorf("RetryFlakyTests = %d, want 3", mq.RetryFlakyTests)
	}
}

func TestDefaultMergeQueueConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultMergeQueueConfig()

	if !cfg.Enabled {
		t.Error("Enabled should be true by default")
	}
	if !cfg.IsPolecatIntegrationEnabled() {
		t.Error("IsPolecatIntegrationEnabled should be true by default")
	}
	if !cfg.IsRefineryIntegrationEnabled() {
		t.Error("IsRefineryIntegrationEnabled should be true by default")
	}
	if cfg.OnConflict != OnConflictAssignBack {
		t.Errorf("OnConflict = %q, want %q", cfg.OnConflict, OnConflictAssignBack)
	}
	if !cfg.IsRunTestsEnabled() {
		t.Error("IsRunTestsEnabled should be true by default")
	}
	if cfg.TestCommand != "" {
		t.Errorf("TestCommand = %q, want empty (language-agnostic default)", cfg.TestCommand)
	}
	if !cfg.IsDeleteMergedBranchesEnabled() {
		t.Error("IsDeleteMergedBranchesEnabled should be true by default")
	}
	if cfg.RetryFlakyTests != 1 {
		t.Errorf("RetryFlakyTests = %d, want 1", cfg.RetryFlakyTests)
	}
	if cfg.PollInterval != "30s" {
		t.Errorf("PollInterval = %q, want '30s'", cfg.PollInterval)
	}
	if cfg.MaxConcurrent != 1 {
		t.Errorf("MaxConcurrent = %d, want 1", cfg.MaxConcurrent)
	}
	if cfg.StaleClaimTimeout != "30m" {
		t.Errorf("StaleClaimTimeout = %q, want '30m'", cfg.StaleClaimTimeout)
	}
}

// TestMergeQueueConfig_PartialJSON_BoolDefaults verifies that omitted *bool fields
// in a partial merge_queue JSON config deserialize to nil (not false), and that the
// nil-safe accessor methods return the correct defaults.
//
// This is a regression test for: "Partial merge_queue config silently disables
// refinery tests — omitted booleans deserialize to Go zero values (false)".
// The *bool pointer approach prevents this, and this test locks in that guarantee.
func TestMergeQueueConfig_PartialJSON_BoolDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		json string
		// Expected accessor results when *bool fields are omitted (nil)
		wantRunTests            bool
		wantDeleteMerged        bool
		wantPolecatIntegration  bool
		wantRefineryIntegration bool
		wantAutoLand            bool
	}{
		{
			name: "minimal config — all *bool fields omitted",
			json: `{"enabled": true, "on_conflict": "assign_back"}`,
			// nil *bool → accessor defaults
			wantRunTests:            true,
			wantDeleteMerged:        true,
			wantPolecatIntegration:  true,
			wantRefineryIntegration: true,
			wantAutoLand:            false,
		},
		{
			name: "explicit false — should be respected",
			json: `{
				"enabled": true,
				"on_conflict": "assign_back",
				"run_tests": false,
				"delete_merged_branches": false,
				"integration_branch_polecat_enabled": false,
				"integration_branch_refinery_enabled": false,
				"integration_branch_auto_land": false
			}`,
			wantRunTests:            false,
			wantDeleteMerged:        false,
			wantPolecatIntegration:  false,
			wantRefineryIntegration: false,
			wantAutoLand:            false,
		},
		{
			name: "explicit true — should be respected",
			json: `{
				"enabled": true,
				"on_conflict": "assign_back",
				"run_tests": true,
				"delete_merged_branches": true,
				"integration_branch_polecat_enabled": true,
				"integration_branch_refinery_enabled": true,
				"integration_branch_auto_land": true
			}`,
			wantRunTests:            true,
			wantDeleteMerged:        true,
			wantPolecatIntegration:  true,
			wantRefineryIntegration: true,
			wantAutoLand:            true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg MergeQueueConfig
			if err := json.Unmarshal([]byte(tt.json), &cfg); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}

			if got := cfg.IsRunTestsEnabled(); got != tt.wantRunTests {
				t.Errorf("IsRunTestsEnabled() = %v, want %v", got, tt.wantRunTests)
			}
			if got := cfg.IsDeleteMergedBranchesEnabled(); got != tt.wantDeleteMerged {
				t.Errorf("IsDeleteMergedBranchesEnabled() = %v, want %v", got, tt.wantDeleteMerged)
			}
			if got := cfg.IsPolecatIntegrationEnabled(); got != tt.wantPolecatIntegration {
				t.Errorf("IsPolecatIntegrationEnabled() = %v, want %v", got, tt.wantPolecatIntegration)
			}
			if got := cfg.IsRefineryIntegrationEnabled(); got != tt.wantRefineryIntegration {
				t.Errorf("IsRefineryIntegrationEnabled() = %v, want %v", got, tt.wantRefineryIntegration)
			}
			if got := cfg.IsIntegrationBranchAutoLandEnabled(); got != tt.wantAutoLand {
				t.Errorf("IsIntegrationBranchAutoLandEnabled() = %v, want %v", got, tt.wantAutoLand)
			}
		})
	}
}

// TestMergeQueueConfig_PartialJSON_NilPointers verifies that omitted *bool fields
// deserialize to nil, not to a pointer to false. This is the underlying mechanism
// that makes the accessor defaults work.
func TestMergeQueueConfig_PartialJSON_NilPointers(t *testing.T) {
	t.Parallel()

	partialJSON := `{"enabled": true, "on_conflict": "assign_back"}`
	var cfg MergeQueueConfig
	if err := json.Unmarshal([]byte(partialJSON), &cfg); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if cfg.RunTests != nil {
		t.Errorf("RunTests should be nil when omitted, got %v", *cfg.RunTests)
	}
	if cfg.DeleteMergedBranches != nil {
		t.Errorf("DeleteMergedBranches should be nil when omitted, got %v", *cfg.DeleteMergedBranches)
	}
	if cfg.IntegrationBranchPolecatEnabled != nil {
		t.Errorf("IntegrationBranchPolecatEnabled should be nil when omitted, got %v", *cfg.IntegrationBranchPolecatEnabled)
	}
	if cfg.IntegrationBranchRefineryEnabled != nil {
		t.Errorf("IntegrationBranchRefineryEnabled should be nil when omitted, got %v", *cfg.IntegrationBranchRefineryEnabled)
	}
	if cfg.IntegrationBranchAutoLand != nil {
		t.Errorf("IntegrationBranchAutoLand should be nil when omitted, got %v", *cfg.IntegrationBranchAutoLand)
	}
}
