package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeConfigDefaults(t *testing.T) {
	t.Parallel()
	rc := DefaultRuntimeConfig()
	if rc.Provider != "claude" {
		t.Errorf("Provider = %q, want %q", rc.Provider, "claude")
	}
	if !isClaudeCommand(rc.Command) {
		t.Errorf("Command = %q, want claude or path ending in /claude", rc.Command)
	}
	if len(rc.Args) != 1 || rc.Args[0] != "--dangerously-skip-permissions" {
		t.Errorf("Args = %v, want [--dangerously-skip-permissions]", rc.Args)
	}
	if rc.Session == nil || rc.Session.SessionIDEnv != "CLAUDE_SESSION_ID" {
		t.Errorf("SessionIDEnv = %q, want %q", rc.Session.SessionIDEnv, "CLAUDE_SESSION_ID")
	}
}

func TestRuntimeConfigBuildCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		rc           *RuntimeConfig
		wantContains []string // Parts the command should contain
		isClaudeCmd  bool     // Whether command should be claude (or path to claude)
	}{
		{
			name:         "nil config uses defaults",
			rc:           nil,
			wantContains: []string{"--dangerously-skip-permissions"},
			isClaudeCmd:  true,
		},
		{
			name:         "default config",
			rc:           DefaultRuntimeConfig(),
			wantContains: []string{"--dangerously-skip-permissions"},
			isClaudeCmd:  true,
		},
		{
			name:         "custom command",
			rc:           &RuntimeConfig{Command: "aider", Args: []string{"--no-git"}},
			wantContains: []string{"aider", "--no-git"},
			isClaudeCmd:  false,
		},
		{
			name:         "multiple args",
			rc:           &RuntimeConfig{Command: "claude", Args: []string{"--model", "opus", "--no-confirm"}},
			wantContains: []string{"--model", "opus", "--no-confirm"},
			isClaudeCmd:  true,
		},
		{
			name:         "empty command uses default",
			rc:           &RuntimeConfig{Command: "", Args: nil},
			wantContains: []string{"--dangerously-skip-permissions"},
			isClaudeCmd:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.rc.BuildCommand()
			// Check command contains expected parts
			for _, part := range tt.wantContains {
				if !strings.Contains(got, part) {
					t.Errorf("BuildCommand() = %q, should contain %q", got, part)
				}
			}
			// Check if command starts with claude (or path to claude)
			if tt.isClaudeCmd {
				parts := strings.Fields(got)
				if len(parts) > 0 && !isClaudeCommand(parts[0]) {
					t.Errorf("BuildCommand() = %q, command should be claude or path to claude", got)
				}
			}
		})
	}
}

func TestRuntimeConfigBuildCommandWithPrompt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		rc           *RuntimeConfig
		prompt       string
		wantContains []string // Parts the command should contain
		isClaudeCmd  bool     // Whether command should be claude (or path to claude)
	}{
		{
			name:         "no prompt",
			rc:           DefaultRuntimeConfig(),
			prompt:       "",
			wantContains: []string{"--dangerously-skip-permissions"},
			isClaudeCmd:  true,
		},
		{
			name:         "with prompt",
			rc:           DefaultRuntimeConfig(),
			prompt:       "gt prime",
			wantContains: []string{"--dangerously-skip-permissions", `"gt prime"`},
			isClaudeCmd:  true,
		},
		{
			name:         "prompt with quotes",
			rc:           DefaultRuntimeConfig(),
			prompt:       `Hello "world"`,
			wantContains: []string{"--dangerously-skip-permissions", `"Hello \"world\""`},
			isClaudeCmd:  true,
		},
		{
			name:         "config initial prompt used if no override",
			rc:           &RuntimeConfig{Command: "aider", Args: []string{}, InitialPrompt: "/help"},
			prompt:       "",
			wantContains: []string{"aider", `"/help"`},
			isClaudeCmd:  false,
		},
		{
			name:         "override takes precedence over config",
			rc:           &RuntimeConfig{Command: "aider", Args: []string{}, InitialPrompt: "/help"},
			prompt:       "custom prompt",
			wantContains: []string{"aider", `"custom prompt"`},
			isClaudeCmd:  false,
		},
		{
			name:         "copilot uses -i flag for prompt",
			rc:           &RuntimeConfig{Command: "copilot", Args: []string{"--yolo"}, PromptMode: "arg"},
			prompt:       "test prompt",
			wantContains: []string{"copilot", "--yolo", "-i", `"test prompt"`},
			isClaudeCmd:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.rc.BuildCommandWithPrompt(tt.prompt)
			// Check command contains expected parts
			for _, part := range tt.wantContains {
				if !strings.Contains(got, part) {
					t.Errorf("BuildCommandWithPrompt(%q) = %q, should contain %q", tt.prompt, got, part)
				}
			}
			// Check if command starts with claude (or path to claude)
			if tt.isClaudeCmd {
				parts := strings.Fields(got)
				if len(parts) > 0 && !isClaudeCommand(parts[0]) {
					t.Errorf("BuildCommandWithPrompt(%q) = %q, command should be claude or path to claude", tt.prompt, got)
				}
			}
		})
	}
}

