package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPrefixInheritance_KiroOpusAutoInheritsFromKiro is the direct regression
// for gu-g3ks: a custom agent whose name prefix-matches a builtin preset
// (kiro-opus-auto → kiro) must inherit the builtin's fields by default, so
// changes to the builtin's Args/Env/etc. automatically flow through.
//
// Before the fix, kiro-opus-auto started from a zero-valued AgentPresetInfo
// and the user's JSON fields (command="kiro-cli", args=[...]) fully shadowed
// the builtin. When Mayor updated AgentKiro to wrap kiro-cli in the
// polecat-kiro-wrapper supervisor, the builtin change was silently ignored
// for every polecat routed through kiro-opus-auto, and it took ~15 minutes
// of tracing to realize the custom entry was shadowing the builtin.
func TestPrefixInheritance_KiroOpusAutoInheritsFromKiro(t *testing.T) {
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	dir := t.TempDir()
	settings := filepath.Join(dir, "agents.json")
	// Intentionally minimal: only the model flag is user-specific.
	// Everything else (command, env, process_names, hooks, non_interactive)
	// should be inherited from the builtin kiro preset.
	data := []byte(`{
		"version": 1,
		"agents": {
			"kiro-opus-auto": {
				"args": ["--model", "claude-opus-4.7"]
			}
		}
	}`)
	if err := os.WriteFile(settings, data, 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	if err := LoadAgentRegistry(settings); err != nil {
		t.Fatalf("LoadAgentRegistry: %v", err)
	}

	info := GetAgentPresetByName("kiro-opus-auto")
	if info == nil {
		t.Fatal("no kiro-opus-auto preset after load")
	}

	// User override: args should be what the user specified.
	if len(info.Args) != 2 || info.Args[0] != "--model" || info.Args[1] != "claude-opus-4.7" {
		t.Errorf("Args = %v, want [--model claude-opus-4.7]", info.Args)
	}

	// Inherited from builtin kiro preset:
	kiroBuiltin := builtinPresets[AgentKiro]
	if info.Command != kiroBuiltin.Command {
		t.Errorf("Command = %q, want inherited %q", info.Command, kiroBuiltin.Command)
	}
	if info.SessionIDEnv != kiroBuiltin.SessionIDEnv {
		t.Errorf("SessionIDEnv = %q, want inherited %q", info.SessionIDEnv, kiroBuiltin.SessionIDEnv)
	}
	if info.HooksProvider != kiroBuiltin.HooksProvider {
		t.Errorf("HooksProvider = %q, want inherited %q", info.HooksProvider, kiroBuiltin.HooksProvider)
	}
	if info.HooksDir != kiroBuiltin.HooksDir {
		t.Errorf("HooksDir = %q, want inherited %q", info.HooksDir, kiroBuiltin.HooksDir)
	}
	if info.NonInteractive == nil {
		t.Error("NonInteractive = nil; want inherited from builtin kiro")
	} else if info.NonInteractive.PromptFlag != kiroBuiltin.NonInteractive.PromptFlag {
		t.Errorf("NonInteractive.PromptFlag = %q, want inherited %q",
			info.NonInteractive.PromptFlag, kiroBuiltin.NonInteractive.PromptFlag)
	}
	// Env map should be the inherited one, which includes GIT_TERMINAL_PROMPT.
	if got := info.Env["GIT_TERMINAL_PROMPT"]; got != "0" {
		t.Errorf("Env[GIT_TERMINAL_PROMPT] = %q, want %q (inherited from builtin)", got, "0")
	}
	// ProcessNames should be inherited.
	if len(info.ProcessNames) != len(kiroBuiltin.ProcessNames) {
		t.Errorf("ProcessNames = %v, want inherited %v", info.ProcessNames, kiroBuiltin.ProcessNames)
	}
}

// TestPrefixInheritance_SiblingCloneIsolation verifies that cloning the
// parent preset does not share slice/map state, so per-variant overrides
// don't leak into the builtin or sibling variants.
func TestPrefixInheritance_SiblingCloneIsolation(t *testing.T) {
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	dir := t.TempDir()
	settings := filepath.Join(dir, "agents.json")
	data := []byte(`{
		"version": 1,
		"agents": {
			"kiro-opus-auto": {
				"args": ["--model", "claude-opus-4.7"]
			},
			"kiro-sonnet": {
				"args": ["--model", "claude-sonnet"]
			}
		}
	}`)
	if err := os.WriteFile(settings, data, 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if err := LoadAgentRegistry(settings); err != nil {
		t.Fatalf("LoadAgentRegistry: %v", err)
	}

	opus := GetAgentPresetByName("kiro-opus-auto")
	sonnet := GetAgentPresetByName("kiro-sonnet")
	builtin := builtinPresets[AgentKiro]

	if opus.Args[1] == sonnet.Args[1] {
		t.Fatalf("sibling variants share Args state: opus=%v sonnet=%v", opus.Args, sonnet.Args)
	}
	// Builtin must be untouched.
	if len(builtin.Args) == 0 || builtin.Args[0] != "polecat-kiro-wrapper" {
		t.Errorf("builtin kiro Args mutated: %v", builtin.Args)
	}
}

// TestPrefixInheritance_LongestPrefixWins ensures that when multiple preset
// names match as prefixes, the longest one wins. e.g., a future builtin
// "groq-compound-fast" should be preferred over "groq" for a custom entry
// named "groq-compound-fast-v2".
func TestPrefixInheritance_LongestPrefixWins(t *testing.T) {
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	// Register a test preset that prefix-matches groq-compound.
	RegisterAgentForTesting("groq-compound-fast", AgentPresetInfo{
		Name:         "groq-compound-fast",
		Command:      "groq-fast-bin",
		ProcessNames: []string{"groq-fast"},
		HooksDir:     ".groq-fast",
	})

	dir := t.TempDir()
	settings := filepath.Join(dir, "agents.json")
	data := []byte(`{
		"version": 1,
		"agents": {
			"groq-compound-fast-v2": {
				"args": ["--variant", "v2"]
			}
		}
	}`)
	if err := os.WriteFile(settings, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := LoadAgentRegistry(settings); err != nil {
		t.Fatalf("LoadAgentRegistry: %v", err)
	}

	info := GetAgentPresetByName("groq-compound-fast-v2")
	if info == nil {
		t.Fatal("no groq-compound-fast-v2 entry")
	}
	// Should inherit from groq-compound-fast (longest prefix), not groq-compound.
	if info.Command != "groq-fast-bin" {
		t.Errorf("Command = %q, want %q (longest-prefix parent groq-compound-fast)", info.Command, "groq-fast-bin")
	}
	if info.HooksDir != ".groq-fast" {
		t.Errorf("HooksDir = %q, want %q", info.HooksDir, ".groq-fast")
	}
}

// TestPrefixInheritance_ExplicitExtends lets users pick any parent preset,
// even one whose name doesn't share a prefix with the custom entry.
func TestPrefixInheritance_ExplicitExtends(t *testing.T) {
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	dir := t.TempDir()
	settings := filepath.Join(dir, "agents.json")
	data := []byte(`{
		"version": 1,
		"agents": {
			"my-custom-claude": {
				"extends": "claude",
				"args": ["--my-custom-flag"]
			}
		}
	}`)
	if err := os.WriteFile(settings, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := LoadAgentRegistry(settings); err != nil {
		t.Fatalf("LoadAgentRegistry: %v", err)
	}

	info := GetAgentPresetByName("my-custom-claude")
	if info == nil {
		t.Fatal("no my-custom-claude entry")
	}
	// Command should be inherited from claude (even though the name
	// doesn't prefix-match claude).
	builtin := builtinPresets[AgentClaude]
	if info.Command != builtin.Command {
		t.Errorf("Command = %q, want inherited %q", info.Command, builtin.Command)
	}
	if info.HooksProvider != "claude" {
		t.Errorf("HooksProvider = %q, want inherited claude", info.HooksProvider)
	}
	// Args should be user-overridden.
	if len(info.Args) != 1 || info.Args[0] != "--my-custom-flag" {
		t.Errorf("Args = %v, want [--my-custom-flag]", info.Args)
	}
	// Extends field itself must be preserved after merge so later code
	// that reflects on the preset can tell inheritance was used.
	if info.Extends != "claude" {
		t.Errorf("Extends = %q, want %q (preserved after merge)", info.Extends, "claude")
	}
}

// TestPrefixInheritance_ExtendsNoneDisablesInheritance verifies the
// explicit opt-out sentinel, for fully standalone custom agents that
// happen to share a hyphen-prefix with a builtin.
func TestPrefixInheritance_ExtendsNoneDisablesInheritance(t *testing.T) {
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	dir := t.TempDir()
	settings := filepath.Join(dir, "agents.json")
	data := []byte(`{
		"version": 1,
		"agents": {
			"kiro-unrelated-tool": {
				"extends": "none",
				"command": "my-unrelated-bin",
				"process_names": ["my-unrelated-bin"]
			}
		}
	}`)
	if err := os.WriteFile(settings, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := LoadAgentRegistry(settings); err != nil {
		t.Fatalf("LoadAgentRegistry: %v", err)
	}

	info := GetAgentPresetByName("kiro-unrelated-tool")
	if info == nil {
		t.Fatal("no kiro-unrelated-tool entry")
	}
	// Must NOT inherit from kiro despite the hyphen-prefix match.
	if info.HooksProvider != "" {
		t.Errorf("HooksProvider = %q, want empty (opt-out)", info.HooksProvider)
	}
	if info.SessionIDEnv != "" {
		t.Errorf("SessionIDEnv = %q, want empty (opt-out)", info.SessionIDEnv)
	}
	if info.NonInteractive != nil {
		t.Errorf("NonInteractive = %+v, want nil (opt-out)", info.NonInteractive)
	}
	// User-set fields must be preserved.
	if info.Command != "my-unrelated-bin" {
		t.Errorf("Command = %q, want %q", info.Command, "my-unrelated-bin")
	}
}

// TestPrefixInheritance_UnknownExtendsFallsBack verifies that a typo in
// the `extends` field doesn't silently bypass inheritance. Instead, it
// falls through to name-based (exact/prefix) resolution so the user
// still gets reasonable defaults, and a warning is logged.
func TestPrefixInheritance_UnknownExtendsFallsBack(t *testing.T) {
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	dir := t.TempDir()
	settings := filepath.Join(dir, "agents.json")
	data := []byte(`{
		"version": 1,
		"agents": {
			"kiro-opus-typo": {
				"extends": "kirooo",
				"args": ["--model", "opus"]
			}
		}
	}`)
	if err := os.WriteFile(settings, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := LoadAgentRegistry(settings); err != nil {
		t.Fatalf("LoadAgentRegistry: %v", err)
	}

	info := GetAgentPresetByName("kiro-opus-typo")
	if info == nil {
		t.Fatal("no kiro-opus-typo entry")
	}
	// Typo in extends → should fall through to prefix match on "kiro".
	if info.HooksProvider != "kiro" {
		t.Errorf("HooksProvider = %q, want kiro (prefix fallback after unknown extends)", info.HooksProvider)
	}
}

// TestPrefixInheritance_NoMatchStaysZero covers the case where neither
// exact name match, extends, nor hyphen-prefix resolves. A fully custom
// agent like {"my-shiny-tool": {...}} should work exactly as before.
func TestPrefixInheritance_NoMatchStaysZero(t *testing.T) {
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	dir := t.TempDir()
	settings := filepath.Join(dir, "agents.json")
	data := []byte(`{
		"version": 1,
		"agents": {
			"entirely-custom": {
				"command": "my-bin",
				"args": ["--run"],
				"process_names": ["my-bin"]
			}
		}
	}`)
	if err := os.WriteFile(settings, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := LoadAgentRegistry(settings); err != nil {
		t.Fatalf("LoadAgentRegistry: %v", err)
	}

	info := GetAgentPresetByName("entirely-custom")
	if info == nil {
		t.Fatal("no entirely-custom entry")
	}
	if info.Command != "my-bin" {
		t.Errorf("Command = %q, want my-bin", info.Command)
	}
	// All other fields zero.
	if info.SessionIDEnv != "" {
		t.Errorf("SessionIDEnv = %q, want empty", info.SessionIDEnv)
	}
	if info.HooksProvider != "" {
		t.Errorf("HooksProvider = %q, want empty", info.HooksProvider)
	}
}

// TestPrefixInheritance_ExactNameMatchStillWorks is the regression for
// PR #3723: when the user writes {"opencode": {...}}, the same-name
// preset is cloned as the base (not a prefix match to some shorter
// preset). This must continue to work.
func TestPrefixInheritance_ExactNameMatchStillWorks(t *testing.T) {
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
		t.Fatalf("write: %v", err)
	}
	if err := LoadAgentRegistry(settings); err != nil {
		t.Fatalf("LoadAgentRegistry: %v", err)
	}

	info := GetAgentPresetByName("opencode")
	if info == nil {
		t.Fatal("no opencode entry")
	}
	// Env from builtin opencode preset must still be there.
	if got := info.Env["OPENCODE_PERMISSION"]; got != `{"*":"allow"}` {
		t.Errorf("Env[OPENCODE_PERMISSION] = %q, want preserved from builtin", got)
	}
	if info.HooksDir != ".opencode/plugins" {
		t.Errorf("HooksDir = %q, want .opencode/plugins (preserved from builtin)", info.HooksDir)
	}
}

// TestResolveBasePresetLocked exercises the helper directly with all four
// resolution paths.
func TestResolveBasePresetLocked(t *testing.T) {
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	// Force builtins into the registry.
	registryMu.Lock()
	initRegistryLocked()
	registryMu.Unlock()

	tests := []struct {
		description string
		name        string
		extends     string
		wantParent  string
		wantSource  string
	}{
		{"explicit opt-out", "kiro-variant", ExtendsNone, "", "none"},
		{"explicit extends to real preset", "some-wrapper", "kiro", "kiro", "extends"},
		{"exact name match wins over prefix", "kiro", "", "kiro", "exact"},
		{"prefix match single-level", "kiro-opus-auto", "", "kiro", "prefix"},
		{"prefix match longest wins", "claude-sonnet-4-5", "", "claude", "prefix"},
		{"no match returns nil", "unrelated-tool", "", "", ""},
		{"hyphenless name with no match returns nil", "standalone", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			registryMu.Lock()
			base, parent, source := resolveBasePresetLocked(tt.name, tt.extends)
			registryMu.Unlock()

			if source != tt.wantSource {
				t.Errorf("source = %q, want %q", source, tt.wantSource)
			}
			if parent != tt.wantParent {
				t.Errorf("parent = %q, want %q", parent, tt.wantParent)
			}
			switch tt.wantSource {
			case "none":
				if base == nil {
					t.Errorf("base = nil, want non-nil empty preset for opt-out")
				} else if base.Command != "" || base.HooksProvider != "" {
					t.Errorf("base should be zero-valued for opt-out, got %+v", base)
				}
			case "":
				if base != nil {
					t.Errorf("base = %+v, want nil for no-match", base)
				}
			default:
				if base == nil {
					t.Errorf("base = nil, want cloned preset %q", tt.wantParent)
				}
			}
		})
	}
}
