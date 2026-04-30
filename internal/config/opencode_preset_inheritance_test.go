package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpencodePresetInheritance_PartialOverridePreservesBuiltinFields reproduces the
// scenario that caused `opencode run` to hang: a partial override (just { "command": "opencode" })
// in settings/agents.json would wipe the built-in env (OPENCODE_PERMISSION), NonInteractive
// config, hooks dir, etc. After PR #3723 the built-in fields are preserved.
func TestOpencodePresetInheritance_PartialOverridePreservesBuiltinFields(t *testing.T) {
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	dir := t.TempDir()
	settings := filepath.Join(dir, "agents.json")
	data := []byte(`{
		"version": 1,
		"agents": {
			"opencode": {
				"command": "opencode"
			}
		}
	}`)
	if err := os.WriteFile(settings, data, 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	if err := LoadAgentRegistry(settings); err != nil {
		t.Fatalf("LoadAgentRegistry: %v", err)
	}

	info := GetAgentPresetByName("opencode")
	if info == nil {
		t.Fatal("no opencode preset after partial override")
	}

	if got, want := info.Command, "opencode"; got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
	// These fields are what opencode needs to run non-interactively and find its plugin.
	// Before PR #3723, all of these would be zero-valued after a partial override,
	// causing opencode to hang on a permission prompt and lose its plugin config.
	if got, want := info.Env["OPENCODE_PERMISSION"], `{"*":"allow"}`; got != want {
		t.Errorf("Env[OPENCODE_PERMISSION] = %q, want %q (this is what caused the hang)", got, want)
	}
	if info.NonInteractive == nil {
		t.Error("NonInteractive = nil; want preserved from built-in")
	} else {
		if info.NonInteractive.Subcommand != "run" {
			t.Errorf("NonInteractive.Subcommand = %q, want %q", info.NonInteractive.Subcommand, "run")
		}
		if info.NonInteractive.OutputFlag != "--format json" {
			t.Errorf("NonInteractive.OutputFlag = %q, want %q", info.NonInteractive.OutputFlag, "--format json")
		}
	}
	if info.ConfigDir != ".opencode" {
		t.Errorf("ConfigDir = %q, want %q", info.ConfigDir, ".opencode")
	}
	if info.HooksDir != ".opencode/plugins" {
		t.Errorf("HooksDir = %q, want %q", info.HooksDir, ".opencode/plugins")
	}
	if info.HooksSettingsFile != "gastown.js" {
		t.Errorf("HooksSettingsFile = %q, want %q", info.HooksSettingsFile, "gastown.js")
	}
	if len(info.ProcessNames) == 0 {
		t.Errorf("ProcessNames empty; want preserved from built-in")
	}
	if info.ReadyDelayMs != 8000 {
		t.Errorf("ReadyDelayMs = %d, want 8000", info.ReadyDelayMs)
	}
}