func TestValidateAgentConfig(t *testing.T) {
	t.Parallel()

	t.Run("valid built-in agent", func(t *testing.T) {
		// claude is a built-in preset and binary should exist
		err := ValidateAgentConfig("claude", nil, nil)
		// Note: This may fail if claude binary is not installed, which is expected
		if err != nil && !strings.Contains(err.Error(), "not found in PATH") {
			t.Errorf("unexpected error for claude: %v", err)
		}
	})

	t.Run("invalid agent name", func(t *testing.T) {
		err := ValidateAgentConfig("nonexistent-agent-xyz", nil, nil)
		if err == nil {
			t.Error("expected error for nonexistent agent")
		}
		if !strings.Contains(err.Error(), "not found in config or built-in presets") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("custom agent with missing binary", func(t *testing.T) {
		townSettings := NewTownSettings()
		townSettings.Agents = map[string]*RuntimeConfig{
			"my-custom-agent": {
				Command: "nonexistent-binary-xyz123",
				Args:    []string{"--some-flag"},
			},
		}
		err := ValidateAgentConfig("my-custom-agent", townSettings, nil)
		if err == nil {
			t.Error("expected error for missing binary")
		}
		if !strings.Contains(err.Error(), "not found in PATH") {
			t.Errorf("unexpected error message: %v", err)
		}
	})
}

func TestExpectedPaneCommands(t *testing.T) {
	t.Parallel()
	t.Run("claude maps to node and claude", func(t *testing.T) {
		got := ExpectedPaneCommands(&RuntimeConfig{Command: "claude"})
		want := []string{"node", "claude"}
		if len(got) != 2 || got[0] != "node" || got[1] != "claude" {
			t.Fatalf("ExpectedPaneCommands(claude) = %v, want %v", got, want)
		}
	})

	t.Run("codex maps to executable", func(t *testing.T) {
		got := ExpectedPaneCommands(&RuntimeConfig{Command: "codex"})
		if len(got) != 1 || got[0] != "codex" {
			t.Fatalf("ExpectedPaneCommands(codex) = %v, want %v", got, []string{"codex"})
		}
	})
}

func TestIsClaudeAgent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		rc       *RuntimeConfig
		expected bool
	}{
		{"empty provider and command (defaults)", &RuntimeConfig{}, true},
		{"explicit claude provider", &RuntimeConfig{Provider: "claude", Command: "anything"}, true},
		{"explicit codex provider", &RuntimeConfig{Provider: "codex", Command: "claude"}, false},
		{"bare claude command", &RuntimeConfig{Command: "claude"}, true},
		{"path to claude binary", &RuntimeConfig{Command: "/usr/local/bin/claude"}, true},
		{"aider command no provider", &RuntimeConfig{Command: "aider"}, false},
		{"generic provider", &RuntimeConfig{Provider: "generic"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isClaudeAgent(tt.rc); got != tt.expected {
				t.Errorf("isClaudeAgent(%+v) = %v, want %v", tt.rc, got, tt.expected)
			}
		})
	}
}

func TestWithRoleSettingsFlag_SkipsNonClaude(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "myrig")
	settingsDir := filepath.Join(rigPath, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("creating settings dir: %v", err)
	}

	// Configure aider (non-Claude agent) for polecat role
	settings := NewRigSettings()
	settings.Runtime = &RuntimeConfig{
		Command: "aider",
		Args:    []string{"--no-git", "--model", "claude-3"},
	}
	if err := SaveRigSettings(filepath.Join(settingsDir, "config.json"), settings); err != nil {
		t.Fatalf("saving settings: %v", err)
	}

	rc := ResolveRoleAgentConfig("polecat", townRoot, rigPath)
	// Should NOT contain --settings since aider is not a Claude agent
	for _, arg := range rc.Args {
		if arg == "--settings" {
			t.Errorf("non-Claude agent 'aider' should not get --settings flag, but Args = %v", rc.Args)
			break
		}
	}
}

func TestWithRoleSettingsFlag_InjectsForClaude(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "myrig")
	settingsDir := filepath.Join(rigPath, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("creating settings dir: %v", err)
	}

	// Default config (Claude agent) for polecat role
	settings := NewRigSettings()
	if err := SaveRigSettings(filepath.Join(settingsDir, "config.json"), settings); err != nil {
		t.Fatalf("saving settings: %v", err)
	}

	rc := ResolveRoleAgentConfig("polecat", townRoot, rigPath)
	// Should contain --settings since default agent is Claude
	found := false
	for _, arg := range rc.Args {
		if arg == "--settings" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("default Claude agent should get --settings flag for polecat role, but Args = %v", rc.Args)
	}
}

