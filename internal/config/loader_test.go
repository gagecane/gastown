package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTownConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "mayor", "town.json")

	original := &TownConfig{
		Type:      "town",
		Version:   1,
		Name:      "test-town",
		CreatedAt: time.Now().Truncate(time.Second),
	}

	if err := SaveTownConfig(path, original); err != nil {
		t.Fatalf("SaveTownConfig: %v", err)
	}

	loaded, err := LoadTownConfig(path)
	if err != nil {
		t.Fatalf("LoadTownConfig: %v", err)
	}

	if loaded.Name != original.Name {
		t.Errorf("Name = %q, want %q", loaded.Name, original.Name)
	}
	if loaded.Type != original.Type {
		t.Errorf("Type = %q, want %q", loaded.Type, original.Type)
	}
}

func TestRigsConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "mayor", "rigs.json")

	original := &RigsConfig{
		Version: 1,
		Rigs: map[string]RigEntry{
			"gastown": {
				GitURL:    "git@github.com:steveyegge/gastown.git",
				LocalRepo: "/tmp/local-repo",
				AddedAt:   time.Now().Truncate(time.Second),
				BeadsConfig: &BeadsConfig{
					Repo:   "local",
					Prefix: "gt-",
				},
			},
		},
	}

	if err := SaveRigsConfig(path, original); err != nil {
		t.Fatalf("SaveRigsConfig: %v", err)
	}

	loaded, err := LoadRigsConfig(path)
	if err != nil {
		t.Fatalf("LoadRigsConfig: %v", err)
	}

	if len(loaded.Rigs) != 1 {
		t.Errorf("Rigs count = %d, want 1", len(loaded.Rigs))
	}

	rig, ok := loaded.Rigs["gastown"]
	if !ok {
		t.Fatal("missing 'gastown' rig")
	}
	if rig.BeadsConfig == nil || rig.BeadsConfig.Prefix != "gt-" {
		t.Errorf("BeadsConfig.Prefix = %v, want 'gt-'", rig.BeadsConfig)
	}
	if rig.LocalRepo != "/tmp/local-repo" {
		t.Errorf("LocalRepo = %q, want %q", rig.LocalRepo, "/tmp/local-repo")
	}
}

func TestLoadTownConfigNotFound(t *testing.T) {
	t.Parallel()
	_, err := LoadTownConfig("/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestValidationErrors(t *testing.T) {
	t.Parallel()
	// Missing name
	tc := &TownConfig{Type: "town", Version: 1}
	if err := validateTownConfig(tc); err == nil {
		t.Error("expected error for missing name")
	}

	// Wrong type
	tc = &TownConfig{Type: "wrong", Version: 1, Name: "test"}
	if err := validateTownConfig(tc); err == nil {
		t.Error("expected error for wrong type")
	}
}

func TestRigConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	original := NewRigConfig("gastown", "git@github.com:test/gastown.git")
	original.CreatedAt = time.Now().Truncate(time.Second)
	original.Beads = &BeadsConfig{Prefix: "gt-"}
	original.LocalRepo = "/tmp/local-repo"

	if err := SaveRigConfig(path, original); err != nil {
		t.Fatalf("SaveRigConfig: %v", err)
	}

	loaded, err := LoadRigConfig(path)
	if err != nil {
		t.Fatalf("LoadRigConfig: %v", err)
	}

	if loaded.Type != "rig" {
		t.Errorf("Type = %q, want 'rig'", loaded.Type)
	}
	if loaded.Version != CurrentRigConfigVersion {
		t.Errorf("Version = %d, want %d", loaded.Version, CurrentRigConfigVersion)
	}
	if loaded.Name != "gastown" {
		t.Errorf("Name = %q, want 'gastown'", loaded.Name)
	}
	if loaded.GitURL != "git@github.com:test/gastown.git" {
		t.Errorf("GitURL = %q, want expected URL", loaded.GitURL)
	}
	if loaded.LocalRepo != "/tmp/local-repo" {
		t.Errorf("LocalRepo = %q, want %q", loaded.LocalRepo, "/tmp/local-repo")
	}
	if loaded.Beads == nil || loaded.Beads.Prefix != "gt-" {
		t.Error("Beads.Prefix not preserved")
	}
}

func TestRigSettingsRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings", "config.json")

	original := NewRigSettings()

	if err := SaveRigSettings(path, original); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	loaded, err := LoadRigSettings(path)
	if err != nil {
		t.Fatalf("LoadRigSettings: %v", err)
	}

	if loaded.Type != "rig-settings" {
		t.Errorf("Type = %q, want 'rig-settings'", loaded.Type)
	}
	if loaded.MergeQueue == nil {
		t.Fatal("MergeQueue is nil")
	}
	if !loaded.MergeQueue.Enabled {
		t.Error("MergeQueue.Enabled = false, want true")
	}
}

func TestRigConfigValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		config  *RigConfig
		wantErr bool
	}{
		{
			name: "valid config",
			config: &RigConfig{
				Type:    "rig",
				Version: 1,
				Name:    "test-rig",
			},
			wantErr: false,
		},
		{
			name: "missing name",
			config: &RigConfig{
				Type:    "rig",
				Version: 1,
			},
			wantErr: true,
		},
		{
			name: "wrong type",
			config: &RigConfig{
				Type:    "wrong",
				Version: 1,
				Name:    "test",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRigConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRigConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRigSettingsValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		settings *RigSettings
		wantErr  bool
	}{
		{
			name: "valid settings",
			settings: &RigSettings{
				Type:       "rig-settings",
				Version:    1,
				MergeQueue: DefaultMergeQueueConfig(),
			},
			wantErr: false,
		},
		{
			name: "valid settings without merge queue",
			settings: &RigSettings{
				Type:    "rig-settings",
				Version: 1,
			},
			wantErr: false,
		},
		{
			name: "wrong type",
			settings: &RigSettings{
				Type:    "wrong",
				Version: 1,
			},
			wantErr: true,
		},
		{
			name: "invalid on_conflict",
			settings: &RigSettings{
				Type:    "rig-settings",
				Version: 1,
				MergeQueue: &MergeQueueConfig{
					OnConflict: "invalid",
				},
			},
			wantErr: true,
		},
		{
			name: "invalid poll_interval",
			settings: &RigSettings{
				Type:    "rig-settings",
				Version: 1,
				MergeQueue: &MergeQueueConfig{
					PollInterval: "not-a-duration",
				},
			},
			wantErr: true,
		},
		{
			name: "invalid stale_claim_timeout",
			settings: &RigSettings{
				Type:    "rig-settings",
				Version: 1,
				MergeQueue: &MergeQueueConfig{
					StaleClaimTimeout: "not-a-duration",
				},
			},
			wantErr: true,
		},
		{
			name: "zero stale_claim_timeout",
			settings: &RigSettings{
				Type:    "rig-settings",
				Version: 1,
				MergeQueue: &MergeQueueConfig{
					StaleClaimTimeout: "0s",
				},
			},
			wantErr: true,
		},
		{
			name: "negative stale_claim_timeout",
			settings: &RigSettings{
				Type:    "rig-settings",
				Version: 1,
				MergeQueue: &MergeQueueConfig{
					StaleClaimTimeout: "-5m",
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRigSettings(tt.settings)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRigSettings() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadRigConfigNotFound(t *testing.T) {
	t.Parallel()
	_, err := LoadRigConfig("/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestLoadRigSettingsNotFound(t *testing.T) {
	t.Parallel()
	_, err := LoadRigSettings("/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestLoadRepoSettings(t *testing.T) {
	t.Parallel()

	t.Run("returns nil when file missing", func(t *testing.T) {
		t.Parallel()
		settings, err := LoadRepoSettings("/nonexistent/repo")
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
		if settings != nil {
			t.Fatal("expected nil settings for missing file")
		}
	})

	t.Run("loads valid repo settings", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		gsDir := filepath.Join(dir, ".gastown")
		if err := os.MkdirAll(gsDir, 0755); err != nil {
			t.Fatal(err)
		}
		data := []byte(`{
			"type": "rig-settings",
			"version": 1,
			"merge_queue": {
				"test_command": "./scripts/ci/api.sh",
				"build_command": "dotnet build"
			}
		}`)
		if err := os.WriteFile(filepath.Join(gsDir, "settings.json"), data, 0644); err != nil {
			t.Fatal(err)
		}

		settings, err := LoadRepoSettings(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if settings == nil {
			t.Fatal("expected non-nil settings")
		}
		if settings.MergeQueue == nil {
			t.Fatal("expected non-nil MergeQueue")
		}
		if settings.MergeQueue.TestCommand != "./scripts/ci/api.sh" {
			t.Errorf("expected test_command='./scripts/ci/api.sh', got %q", settings.MergeQueue.TestCommand)
		}
		if settings.MergeQueue.BuildCommand != "dotnet build" {
			t.Errorf("expected build_command='dotnet build', got %q", settings.MergeQueue.BuildCommand)
		}
	})
}

func TestMergeSettingsCommand(t *testing.T) {
	t.Parallel()

	t.Run("nil inputs returns nil", func(t *testing.T) {
		t.Parallel()
		result := MergeSettingsCommand(nil, nil)
		if result != nil {
			t.Fatal("expected nil")
		}
	})

	t.Run("repo only", func(t *testing.T) {
		t.Parallel()
		repo := &MergeQueueConfig{TestCommand: "repo-test", BuildCommand: "repo-build"}
		result := MergeSettingsCommand(repo, nil)
		if result.TestCommand != "repo-test" {
			t.Errorf("expected 'repo-test', got %q", result.TestCommand)
		}
	})

	t.Run("local overrides repo", func(t *testing.T) {
		t.Parallel()
		repo := &MergeQueueConfig{TestCommand: "repo-test", BuildCommand: "repo-build", LintCommand: "repo-lint"}
		local := &MergeQueueConfig{TestCommand: "local-test"}
		result := MergeSettingsCommand(repo, local)
		if result.TestCommand != "local-test" {
			t.Errorf("expected 'local-test', got %q", result.TestCommand)
		}
		if result.BuildCommand != "repo-build" {
			t.Errorf("expected 'repo-build' (not overridden), got %q", result.BuildCommand)
		}
		if result.LintCommand != "repo-lint" {
			t.Errorf("expected 'repo-lint' (not overridden), got %q", result.LintCommand)
		}
	})

	t.Run("local only", func(t *testing.T) {
		t.Parallel()
		local := &MergeQueueConfig{TestCommand: "local-test"}
		result := MergeSettingsCommand(nil, local)
		if result.TestCommand != "local-test" {
			t.Errorf("expected 'local-test', got %q", result.TestCommand)
		}
	})
}

func TestMayorConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "mayor", "config.json")

	original := NewMayorConfig()
	original.Theme = &TownThemeConfig{
		Disabled: true,
		Name:     "forest",
		Custom: &CustomTheme{
			BG: "#111111",
			FG: "#eeeeee",
		},
		RoleDefaults: map[string]string{
			"witness": "rust",
		},
	}

	if err := SaveMayorConfig(path, original); err != nil {
		t.Fatalf("SaveMayorConfig: %v", err)
	}

	loaded, err := LoadMayorConfig(path)
	if err != nil {
		t.Fatalf("LoadMayorConfig: %v", err)
	}

	if loaded.Type != "mayor-config" {
		t.Errorf("Type = %q, want 'mayor-config'", loaded.Type)
	}
	if loaded.Version != CurrentMayorConfigVersion {
		t.Errorf("Version = %d, want %d", loaded.Version, CurrentMayorConfigVersion)
	}
	if loaded.Theme == nil || loaded.Theme.RoleDefaults["witness"] != "rust" {
		t.Error("Theme.RoleDefaults not preserved")
	}
	if loaded.Theme == nil || !loaded.Theme.Disabled {
		t.Error("Theme.Disabled not preserved")
	}
	if loaded.Theme == nil || loaded.Theme.Name != "forest" {
		t.Error("Theme.Name not preserved")
	}
	if loaded.Theme == nil || loaded.Theme.Custom == nil || loaded.Theme.Custom.BG != "#111111" || loaded.Theme.Custom.FG != "#eeeeee" {
		t.Error("Theme.Custom not preserved")
	}
}

func TestLoadMayorConfigNotFound(t *testing.T) {
	t.Parallel()
	_, err := LoadMayorConfig("/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestAccountsConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "mayor", "accounts.json")

	original := NewAccountsConfig()
	original.Accounts["yegge"] = Account{
		Email:       "steve.yegge@gmail.com",
		Description: "Personal account",
		ConfigDir:   "~/.claude-accounts/yegge",
	}
	original.Accounts["ghosttrack"] = Account{
		Email:       "steve@ghosttrack.com",
		Description: "Business account",
		ConfigDir:   "~/.claude-accounts/ghosttrack",
	}
	original.Default = "ghosttrack"

	if err := SaveAccountsConfig(path, original); err != nil {
		t.Fatalf("SaveAccountsConfig: %v", err)
	}

	loaded, err := LoadAccountsConfig(path)
	if err != nil {
		t.Fatalf("LoadAccountsConfig: %v", err)
	}

	if loaded.Version != CurrentAccountsVersion {
		t.Errorf("Version = %d, want %d", loaded.Version, CurrentAccountsVersion)
	}
	if len(loaded.Accounts) != 2 {
		t.Errorf("Accounts count = %d, want 2", len(loaded.Accounts))
	}
	if loaded.Default != "ghosttrack" {
		t.Errorf("Default = %q, want 'ghosttrack'", loaded.Default)
	}

	yegge := loaded.GetAccount("yegge")
	if yegge == nil {
		t.Fatal("GetAccount('yegge') returned nil")
	}
	if yegge.Email != "steve.yegge@gmail.com" {
		t.Errorf("yegge.Email = %q, want 'steve.yegge@gmail.com'", yegge.Email)
	}

	defAcct := loaded.GetDefaultAccount()
	if defAcct == nil {
		t.Fatal("GetDefaultAccount() returned nil")
	}
	if defAcct.Email != "steve@ghosttrack.com" {
		t.Errorf("default.Email = %q, want 'steve@ghosttrack.com'", defAcct.Email)
	}
}

func TestAccountsConfigValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		config  *AccountsConfig
		wantErr bool
	}{
		{
			name:    "valid empty config",
			config:  NewAccountsConfig(),
			wantErr: false,
		},
		{
			name: "valid config with accounts",
			config: &AccountsConfig{
				Version: 1,
				Accounts: map[string]Account{
					"test": {Email: "test@example.com", ConfigDir: "~/.claude-accounts/test"},
				},
				Default: "test",
			},
			wantErr: false,
		},
		{
			name: "default refers to nonexistent account",
			config: &AccountsConfig{
				Version: 1,
				Accounts: map[string]Account{
					"test": {Email: "test@example.com", ConfigDir: "~/.claude-accounts/test"},
				},
				Default: "nonexistent",
			},
			wantErr: true,
		},
		{
			name: "account missing config_dir",
			config: &AccountsConfig{
				Version: 1,
				Accounts: map[string]Account{
					"test": {Email: "test@example.com"},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAccountsConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateAccountsConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadAccountsConfigNotFound(t *testing.T) {
	t.Parallel()
	_, err := LoadAccountsConfig("/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}
