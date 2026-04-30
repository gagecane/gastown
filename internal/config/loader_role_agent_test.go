package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
)

func TestResolveAgentConfigWithOverride(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Town settings: default agent is gemini, plus a custom alias.
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "gemini"
	townSettings.Agents["claude-haiku"] = &RuntimeConfig{
		Command: "claude",
		Args:    []string{"--model", "haiku", "--dangerously-skip-permissions"},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Rig settings: prefer codex unless overridden.
	rigSettings := NewRigSettings()
	rigSettings.Agent = "codex"
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	t.Run("no override uses rig agent", func(t *testing.T) {
		rc, name, err := ResolveAgentConfigWithOverride(townRoot, rigPath, "")
		if err != nil {
			t.Fatalf("ResolveAgentConfigWithOverride: %v", err)
		}
		if name != "codex" {
			t.Fatalf("name = %q, want %q", name, "codex")
		}
		if rc.Command != "codex" {
			t.Fatalf("rc.Command = %q, want %q", rc.Command, "codex")
		}
	})

	t.Run("override uses built-in preset", func(t *testing.T) {
		rc, name, err := ResolveAgentConfigWithOverride(townRoot, rigPath, "gemini")
		if err != nil {
			t.Fatalf("ResolveAgentConfigWithOverride: %v", err)
		}
		if name != "gemini" {
			t.Fatalf("name = %q, want %q", name, "gemini")
		}
		if rc.Command != "gemini" {
			t.Fatalf("rc.Command = %q, want %q", rc.Command, "gemini")
		}
	})

	t.Run("override uses custom agent alias", func(t *testing.T) {
		rc, name, err := ResolveAgentConfigWithOverride(townRoot, rigPath, "claude-haiku")
		if err != nil {
			t.Fatalf("ResolveAgentConfigWithOverride: %v", err)
		}
		if name != "claude-haiku" {
			t.Fatalf("name = %q, want %q", name, "claude-haiku")
		}
		if !isClaudeCommand(rc.Command) {
			t.Fatalf("rc.Command = %q, want claude or path ending in /claude", rc.Command)
		}
		got := rc.BuildCommand()
		// Check command includes expected flags (path to claude may vary)
		if !strings.Contains(got, "--model haiku") || !strings.Contains(got, "--dangerously-skip-permissions") {
			t.Fatalf("BuildCommand() = %q, want command with --model haiku and --dangerously-skip-permissions", got)
		}
	})

	t.Run("override uses custom codex hooks alias", func(t *testing.T) {
		townSettings := NewTownSettings()
		townSettings.Agents["codex-worker-hooks"] = &RuntimeConfig{
			Command:    "codex",
			Args:       []string{"--dangerously-bypass-approvals-and-sandbox"},
			PromptMode: "arg",
			Hooks: &RuntimeHooksConfig{
				Provider:     "codex",
				Dir:          ".codex",
				SettingsFile: "hooks.json",
			},
		}
		if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
			t.Fatalf("SaveTownSettings: %v", err)
		}

		rc, name, err := ResolveAgentConfigWithOverride(townRoot, rigPath, "codex-worker-hooks")
		if err != nil {
			t.Fatalf("ResolveAgentConfigWithOverride: %v", err)
		}
		if name != "codex-worker-hooks" {
			t.Fatalf("name = %q, want %q", name, "codex-worker-hooks")
		}
		if rc.Command != "codex" {
			t.Fatalf("rc.Command = %q, want %q", rc.Command, "codex")
		}
		if rc.PromptMode != "arg" {
			t.Fatalf("rc.PromptMode = %q, want %q", rc.PromptMode, "arg")
		}
		if rc.Hooks == nil {
			t.Fatal("expected hooks config")
		}
		if rc.Hooks.Provider != "codex" || rc.Hooks.Dir != ".codex" || rc.Hooks.SettingsFile != "hooks.json" {
			t.Fatalf("unexpected hooks config: %+v", rc.Hooks)
		}
		args := rc.BuildArgsWithPrompt("start here")
		if len(args) == 0 || args[len(args)-1] != "start here" {
			t.Fatalf("BuildArgsWithPrompt should append prompt positionally, got %v", args)
		}
	})

	t.Run("unknown override errors", func(t *testing.T) {
		_, _, err := ResolveAgentConfigWithOverride(townRoot, rigPath, "nope-not-an-agent")
		if err == nil {
			t.Fatal("expected error for unknown agent override")
		}
	})

	t.Run("override with subcommand", func(t *testing.T) {
		rc, name, err := ResolveAgentConfigWithOverride(townRoot, rigPath, "opencode acp")
		if err != nil {
			t.Fatalf("ResolveAgentConfigWithOverride: %v", err)
		}
		if name != "opencode" {
			t.Fatalf("name = %q, want %q", name, "opencode")
		}
		if rc.Command != "opencode" {
			t.Fatalf("rc.Command = %q, want %q", rc.Command, "opencode")
		}
		// Verify "acp" was appended to Args
		found := false
		for _, arg := range rc.Args {
			if arg == "acp" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("rc.Args = %v, want it to contain %q", rc.Args, "acp")
		}
	})
}