func TestRoleSettingsDir(t *testing.T) {
	t.Parallel()
	rigPath := "/fake/rig"
	tests := []struct {
		role string
		want string
	}{
		{"crew", filepath.Join(rigPath, "crew")},
		{"witness", filepath.Join(rigPath, "witness")},
		{"refinery", filepath.Join(rigPath, "refinery")},
		{"polecat", filepath.Join(rigPath, "polecats")},
		{"mayor", ""},
		{"deacon", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := RoleSettingsDir(tt.role, rigPath)
		if got != tt.want {
			t.Errorf("RoleSettingsDir(%q, %q) = %q, want %q", tt.role, rigPath, got, tt.want)
		}
	}
}

// TestLookupAgentConfigWithRigSettings verifies that lookupAgentConfig checks
// rig-level agents first, then town-level agents, then built-ins.
func TestLookupAgentConfigWithRigSettings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		rigSettings     *RigSettings
		townSettings    *TownSettings
		expectedCommand string
		expectedFrom    string
	}{
		{
			name: "rig-custom-agent",
			rigSettings: &RigSettings{
				Agent: "default-rig-agent",
				Agents: map[string]*RuntimeConfig{
					"rig-custom-agent": {
						Command: "custom-rig-cmd",
						Args:    []string{"--rig-flag"},
					},
				},
			},
			townSettings: &TownSettings{
				Agents: map[string]*RuntimeConfig{
					"town-custom-agent": {
						Command: "custom-town-cmd",
						Args:    []string{"--town-flag"},
					},
				},
			},
			expectedCommand: "custom-rig-cmd",
			expectedFrom:    "rig",
		},
		{
			name: "town-custom-agent",
			rigSettings: &RigSettings{
				Agents: map[string]*RuntimeConfig{
					"other-rig-agent": {
						Command: "other-rig-cmd",
					},
				},
			},
			townSettings: &TownSettings{
				Agents: map[string]*RuntimeConfig{
					"town-custom-agent": {
						Command: "custom-town-cmd",
						Args:    []string{"--town-flag"},
					},
				},
			},
			expectedCommand: "custom-town-cmd",
			expectedFrom:    "town",
		},
		{
			name:            "unknown-agent",
			rigSettings:     nil,
			townSettings:    nil,
			expectedCommand: "claude",
			expectedFrom:    "builtin",
		},
		{
			name: "claude",
			rigSettings: &RigSettings{
				Agent: "claude",
			},
			townSettings: &TownSettings{
				Agents: map[string]*RuntimeConfig{
					"claude": {
						Command: "custom-claude",
					},
				},
			},
			expectedCommand: "custom-claude",
			expectedFrom:    "town",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := lookupAgentConfig(tt.name, tt.townSettings, tt.rigSettings)

			if rc == nil {
				t.Errorf("lookupAgentConfig(%s) returned nil", tt.name)
			}

			// For claude commands, allow either "claude" or path ending in /claude
			if tt.expectedCommand == "claude" {
				if !isClaudeCommand(rc.Command) {
					t.Errorf("lookupAgentConfig(%s).Command = %s, want claude or path ending in /claude", tt.name, rc.Command)
				}
			} else if rc.Command != tt.expectedCommand {
				t.Errorf("lookupAgentConfig(%s).Command = %s, want %s", tt.name, rc.Command, tt.expectedCommand)
			}
		})
	}
}

