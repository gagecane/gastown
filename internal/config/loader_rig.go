package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/atomicfile"
)

// LoadRigsConfig loads and validates a rigs registry file.
// Retries once on read/parse errors to tolerate the brief window during which a
// concurrent non-atomic writer could leave the file truncated. With
// SaveRigsConfig now using atomic write-then-rename this is belt-and-suspenders
// against older versions that may still be writing the file.
func LoadRigsConfig(path string) (*RigsConfig, error) {
	readAndParse := func() (*RigsConfig, error) {
		data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed internally, not from user input
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
			}
			return nil, fmt.Errorf("reading config: %w", err)
		}

		var config RigsConfig
		if err := json.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("parsing config: %w", err)
		}

		if err := validateRigsConfig(&config); err != nil {
			return nil, err
		}

		return &config, nil
	}

	cfg, err := readAndParse()
	if err != nil && !errors.Is(err, ErrNotFound) {
		cfg, err = readAndParse()
	}
	return cfg, err
}

// SaveRigsConfig saves a rigs registry to a file atomically.
// Writes to a temp file in the same directory then renames into place; the
// rename is atomic on POSIX, so concurrent readers never observe a zero-byte
// or partially-written rigs.json.
func SaveRigsConfig(path string, config *RigsConfig) error {
	if err := validateRigsConfig(config); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}

	if err := atomicfile.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}

// validateRigsConfig validates a RigsConfig.
func validateRigsConfig(c *RigsConfig) error {
	if c.Version > CurrentRigsVersion {
		return fmt.Errorf("%w: got %d, max supported %d", ErrInvalidVersion, c.Version, CurrentRigsVersion)
	}
	if c.Rigs == nil {
		c.Rigs = make(map[string]RigEntry)
	}
	return nil
}

// LoadRigConfig loads and validates a rig configuration file.
func LoadRigConfig(path string) (*RigConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed internally, not from user input
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var config RigConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if err := validateRigConfig(&config); err != nil {
		return nil, err
	}

	return &config, nil
}

// SaveRigConfig saves a rig configuration to a file.
func SaveRigConfig(path string, config *RigConfig) error {
	if err := validateRigConfig(config); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil { //nolint:gosec // G306: config files don't contain secrets
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}

// validateRigConfig validates a RigConfig (identity only).
func validateRigConfig(c *RigConfig) error {
	if c.Type != "rig" && c.Type != "" {
		return fmt.Errorf("%w: expected type 'rig', got '%s'", ErrInvalidType, c.Type)
	}
	if c.Version > CurrentRigConfigVersion {
		return fmt.Errorf("%w: got %d, max supported %d", ErrInvalidVersion, c.Version, CurrentRigConfigVersion)
	}
	if c.Name == "" {
		return fmt.Errorf("%w: name", ErrMissingField)
	}
	return nil
}

// validateRigSettings validates a RigSettings.
func validateRigSettings(c *RigSettings) error {
	if c.Type != "rig-settings" && c.Type != "" {
		return fmt.Errorf("%w: expected type 'rig-settings', got '%s'", ErrInvalidType, c.Type)
	}
	if c.Version > CurrentRigSettingsVersion {
		return fmt.Errorf("%w: got %d, max supported %d", ErrInvalidVersion, c.Version, CurrentRigSettingsVersion)
	}
	if c.MergeQueue != nil {
		if err := validateMergeQueueConfig(c.MergeQueue); err != nil {
			return err
		}
	}
	return nil
}

// ErrInvalidOnConflict indicates an invalid on_conflict strategy.
var ErrInvalidOnConflict = errors.New("invalid on_conflict strategy")

// validateMergeQueueConfig validates a MergeQueueConfig.
func validateMergeQueueConfig(c *MergeQueueConfig) error {
	// Validate on_conflict strategy
	if c.OnConflict != "" && c.OnConflict != OnConflictAssignBack && c.OnConflict != OnConflictAutoRebase {
		return fmt.Errorf("%w: got '%s', want '%s' or '%s'",
			ErrInvalidOnConflict, c.OnConflict, OnConflictAssignBack, OnConflictAutoRebase)
	}

	// Validate poll_interval if specified
	if c.PollInterval != "" {
		if _, err := time.ParseDuration(c.PollInterval); err != nil {
			return fmt.Errorf("invalid poll_interval: %w", err)
		}
	}

	// Validate stale_claim_timeout if specified
	if c.StaleClaimTimeout != "" {
		dur, err := time.ParseDuration(c.StaleClaimTimeout)
		if err != nil {
			return fmt.Errorf("invalid stale_claim_timeout: %w", err)
		}
		if dur <= 0 {
			return fmt.Errorf("stale_claim_timeout must be positive, got %v", dur)
		}
	}

	// Validate non-negative values
	if c.RetryFlakyTests < 0 {
		return fmt.Errorf("%w: retry_flaky_tests must be non-negative", ErrMissingField)
	}
	if c.MaxConcurrent < 0 {
		return fmt.Errorf("%w: max_concurrent must be non-negative", ErrMissingField)
	}

	return nil
}

// NewRigConfig creates a new RigConfig (identity only).
func NewRigConfig(name, gitURL string) *RigConfig {
	return &RigConfig{
		Type:    "rig",
		Version: CurrentRigConfigVersion,
		Name:    name,
		GitURL:  gitURL,
	}
}

// NewRigSettings creates a new RigSettings with defaults.
func NewRigSettings() *RigSettings {
	return &RigSettings{
		Type:       "rig-settings",
		Version:    CurrentRigSettingsVersion,
		MergeQueue: DefaultMergeQueueConfig(),
		Namepool:   DefaultNamepoolConfig(),
	}
}

// RepoSettingsPath is the conventional path within a repository where
// gastown rig settings can be stored. This file is committed to git and
// provides durable defaults that survive rig re-scaffolding.
const RepoSettingsPath = ".gastown/settings.json"

// LoadRepoSettings loads rig settings from a repository's .gastown/settings.json.
// Returns nil, nil if the file does not exist (repo has no gastown settings).
func LoadRepoSettings(repoRoot string) (*RigSettings, error) {
	path := filepath.Join(repoRoot, RepoSettingsPath)
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed internally
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading repo settings: %w", err)
	}

	var settings RigSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parsing repo settings %s: %w", path, err)
	}

	return &settings, nil
}