func TestResolveRoleAgentConfig_FallsBackOnInvalidAgent(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Configure town settings with an invalid agent for refinery
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.RoleAgents = map[string]string{
		constants.RoleRefinery: "nonexistent-agent-xyz", // Invalid agent
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Create empty rig settings
	rigSettings := NewRigSettings()
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	// Should fall back to default (claude) when agent is invalid
	rc := ResolveRoleAgentConfig(constants.RoleRefinery, townRoot, rigPath)
	// Command can be "claude" or a resolved platform-specific claude binary path.
	if !isClaudeCommand(rc.Command) {
		t.Errorf("expected fallback to claude or path ending in /claude, got: %s", rc.Command)
	}
}

func TestResolveRoleAgentConfigFromRigSettings(t *testing.T) {
	t.Parallel()
	// Create temp town with rig containing custom runtime config
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "myrig")
	settingsDir := filepath.Join(rigPath, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("creating settings dir: %v", err)
	}

	settings := NewRigSettings()
	settings.Runtime = &RuntimeConfig{
		Command:  "aider",
		Provider: "aider",
		Args:     []string{"--no-git", "--model", "claude-3"},
	}
	if err := SaveRigSettings(filepath.Join(settingsDir, "config.json"), settings); err != nil {
		t.Fatalf("saving settings: %v", err)
	}

	// Load and verify using ResolveRoleAgentConfig
	rc := ResolveRoleAgentConfig("polecat", townRoot, rigPath)
	if rc.Command != "aider" {
		t.Errorf("Command = %q, want %q", rc.Command, "aider")
	}
	if len(rc.Args) != 3 {
		t.Errorf("Args = %v, want 3 args", rc.Args)
	}

	cmd := rc.BuildCommand()
	if cmd != "aider --no-git --model claude-3" {
		t.Errorf("BuildCommand() = %q, want %q", cmd, "aider --no-git --model claude-3")
	}
}

func TestResolveRoleAgentConfigFallsBackToDefaults(t *testing.T) {
	t.Parallel()
	// Non-existent paths should use defaults
	rc := ResolveRoleAgentConfig("polecat", "/nonexistent/town", "/nonexistent/rig")
	if !isClaudeCommand(rc.Command) {
		t.Errorf("Command = %q, want claude or path ending in /claude (default)", rc.Command)
	}
}

func TestResolveWorkerAgentConfig_WorkerSpecificOverridesRole(t *testing.T) {
	// Cannot use t.Parallel — uses t.Setenv
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "myrig")

	// Create a fake codex binary so ValidateAgentConfig passes
	binDir := t.TempDir()
	writeAgentStub(t, binDir, "codex")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	settings := NewRigSettings()
	settings.RoleAgents = map[string]string{constants.RoleCrew: "claude"}
	settings.WorkerAgents = map[string]string{"denali": "codex"}
	if err := SaveRigSettings(RigSettingsPath(rigPath), settings); err != nil {
		t.Fatalf("saving settings: %v", err)
	}

	rc := ResolveWorkerAgentConfig("denali", townRoot, rigPath)
	if rc.Provider != "codex" && !strings.Contains(rc.Command, "codex") {
		t.Errorf("expected codex for worker denali, got provider=%q command=%q", rc.Provider, rc.Command)
	}
}

func TestResolveWorkerAgentConfig_FallsBackToRoleAgents(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "myrig")

	settings := NewRigSettings()
	settings.RoleAgents = map[string]string{constants.RoleCrew: "claude"}
	settings.WorkerAgents = map[string]string{"denali": "codex"} // only denali is overridden
	if err := SaveRigSettings(RigSettingsPath(rigPath), settings); err != nil {
		t.Fatalf("saving settings: %v", err)
	}

	// "glacier" is not in worker_agents — should fall through to role_agents["crew"] = claude
	rc := ResolveWorkerAgentConfig("glacier", townRoot, rigPath)
	if !isClaudeCommand(rc.Command) {
		t.Errorf("expected claude fallback for glacier (not in worker_agents), got command=%q", rc.Command)
	}
}