// TestFillRuntimeDefaults tests the fillRuntimeDefaults function comprehensively.
func TestFillRuntimeDefaults(t *testing.T) {
	t.Parallel()

	t.Run("preserves all fields", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Provider:      "codex",
			Command:       "opencode",
			Args:          []string{"-m", "gpt-5"},
			Env:           map[string]string{"OPENCODE_PERMISSION": `{"*":"allow"}`},
			InitialPrompt: "test prompt",
			PromptMode:    "none",
			ResolvedAgent: "opencode",
			Session: &RuntimeSessionConfig{
				SessionIDEnv: "OPENCODE_SESSION_ID",
			},
			Hooks: &RuntimeHooksConfig{
				Provider: "opencode",
			},
			Tmux: &RuntimeTmuxConfig{
				ProcessNames: []string{"opencode", "node"},
			},
			Instructions: &RuntimeInstructionsConfig{
				File: "OPENCODE.md",
			},
		}

		result := fillRuntimeDefaults(input)

		if result.Provider != input.Provider {
			t.Errorf("Provider: got %q, want %q", result.Provider, input.Provider)
		}
		if result.Command != input.Command {
			t.Errorf("Command: got %q, want %q", result.Command, input.Command)
		}
		if len(result.Args) != len(input.Args) {
			t.Errorf("Args: got %v, want %v", result.Args, input.Args)
		}
		if result.Env["OPENCODE_PERMISSION"] != input.Env["OPENCODE_PERMISSION"] {
			t.Errorf("Env: got %v, want %v", result.Env, input.Env)
		}
		if result.InitialPrompt != input.InitialPrompt {
			t.Errorf("InitialPrompt: got %q, want %q", result.InitialPrompt, input.InitialPrompt)
		}
		if result.PromptMode != input.PromptMode {
			t.Errorf("PromptMode: got %q, want %q", result.PromptMode, input.PromptMode)
		}
		if result.Session == nil || result.Session.SessionIDEnv != input.Session.SessionIDEnv {
			t.Errorf("Session: got %+v, want %+v", result.Session, input.Session)
		}
		if result.Hooks == nil || result.Hooks.Provider != input.Hooks.Provider {
			t.Errorf("Hooks: got %+v, want %+v", result.Hooks, input.Hooks)
		}
		if result.Tmux == nil || len(result.Tmux.ProcessNames) != len(input.Tmux.ProcessNames) {
			t.Errorf("Tmux: got %+v, want %+v", result.Tmux, input.Tmux)
		}
		if result.Instructions == nil || result.Instructions.File != input.Instructions.File {
			t.Errorf("Instructions: got %+v, want %+v", result.Instructions, input.Instructions)
		}
		if result.ResolvedAgent != input.ResolvedAgent {
			t.Errorf("ResolvedAgent: got %q, want %q", result.ResolvedAgent, input.ResolvedAgent)
		}
	})

	t.Run("nil input returns defaults", func(t *testing.T) {
		t.Parallel()
		result := fillRuntimeDefaults(nil)

		if result == nil {
			t.Fatal("fillRuntimeDefaults(nil) returned nil")
		}
		if result.Command == "" {
			t.Error("Command should have default value")
		}
	})

	t.Run("empty command defaults to claude", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "",
			Args:    []string{"--custom-flag"},
		}

		result := fillRuntimeDefaults(input)

		// Use isClaudeCommand to handle resolved paths (e.g., /opt/homebrew/bin/claude)
		if !isClaudeCommand(result.Command) {
			t.Errorf("Command: got %q, want claude or path ending in claude", result.Command)
		}
		// Args should be preserved, not overwritten
		if len(result.Args) != 1 || result.Args[0] != "--custom-flag" {
			t.Errorf("Args should be preserved: got %v", result.Args)
		}
	})

	t.Run("nil args defaults to skip-permissions", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "claude",
			Args:    nil,
		}

		result := fillRuntimeDefaults(input)

		if result.Args == nil || len(result.Args) == 0 {
			t.Error("Args should have default value")
		}
		if result.Args[0] != "--dangerously-skip-permissions" {
			t.Errorf("Args: got %v, want [--dangerously-skip-permissions]", result.Args)
		}
	})

	t.Run("empty args slice is preserved", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "claude",
			Args:    []string{}, // Explicitly empty, not nil
		}

		result := fillRuntimeDefaults(input)

		// Empty slice means "no args", not "use defaults"
		// This is intentional per RuntimeConfig docs
		if result.Args == nil {
			t.Error("Empty Args slice should be preserved as empty, not nil")
		}
	})

	t.Run("env map is copied not shared", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "opencode",
			Env:     map[string]string{"KEY": "value"},
		}

		result := fillRuntimeDefaults(input)

		// Modify result's env
		result.Env["NEW_KEY"] = "new_value"

		// Original should be unchanged
		if _, ok := input.Env["NEW_KEY"]; ok {
			t.Error("Env map was not copied - modifications affect original")
		}
	})

	t.Run("prompt_mode none is preserved for custom agents", func(t *testing.T) {
		t.Parallel()
		// This is the specific bug that was fixed - opencode needs prompt_mode: "none"
		// to prevent the startup beacon from being passed as an argument
		input := &RuntimeConfig{
			Provider:   "opencode",
			Command:    "opencode",
			Args:       []string{"-m", "gpt-5"},
			PromptMode: "none",
		}

		result := fillRuntimeDefaults(input)

		if result.PromptMode != "none" {
			t.Errorf("PromptMode: got %q, want %q - custom prompt_mode was not preserved", result.PromptMode, "none")
		}
	})

	t.Run("args slice is deep copied not shared", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "opencode",
			Args:    []string{"original-arg"},
		}

		result := fillRuntimeDefaults(input)

		// Modify result's args
		result.Args[0] = "modified-arg"

		// Original should be unchanged
		if input.Args[0] != "original-arg" {
			t.Errorf("Args slice was not deep copied - modifications affect original: got %q, want %q",
				input.Args[0], "original-arg")
		}
	})

	t.Run("session struct is deep copied", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "claude",
			Session: &RuntimeSessionConfig{
				SessionIDEnv: "ORIGINAL_SESSION_ID",
				ConfigDirEnv: "ORIGINAL_CONFIG_DIR",
			},
		}

		result := fillRuntimeDefaults(input)

		// Modify result's session
		result.Session.SessionIDEnv = "MODIFIED_SESSION_ID"

		// Original should be unchanged
		if input.Session.SessionIDEnv != "ORIGINAL_SESSION_ID" {
			t.Errorf("Session struct was not deep copied - modifications affect original: got %q, want %q",
				input.Session.SessionIDEnv, "ORIGINAL_SESSION_ID")
		}
	})

	t.Run("hooks struct is deep copied", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "claude",
			Hooks: &RuntimeHooksConfig{
				Provider:     "original-provider",
				Dir:          "original-dir",
				SettingsFile: "original-file",
			},
		}

		result := fillRuntimeDefaults(input)

		// Modify result's hooks
		result.Hooks.Provider = "modified-provider"

		// Original should be unchanged
		if input.Hooks.Provider != "original-provider" {
			t.Errorf("Hooks struct was not deep copied - modifications affect original: got %q, want %q",
				input.Hooks.Provider, "original-provider")
		}
	})

	t.Run("tmux struct and process_names are deep copied", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "opencode",
			Tmux: &RuntimeTmuxConfig{
				ProcessNames:      []string{"original-process"},
				ReadyPromptPrefix: "original-prefix",
				ReadyDelayMs:      5000,
			},
		}

		result := fillRuntimeDefaults(input)

		// Modify result's tmux
		result.Tmux.ProcessNames[0] = "modified-process"
		result.Tmux.ReadyPromptPrefix = "modified-prefix"

		// Original should be unchanged
		if input.Tmux.ProcessNames[0] != "original-process" {
			t.Errorf("Tmux.ProcessNames was not deep copied - modifications affect original: got %q, want %q",
				input.Tmux.ProcessNames[0], "original-process")
		}
		if input.Tmux.ReadyPromptPrefix != "original-prefix" {
			t.Errorf("Tmux struct was not deep copied - modifications affect original: got %q, want %q",
				input.Tmux.ReadyPromptPrefix, "original-prefix")
		}
	})

	t.Run("instructions struct is deep copied", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "opencode",
			Instructions: &RuntimeInstructionsConfig{
				File: "ORIGINAL.md",
			},
		}

		result := fillRuntimeDefaults(input)

		// Modify result's instructions
		result.Instructions.File = "MODIFIED.md"

		// Original should be unchanged
		if input.Instructions.File != "ORIGINAL.md" {
			t.Errorf("Instructions struct was not deep copied - modifications affect original: got %q, want %q",
				input.Instructions.File, "ORIGINAL.md")
		}
	})

	t.Run("nil nested structs are auto-filled from preset for known agents", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "claude",
			// All nested structs left nil
		}

		result := fillRuntimeDefaults(input)

		// Hooks is auto-filled for known agents (claude, opencode) to ensure
		// EnsureSettingsForRole creates the correct settings files.
		if result.Hooks == nil {
			t.Error("Hooks should be auto-filled for claude command")
		} else if result.Hooks.Provider != "claude" {
			t.Errorf("Hooks.Provider = %q, want %q", result.Hooks.Provider, "claude")
		}
		// Session is auto-filled from preset so handoffs can propagate GT_SESSION_ID_ENV.
		if result.Session == nil {
			t.Error("Session should be auto-filled for claude command")
		} else if result.Session.SessionIDEnv != "CLAUDE_SESSION_ID" {
			t.Errorf("Session.SessionIDEnv = %q, want CLAUDE_SESSION_ID", result.Session.SessionIDEnv)
		}
		// Tmux is auto-filled from preset so WaitForRuntimeReady uses prompt detection.
		if result.Tmux == nil {
			t.Error("Tmux should be auto-filled for claude command")
		} else if result.Tmux.ReadyPromptPrefix != "❯ " {
			t.Errorf("Tmux.ReadyPromptPrefix = %q, want \"❯ \"", result.Tmux.ReadyPromptPrefix)
		}
		// Instructions is auto-filled from preset when nil.
		if result.Instructions == nil {
			t.Error("Instructions should be auto-filled for claude command")
		} else if result.Instructions.File != "CLAUDE.md" {
			t.Errorf("Instructions.File = %q, want CLAUDE.md", result.Instructions.File)
		}
	})

	t.Run("partial nested struct is copied without defaults", func(t *testing.T) {
		t.Parallel()
		// User defines partial Tmux config - only ProcessNames, no other fields
		input := &RuntimeConfig{
			Command: "opencode",
			Tmux: &RuntimeTmuxConfig{
				ProcessNames: []string{"opencode"},
				// ReadyPromptPrefix and ReadyDelayMs left at zero values
			},
		}

		result := fillRuntimeDefaults(input)

		// ProcessNames should be copied
		if len(result.Tmux.ProcessNames) != 1 || result.Tmux.ProcessNames[0] != "opencode" {
			t.Errorf("Tmux.ProcessNames not copied correctly: got %v", result.Tmux.ProcessNames)
		}
		// Zero values should remain zero (fillRuntimeDefaults doesn't fill nested defaults)
		if result.Tmux.ReadyDelayMs != 0 {
			t.Errorf("Tmux.ReadyDelayMs should be 0 (unfilled), got %d", result.Tmux.ReadyDelayMs)
		}
	})

	t.Run("pi command gets hooks and tmux defaults", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "pi",
		}

		result := fillRuntimeDefaults(input)

		// Hooks should be auto-filled for pi
		if result.Hooks == nil {
			t.Fatal("Hooks should be auto-filled for pi command")
		}
		if result.Hooks.Provider != "pi" {
			t.Errorf("Hooks.Provider = %q, want pi", result.Hooks.Provider)
		}
		if result.Hooks.Dir != ".pi/extensions" {
			t.Errorf("Hooks.Dir = %q, want .pi/extensions", result.Hooks.Dir)
		}
		if result.Hooks.SettingsFile != "gastown-hooks.js" {
			t.Errorf("Hooks.SettingsFile = %q, want gastown-hooks.js", result.Hooks.SettingsFile)
		}

		// Tmux should be auto-filled for pi
		if result.Tmux == nil {
			t.Fatal("Tmux should be auto-filled for pi command")
		}
		if len(result.Tmux.ProcessNames) != 3 {
			t.Errorf("Tmux.ProcessNames length = %d, want 3", len(result.Tmux.ProcessNames))
		}
		expectedNames := []string{"pi", "node", "bun"}
		for i, want := range expectedNames {
			if i < len(result.Tmux.ProcessNames) && result.Tmux.ProcessNames[i] != want {
				t.Errorf("Tmux.ProcessNames[%d] = %q, want %q", i, result.Tmux.ProcessNames[i], want)
			}
		}
		if result.Tmux.ReadyDelayMs != 8000 {
			t.Errorf("Tmux.ReadyDelayMs = %d, want 8000", result.Tmux.ReadyDelayMs)
		}

		// PromptMode should be "arg" for pi (from preset)
		if result.PromptMode != "arg" {
			t.Errorf("PromptMode = %q, want arg", result.PromptMode)
		}
	})

	t.Run("pi preserves user-specified hooks", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "pi",
			Hooks: &RuntimeHooksConfig{
				Provider:     "custom",
				Dir:          "custom-dir",
				SettingsFile: "custom.js",
			},
		}

		result := fillRuntimeDefaults(input)

		// User-specified hooks should be preserved
		if result.Hooks.Provider != "custom" {
			t.Errorf("Hooks.Provider = %q, want custom (user-specified)", result.Hooks.Provider)
		}
	})

	t.Run("pi preserves user-specified tmux", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "pi",
			Tmux: &RuntimeTmuxConfig{
				ProcessNames: []string{"custom-pi"},
				ReadyDelayMs: 5000,
			},
		}

		result := fillRuntimeDefaults(input)

		// User-specified tmux should be preserved
		if result.Tmux.ProcessNames[0] != "custom-pi" {
			t.Errorf("Tmux.ProcessNames[0] = %q, want custom-pi (user-specified)", result.Tmux.ProcessNames[0])
		}
		if result.Tmux.ReadyDelayMs != 5000 {
			t.Errorf("Tmux.ReadyDelayMs = %d, want 5000 (user-specified)", result.Tmux.ReadyDelayMs)
		}
	})

	t.Run("custom claude agent inherits Session and Tmux from preset", func(t *testing.T) {
		t.Parallel()
		// Simulates: gt config agent set claude-opus 'claude --model claude-opus-4-6'
		input := &RuntimeConfig{
			Command: "claude",
			Args:    []string{"--dangerously-skip-permissions", "--model", "claude-opus-4-6"},
		}

		result := fillRuntimeDefaults(input)

		if result.Session == nil {
			t.Fatal("Session should be auto-filled for claude command")
		}
		if result.Session.SessionIDEnv != "CLAUDE_SESSION_ID" {
			t.Errorf("Session.SessionIDEnv = %q, want CLAUDE_SESSION_ID", result.Session.SessionIDEnv)
		}
		if result.Tmux == nil {
			t.Fatal("Tmux should be auto-filled for claude command")
		}
		if result.Tmux.ReadyPromptPrefix != "❯ " {
			t.Errorf("Tmux.ReadyPromptPrefix = %q, want \"❯ \"", result.Tmux.ReadyPromptPrefix)
		}
		found := false
		for _, n := range result.Tmux.ProcessNames {
			if n == "claude" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Tmux.ProcessNames = %v, want to contain \"claude\"", result.Tmux.ProcessNames)
		}
		if len(result.Args) < 2 || result.Args[len(result.Args)-1] != "claude-opus-4-6" {
			t.Errorf("Args should be preserved: got %v", result.Args)
		}
	})

	t.Run("explicit Session config is not overridden by preset", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "claude",
			Session: &RuntimeSessionConfig{
				SessionIDEnv: "MY_CUSTOM_SESSION_ID",
			},
		}

		result := fillRuntimeDefaults(input)

		if result.Session.SessionIDEnv != "MY_CUSTOM_SESSION_ID" {
			t.Errorf("Session.SessionIDEnv = %q, want MY_CUSTOM_SESSION_ID (user-specified)", result.Session.SessionIDEnv)
		}
	})

	t.Run("explicit Tmux config is not overridden by preset", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "claude",
			Tmux: &RuntimeTmuxConfig{
				ProcessNames: []string{"my-claude-wrapper"},
			},
		}

		result := fillRuntimeDefaults(input)

		if len(result.Tmux.ProcessNames) != 1 || result.Tmux.ProcessNames[0] != "my-claude-wrapper" {
			t.Errorf("Tmux.ProcessNames = %v, want [my-claude-wrapper] (user-specified)", result.Tmux.ProcessNames)
		}
	})
}

