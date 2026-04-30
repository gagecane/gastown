package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
)

func TestBuildAgentStartupCommand(t *testing.T) {
	// BuildAgentStartupCommand auto-detects town root from cwd when rigPath is empty.
	// Use a temp directory to ensure we exercise the fallback default config path.
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmpWD := t.TempDir()
	if err := os.Chdir(tmpWD); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	// Test without rig config (uses defaults)
	// New signature: (role, rig, townRoot, rigPath, prompt)
	cmd := BuildAgentStartupCommand("witness", "gastown", "", "", "")

	// Should contain environment variables (via 'exec env') and claude command
	if !strings.Contains(cmd, "exec env") {
		t.Error("expected 'exec env' in command")
	}
	if !strings.Contains(cmd, "GT_ROLE=gastown/witness") {
		t.Error("expected GT_ROLE=gastown/witness in command")
	}
	if !strings.Contains(cmd, "BD_ACTOR=gastown/witness") {
		t.Error("expected BD_ACTOR in command")
	}
	parts := strings.Fields(cmd)
	if len(parts) < 2 || !isClaudeCommand(parts[len(parts)-2]) || parts[len(parts)-1] != "--dangerously-skip-permissions" {
		t.Error("expected claude command in output")
	}
}

func TestBuildPolecatStartupCommand(t *testing.T) {
	t.Parallel()
	cmd := BuildPolecatStartupCommand("gastown", "toast", "", "")

	if !strings.Contains(cmd, "GT_ROLE=gastown/polecats/toast") {
		t.Error("expected GT_ROLE=gastown/polecats/toast in command")
	}
	if !strings.Contains(cmd, "GT_RIG=gastown") {
		t.Error("expected GT_RIG=gastown in command")
	}
	if !strings.Contains(cmd, "GT_POLECAT=toast") {
		t.Error("expected GT_POLECAT=toast in command")
	}
	if !strings.Contains(cmd, "BD_ACTOR=gastown/polecats/toast") {
		t.Error("expected BD_ACTOR in command")
	}
}

func TestBuildCrewStartupCommand(t *testing.T) {
	t.Parallel()
	cmd := BuildCrewStartupCommand("gastown", "max", "", "")

	if !strings.Contains(cmd, "GT_ROLE=gastown/crew/max") {
		t.Error("expected GT_ROLE=gastown/crew/max in command")
	}
	if !strings.Contains(cmd, "GT_RIG=gastown") {
		t.Error("expected GT_RIG=gastown in command")
	}
	if !strings.Contains(cmd, "GT_CREW=max") {
		t.Error("expected GT_CREW=max in command")
	}
	if !strings.Contains(cmd, "BD_ACTOR=gastown/crew/max") {
		t.Error("expected BD_ACTOR in command")
	}
}

func TestBuildPolecatStartupCommandWithAgentOverride(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// The rig settings file must exist for resolver calls that load it.
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildPolecatStartupCommandWithAgentOverride("testrig", "toast", rigPath, "", "gemini")
	if err != nil {
		t.Fatalf("BuildPolecatStartupCommandWithAgentOverride: %v", err)
	}
	if !strings.Contains(cmd, "GT_ROLE=testrig/polecats/toast") {
		t.Fatalf("expected GT_ROLE export in command: %q", cmd)
	}
	if !strings.Contains(cmd, "GT_RIG=testrig") {
		t.Fatalf("expected GT_RIG export in command: %q", cmd)
	}
	if !strings.Contains(cmd, "GT_POLECAT=toast") {
		t.Fatalf("expected GT_POLECAT export in command: %q", cmd)
	}
	if !strings.Contains(cmd, "gemini --approval-mode yolo") {
		t.Fatalf("expected gemini command in output: %q", cmd)
	}
}