func TestResolveWorkerAgentConfig_EmptyWorkerNameFallsBackToRole(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "myrig")

	settings := NewRigSettings()
	settings.WorkerAgents = map[string]string{"denali": "codex"}
	if err := SaveRigSettings(RigSettingsPath(rigPath), settings); err != nil {
		t.Fatalf("saving settings: %v", err)
	}

	// Empty worker name should fall back to crew role resolution (claude default)
	rc := ResolveWorkerAgentConfig("", townRoot, rigPath)
	if !isClaudeCommand(rc.Command) {
		t.Errorf("expected claude for empty worker name, got command=%q", rc.Command)
	}
}

func TestResolveWorkerAgentConfig_TownCrewAgents(t *testing.T) {
	// Cannot use t.Parallel — uses t.Setenv
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "myrig")

	// Create fake agent binaries (needs .exe on Windows for exec.LookPath).
	// Both codex and claude stubs are needed: codex for town crew_agents,
	// claude for the rig worker_agents override subtest.
	binDir := t.TempDir()
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	for _, name := range []string{"codex", "claude"} {
		stubPath := filepath.Join(binDir, name+ext)
		if err := os.WriteFile(stubPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatalf("write %s stub: %v", name, err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Set up town settings with crew_agents but NO rig worker_agents
	townSettings := NewTownSettings()
	townSettings.CrewAgents = map[string]string{"bob": "codex"}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("saving town settings: %v", err)
	}

	// Save rig settings without worker_agents
	rigSettings := NewRigSettings()
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("saving rig settings: %v", err)
	}

	t.Run("town crew_agents resolves for named worker", func(t *testing.T) {
		rc := ResolveWorkerAgentConfig("bob", townRoot, rigPath)
		if rc.Provider != "codex" && !strings.Contains(rc.Command, "codex") {
			t.Errorf("expected codex for crew worker bob via town crew_agents, got provider=%q command=%q", rc.Provider, rc.Command)
		}
	})

	t.Run("worker not in town crew_agents falls through to defaults", func(t *testing.T) {
		rc := ResolveWorkerAgentConfig("alice", townRoot, rigPath)
		if !isClaudeCommand(rc.Command) {
			t.Errorf("expected claude fallback for alice (not in crew_agents), got command=%q", rc.Command)
		}
	})

	t.Run("rig worker_agents takes priority over town crew_agents", func(t *testing.T) {
		// Add rig-level worker_agents that should override town crew_agents
		rigSettings2 := NewRigSettings()
		rigSettings2.WorkerAgents = map[string]string{"bob": "claude"}
		if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings2); err != nil {
			t.Fatalf("saving rig settings: %v", err)
		}
		rc := ResolveWorkerAgentConfig("bob", townRoot, rigPath)
		if !isClaudeCommand(rc.Command) {
			t.Errorf("expected claude for bob (rig worker_agents should override town crew_agents), got command=%q", rc.Command)
		}
	})
}