// TestFillRuntimeDefaultsPresetMerging verifies preset defaults are merged
// into custom agent configs based on the Provider field or inferred command name.
func TestFillRuntimeDefaultsPresetMerging(t *testing.T) {
	t.Parallel()

	t.Run("custom agent with provider=gemini gets session defaults", func(t *testing.T) {
		t.Parallel()
		// Custom agent using a different binary but declaring gemini as provider
		input := &RuntimeConfig{
			Provider: "gemini",
			Command:  "gemini-custom",
			Args:     []string{"--fast-mode"},
		}

		result := fillRuntimeDefaults(input)

		// Session should be auto-filled from gemini preset
		if result.Session == nil {
			t.Fatal("Session should be auto-filled from gemini preset")
		}
		if result.Session.SessionIDEnv != "GEMINI_SESSION_ID" {
			t.Errorf("Session.SessionIDEnv = %q, want GEMINI_SESSION_ID", result.Session.SessionIDEnv)
		}
		// Tmux should be auto-filled from gemini preset
		if result.Tmux == nil {
			t.Fatal("Tmux should be auto-filled from gemini preset")
		}
		found := false
		for _, name := range result.Tmux.ProcessNames {
			if name == "gemini" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Tmux.ProcessNames should contain 'gemini', got %v", result.Tmux.ProcessNames)
		}
		// User-specified Args should be preserved
		if len(result.Args) != 1 || result.Args[0] != "--fast-mode" {
			t.Errorf("Args should be preserved: got %v", result.Args)
		}
	})

	t.Run("custom agent infers preset from command name", func(t *testing.T) {
		t.Parallel()
		// No provider set, but command matches a known preset
		input := &RuntimeConfig{
			Command: "gemini",
			Args:    []string{"--approval-mode", "custom"},
		}

		result := fillRuntimeDefaults(input)

		// Should get gemini preset defaults
		if result.Session == nil {
			t.Fatal("Session should be auto-filled from gemini preset (inferred from command)")
		}
		if result.Session.SessionIDEnv != "GEMINI_SESSION_ID" {
			t.Errorf("Session.SessionIDEnv = %q, want GEMINI_SESSION_ID", result.Session.SessionIDEnv)
		}
		// Args should be preserved (user override)
		if len(result.Args) != 2 || result.Args[0] != "--approval-mode" {
			t.Errorf("Args should be preserved: got %v", result.Args)
		}
	})

	t.Run("preset defaults not applied when fields already set", func(t *testing.T) {
		t.Parallel()
		// All fields explicitly set — preset should not override
		input := &RuntimeConfig{
			Provider: "claude",
			Command:  "custom-claude",
			Session: &RuntimeSessionConfig{
				SessionIDEnv: "MY_SESSION_ID",
			},
			Tmux: &RuntimeTmuxConfig{
				ProcessNames: []string{"my-process"},
			},
			Instructions: &RuntimeInstructionsConfig{
				File: "MY.md",
			},
			PromptMode: "none",
		}

		result := fillRuntimeDefaults(input)

		// User-set fields should not be overridden by preset
		if result.Session.SessionIDEnv != "MY_SESSION_ID" {
			t.Errorf("Session.SessionIDEnv overridden: got %q, want MY_SESSION_ID", result.Session.SessionIDEnv)
		}
		if len(result.Tmux.ProcessNames) != 1 || result.Tmux.ProcessNames[0] != "my-process" {
			t.Errorf("Tmux.ProcessNames overridden: got %v, want [my-process]", result.Tmux.ProcessNames)
		}
		if result.Instructions.File != "MY.md" {
			t.Errorf("Instructions.File overridden: got %q, want MY.md", result.Instructions.File)
		}
		if result.PromptMode != "none" {
			t.Errorf("PromptMode overridden: got %q, want none", result.PromptMode)
		}
	})
}

