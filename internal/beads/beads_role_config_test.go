package beads

import (
	"strings"
	"testing"
)

// TestParseRoleConfig tests parsing role configuration from descriptions.
func TestParseRoleConfig(t *testing.T) {
	tests := []struct {
		name        string
		description string
		wantNil     bool
		wantConfig  *RoleConfig
	}{
		{
			name:        "empty description",
			description: "",
			wantNil:     true,
		},
		{
			name:        "no role config fields",
			description: "This is just plain text\nwith no role config fields",
			wantNil:     true,
		},
		{
			name: "all fields",
			description: `session_pattern: gt-{rig}-{name}
work_dir_pattern: {town}/{rig}/polecats/{name}
needs_pre_sync: true
start_command: exec claude --dangerously-skip-permissions
env_var: GT_ROLE=polecat
env_var: GT_RIG={rig}`,
			wantConfig: &RoleConfig{
				SessionPattern: "gt-{rig}-{name}",
				WorkDirPattern: "{town}/{rig}/polecats/{name}",
				NeedsPreSync:   true,
				StartCommand:   "exec claude --dangerously-skip-permissions",
				EnvVars:        map[string]string{"GT_ROLE": "polecat", "GT_RIG": "{rig}"},
			},
		},
		{
			name: "partial fields",
			description: `session_pattern: gt-mayor
work_dir_pattern: {town}`,
			wantConfig: &RoleConfig{
				SessionPattern: "gt-mayor",
				WorkDirPattern: "{town}",
				EnvVars:        map[string]string{},
			},
		},
		{
			name: "mixed with prose",
			description: `You are the Witness.

session_pattern: gt-{rig}-witness
work_dir_pattern: {town}/{rig}
needs_pre_sync: false

Your job is to monitor workers.`,
			wantConfig: &RoleConfig{
				SessionPattern: "gt-{rig}-witness",
				WorkDirPattern: "{town}/{rig}",
				NeedsPreSync:   false,
				EnvVars:        map[string]string{},
			},
		},
		{
			name: "alternate key formats (hyphen)",
			description: `session-pattern: gt-{rig}-{name}
work-dir-pattern: {town}/{rig}/polecats/{name}
needs-pre-sync: true`,
			wantConfig: &RoleConfig{
				SessionPattern: "gt-{rig}-{name}",
				WorkDirPattern: "{town}/{rig}/polecats/{name}",
				NeedsPreSync:   true,
				EnvVars:        map[string]string{},
			},
		},
		{
			name: "case insensitive keys",
			description: `SESSION_PATTERN: gt-mayor
Work_Dir_Pattern: {town}`,
			wantConfig: &RoleConfig{
				SessionPattern: "gt-mayor",
				WorkDirPattern: "{town}",
				EnvVars:        map[string]string{},
			},
		},
		{
			name: "ignores null values",
			description: `session_pattern: gt-{rig}-witness
work_dir_pattern: null
needs_pre_sync: false`,
			wantConfig: &RoleConfig{
				SessionPattern: "gt-{rig}-witness",
				EnvVars:        map[string]string{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := ParseRoleConfig(tt.description)

			if tt.wantNil {
				if config != nil {
					t.Errorf("ParseRoleConfig() = %+v, want nil", config)
				}
				return
			}

			if config == nil {
				t.Fatal("ParseRoleConfig() = nil, want non-nil")
			}

			if config.SessionPattern != tt.wantConfig.SessionPattern {
				t.Errorf("SessionPattern = %q, want %q", config.SessionPattern, tt.wantConfig.SessionPattern)
			}
			if config.WorkDirPattern != tt.wantConfig.WorkDirPattern {
				t.Errorf("WorkDirPattern = %q, want %q", config.WorkDirPattern, tt.wantConfig.WorkDirPattern)
			}
			if config.NeedsPreSync != tt.wantConfig.NeedsPreSync {
				t.Errorf("NeedsPreSync = %v, want %v", config.NeedsPreSync, tt.wantConfig.NeedsPreSync)
			}
			if config.StartCommand != tt.wantConfig.StartCommand {
				t.Errorf("StartCommand = %q, want %q", config.StartCommand, tt.wantConfig.StartCommand)
			}
			if len(config.EnvVars) != len(tt.wantConfig.EnvVars) {
				t.Errorf("EnvVars len = %d, want %d", len(config.EnvVars), len(tt.wantConfig.EnvVars))
			}
			for k, v := range tt.wantConfig.EnvVars {
				if config.EnvVars[k] != v {
					t.Errorf("EnvVars[%q] = %q, want %q", k, config.EnvVars[k], v)
				}
			}
		})
	}
}

// TestExpandRolePattern tests pattern expansion with placeholders.
func TestExpandRolePattern(t *testing.T) {
	tests := []struct {
		pattern  string
		townRoot string
		rig      string
		name     string
		role     string
		prefix   string
		want     string
	}{
		{
			pattern:  "gt-mayor",
			townRoot: "/Users/stevey/gt",
			want:     "gt-mayor",
		},
		{
			pattern:  "{prefix}-{role}",
			townRoot: "/Users/stevey/gt",
			rig:      "gastown",
			role:     "witness",
			prefix:   "gt",
			want:     "gt-witness",
		},
		{
			pattern:  "{prefix}-{name}",
			townRoot: "/Users/stevey/gt",
			rig:      "gastown",
			name:     "toast",
			prefix:   "gt",
			want:     "gt-toast",
		},
		{
			pattern:  "{town}/{rig}/polecats/{name}",
			townRoot: "/Users/stevey/gt",
			rig:      "gastown",
			name:     "toast",
			want:     "/Users/stevey/gt/gastown/polecats/toast",
		},
		{
			pattern:  "{town}/{rig}/refinery/rig",
			townRoot: "/Users/stevey/gt",
			rig:      "gastown",
			want:     "/Users/stevey/gt/gastown/refinery/rig",
		},
		{
			pattern:  "export GT_ROLE={role} GT_RIG={rig} BD_ACTOR={rig}/polecats/{name}",
			townRoot: "/Users/stevey/gt",
			rig:      "gastown",
			name:     "toast",
			role:     "polecat",
			want:     "export GT_ROLE=polecat GT_RIG=gastown BD_ACTOR=gastown/polecats/toast",
		},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			got := ExpandRolePattern(tt.pattern, tt.townRoot, tt.rig, tt.name, tt.role, tt.prefix)
			if got != tt.want {
				t.Errorf("ExpandRolePattern() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFormatRoleConfig tests formatting role config to string.
func TestFormatRoleConfig(t *testing.T) {
	tests := []struct {
		name   string
		config *RoleConfig
		want   string
	}{
		{
			name:   "nil config",
			config: nil,
			want:   "",
		},
		{
			name:   "empty config",
			config: &RoleConfig{EnvVars: map[string]string{}},
			want:   "",
		},
		{
			name: "all fields",
			config: &RoleConfig{
				SessionPattern: "gt-{rig}-{name}",
				WorkDirPattern: "{town}/{rig}/polecats/{name}",
				NeedsPreSync:   true,
				StartCommand:   "exec claude",
				EnvVars:        map[string]string{},
			},
			want: `session_pattern: gt-{rig}-{name}
work_dir_pattern: {town}/{rig}/polecats/{name}
needs_pre_sync: true
start_command: exec claude`,
		},
		{
			name: "only session pattern",
			config: &RoleConfig{
				SessionPattern: "gt-mayor",
				EnvVars:        map[string]string{},
			},
			want: "session_pattern: gt-mayor",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatRoleConfig(tt.config)
			if got != tt.want {
				t.Errorf("FormatRoleConfig() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

// TestRoleConfigRoundTrip tests that parse/format round-trips correctly.
func TestRoleConfigRoundTrip(t *testing.T) {
	original := &RoleConfig{
		SessionPattern: "gt-{rig}-{name}",
		WorkDirPattern: "{town}/{rig}/polecats/{name}",
		NeedsPreSync:   true,
		StartCommand:   "exec claude --dangerously-skip-permissions",
		EnvVars:        map[string]string{}, // Can't round-trip env vars due to order
	}

	// Format to string
	formatted := FormatRoleConfig(original)

	// Parse back
	parsed := ParseRoleConfig(formatted)

	if parsed == nil {
		t.Fatal("round-trip parse returned nil")
	}

	if parsed.SessionPattern != original.SessionPattern {
		t.Errorf("round-trip SessionPattern = %q, want %q", parsed.SessionPattern, original.SessionPattern)
	}
	if parsed.WorkDirPattern != original.WorkDirPattern {
		t.Errorf("round-trip WorkDirPattern = %q, want %q", parsed.WorkDirPattern, original.WorkDirPattern)
	}
	if parsed.NeedsPreSync != original.NeedsPreSync {
		t.Errorf("round-trip NeedsPreSync = %v, want %v", parsed.NeedsPreSync, original.NeedsPreSync)
	}
	if parsed.StartCommand != original.StartCommand {
		t.Errorf("round-trip StartCommand = %q, want %q", parsed.StartCommand, original.StartCommand)
	}
}

// TestParseRoleConfigWispTTLs tests parsing wisp_ttl_* fields from role config.
func TestParseRoleConfigWispTTLs(t *testing.T) {
	tests := []struct {
		name        string
		description string
		wantNil     bool
		wantTTLs    map[string]string
	}{
		{
			name: "single wisp TTL",
			description: `session_pattern: gt-{rig}-{name}
wisp_ttl_patrol: 48h`,
			wantTTLs: map[string]string{"patrol": "48h"},
		},
		{
			name: "multiple wisp TTLs",
			description: `wisp_ttl_patrol: 48h
wisp_ttl_error: 336h
wisp_ttl_gc_report: 24h`,
			wantTTLs: map[string]string{
				"patrol":    "48h",
				"error":     "336h",
				"gc_report": "24h",
			},
		},
		{
			name: "hyphenated key format",
			description: `wisp-ttl-patrol: 48h
wisp-ttl-error: 336h`,
			wantTTLs: map[string]string{
				"patrol": "48h",
				"error":  "336h",
			},
		},
		{
			name: "mixed with other role config fields",
			description: `session_pattern: gt-{rig}-{name}
work_dir_pattern: {town}/{rig}
wisp_ttl_patrol: 48h
ping_timeout: 30s
wisp_ttl_error: 336h`,
			wantTTLs: map[string]string{
				"patrol": "48h",
				"error":  "336h",
			},
		},
		{
			name:        "wisp TTL only (no other fields)",
			description: `wisp_ttl_patrol: 24h`,
			wantTTLs:    map[string]string{"patrol": "24h"},
		},
		{
			name:        "no wisp TTLs present",
			description: `session_pattern: gt-{rig}-{name}`,
			wantTTLs:    map[string]string{},
		},
		{
			name: "case insensitive keys",
			description: `WISP_TTL_PATROL: 48h
Wisp_TTL_Error: 336h`,
			wantTTLs: map[string]string{
				"patrol": "48h",
				"error":  "336h",
			},
		},
		{
			name:        "wisp TTL with default type",
			description: `wisp_ttl_default: 168h`,
			wantTTLs:    map[string]string{"default": "168h"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := ParseRoleConfig(tt.description)

			if tt.wantNil {
				if config != nil {
					t.Errorf("ParseRoleConfig() = %+v, want nil", config)
				}
				return
			}

			if config == nil {
				t.Fatal("ParseRoleConfig() = nil, want non-nil")
			}

			if len(config.WispTTLs) != len(tt.wantTTLs) {
				t.Errorf("WispTTLs len = %d, want %d\ngot: %v\nwant: %v",
					len(config.WispTTLs), len(tt.wantTTLs), config.WispTTLs, tt.wantTTLs)
			}
			for k, v := range tt.wantTTLs {
				if config.WispTTLs[k] != v {
					t.Errorf("WispTTLs[%q] = %q, want %q", k, config.WispTTLs[k], v)
				}
			}
		})
	}
}

// TestFormatRoleConfigWispTTLs tests that wisp TTLs are included in format output.
func TestFormatRoleConfigWispTTLs(t *testing.T) {
	config := &RoleConfig{
		SessionPattern: "gt-{rig}-{name}",
		EnvVars:        map[string]string{},
		WispTTLs: map[string]string{
			"patrol": "48h",
			"error":  "336h",
		},
	}

	formatted := FormatRoleConfig(config)

	if !strings.Contains(formatted, "wisp_ttl_error: 336h") {
		t.Errorf("formatted output missing wisp_ttl_error, got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "wisp_ttl_patrol: 48h") {
		t.Errorf("formatted output missing wisp_ttl_patrol, got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "session_pattern: gt-{rig}-{name}") {
		t.Errorf("formatted output missing session_pattern, got:\n%s", formatted)
	}
}

// TestRoleConfigWispTTLRoundTrip tests that wisp TTLs survive parse/format round-trip.
func TestRoleConfigWispTTLRoundTrip(t *testing.T) {
	original := &RoleConfig{
		SessionPattern: "gt-{rig}-{name}",
		EnvVars:        map[string]string{},
		WispTTLs: map[string]string{
			"patrol":    "48h",
			"error":     "336h",
			"gc_report": "24h",
		},
	}

	formatted := FormatRoleConfig(original)
	parsed := ParseRoleConfig(formatted)

	if parsed == nil {
		t.Fatal("round-trip parse returned nil")
	}

	if len(parsed.WispTTLs) != len(original.WispTTLs) {
		t.Fatalf("round-trip WispTTLs len = %d, want %d", len(parsed.WispTTLs), len(original.WispTTLs))
	}
	for k, v := range original.WispTTLs {
		if parsed.WispTTLs[k] != v {
			t.Errorf("round-trip WispTTLs[%q] = %q, want %q", k, parsed.WispTTLs[k], v)
		}
	}
}

// TestParseWispTTLKey tests the wisp TTL key parser directly.
func TestParseWispTTLKey(t *testing.T) {
	tests := []struct {
		key      string
		wantType string
		wantOK   bool
	}{
		{"wisp_ttl_patrol", "patrol", true},
		{"wisp_ttl_error", "error", true},
		{"wisp_ttl_gc_report", "gc_report", true},
		{"wisp-ttl-patrol", "patrol", true},
		{"wisp-ttl-error", "error", true},
		{"wispttlpatrol", "patrol", true},
		{"wisp_ttl_", "", false}, // empty type
		{"wisp-ttl-", "", false}, // empty type
		{"session_pattern", "", false},
		{"wisp_patrol", "", false},
		{"ttl_patrol", "", false},
		{"", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			gotType, gotOK := ParseWispTTLKey(tt.key)
			if gotOK != tt.wantOK {
				t.Errorf("ParseWispTTLKey(%q) ok = %v, want %v", tt.key, gotOK, tt.wantOK)
			}
			if gotType != tt.wantType {
				t.Errorf("ParseWispTTLKey(%q) type = %q, want %q", tt.key, gotType, tt.wantType)
			}
		})
	}
}