// TestRoleAgentConfigWithCustomAgent tests role-based agent resolution with
// custom agents that have special settings like prompt_mode: "none".
//
// This test mirrors manual verification using settings/config.json:
//
//	{
//	  "type": "town-settings",
//	  "version": 1,
//	  "default_agent": "claude-opus",
//	  "agents": {
//	    "amp-yolo": {
//	      "command": "amp",
//	      "args": ["--dangerously-allow-all"]
//	    },
//	    "opencode-mayor": {
//	      "command": "opencode",
//	      "args": ["-m", "openai/gpt-5.2-codex"],
//	      "prompt_mode": "none",
//	      "process_names": ["opencode", "node"],
//	      "env": {
//	        "OPENCODE_PERMISSION": "{\"*\":\"allow\"}"
//	      }
//	    }
//	  },
//	  "role_agents": {
//	    "crew": "claude-sonnet",
//	    "deacon": "claude-haiku",
//	    "mayor": "opencode-mayor",
//	    "polecat": "claude-opus",
//	    "refinery": "claude-opus",
//	    "witness": "claude-sonnet"
//	  }
//	}
//
// Manual test procedure:
//  1. Set role_agents.mayor to each agent (claude, gemini, codex, cursor, auggie, amp, opencode)
//  2. Run: gt start
//  3. Verify mayor starts with correct agent config
//  4. Run: GT_NUKE_ACKNOWLEDGED=1 gt down --nuke
//  5. Repeat for all 7 built-in agents
func TestRoleAgentConfigWithCustomAgent(t *testing.T) {
	skipIfAgentBinaryMissing(t, "opencode", "claude")
	t.Parallel()

	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Create town settings mirroring the manual test config
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude-opus"
	townSettings.RoleAgents = map[string]string{
		constants.RoleMayor:    "opencode-mayor",
		constants.RoleDeacon:   "claude-haiku",
		constants.RolePolecat:  "claude-opus",
		constants.RoleRefinery: "claude-opus",
		constants.RoleWitness:  "claude-sonnet",
		constants.RoleCrew:     "claude-sonnet",
	}
	townSettings.Agents = map[string]*RuntimeConfig{
		"opencode-mayor": {
			Command:    "opencode",
			Args:       []string{"-m", "openai/gpt-5.2-codex"},
			PromptMode: "none",
			Env:        map[string]string{"OPENCODE_PERMISSION": `{"*":"allow"}`},
			Tmux: &RuntimeTmuxConfig{
				ProcessNames: []string{"opencode", "node"},
			},
		},
		"amp-yolo": {
			Command: "amp",
			Args:    []string{"--dangerously-allow-all"},
		},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Create minimal rig settings
	rigSettings := NewRigSettings()
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	// Test mayor role gets opencode-mayor with prompt_mode: none
	t.Run("mayor gets opencode-mayor config", func(t *testing.T) {
		rc := ResolveRoleAgentConfig(constants.RoleMayor, townRoot, rigPath)
		if rc == nil {
			t.Fatal("ResolveRoleAgentConfig returned nil for mayor")
		}
		if rc.Command != "opencode" {
			t.Errorf("Command: got %q, want %q", rc.Command, "opencode")
		}
		if rc.PromptMode != "none" {
			t.Errorf("PromptMode: got %q, want %q - critical for opencode", rc.PromptMode, "none")
		}
		if rc.Env["OPENCODE_PERMISSION"] != `{"*":"allow"}` {
			t.Errorf("Env not preserved: got %v", rc.Env)
		}

		// Verify startup beacon is NOT added to command
		cmd := rc.BuildCommandWithPrompt("[GAS TOWN] mayor <- human • cold-start")
		if strings.Contains(cmd, "GAS TOWN") {
			t.Errorf("prompt_mode=none should prevent beacon, got: %s", cmd)
		}
	})

	// Test other roles get their configured agents
	t.Run("deacon gets claude-haiku", func(t *testing.T) {
		rc := ResolveRoleAgentConfig(constants.RoleDeacon, townRoot, rigPath)
		if rc == nil {
			t.Fatal("ResolveRoleAgentConfig returned nil for deacon")
		}
		// claude-haiku is a built-in preset
		if !strings.Contains(rc.Command, "claude") && rc.Command != "claude" {
			t.Errorf("Command: got %q, want claude-based command", rc.Command)
		}
	})

	t.Run("polecat gets claude-opus", func(t *testing.T) {
		rc := ResolveRoleAgentConfig(constants.RolePolecat, townRoot, rigPath)
		if rc == nil {
			t.Fatal("ResolveRoleAgentConfig returned nil for polecat")
		}
		if !strings.Contains(rc.Command, "claude") && rc.Command != "claude" {
			t.Errorf("Command: got %q, want claude-based command", rc.Command)
		}
	})
}

// TestMultipleAgentTypes tests that various built-in agent presets work correctly.
// NOTE: Only these are actual built-in presets: claude, gemini, codex, cursor, auggie, amp, opencode.
// Variants like "claude-opus", "claude-haiku", "claude-sonnet" are NOT built-in - they need
// to be defined as custom agents in TownSettings.Agents if specific model selection is needed.
func TestMultipleAgentTypes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		agentName     string
		expectCommand string
		isBuiltIn     bool // true if this is an actual built-in preset
	}{
		{
			name:          "claude built-in preset",
			agentName:     "claude",
			expectCommand: "claude",
			isBuiltIn:     true,
		},
		{
			name:          "codex built-in preset",
			agentName:     "codex",
			expectCommand: "codex",
			isBuiltIn:     true,
		},
		{
			name:          "gemini built-in preset",
			agentName:     "gemini",
			expectCommand: "gemini",
			isBuiltIn:     true,
		},
		{
			name:          "amp built-in preset",
			agentName:     "amp",
			expectCommand: "amp",
			isBuiltIn:     true,
		},
		{
			name:          "opencode built-in preset",
			agentName:     "opencode",
			expectCommand: "opencode",
			isBuiltIn:     true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Skip if agent binary not installed (prevents flaky CI failures)
			skipIfAgentBinaryMissing(t, tc.agentName)

			// Verify it's actually a built-in preset
			if tc.isBuiltIn {
				preset := GetAgentPresetByName(tc.agentName)
				if preset == nil {
					t.Errorf("%s should be a built-in preset but GetAgentPresetByName returned nil", tc.agentName)
					return
				}
			}

			townRoot := t.TempDir()
			rigPath := filepath.Join(townRoot, "testrig")

			townSettings := NewTownSettings()
			townSettings.DefaultAgent = "claude"
			townSettings.RoleAgents = map[string]string{
				constants.RoleMayor: tc.agentName,
			}
			if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
				t.Fatalf("SaveTownSettings: %v", err)
			}

			rigSettings := NewRigSettings()
			if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
				t.Fatalf("SaveRigSettings: %v", err)
			}

			rc := ResolveRoleAgentConfig(constants.RoleMayor, townRoot, rigPath)
			if rc == nil {
				t.Fatalf("ResolveRoleAgentConfig returned nil for %s", tc.agentName)
			}

			// Allow path-based commands (e.g., /opt/homebrew/bin/claude)
			if !strings.Contains(rc.Command, tc.expectCommand) {
				t.Errorf("Command: got %q, want command containing %q", rc.Command, tc.expectCommand)
			}
		})
	}
}

