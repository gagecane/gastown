package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEscalationConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings", "escalation.json")

	original := &EscalationConfig{
		Type:    "escalation",
		Version: CurrentEscalationVersion,
		Routes: map[string][]string{
			SeverityLow:      {"bead"},
			SeverityMedium:   {"bead", "mail:mayor"},
			SeverityHigh:     {"bead", "mail:mayor", "email:human"},
			SeverityCritical: {"bead", "mail:mayor", "email:human", "sms:human"},
		},
		Contacts: EscalationContacts{
			HumanEmail: "test@example.com",
			HumanSMS:   "+15551234567",
		},
		StaleThreshold:   "2h",
		MaxReescalations: intPtr(3),
	}

	if err := SaveEscalationConfig(path, original); err != nil {
		t.Fatalf("SaveEscalationConfig: %v", err)
	}

	loaded, err := LoadEscalationConfig(path)
	if err != nil {
		t.Fatalf("LoadEscalationConfig: %v", err)
	}

	if loaded.Type != original.Type {
		t.Errorf("Type = %q, want %q", loaded.Type, original.Type)
	}
	if loaded.Version != original.Version {
		t.Errorf("Version = %d, want %d", loaded.Version, original.Version)
	}
	if loaded.StaleThreshold != original.StaleThreshold {
		t.Errorf("StaleThreshold = %q, want %q", loaded.StaleThreshold, original.StaleThreshold)
	}
	if *loaded.MaxReescalations != *original.MaxReescalations {
		t.Errorf("MaxReescalations = %d, want %d", *loaded.MaxReescalations, *original.MaxReescalations)
	}
	if loaded.Contacts.HumanEmail != original.Contacts.HumanEmail {
		t.Errorf("Contacts.HumanEmail = %q, want %q", loaded.Contacts.HumanEmail, original.Contacts.HumanEmail)
	}
	if loaded.Contacts.HumanSMS != original.Contacts.HumanSMS {
		t.Errorf("Contacts.HumanSMS = %q, want %q", loaded.Contacts.HumanSMS, original.Contacts.HumanSMS)
	}

	// Check routes
	for severity, actions := range original.Routes {
		loadedActions := loaded.Routes[severity]
		if len(loadedActions) != len(actions) {
			t.Errorf("Routes[%s] len = %d, want %d", severity, len(loadedActions), len(actions))
			continue
		}
		for i, action := range actions {
			if loadedActions[i] != action {
				t.Errorf("Routes[%s][%d] = %q, want %q", severity, i, loadedActions[i], action)
			}
		}
	}
}

func TestEscalationConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg := NewEscalationConfig()

	if cfg.Type != "escalation" {
		t.Errorf("Type = %q, want %q", cfg.Type, "escalation")
	}
	if cfg.Version != CurrentEscalationVersion {
		t.Errorf("Version = %d, want %d", cfg.Version, CurrentEscalationVersion)
	}
	if cfg.StaleThreshold != "4h" {
		t.Errorf("StaleThreshold = %q, want %q", cfg.StaleThreshold, "4h")
	}
	if cfg.MaxReescalations == nil || *cfg.MaxReescalations != 2 {
		t.Errorf("MaxReescalations = %v, want %d", cfg.MaxReescalations, 2)
	}

	// Check default routes
	if len(cfg.Routes) != 4 {
		t.Errorf("Routes count = %d, want 4", len(cfg.Routes))
	}
	if len(cfg.Routes[SeverityLow]) != 1 || cfg.Routes[SeverityLow][0] != "bead" {
		t.Errorf("Routes[low] = %v, want [bead]", cfg.Routes[SeverityLow])
	}
	if len(cfg.Routes[SeverityCritical]) != 4 {
		t.Errorf("Routes[critical] len = %d, want 4", len(cfg.Routes[SeverityCritical]))
	}
}

func TestEscalationConfigValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  *EscalationConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: &EscalationConfig{
				Type:    "escalation",
				Version: 1,
				Routes: map[string][]string{
					SeverityLow: {"bead"},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid type",
			config: &EscalationConfig{
				Type:    "wrong-type",
				Version: 1,
			},
			wantErr: true,
			errMsg:  "invalid config type",
		},
		{
			name: "unsupported version",
			config: &EscalationConfig{
				Type:    "escalation",
				Version: 999,
			},
			wantErr: true,
			errMsg:  "unsupported config version",
		},
		{
			name: "invalid stale threshold",
			config: &EscalationConfig{
				Type:           "escalation",
				Version:        1,
				StaleThreshold: "not-a-duration",
			},
			wantErr: true,
			errMsg:  "invalid stale_threshold",
		},
		{
			name: "invalid severity key",
			config: &EscalationConfig{
				Type:    "escalation",
				Version: 1,
				Routes: map[string][]string{
					"invalid-severity": {"bead"},
				},
			},
			wantErr: true,
			errMsg:  "unknown severity",
		},
		{
			name: "negative max reescalations",
			config: &EscalationConfig{
				Type:             "escalation",
				Version:          1,
				MaxReescalations: intPtr(-1),
			},
			wantErr: true,
			errMsg:  "max_reescalations must be non-negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateEscalationConfig(tt.config)
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateEscalationConfig() expected error containing %q, got nil", tt.errMsg)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateEscalationConfig() error = %v, want error containing %q", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validateEscalationConfig() unexpected error: %v", err)
				}
			}
		})
	}
}