// TestLookupAgentConfigPreservesCustomFields verifies that custom agents
// have all their settings preserved through the lookup chain.
func TestLookupAgentConfigPreservesCustomFields(t *testing.T) {
	t.Parallel()

	townSettings := &TownSettings{
		Type:         "town-settings",
		Version:      1,
		DefaultAgent: "claude",
		Agents: map[string]*RuntimeConfig{
			"opencode-mayor": {
				Command:    "opencode",
				Args:       []string{"-m", "gpt-5"},
				PromptMode: "none",
				Env:        map[string]string{"OPENCODE_PERMISSION": `{"*":"allow"}`},
				Tmux: &RuntimeTmuxConfig{
					ProcessNames: []string{"opencode", "node"},
				},
			},
		},
	}

	rc := lookupAgentConfig("opencode-mayor", townSettings, nil)

	if rc == nil {
		t.Fatal("lookupAgentConfig returned nil for custom agent")
	}
	if rc.PromptMode != "none" {
		t.Errorf("PromptMode: got %q, want %q - setting was lost in lookup chain", rc.PromptMode, "none")
	}
	if rc.Command != "opencode" {
		t.Errorf("Command: got %q, want %q", rc.Command, "opencode")
	}
	if rc.Env["OPENCODE_PERMISSION"] != `{"*":"allow"}` {
		t.Errorf("Env was not preserved: got %v", rc.Env)
	}
	if rc.Tmux == nil || len(rc.Tmux.ProcessNames) != 2 {
		t.Errorf("Tmux.ProcessNames not preserved: got %+v", rc.Tmux)
	}
}