// MergeSettingsCommand merges a repo-sourced MergeQueueConfig (floor) with
// a local override. Non-empty fields in the override take precedence.
// Returns a new config without mutating either input.
func MergeSettingsCommand(repo, local *MergeQueueConfig) *MergeQueueConfig {
	if repo == nil && local == nil {
		return nil
	}
	result := &MergeQueueConfig{}
	// Start from repo defaults
	if repo != nil {
		*result = *repo
	}
	// Overlay local overrides (non-empty fields win)
	if local != nil {
		if local.SetupCommand != "" {
			result.SetupCommand = local.SetupCommand
		}
		if local.TypecheckCommand != "" {
			result.TypecheckCommand = local.TypecheckCommand
		}
		if local.LintCommand != "" {
			result.LintCommand = local.LintCommand
		}
		if local.TestCommand != "" {
			result.TestCommand = local.TestCommand
		}
		if local.BuildCommand != "" {
			result.BuildCommand = local.BuildCommand
		}
		// Merge non-command fields from local if explicitly set
		if local.Enabled {
			result.Enabled = local.Enabled
		}
		if local.MergeStrategy != "" {
			result.MergeStrategy = local.MergeStrategy
		}
		if local.OnConflict != "" {
			result.OnConflict = local.OnConflict
		}
		if local.RunTests != nil {
			result.RunTests = local.RunTests
		}
		if local.DeleteMergedBranches != nil {
			result.DeleteMergedBranches = local.DeleteMergedBranches
		}
		if local.RetryFlakyTests > 0 {
			result.RetryFlakyTests = local.RetryFlakyTests
		}
		if local.PollInterval != "" {
			result.PollInterval = local.PollInterval
		}
		if local.MaxConcurrent > 0 {
			result.MaxConcurrent = local.MaxConcurrent
		}
		if local.StaleClaimTimeout != "" {
			result.StaleClaimTimeout = local.StaleClaimTimeout
		}
		if local.MergeStrategy != "" {
			result.MergeStrategy = local.MergeStrategy
		}
		if local.RequireReview != nil {
			result.RequireReview = local.RequireReview
		}
	}
	return result
}

// LoadRigSettings loads and validates a rig settings file.
func LoadRigSettings(path string) (*RigSettings, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed internally, not from user input
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("reading settings: %w", err)
	}

	var settings RigSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parsing settings: %w", err)
	}

	if err := validateRigSettings(&settings); err != nil {
		return nil, err
	}

	// Check for deprecated merge_queue keys that were removed.
	// These are silently ignored by json.Unmarshal but may indicate stale config.
	warnDeprecatedMergeQueueKeys(data, path)

	return &settings, nil
}

// DeprecatedMergeQueueKeys lists merge_queue config keys that have been removed.
// target_branch and integration_branches were replaced by rig default_branch
// and per-epic integration branch metadata.
var DeprecatedMergeQueueKeys = []string{"target_branch", "integration_branches"}

// warnDeprecatedMergeQueueKeys checks raw settings JSON for removed merge_queue keys
// and prints a stderr warning. This is advisory only — not a validation error.
func warnDeprecatedMergeQueueKeys(data []byte, path string) {
	var raw struct {
		MergeQueue map[string]json.RawMessage `json:"merge_queue"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || raw.MergeQueue == nil {
		return
	}
	for _, key := range DeprecatedMergeQueueKeys {
		if _, ok := raw.MergeQueue[key]; ok {
			fmt.Fprintf(os.Stderr, "warning: %s: merge_queue.%s is deprecated and ignored (use rig default_branch instead)\n", path, key)
		}
	}
}

// SaveRigSettings saves rig settings to a file.
func SaveRigSettings(path string, settings *RigSettings) error {
	if err := validateRigSettings(settings); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding settings: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil { //nolint:gosec // G306: settings files don't contain secrets
		return fmt.Errorf("writing settings: %w", err)
	}

	return nil
}

// RigSettingsPath returns the path to rig settings file.
func RigSettingsPath(rigPath string) string {
	return filepath.Join(rigPath, "settings", "config.json")
}