func TestBuildAgentStartupCommandWithAgentOverride(t *testing.T) {
	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0600); err != nil {
		t.Fatalf("WriteFile town.json: %v", err)
	}

	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "gemini"
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	originalWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(originalWd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	t.Run("empty override uses default agent", func(t *testing.T) {
		// New signature: (role, rig, townRoot, rigPath, prompt, agentOverride)
		cmd, err := BuildAgentStartupCommandWithAgentOverride("mayor", "", "", "", "", "")
		if err != nil {
			t.Fatalf("BuildAgentStartupCommandWithAgentOverride: %v", err)
		}
		if !strings.Contains(cmd, "GT_ROLE=mayor") {
			t.Fatalf("expected GT_ROLE export in command: %q", cmd)
		}
		if !strings.Contains(cmd, "BD_ACTOR=mayor") {
			t.Fatalf("expected BD_ACTOR export in command: %q", cmd)
		}
		if !strings.Contains(cmd, "gemini --approval-mode yolo") {
			t.Fatalf("expected gemini command in output: %q", cmd)
		}
	})

	t.Run("override switches agent", func(t *testing.T) {
		// New signature: (role, rig, townRoot, rigPath, prompt, agentOverride)
		cmd, err := BuildAgentStartupCommandWithAgentOverride("mayor", "", "", "", "", "codex")
		if err != nil {
			t.Fatalf("BuildAgentStartupCommandWithAgentOverride: %v", err)
		}
		if !strings.Contains(cmd, "codex") {
			t.Fatalf("expected codex command in output: %q", cmd)
		}
	})
}

func TestBuildCrewStartupCommandWithAgentOverride(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildCrewStartupCommandWithAgentOverride("testrig", "max", rigPath, "gt prime", "gemini")
	if err != nil {
		t.Fatalf("BuildCrewStartupCommandWithAgentOverride: %v", err)
	}
	if !strings.Contains(cmd, "GT_ROLE=testrig/crew/max") {
		t.Fatalf("expected GT_ROLE export in command: %q", cmd)
	}
	if !strings.Contains(cmd, "GT_RIG=testrig") {
		t.Fatalf("expected GT_RIG export in command: %q", cmd)
	}
	if !strings.Contains(cmd, "GT_CREW=max") {
		t.Fatalf("expected GT_CREW export in command: %q", cmd)
	}
	if !strings.Contains(cmd, "BD_ACTOR=testrig/crew/max") {
		t.Fatalf("expected BD_ACTOR export in command: %q", cmd)
	}
	if !strings.Contains(cmd, "gemini --approval-mode yolo") {
		t.Fatalf("expected gemini command in output: %q", cmd)
	}
}

func TestBuildStartupCommand_UsesRigAgentWhenRigPathProvided(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "gemini"
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	rigSettings := NewRigSettings()
	rigSettings.Agent = "codex"
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd := BuildStartupCommand(map[string]string{"GT_ROLE": "witness"}, rigPath, "")
	if !strings.Contains(cmd, "codex") {
		t.Fatalf("expected rig agent (codex) in command: %q", cmd)
	}
	if strings.Contains(cmd, "gemini --approval-mode yolo") {
		t.Fatalf("did not expect town default agent in command: %q", cmd)
	}
}