// TestBuildCommandWithPromptRespectsPromptModeNone verifies that when PromptMode
// is "none", the prompt is not appended to the command.
func TestBuildCommandWithPromptRespectsPromptModeNone(t *testing.T) {
	t.Parallel()

	rc := &RuntimeConfig{
		Command:    "opencode",
		Args:       []string{"-m", "gpt-5"},
		PromptMode: "none",
	}

	// Build command with a prompt that should be ignored
	cmd := rc.BuildCommandWithPrompt("This prompt should not appear")

	if strings.Contains(cmd, "This prompt should not appear") {
		t.Errorf("prompt_mode=none should prevent prompt from being added, got: %s", cmd)
	}
	if !strings.HasPrefix(cmd, "opencode") {
		t.Errorf("Command should start with opencode, got: %s", cmd)
	}
}

func TestTryResolveFromEphemeralTier(t *testing.T) {
	t.Run("no env var returns not handled", func(t *testing.T) {
		t.Setenv("GT_COST_TIER", "")
		rc, handled := tryResolveFromEphemeralTier("witness")
		if handled {
			t.Error("expected handled=false when GT_COST_TIER not set")
		}
		if rc != nil {
			t.Errorf("expected nil rc when GT_COST_TIER not set, got %+v", rc)
		}
	})

	t.Run("invalid tier returns not handled", func(t *testing.T) {
		t.Setenv("GT_COST_TIER", "premium")
		rc, handled := tryResolveFromEphemeralTier("witness")
		if handled {
			t.Error("expected handled=false for invalid tier")
		}
		if rc != nil {
			t.Errorf("expected nil rc for invalid tier, got %+v", rc)
		}
	})

	t.Run("budget tier witness gets haiku", func(t *testing.T) {
		t.Setenv("GT_COST_TIER", "budget")
		rc, handled := tryResolveFromEphemeralTier("witness")
		if !handled {
			t.Fatal("expected handled=true for witness in budget tier")
		}
		if rc == nil {
			t.Fatal("expected RuntimeConfig for witness in budget tier")
		}
		if !isClaudeCommand(rc.Command) {
			t.Errorf("Command = %q, want claude", rc.Command)
		}
		found := false
		for i, arg := range rc.Args {
			if arg == "--model" && i+1 < len(rc.Args) && rc.Args[i+1] == "haiku" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Args %v missing --model haiku", rc.Args)
		}
	})

	t.Run("economy tier polecat returns handled with nil rc (use default)", func(t *testing.T) {
		t.Setenv("GT_COST_TIER", "economy")
		rc, handled := tryResolveFromEphemeralTier("polecat")
		if !handled {
			t.Error("expected handled=true for polecat in economy tier (tier manages this role)")
		}
		if rc != nil {
			t.Errorf("expected nil rc for polecat in economy tier (should use default), got %+v", rc)
		}
	})

	t.Run("economy tier mayor gets sonnet", func(t *testing.T) {
		t.Setenv("GT_COST_TIER", "economy")
		rc, handled := tryResolveFromEphemeralTier("mayor")
		if !handled {
			t.Fatal("expected handled=true for mayor in economy tier")
		}
		if rc == nil {
			t.Fatal("expected RuntimeConfig for mayor in economy tier")
		}
		found := false
		for i, arg := range rc.Args {
			if arg == "--model" && i+1 < len(rc.Args) && rc.Args[i+1] == "sonnet[1m]" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Args %v missing --model sonnet[1m]", rc.Args)
		}
	})

	t.Run("standard tier returns handled with nil rc for all roles", func(t *testing.T) {
		t.Setenv("GT_COST_TIER", "standard")
		for _, role := range []string{"mayor", "deacon", "witness", "refinery", "polecat", "crew"} {
			rc, handled := tryResolveFromEphemeralTier(role)
			if !handled {
				t.Errorf("standard tier should return handled=true for %s", role)
			}
			if rc != nil {
				t.Errorf("standard tier should return nil rc for %s (use default), got %+v", role, rc)
			}
		}
	})
}

func TestWithRoleSettingsFlag_IdempotencyGuard(t *testing.T) {
	t.Parallel()
	rigPath := "/fake/town/myrig"
	rc := &RuntimeConfig{
		Command: "claude",
		Args:    []string{"--dangerously-skip-permissions", "--settings", "/already/set/.claude/settings.json"},
	}

	before := len(rc.Args)
	result := withRoleSettingsFlag(rc, "polecat", rigPath)

	if len(result.Args) != before {
		t.Errorf("idempotency guard failed: expected %d args, got %d — Args = %v", before, len(result.Args), result.Args)
	}
	count := 0
	for _, arg := range result.Args {
		if arg == "--settings" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 --settings flag, got %d — Args = %v", count, result.Args)
	}
}