func TestEscalationConfigGetStaleThreshold(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   *EscalationConfig
		expected time.Duration
	}{
		{
			name:     "default when empty",
			config:   &EscalationConfig{},
			expected: 4 * time.Hour,
		},
		{
			name: "2 hours",
			config: &EscalationConfig{
				StaleThreshold: "2h",
			},
			expected: 2 * time.Hour,
		},
		{
			name: "30 minutes",
			config: &EscalationConfig{
				StaleThreshold: "30m",
			},
			expected: 30 * time.Minute,
		},
		{
			name: "invalid duration falls back to default",
			config: &EscalationConfig{
				StaleThreshold: "invalid",
			},
			expected: 4 * time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.GetStaleThreshold()
			if got != tt.expected {
				t.Errorf("GetStaleThreshold() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestEscalationConfigGetRouteForSeverity(t *testing.T) {
	t.Parallel()

	cfg := &EscalationConfig{
		Routes: map[string][]string{
			SeverityLow:    {"bead"},
			SeverityMedium: {"bead", "mail:mayor"},
		},
	}

	tests := []struct {
		severity string
		expected []string
	}{
		{SeverityLow, []string{"bead"}},
		{SeverityMedium, []string{"bead", "mail:mayor"}},
		{SeverityHigh, []string{"bead", "mail:mayor"}},     // fallback for missing
		{SeverityCritical, []string{"bead", "mail:mayor"}}, // fallback for missing
	}

	for _, tt := range tests {
		t.Run(tt.severity, func(t *testing.T) {
			got := cfg.GetRouteForSeverity(tt.severity)
			if len(got) != len(tt.expected) {
				t.Errorf("GetRouteForSeverity(%s) len = %d, want %d", tt.severity, len(got), len(tt.expected))
				return
			}
			for i, action := range tt.expected {
				if got[i] != action {
					t.Errorf("GetRouteForSeverity(%s)[%d] = %q, want %q", tt.severity, i, got[i], action)
				}
			}
		})
	}
}

func TestEscalationConfigGetMaxReescalations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   *EscalationConfig
		expected int
	}{
		{
			name:     "default when nil",
			config:   &EscalationConfig{},
			expected: 2,
		},
		{
			name: "explicit zero means never re-escalate",
			config: &EscalationConfig{
				MaxReescalations: intPtr(0),
			},
			expected: 0,
		},
		{
			name: "custom value",
			config: &EscalationConfig{
				MaxReescalations: intPtr(5),
			},
			expected: 5,
		},
		{
			name: "negative returns negative (should not happen after validation)",
			config: &EscalationConfig{
				MaxReescalations: intPtr(-1),
			},
			expected: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.GetMaxReescalations()
			if got != tt.expected {
				t.Errorf("GetMaxReescalations() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestLoadOrCreateEscalationConfig(t *testing.T) {
	t.Parallel()

	t.Run("creates default when not found", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings", "escalation.json")

		cfg, err := LoadOrCreateEscalationConfig(path)
		if err != nil {
			t.Fatalf("LoadOrCreateEscalationConfig: %v", err)
		}

		if cfg.Type != "escalation" {
			t.Errorf("Type = %q, want %q", cfg.Type, "escalation")
		}
		if len(cfg.Routes) != 4 {
			t.Errorf("Routes count = %d, want 4", len(cfg.Routes))
		}
	})

	t.Run("loads existing config", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings", "escalation.json")

		// Create a config first
		original := &EscalationConfig{
			Type:           "escalation",
			Version:        1,
			StaleThreshold: "1h",
			Routes: map[string][]string{
				SeverityLow: {"bead"},
			},
		}
		if err := SaveEscalationConfig(path, original); err != nil {
			t.Fatalf("SaveEscalationConfig: %v", err)
		}

		// Load it
		cfg, err := LoadOrCreateEscalationConfig(path)
		if err != nil {
			t.Fatalf("LoadOrCreateEscalationConfig: %v", err)
		}

		if cfg.StaleThreshold != "1h" {
			t.Errorf("StaleThreshold = %q, want %q", cfg.StaleThreshold, "1h")
		}
	})
}

func TestEscalationConfigPath(t *testing.T) {
	t.Parallel()

	path := EscalationConfigPath("/home/user/gt")
	expected := "/home/user/gt/settings/escalation.json"
	if filepath.ToSlash(path) != expected {
		t.Errorf("EscalationConfigPath = %q, want %q", path, expected)
	}
}