func TestBuildStartupCommand_UsesRoleAgentsFromTownSettings(t *testing.T) {
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	binDir := t.TempDir()
	for _, name := range []string{"gemini", "codex"} {
		if runtime.GOOS == "windows" {
			path := filepath.Join(binDir, name+".cmd")
			if err := os.WriteFile(path, []byte("@echo off\r\nexit /b 0\r\n"), 0644); err != nil {
				t.Fatalf("write %s stub: %v", name, err)
			}
			continue
		}
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatalf("write %s stub: %v", name, err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Configure town settings with role_agents
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.RoleAgents = map[string]string{
		constants.RoleRefinery: "gemini",
		constants.RoleWitness:  "codex",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Create empty rig settings (no agent override)
	rigSettings := NewRigSettings()
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	t.Run("refinery role gets gemini from role_agents", func(t *testing.T) {
		cmd := BuildStartupCommand(map[string]string{"GT_ROLE": constants.RoleRefinery}, rigPath, "")
		if !strings.Contains(cmd, "gemini") {
			t.Fatalf("expected gemini for refinery role, got: %q", cmd)
		}
	})

	t.Run("witness role gets codex from role_agents", func(t *testing.T) {
		cmd := BuildStartupCommand(map[string]string{"GT_ROLE": constants.RoleWitness}, rigPath, "")
		if !strings.Contains(cmd, "codex") {
			t.Fatalf("expected codex for witness role, got: %q", cmd)
		}
	})

	t.Run("crew role falls back to default_agent (not in role_agents)", func(t *testing.T) {
		cmd := BuildStartupCommand(map[string]string{"GT_ROLE": constants.RoleCrew}, rigPath, "")
		if !strings.Contains(cmd, "claude") {
			t.Fatalf("expected claude fallback for crew role, got: %q", cmd)
		}
	})

	t.Run("no role falls back to default resolution", func(t *testing.T) {
		cmd := BuildStartupCommand(map[string]string{}, rigPath, "")
		if !strings.Contains(cmd, "claude") {
			t.Fatalf("expected claude for no role, got: %q", cmd)
		}
	})
}

func TestBuildStartupCommand_RigRoleAgentsOverridesTownRoleAgents(t *testing.T) {
	skipIfAgentBinaryMissing(t, "gemini", "codex")
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Town settings has witness = gemini
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.RoleAgents = map[string]string{
		constants.RoleWitness: "gemini",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Rig settings overrides witness to codex
	rigSettings := NewRigSettings()
	rigSettings.RoleAgents = map[string]string{
		constants.RoleWitness: "codex",
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd := BuildStartupCommand(map[string]string{"GT_ROLE": constants.RoleWitness}, rigPath, "")
	if !strings.Contains(cmd, "codex") {
		t.Fatalf("expected codex from rig role_agents override, got: %q", cmd)
	}
	if strings.Contains(cmd, "gemini") {
		t.Fatalf("did not expect town role_agents (gemini) in command: %q", cmd)
	}
}

func TestBuildAgentStartupCommand_UsesRoleAgents(t *testing.T) {
	skipIfAgentBinaryMissing(t, "codex")
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Configure town settings with role_agents
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.RoleAgents = map[string]string{
		constants.RoleRefinery: "codex",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Create empty rig settings
	rigSettings := NewRigSettings()
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	// BuildAgentStartupCommand passes role via GT_ROLE env var (compound format)
	cmd := BuildAgentStartupCommand(constants.RoleRefinery, "testrig", townRoot, rigPath, "")
	if !strings.Contains(cmd, "codex") {
		t.Fatalf("expected codex for refinery role, got: %q", cmd)
	}
	if !strings.Contains(cmd, "GT_ROLE=testrig/refinery") {
		t.Fatalf("expected GT_ROLE=testrig/refinery in command: %q", cmd)
	}
}

func TestBuildAgentStartupCommand_DogUsesRoleAgents(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude-opus"
	townSettings.Agents = map[string]*RuntimeConfig{
		"claude-opus": {
			Command: "claude",
			Args:    []string{"--dangerously-skip-permissions", "--model", "opus"},
		},
		"claude-haiku": {
			Command: "claude",
			Args:    []string{"--dangerously-skip-permissions", "--model", "haiku"},
		},
	}
	townSettings.RoleAgents = map[string]string{
		"dog": "claude-haiku",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	cmd := BuildAgentStartupCommand("dog", "", townRoot, "", "")
	if !strings.Contains(cmd, "GT_ROLE=dog") {
		t.Fatalf("expected GT_ROLE=dog in command, got: %q", cmd)
	}
	if !strings.Contains(cmd, "--model haiku") {
		t.Fatalf("expected --model haiku from role_agents[dog], got: %q", cmd)
	}
	if strings.Contains(cmd, "--model opus") {
		t.Fatalf("did not expect --model opus (default_agent) for dog role, got: %q", cmd)
	}
}

func TestGetRuntimeCommand_UsesRigAgentWhenRigPathProvided(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "gemini"
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	rigSettings := NewRigSettings()
	rigSettings.Agent = "codex"
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd := GetRuntimeCommand(rigPath)
	if !strings.HasPrefix(cmd, "codex") {
		t.Fatalf("GetRuntimeCommand() = %q, want prefix %q", cmd, "codex")
	}
}

func TestBuildStartupCommand_WorkerAgentsViaCrew(t *testing.T) {
	// Cannot use t.Parallel — uses t.Setenv
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "myrig")

	// Create a fake codex binary
	binDir := t.TempDir()
	writeAgentStub(t, binDir, "codex")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	settings := NewRigSettings()
	settings.WorkerAgents = map[string]string{"denali": "codex"}
	if err := SaveRigSettings(RigSettingsPath(rigPath), settings); err != nil {
		t.Fatalf("saving settings: %v", err)
	}

	t.Run("crew worker with worker_agents entry uses codex", func(t *testing.T) {
		envVars := map[string]string{
			"GT_ROLE": constants.RoleCrew,
			"GT_CREW": "denali",
		}
		cmd := BuildStartupCommand(envVars, rigPath, "")
		if !strings.Contains(cmd, "codex") {
			t.Errorf("expected codex for crew worker denali, got: %q", cmd)
		}
	})

	t.Run("crew worker without worker_agents entry falls back to default", func(t *testing.T) {
		envVars := map[string]string{
			"GT_ROLE": constants.RoleCrew,
			"GT_CREW": "glacier",
		}
		cmd := BuildStartupCommand(envVars, rigPath, "")
		if strings.Contains(cmd, "codex") {
			t.Errorf("expected non-codex for crew worker glacier (not in worker_agents), got: %q", cmd)
		}
	})

	t.Run("crew role without GT_CREW falls back to role resolution", func(t *testing.T) {
		envVars := map[string]string{
			"GT_ROLE": constants.RoleCrew,
		}
		cmd := BuildStartupCommand(envVars, rigPath, "")
		if strings.Contains(cmd, "codex") {
			t.Errorf("expected non-codex when GT_CREW not set, got: %q", cmd)
		}
	})
}

func TestBuildStartupCommandWithAgentOverride_PriorityOverRoleAgents(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Configure town settings with role_agents: refinery = codex
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.RoleAgents = map[string]string{
		constants.RoleRefinery: "codex",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Create empty rig settings
	rigSettings := NewRigSettings()
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	// agentOverride = "gemini" should take priority over role_agents[refinery] = "codex"
	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": constants.RoleRefinery},
		rigPath,
		"",
		"gemini", // explicit override
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	if !strings.Contains(cmd, "gemini") {
		t.Errorf("expected gemini (override) in command, got: %q", cmd)
	}
	if strings.Contains(cmd, "codex") {
		t.Errorf("did not expect codex (role_agents) when override is set: %q", cmd)
	}
}

func TestBuildStartupCommandWithAgentOverride_IncludesGTRoot(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Create necessary config files
	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": constants.RoleWitness},
		rigPath,
		"",
		"gemini",
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	// Should include GT_ROOT in export
	expected := "GT_ROOT=" + ShellQuote(townRoot)
	if !strings.Contains(cmd, expected) {
		t.Errorf("expected %s in command, got: %q", expected, cmd)
	}
}

func TestQuoteForShell(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple string",
			input: "hello",
			want:  `"hello"`,
		},
		{
			name:  "string with double quote",
			input: `say "hello"`,
			want:  `"say \"hello\""`,
		},
		{
			name:  "string with backslash",
			input: `path\to\file`,
			want:  `"path\\to\\file"`,
		},
		{
			name:  "string with backtick",
			input: "run `cmd`",
			want:  "\"run \\`cmd\\`\"",
		},
		{
			name:  "string with dollar sign",
			input: "cost is $100",
			want:  `"cost is \$100"`,
		},
		{
			name:  "variable expansion prevented",
			input: "$HOME/path",
			want:  `"\$HOME/path"`,
		},
		{
			name:  "empty string",
			input: "",
			want:  `""`,
		},
		{
			name:  "combined special chars",
			input: "`$HOME`",
			want:  "\"\\`\\$HOME\\`\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quoteForShell(tt.input)
			if got != tt.want {
				t.Errorf("quoteForShell(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildStartupCommandWithAgentOverride_SetsGTAgent(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Create necessary config files
	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": constants.RoleWitness},
		rigPath,
		"",
		"gemini",
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	// Should include GT_AGENT=gemini in export so handoff can preserve it
	if !strings.Contains(cmd, "GT_AGENT=gemini") {
		t.Errorf("expected GT_AGENT=gemini in command, got: %q", cmd)
	}
}

func TestBuildStartupCommandWithAgentOverride_SetsGTProcessNames(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Create necessary config files
	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": constants.RoleWitness},
		rigPath,
		"",
		"gemini",
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	// Should include GT_PROCESS_NAMES with gemini's process names
	if !strings.Contains(cmd, "GT_PROCESS_NAMES=gemini") {
		t.Errorf("expected GT_PROCESS_NAMES=gemini in command, got: %q", cmd)
	}
}

func TestBuildStartupCommand_SetsGTProcessNames(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd := BuildStartupCommand(
		map[string]string{"GT_ROLE": constants.RoleWitness},
		rigPath,
		"",
	)

	// Default agent is claude — GT_PROCESS_NAMES should include node,claude
	if !strings.Contains(cmd, "GT_PROCESS_NAMES=") {
		t.Errorf("expected GT_PROCESS_NAMES in command, got: %q", cmd)
	}
}

// TestBuildStartupCommandWithAgentOverride_UsesOverrideWhenNoTownRoot tests that
// agentOverride is respected even when findTownRootFromCwd fails.
// This is a regression test for the bug where `gt deacon start --agent codex`
// would still launch Claude if run from outside the town directory.
func TestBuildStartupCommandWithAgentOverride_UsesOverrideWhenNoTownRoot(t *testing.T) {
	t.Parallel()
	ResetRegistryForTesting()

	// Change to a directory that is definitely NOT in a Gas Town workspace
	// by using a temp directory with no mayor/town.json
	tmpDir := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWd)
	})

	// Call with rigPath="" (like deacon does) and agentOverride="codex"
	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": "deacon"},
		"",      // rigPath is empty for town-level roles
		"",      // no prompt
		"codex", // agent override
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	// Should use codex, NOT claude (the default)
	if !strings.Contains(cmd, "codex") {
		t.Errorf("expected command to contain 'codex' but got: %q", cmd)
	}
	if strings.Contains(cmd, "claude") {
		t.Errorf("expected command to NOT contain 'claude' but got: %q", cmd)
	}
	// Should have the codex permissive approval/sandbox flag.
	if !strings.Contains(cmd, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("expected command to contain '--dangerously-bypass-approvals-and-sandbox' (codex flag) but got: %q", cmd)
	}
	// Should set GT_AGENT=codex
	if !strings.Contains(cmd, "GT_AGENT=codex") {
		t.Errorf("expected command to contain 'GT_AGENT=codex' but got: %q", cmd)
	}
}

func TestBuildStartupCommandWithAgentOverride_GTAgentFromResolvedAgent(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Create necessary config files
	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": constants.RoleWitness},
		rigPath,
		"",
		"", // No override — should still get GT_AGENT from resolved agent
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	// GT_AGENT should be set from the resolved agent for liveness detection,
	// even when no explicit override is used.
	if !strings.Contains(cmd, "GT_AGENT=") {
		t.Errorf("expected GT_AGENT in command for liveness detection, got: %q", cmd)
	}
}

// TestBuildStartupCommand_RoleAgentsSetGTAgent verifies that when a non-Claude agent
// is configured via role_agents, GT_AGENT is set in the startup command.
// Without this, IsAgentAlive falls back to ["node", "claude"] and witness patrol
// auto-nukes polecats running non-Claude agents. See: fix/gt-agent-role-agents.
func TestBuildStartupCommand_RoleAgentsSetGTAgent(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Configure opencode as the polecat agent via role_agents.
	// Define it as a custom agent so the test doesn't depend on the
	// opencode binary being in PATH (ValidateAgentConfig calls exec.LookPath).
	townSettings := NewTownSettings()
	townSettings.Agents["opencode"] = &RuntimeConfig{
		Command: "opencode",
	}
	townSettings.RoleAgents = map[string]string{
		"polecat": "opencode",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd := BuildPolecatStartupCommand("testrig", "furiosa", rigPath, "do work")

	// GT_AGENT must be set to "opencode" so IsAgentAlive detects the process
	if !strings.Contains(cmd, "GT_AGENT=opencode") {
		t.Errorf("expected GT_AGENT=opencode in command, got: %q", cmd)
	}
}

// TestBuildStartupCommand_RoleAgentsCustomAgentSetGTAgent verifies that custom
// agents defined in town settings and used via role_agents also get GT_AGENT set.
func TestBuildStartupCommand_RoleAgentsCustomAgentSetGTAgent(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Configure a custom agent "codex" mapped to opencode
	townSettings := NewTownSettings()
	townSettings.Agents["codex"] = &RuntimeConfig{
		Command: "opencode",
		Args:    []string{"-m", "openai/gpt-5.3-codex"},
	}
	townSettings.RoleAgents = map[string]string{
		"polecat": "codex",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd := BuildPolecatStartupCommand("testrig", "furiosa", rigPath, "do work")

	// GT_AGENT must be set to the custom agent name "codex"
	if !strings.Contains(cmd, "GT_AGENT=codex") {
		t.Errorf("expected GT_AGENT=codex in command, got: %q", cmd)
	}
}

// TestBuildStartupCommand_UsesGTRootFromEnvVars verifies that when rigPath is empty
// but GT_ROOT is provided in envVars, the function uses GT_ROOT to resolve town
// settings and respects role_agents configuration. This is the path hit when the
// daemon spawns town-level agents (deacon, mayor) where rigPath is always empty.
// Fixes #433
func TestBuildStartupCommand_UsesGTRootFromEnvVars(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.Agents = map[string]*RuntimeConfig{
		"claude-sonnet": {
			Command: "claude",
			Args:    []string{"--dangerously-skip-permissions", "--model", "sonnet"},
		},
	}
	townSettings.RoleAgents = map[string]string{
		constants.RoleDeacon: "claude-sonnet",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	envVars := map[string]string{
		"GT_ROLE": constants.RoleDeacon,
		"GT_ROOT": townRoot,
	}
	cmd := BuildStartupCommand(envVars, "", "")

	if !strings.Contains(cmd, "--model sonnet") {
		t.Errorf("expected --model sonnet from role_agents[deacon], got: %q", cmd)
	}
}

func TestBuildStartupCommandWithAgentOverride_UsesGTRootFromEnvVars(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.Agents = map[string]*RuntimeConfig{
		"claude-sonnet": {
			Command: "claude",
			Args:    []string{"--dangerously-skip-permissions", "--model", "sonnet"},
		},
	}
	townSettings.RoleAgents = map[string]string{
		constants.RoleDeacon: "claude-sonnet",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	envVars := map[string]string{
		"GT_ROLE": constants.RoleDeacon,
		"GT_ROOT": townRoot,
	}
	cmd, err := BuildStartupCommandWithAgentOverride(envVars, "", "", "")
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	if !strings.Contains(cmd, "--model sonnet") {
		t.Errorf("expected --model sonnet from role_agents[deacon], got: %q", cmd)
	}
}

func TestBuildStartupCommand_ExecWrapper(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Create a rig settings with exec_wrapper configured
	rigSettings := NewRigSettings()
	rigSettings.Runtime = &RuntimeConfig{
		Command:     "claude",
		ExecWrapper: []string{"exitbox", "run", "--profile=gastown-polecat", "--"},
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd := BuildStartupCommand(map[string]string{"GT_ROLE": "polecat"}, rigPath, "hello")

	// Must contain exec wrapper tokens
	if !strings.Contains(cmd, "exitbox run --profile=gastown-polecat --") {
		t.Errorf("expected exec wrapper in command, got: %q", cmd)
	}

	// The wrapper + agent command should appear as a contiguous sequence
	// "exitbox run --profile=gastown-polecat -- claude"
	if !strings.Contains(cmd, "exitbox run --profile=gastown-polecat -- claude") {
		t.Errorf("expected wrapper immediately before claude command, got: %q", cmd)
	}

	// Env vars (exec env ...) must appear before the wrapper
	envIdx := strings.Index(cmd, "exec env")
	wrapperIdx := strings.Index(cmd, "exitbox run")
	if envIdx == -1 || wrapperIdx == -1 || envIdx >= wrapperIdx {
		t.Errorf("expected 'exec env' before wrapper, got: %q", cmd)
	}
}

func TestBuildStartupCommandWithAgentOverride_ExecWrapper(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Create rig settings with exec_wrapper
	rigSettings := NewRigSettings()
	rigSettings.Runtime = &RuntimeConfig{
		Command:     "claude",
		ExecWrapper: []string{"daytona", "exec", "furiosa-ws", "--"},
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": "polecat"},
		rigPath, "hello", "",
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	if !strings.Contains(cmd, "daytona exec furiosa-ws --") {
		t.Errorf("expected exec wrapper in command, got: %q", cmd)
	}

	// The wrapper + agent command should appear as a contiguous sequence
	if !strings.Contains(cmd, "daytona exec furiosa-ws -- claude") {
		t.Errorf("expected wrapper immediately before claude command, got: %q", cmd)
	}
}

func TestBuildStartupCommandWithAgentOverride_SettingsFlagForClaudeOverride(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	townSettings.Agents["claude-sonnet"] = &RuntimeConfig{
		Command: "claude",
		Args:    []string{"--model", "sonnet", "--dangerously-skip-permissions"},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": "testrig/polecats/toast"},
		rigPath,
		"",
		"claude-sonnet",
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	if !strings.Contains(cmd, "--settings") {
		t.Errorf("Claude override on polecat role should include --settings, got: %q", cmd)
	}
	expectedPath := filepath.Join(rigPath, "polecats", ".claude", "settings.json")
	if !strings.Contains(cmd, expectedPath) {
		t.Errorf("expected settings path %q in command, got: %q", expectedPath, cmd)
	}
}

func TestBuildPolecatStartupCommandWithAgentOverride_IncludesSettingsFlag(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	townSettings.Agents["claude-sonnet"] = &RuntimeConfig{
		Command: "claude",
		Args:    []string{"--model", "sonnet", "--dangerously-skip-permissions"},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildPolecatStartupCommandWithAgentOverride("testrig", "toast", rigPath, "", "claude-sonnet")
	if err != nil {
		t.Fatalf("BuildPolecatStartupCommandWithAgentOverride: %v", err)
	}

	if !strings.Contains(cmd, "--settings") {
		t.Errorf("polecat with Claude override must get --settings for hooks to fire, got: %q", cmd)
	}
	expectedPath := filepath.Join(rigPath, "polecats", ".claude", "settings.json")
	if !strings.Contains(cmd, expectedPath) {
		t.Errorf("expected settings path %q in command, got: %q", expectedPath, cmd)
	}
}

func TestBuildStartupCommandWithAgentOverride_NoSettingsFlagForNonClaude(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": "testrig/polecats/toast"},
		rigPath,
		"",
		"gemini",
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	if strings.Contains(cmd, "--settings") {
		t.Errorf("non-Claude override (gemini) should NOT get --settings, got: %q", cmd)
	}
}

func TestBuildStartupCommandWithAgentOverride_NoDoubleSettingsOnNonOverridePath(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	// No override — ResolveRoleAgentConfig already adds --settings for polecat role.
	// The new withRoleSettingsFlag call in BuildStartupCommandWithAgentOverride should
	// be a no-op (idempotency guard), not double-add.
	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": "testrig/polecats/toast"},
		rigPath,
		"",
		"", // no override
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	count := strings.Count(cmd, "--settings")
	if count > 1 {
		t.Errorf("expected at most 1 --settings flag (idempotency guard), got %d — cmd: %q", count, cmd)
	}
	if count == 0 {
		t.Errorf("default Claude agent on polecat role should still get --settings, got: %q", cmd)
	}
}
