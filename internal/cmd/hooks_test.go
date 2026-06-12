package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/hooks"
)

func TestInstallHookToSerializesCorrectly(t *testing.T) {
	// Regression test: installHookTo must use hooks.MarshalSettings, not
	// json.MarshalIndent. SettingsJSON fields use json:"-" tags, so
	// encoding/json produces {} and silently clobbers hooks/plugins.
	tmpDir := t.TempDir()

	hookDef := HookDefinition{
		Event:    "SessionStart",
		Command:  "echo hello",
		Matchers: []string{""},
		Roles:    []string{"crew"},
		Enabled:  true,
	}

	err := installHookTo(tmpDir, hookDef, false)
	if err != nil {
		t.Fatalf("installHookTo failed: %v", err)
	}

	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("failed to read settings: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "hooks") {
		t.Error("installed settings.json missing 'hooks' key — likely serialized with json.MarshalIndent instead of hooks.MarshalSettings")
	}
	if !strings.Contains(content, "enabledPlugins") {
		t.Error("installed settings.json missing 'enabledPlugins' key")
	}
	if !strings.Contains(content, "echo hello") {
		t.Error("installed settings.json missing hook command")
	}
}

func TestDiscoverHooksCrewLevel(t *testing.T) {
	// Create a temp directory structure simulating a Gas Town workspace
	tmpDir := t.TempDir()

	// Create rig structure with shared crew and polecats settings at the parent level.
	// DiscoverTargets targets the shared parent directories (crew/.claude/settings.json),
	// not individual crew member or polecat worktree directories.
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	// Create shared crew settings (crew/.claude/settings.json)
	crewClaudeDir := filepath.Join(rigDir, "crew", ".claude")
	if err := os.MkdirAll(crewClaudeDir, 0755); err != nil {
		t.Fatalf("failed to create crew/.claude dir: %v", err)
	}

	crewSettings := hooks.SettingsJSON{
		Hooks: hooks.HooksConfig{
			SessionStart: []hooks.HookEntry{
				{
					Matcher: "",
					Hooks: []hooks.Hook{
						{Type: "command", Command: "crew-level-hook"},
					},
				},
			},
		},
	}
	crewData, _ := hooks.MarshalSettings(&crewSettings)
	if err := os.WriteFile(filepath.Join(crewClaudeDir, "settings.json"), crewData, 0644); err != nil {
		t.Fatalf("failed to write crew settings: %v", err)
	}

	// Create shared polecats settings (polecats/.claude/settings.json)
	polecatsClaudeDir := filepath.Join(rigDir, "polecats", ".claude")
	if err := os.MkdirAll(polecatsClaudeDir, 0755); err != nil {
		t.Fatalf("failed to create polecats/.claude dir: %v", err)
	}

	polecatsSettings := hooks.SettingsJSON{
		Hooks: hooks.HooksConfig{
			PreToolUse: []hooks.HookEntry{
				{
					Matcher: "",
					Hooks: []hooks.Hook{
						{Type: "command", Command: "polecats-level-hook"},
					},
				},
			},
		},
	}
	polecatsData, _ := hooks.MarshalSettings(&polecatsSettings)
	if err := os.WriteFile(filepath.Join(polecatsClaudeDir, "settings.json"), polecatsData, 0644); err != nil {
		t.Fatalf("failed to write polecats settings: %v", err)
	}

	// Discover hooks
	hookInfos, err := discoverHooks(tmpDir)
	if err != nil {
		t.Fatalf("discoverHooks failed: %v", err)
	}

	// Verify shared crew and polecats hooks were discovered
	var foundCrewLevel, foundPolecatsLevel bool
	for _, h := range hookInfos {
		if h.Agent == "testrig/crew" && len(h.Commands) > 0 && h.Commands[0] == "crew-level-hook" {
			foundCrewLevel = true
		}
		if h.Agent == "testrig/polecats" && len(h.Commands) > 0 && h.Commands[0] == "polecats-level-hook" {
			foundPolecatsLevel = true
		}
	}

	if !foundCrewLevel {
		t.Error("expected crew hook to be discovered (testrig/crew)")
	}
	if !foundPolecatsLevel {
		t.Error("expected polecats hook to be discovered (testrig/polecats)")
	}
}

func TestResolveSettingsTarget(t *testing.T) {
	townRoot := "/home/user/gt"

	tests := []struct {
		name     string
		cwd      string
		expected string
	}{
		{
			name:     "crew member worktree resolves to crew parent",
			cwd:      "/home/user/gt/myrig/crew/alice",
			expected: "/home/user/gt/myrig/crew",
		},
		{
			name:     "deeply nested crew path resolves to crew parent",
			cwd:      "/home/user/gt/myrig/crew/alice/src/pkg",
			expected: "/home/user/gt/myrig/crew",
		},
		{
			name:     "polecat worktree resolves to polecats parent",
			cwd:      "/home/user/gt/myrig/polecats/toast/myrig",
			expected: "/home/user/gt/myrig/polecats",
		},
		{
			name:     "witness subdir resolves to witness parent",
			cwd:      "/home/user/gt/myrig/witness/rig",
			expected: "/home/user/gt/myrig/witness",
		},
		{
			name:     "refinery subdir resolves to refinery parent",
			cwd:      "/home/user/gt/myrig/refinery/rig",
			expected: "/home/user/gt/myrig/refinery",
		},
		{
			name:     "mayor stays at cwd",
			cwd:      "/home/user/gt/mayor",
			expected: "/home/user/gt/mayor",
		},
		{
			name:     "deacon stays at cwd",
			cwd:      "/home/user/gt/deacon",
			expected: "/home/user/gt/deacon",
		},
		{
			name:     "town root stays at cwd",
			cwd:      "/home/user/gt",
			expected: "/home/user/gt",
		},
		{
			name:     "rig root stays at cwd",
			cwd:      "/home/user/gt/myrig",
			expected: "/home/user/gt/myrig",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.FromSlash(townRoot)
			cwd := filepath.FromSlash(tt.cwd)
			want := filepath.FromSlash(tt.expected)
			got := resolveSettingsTarget(root, cwd)
			if got != want {
				t.Errorf("resolveSettingsTarget(%q, %q) = %q, want %q", root, cwd, got, want)
			}
		})
	}
}