// TestCustomClaudeVariants tests that Claude model variants (opus, sonnet, haiku) need
// to be explicitly defined as custom agents since they are NOT built-in presets.
func TestCustomClaudeVariants(t *testing.T) {
	skipIfAgentBinaryMissing(t, "claude")
	t.Parallel()

	// Verify that claude-opus/sonnet/haiku are NOT built-in presets
	variants := []string{"claude-opus", "claude-sonnet", "claude-haiku"}
	for _, variant := range variants {
		if preset := GetAgentPresetByName(variant); preset != nil {
			t.Errorf("%s should NOT be a built-in preset (only 'claude' is), but GetAgentPresetByName returned non-nil", variant)
		}
	}

	// Test that custom claude variants work when explicitly defined
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.RoleAgents = map[string]string{
		constants.RoleMayor:  "claude-opus",
		constants.RoleDeacon: "claude-haiku",
	}
	// Define the custom variants
	townSettings.Agents = map[string]*RuntimeConfig{
		"claude-opus": {
			Command: "claude",
			Args:    []string{"--model", "claude-opus-4", "--dangerously-skip-permissions"},
		},
		"claude-haiku": {
			Command: "claude",
			Args:    []string{"--model", "claude-haiku-3", "--dangerously-skip-permissions"},
		},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	rigSettings := NewRigSettings()
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	// Test claude-opus custom agent
	rc := ResolveRoleAgentConfig(constants.RoleMayor, townRoot, rigPath)
	if rc == nil {
		t.Fatal("ResolveRoleAgentConfig returned nil for claude-opus")
	}
	if !strings.Contains(rc.Command, "claude") {
		t.Errorf("claude-opus Command: got %q, want claude", rc.Command)
	}
	foundModel := false
	for _, arg := range rc.Args {
		if arg == "claude-opus-4" {
			foundModel = true
			break
		}
	}
	if !foundModel {
		t.Errorf("claude-opus Args should contain model flag: got %v", rc.Args)
	}

	// Test claude-haiku custom agent
	rc = ResolveRoleAgentConfig(constants.RoleDeacon, townRoot, rigPath)
	if rc == nil {
		t.Fatal("ResolveRoleAgentConfig returned nil for claude-haiku")
	}
	foundModel = false
	for _, arg := range rc.Args {
		if arg == "claude-haiku-3" {
			foundModel = true
			break
		}
	}
	if !foundModel {
		t.Errorf("claude-haiku Args should contain model flag: got %v", rc.Args)
	}
}

// TestCustomAgentWithAmp tests custom agent configuration for amp.
// This mirrors the manual test: amp-yolo started successfully with custom args.
func TestCustomAgentWithAmp(t *testing.T) {
	skipIfAgentBinaryMissing(t, "amp")
	t.Parallel()

	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.RoleAgents = map[string]string{
		constants.RoleMayor: "amp-yolo",
	}
	townSettings.Agents = map[string]*RuntimeConfig{
		"amp-yolo": {
			Command: "amp",
			Args:    []string{"--dangerously-allow-all"},
		},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	rigSettings := NewRigSettings()
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	rc := ResolveRoleAgentConfig(constants.RoleMayor, townRoot, rigPath)
	if rc == nil {
		t.Fatal("ResolveRoleAgentConfig returned nil for amp-yolo")
	}

	if rc.Command != "amp" {
		t.Errorf("Command: got %q, want %q", rc.Command, "amp")
	}
	if len(rc.Args) != 1 || rc.Args[0] != "--dangerously-allow-all" {
		t.Errorf("Args: got %v, want [--dangerously-allow-all]", rc.Args)
	}

	// Verify command generation
	cmd := rc.BuildCommand()
	if !strings.Contains(cmd, "amp") {
		t.Errorf("BuildCommand should contain amp, got: %s", cmd)
	}
	if !strings.Contains(cmd, "--dangerously-allow-all") {
		t.Errorf("BuildCommand should contain custom args, got: %s", cmd)
	}
}

func TestResolveRoleAgentConfig(t *testing.T) {
	skipIfAgentBinaryMissing(t, "gemini", "codex")
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Create town settings with role-specific agents
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.RoleAgents = map[string]string{
		"mayor":   "claude", // mayor uses default claude
		"witness": "gemini", // witness uses gemini
		"polecat": "codex",  // polecats use codex
	}
	townSettings.Agents = map[string]*RuntimeConfig{
		"claude-haiku": {
			Command: "claude",
			Args:    []string{"--model", "haiku", "--dangerously-skip-permissions"},
		},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Create rig settings that override some roles
	rigSettings := NewRigSettings()
	rigSettings.Agent = "gemini" // default for this rig
	rigSettings.RoleAgents = map[string]string{
		"witness": "claude-haiku", // override witness to use haiku
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	t.Run("rig RoleAgents overrides town RoleAgents", func(t *testing.T) {
		rc := ResolveRoleAgentConfig("witness", townRoot, rigPath)
		// Should get claude-haiku from rig's RoleAgents
		if !isClaudeCommand(rc.Command) {
			t.Errorf("Command = %q, want claude or path ending in /claude", rc.Command)
		}
		cmd := rc.BuildCommand()
		if !strings.Contains(cmd, "--model haiku") {
			t.Errorf("BuildCommand() = %q, should contain --model haiku", cmd)
		}
	})

	t.Run("town RoleAgents used when rig has no override", func(t *testing.T) {
		rc := ResolveRoleAgentConfig("polecat", townRoot, rigPath)
		// Should get codex from town's RoleAgents (rig doesn't override polecat)
		if rc.Command != "codex" {
			t.Errorf("Command = %q, want %q", rc.Command, "codex")
		}
	})

	t.Run("falls back to default agent when role not in RoleAgents", func(t *testing.T) {
		rc := ResolveRoleAgentConfig("crew", townRoot, rigPath)
		// crew is not in any RoleAgents, should use rig's default agent (gemini)
		if rc.Command != "gemini" {
			t.Errorf("Command = %q, want %q", rc.Command, "gemini")
		}
	})

	t.Run("town-level role (no rigPath) uses town RoleAgents", func(t *testing.T) {
		rc := ResolveRoleAgentConfig("mayor", townRoot, "")
		// mayor is in town's RoleAgents and may resolve to a platform-specific claude binary path.
		if !isClaudeCommand(rc.Command) {
			t.Errorf("Command = %q, want claude or path ending in /claude", rc.Command)
		}
	})
}

func TestResolveRoleAgentName(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Create town settings with role-specific agents
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.RoleAgents = map[string]string{
		"witness": "gemini",
		"polecat": "codex",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Create rig settings
	rigSettings := NewRigSettings()
	rigSettings.Agent = "amp"
	rigSettings.RoleAgents = map[string]string{
		"witness": "cursor", // override witness
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	t.Run("rig role-specific agent", func(t *testing.T) {
		name, isRoleSpecific := ResolveRoleAgentName("witness", townRoot, rigPath)
		if name != "cursor" {
			t.Errorf("name = %q, want %q", name, "cursor")
		}
		if !isRoleSpecific {
			t.Error("isRoleSpecific = false, want true")
		}
	})

	t.Run("town role-specific agent", func(t *testing.T) {
		name, isRoleSpecific := ResolveRoleAgentName("polecat", townRoot, rigPath)
		if name != "codex" {
			t.Errorf("name = %q, want %q", name, "codex")
		}
		if !isRoleSpecific {
			t.Error("isRoleSpecific = false, want true")
		}
	})

	t.Run("falls back to rig default agent", func(t *testing.T) {
		name, isRoleSpecific := ResolveRoleAgentName("crew", townRoot, rigPath)
		if name != "amp" {
			t.Errorf("name = %q, want %q", name, "amp")
		}
		if isRoleSpecific {
			t.Error("isRoleSpecific = true, want false")
		}
	})

	t.Run("falls back to town default agent when no rig path", func(t *testing.T) {
		name, isRoleSpecific := ResolveRoleAgentName("refinery", townRoot, "")
		if name != "claude" {
			t.Errorf("name = %q, want %q", name, "claude")
		}
		if isRoleSpecific {
			t.Error("isRoleSpecific = true, want false")
		}
	})
}

func TestRoleAgentsRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	townSettingsPath := filepath.Join(dir, "settings", "config.json")
	rigSettingsPath := filepath.Join(dir, "rig", "settings", "config.json")

	// Test TownSettings with RoleAgents
	t.Run("town settings with role_agents", func(t *testing.T) {
		original := NewTownSettings()
		original.RoleAgents = map[string]string{
			"mayor":   "claude-opus",
			"witness": "claude-haiku",
			"polecat": "claude-sonnet",
		}

		if err := SaveTownSettings(townSettingsPath, original); err != nil {
			t.Fatalf("SaveTownSettings: %v", err)
		}

		loaded, err := LoadOrCreateTownSettings(townSettingsPath)
		if err != nil {
			t.Fatalf("LoadOrCreateTownSettings: %v", err)
		}

		if len(loaded.RoleAgents) != 3 {
			t.Errorf("RoleAgents count = %d, want 3", len(loaded.RoleAgents))
		}
		if loaded.RoleAgents["mayor"] != "claude-opus" {
			t.Errorf("RoleAgents[mayor] = %q, want %q", loaded.RoleAgents["mayor"], "claude-opus")
		}
		if loaded.RoleAgents["witness"] != "claude-haiku" {
			t.Errorf("RoleAgents[witness] = %q, want %q", loaded.RoleAgents["witness"], "claude-haiku")
		}
		if loaded.RoleAgents["polecat"] != "claude-sonnet" {
			t.Errorf("RoleAgents[polecat] = %q, want %q", loaded.RoleAgents["polecat"], "claude-sonnet")
		}
	})

	// Test RigSettings with RoleAgents
	t.Run("rig settings with role_agents", func(t *testing.T) {
		original := NewRigSettings()
		original.RoleAgents = map[string]string{
			"witness": "gemini",
			"crew":    "codex",
		}

		if err := SaveRigSettings(rigSettingsPath, original); err != nil {
			t.Fatalf("SaveRigSettings: %v", err)
		}

		loaded, err := LoadRigSettings(rigSettingsPath)
		if err != nil {
			t.Fatalf("LoadRigSettings: %v", err)
		}

		if len(loaded.RoleAgents) != 2 {
			t.Errorf("RoleAgents count = %d, want 2", len(loaded.RoleAgents))
		}
		if loaded.RoleAgents["witness"] != "gemini" {
			t.Errorf("RoleAgents[witness] = %q, want %q", loaded.RoleAgents["witness"], "gemini")
		}
		if loaded.RoleAgents["crew"] != "codex" {
			t.Errorf("RoleAgents[crew] = %q, want %q", loaded.RoleAgents["crew"], "codex")
		}
	})
}

func TestResolveRoleAgentConfig_WithEphemeralTier(t *testing.T) {
	townRoot := t.TempDir()

	// Create minimal town settings
	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	t.Setenv("GT_COST_TIER", "budget")

	rc := ResolveRoleAgentConfig("witness", townRoot, "")
	if rc == nil {
		t.Fatal("expected RuntimeConfig for witness with ephemeral budget tier")
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
		t.Errorf("Args %v missing --model haiku for budget witness", rc.Args)
	}
}

func TestResolveRoleAgentConfig_EphemeralOverridesPersistent(t *testing.T) {
	townRoot := t.TempDir()

	// Create town settings with economy tier persisted
	townSettings := NewTownSettings()
	if err := ApplyCostTier(townSettings, TierEconomy); err != nil {
		t.Fatalf("ApplyCostTier: %v", err)
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Set ephemeral to budget — should override
	t.Setenv("GT_COST_TIER", "budget")

	// witness is sonnet in economy, haiku in budget
	rc := ResolveRoleAgentConfig("witness", townRoot, "")
	if rc == nil {
		t.Fatal("expected RuntimeConfig for witness")
	}
	found := false
	for i, arg := range rc.Args {
		if arg == "--model" && i+1 < len(rc.Args) && rc.Args[i+1] == "haiku" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ephemeral budget should override persistent economy; witness Args %v missing --model haiku", rc.Args)
	}
}

func TestResolveRoleAgentConfig_EphemeralStandardSkipsPersisted(t *testing.T) {
	townRoot := t.TempDir()

	// Create town settings with budget tier persisted (haiku for witness)
	townSettings := NewTownSettings()
	if err := ApplyCostTier(townSettings, TierBudget); err != nil {
		t.Fatalf("ApplyCostTier: %v", err)
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Set ephemeral to standard — should skip persisted budget config
	t.Setenv("GT_COST_TIER", "standard")

	// polecat was claude-sonnet in budget, should now use default (opus/claude)
	rc := ResolveRoleAgentConfig("polecat", townRoot, "")
	if rc == nil {
		t.Fatal("expected RuntimeConfig for polecat")
	}
	// Should be default claude (opus), NOT claude-sonnet from stale budget config
	for i, arg := range rc.Args {
		if arg == "--model" && i+1 < len(rc.Args) {
			model := rc.Args[i+1]
			if model == "sonnet" || model == "sonnet[1m]" || model == "haiku" {
				t.Errorf("ephemeral standard should not use stale budget model; got --model %s", model)
			}
		}
	}
}

func TestResolveRoleAgentConfig_EphemeralRespectsNonClaudeOverride(t *testing.T) {
	townRoot := t.TempDir()
	rigPath := t.TempDir()

	// Create rig settings with gemini as witness (non-Claude agent)
	rigSettingsDir := filepath.Join(rigPath, ".settings")
	if err := os.MkdirAll(rigSettingsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	rigSettings := &RigSettings{
		Type:    "rig-settings",
		Version: 1,
		RoleAgents: map[string]string{
			"witness": "gemini",
		},
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	// Create town settings with gemini agent defined
	townSettings := NewTownSettings()
	townSettings.Agents["gemini"] = &RuntimeConfig{
		Command: "gemini",
		Args:    []string{},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Set ephemeral budget tier — should NOT override the gemini witness
	t.Setenv("GT_COST_TIER", "budget")

	rc := ResolveRoleAgentConfig("witness", townRoot, rigPath)
	if rc == nil {
		t.Fatal("expected RuntimeConfig for witness")
	}
	// Should still be gemini, not claude-haiku from budget tier
	if rc.Command != "gemini" {
		t.Errorf("expected gemini for witness (non-Claude rig override), got Command=%q", rc.Command)
	}
}

func TestResolveRoleAgentConfig_EphemeralDefaultPreservesNonClaudeOverride(t *testing.T) {
	townRoot := t.TempDir()
	rigPath := t.TempDir()

	// Create rig settings with gemini as polecat (non-Claude agent)
	rigSettingsDir := filepath.Join(rigPath, ".settings")
	if err := os.MkdirAll(rigSettingsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	rigSettings := &RigSettings{
		Type:    "rig-settings",
		Version: 1,
		RoleAgents: map[string]string{
			"polecat": "gemini",
		},
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	// Create town settings with gemini agent defined
	townSettings := NewTownSettings()
	townSettings.Agents["gemini"] = &RuntimeConfig{
		Command: "gemini",
		Args:    []string{},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Economy tier maps polecat to "" (use default) — should NOT override gemini
	t.Setenv("GT_COST_TIER", "economy")

	rc := ResolveRoleAgentConfig("polecat", townRoot, rigPath)
	if rc == nil {
		t.Fatal("expected RuntimeConfig for polecat")
	}
	// Should still be gemini, not default claude — the nil-rc path must
	// preserve explicit non-Claude overrides
	if rc.Command != "gemini" {
		t.Errorf("expected gemini for polecat (non-Claude rig override with tier default), got Command=%q", rc.Command)
	}
}
