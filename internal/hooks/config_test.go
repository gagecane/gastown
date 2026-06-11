package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
)

// setTestHome sets HOME (and USERPROFILE on Windows) so that
// os.UserHomeDir() returns tmpDir on all platforms.
func setTestHome(t *testing.T, tmpDir string) {
	t.Helper()
	t.Setenv("HOME", tmpDir)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", tmpDir)
	}
}

func TestLoadSaveBase(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	cfg := DefaultBase()

	if err := SaveBase(cfg); err != nil {
		t.Fatalf("SaveBase failed: %v", err)
	}

	if _, err := os.Stat(BasePath()); err != nil {
		t.Fatalf("base config file not created: %v", err)
	}

	loaded, err := LoadBase()
	if err != nil {
		t.Fatalf("LoadBase failed: %v", err)
	}

	if len(loaded.SessionStart) != 1 {
		t.Errorf("expected 1 SessionStart hook, got %d", len(loaded.SessionStart))
	}
	if len(loaded.PreCompact) != 1 {
		t.Errorf("expected 1 PreCompact hook, got %d", len(loaded.PreCompact))
	}
	if len(loaded.UserPromptSubmit) != 1 {
		t.Errorf("expected 1 UserPromptSubmit hook, got %d", len(loaded.UserPromptSubmit))
	}
	if len(loaded.Stop) != 1 {
		t.Errorf("expected 1 Stop hook, got %d", len(loaded.Stop))
	}
}

func TestLoadSaveOverride(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	cfg := &HooksConfig{
		PreToolUse: []HookEntry{
			{
				Matcher: "Bash(git push*)",
				Hooks:   []Hook{{Type: "command", Command: "echo blocked && exit 2"}},
			},
		},
	}

	if err := SaveOverride("crew", cfg); err != nil {
		t.Fatalf("SaveOverride failed: %v", err)
	}

	loaded, err := LoadOverride("crew")
	if err != nil {
		t.Fatalf("LoadOverride failed: %v", err)
	}

	if len(loaded.PreToolUse) != 1 {
		t.Fatalf("expected 1 PreToolUse hook, got %d", len(loaded.PreToolUse))
	}
	if loaded.PreToolUse[0].Matcher != "Bash(git push*)" {
		t.Errorf("expected matcher 'Bash(git push*)', got %q", loaded.PreToolUse[0].Matcher)
	}
}

func TestLoadOverrideRejectsDuplicateMatchers(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	overridePath := OverridePath("crew")
	if err := os.MkdirAll(filepath.Dir(overridePath), 0755); err != nil {
		t.Fatalf("creating overrides dir: %v", err)
	}

	raw := `{
  "PreToolUse": [
    {"matcher": "Bash(git push*)", "hooks": [{"type": "command", "command": "first"}]},
    {"matcher": "Bash(git push*)", "hooks": [{"type": "command", "command": "second"}]}
  ]
}`
	if err := os.WriteFile(overridePath, []byte(raw), 0644); err != nil {
		t.Fatalf("writing override: %v", err)
	}

	_, err := LoadOverride("crew")
	if err == nil {
		t.Fatal("expected duplicate matcher error")
	}
	if !strings.Contains(err.Error(), "duplicate matcher") {
		t.Fatalf("expected duplicate matcher error, got: %v", err)
	}
}

func TestLoadSaveOverrideRigRole(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	cfg := &HooksConfig{
		SessionStart: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "echo gastown-crew"}}},
		},
	}

	if err := SaveOverride("gastown/crew", cfg); err != nil {
		t.Fatalf("SaveOverride failed: %v", err)
	}

	expectedPath := filepath.Join(tmpDir, ".gt", "hooks-overrides", "gastown__crew.json")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("expected override file at %s: %v", expectedPath, err)
	}

	loaded, err := LoadOverride("gastown/crew")
	if err != nil {
		t.Fatalf("LoadOverride failed: %v", err)
	}

	if len(loaded.SessionStart) != 1 {
		t.Fatalf("expected 1 SessionStart hook, got %d", len(loaded.SessionStart))
	}
}

func TestLoadMissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	_, err := LoadBase()
	if err == nil {
		t.Error("expected error loading missing base config")
	}

	_, err = LoadOverride("crew")
	if err == nil {
		t.Error("expected error loading missing override config")
	}
}

func TestValidTarget(t *testing.T) {
	tests := []struct {
		target string
		valid  bool
	}{
		{"crew", true},
		{"witness", true},
		{"refinery", true},
		{"polecats", true},
		{"polecat", true},
		{"mayor", true},
		{"deacon", true},
		{"rig", false},
		{"gastown/rig", false},
		{"gastown/crew", true},
		{"beads/witness", true},
		{"sky/polecats", true},
		{"wyvern/refinery", true},
		{"", false},
		{"invalid", false},
		{"gastown/invalid", false},
		{"/crew", false},
		{"gastown/", false},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			if got := ValidTarget(tt.target); got != tt.valid {
				t.Errorf("ValidTarget(%q) = %v, want %v", tt.target, got, tt.valid)
			}
		})
	}
}

func TestNormalizeTarget(t *testing.T) {
	tests := []struct {
		input      string
		normalized string
		valid      bool
	}{
		{"crew", "crew", true},
		{"polecats", "polecats", true},
		{"polecat", "polecats", true},
		{"gastown/polecats", "gastown/polecats", true},
		{"gastown/polecat", "gastown/polecats", true},
		{"mayor", "mayor", true},
		{"invalid", "", false},
		{"gastown/invalid", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := NormalizeTarget(tt.input)
			if ok != tt.valid {
				t.Errorf("NormalizeTarget(%q) valid = %v, want %v", tt.input, ok, tt.valid)
			}
			if got != tt.normalized {
				t.Errorf("NormalizeTarget(%q) = %q, want %q", tt.input, got, tt.normalized)
			}
		})
	}
}

func TestGetApplicableOverrides(t *testing.T) {
	tests := []struct {
		target   string
		expected []string
	}{
		{"mayor", []string{"mayor"}},
		{"crew", []string{"crew"}},
		{"gastown/crew", []string{"crew", "gastown/crew"}},
		{"beads/witness", []string{"witness", "beads/witness"}},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			got := GetApplicableOverrides(tt.target)
			if len(got) != len(tt.expected) {
				t.Fatalf("GetApplicableOverrides(%q) returned %d items, want %d", tt.target, len(got), len(tt.expected))
			}
			for i, v := range got {
				if v != tt.expected[i] {
					t.Errorf("GetApplicableOverrides(%q)[%d] = %q, want %q", tt.target, i, v, tt.expected[i])
				}
			}
		})
	}
}

func TestDefaultBase(t *testing.T) {
	cfg := DefaultBase()

	if len(cfg.SessionStart) == 0 {
		t.Error("DefaultBase should have SessionStart hooks")
	}
	if len(cfg.PreCompact) == 0 {
		t.Error("DefaultBase should have PreCompact hooks")
	}
	if len(cfg.UserPromptSubmit) == 0 {
		t.Error("DefaultBase should have UserPromptSubmit hooks")
	}
	if len(cfg.Stop) == 0 {
		t.Error("DefaultBase should have Stop hooks")
	}

	found := false
	for _, entry := range cfg.SessionStart {
		for _, h := range entry.Hooks {
			if h.Command != "" {
				found = true
			}
		}
	}
	if !found {
		t.Error("DefaultBase SessionStart should have a command")
	}
}

func TestMerge(t *testing.T) {
	base := &HooksConfig{
		SessionStart: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "base-session"}}},
		},
		Stop: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "base-stop"}}},
		},
	}

	override := &HooksConfig{
		SessionStart: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "override-session"}}},
		},
		PreToolUse: []HookEntry{
			{Matcher: "Bash(git*)", Hooks: []Hook{{Type: "command", Command: "block-git"}}},
		},
	}

	result := Merge(base, override)

	if len(result.SessionStart) != 1 || result.SessionStart[0].Hooks[0].Command != "override-session" {
		t.Errorf("expected override SessionStart, got %v", result.SessionStart)
	}
	if len(result.Stop) != 1 || result.Stop[0].Hooks[0].Command != "base-stop" {
		t.Errorf("expected base Stop, got %v", result.Stop)
	}
	if len(result.PreToolUse) != 1 || result.PreToolUse[0].Matcher != "Bash(git*)" {
		t.Errorf("expected override PreToolUse, got %v", result.PreToolUse)
	}
	if len(base.PreToolUse) != 0 {
		t.Error("Merge mutated the original base config")
	}
}

// TestMergePerMatcherPreservation is the exact bug scenario from the spec:
// base has PreToolUse with matchers ["Bash(git*)", "Bash(rm*)"], override has
// PreToolUse with matcher ["Bash(git*)"]. The "Bash(rm*)" matcher must be preserved.
func TestMergePerMatcherPreservation(t *testing.T) {
	base := &HooksConfig{
		PreToolUse: []HookEntry{
			{Matcher: "Bash(git*)", Hooks: []Hook{{Type: "command", Command: "git-guard"}}},
			{Matcher: "Bash(rm*)", Hooks: []Hook{{Type: "command", Command: "rm-guard"}}},
		},
	}
	override := &HooksConfig{
		PreToolUse: []HookEntry{
			{Matcher: "Bash(git*)", Hooks: []Hook{{Type: "command", Command: "crew-git-guard"}}},
		},
	}

	result := Merge(base, override)

	if len(result.PreToolUse) != 2 {
		t.Fatalf("expected 2 PreToolUse entries (per-matcher merge), got %d", len(result.PreToolUse))
	}

	// Bash(git*) should be replaced by override
	if result.PreToolUse[0].Matcher != "Bash(git*)" {
		t.Errorf("expected first matcher Bash(git*), got %q", result.PreToolUse[0].Matcher)
	}
	if result.PreToolUse[0].Hooks[0].Command != "crew-git-guard" {
		t.Errorf("expected override command for Bash(git*), got %q", result.PreToolUse[0].Hooks[0].Command)
	}

	// Bash(rm*) should be preserved from base
	if result.PreToolUse[1].Matcher != "Bash(rm*)" {
		t.Errorf("expected second matcher Bash(rm*), got %q", result.PreToolUse[1].Matcher)
	}
	if result.PreToolUse[1].Hooks[0].Command != "rm-guard" {
		t.Errorf("expected base command for Bash(rm*), got %q", result.PreToolUse[1].Hooks[0].Command)
	}
}

func TestMergeDifferentMatchersBothIncluded(t *testing.T) {
	base := &HooksConfig{
		PreToolUse: []HookEntry{
			{Matcher: "Write", Hooks: []Hook{{Type: "command", Command: "write-check"}}},
		},
	}
	override := &HooksConfig{
		PreToolUse: []HookEntry{
			{Matcher: "Bash", Hooks: []Hook{{Type: "command", Command: "bash-check"}}},
		},
	}

	result := Merge(base, override)

	if len(result.PreToolUse) != 2 {
		t.Fatalf("expected 2 PreToolUse entries, got %d", len(result.PreToolUse))
	}
	if result.PreToolUse[0].Matcher != "Write" {
		t.Errorf("expected base Write matcher first, got %q", result.PreToolUse[0].Matcher)
	}
	if result.PreToolUse[1].Matcher != "Bash" {
		t.Errorf("expected override Bash matcher second, got %q", result.PreToolUse[1].Matcher)
	}
}

func TestMergeExplicitDisable(t *testing.T) {
	base := &HooksConfig{
		PreToolUse: []HookEntry{
			{Matcher: "Write", Hooks: []Hook{{Type: "command", Command: "write-check"}}},
			{Matcher: "Bash", Hooks: []Hook{{Type: "command", Command: "bash-check"}}},
		},
	}
	override := &HooksConfig{
		PreToolUse: []HookEntry{
			{Matcher: "Write", Hooks: []Hook{}}, // Explicit disable
		},
	}

	result := Merge(base, override)

	if len(result.PreToolUse) != 1 {
		t.Fatalf("expected 1 PreToolUse entry after disable, got %d", len(result.PreToolUse))
	}
	if result.PreToolUse[0].Matcher != "Bash" {
		t.Errorf("expected Bash matcher to remain, got %q", result.PreToolUse[0].Matcher)
	}
}

func TestMergeEmptyOverride(t *testing.T) {
	base := DefaultBase()
	override := &HooksConfig{}

	result := Merge(base, override)

	if !HooksEqual(base, result) {
		t.Error("empty override should not change base config")
	}
}

func TestComputeExpected(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	base := &HooksConfig{
		SessionStart: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "base-cmd"}}},
		},
	}
	if err := SaveBase(base); err != nil {
		t.Fatalf("SaveBase failed: %v", err)
	}

	crewOverride := &HooksConfig{
		PreToolUse: []HookEntry{
			{Matcher: "Bash(git*)", Hooks: []Hook{{Type: "command", Command: "crew-guard"}}},
		},
	}
	if err := SaveOverride("crew", crewOverride); err != nil {
		t.Fatalf("SaveOverride crew failed: %v", err)
	}

	gcOverride := &HooksConfig{
		SessionStart: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "gastown-crew-session"}}},
		},
	}
	if err := SaveOverride("gastown/crew", gcOverride); err != nil {
		t.Fatalf("SaveOverride gastown/crew failed: %v", err)
	}

	expected, err := ComputeExpected("gastown/crew")
	if err != nil {
		t.Fatalf("ComputeExpected failed: %v", err)
	}

	if len(expected.SessionStart) != 1 || expected.SessionStart[0].Hooks[0].Command != "gastown-crew-session" {
		t.Errorf("expected gastown/crew SessionStart, got %v", expected.SessionStart)
	}
	// On-disk base has no PreToolUse, so DefaultBase's 3 pr-workflow guards are
	// backfilled. The crew override adds Bash(git*), making 4 total.
	defaultPTU := len(DefaultBase().PreToolUse)
	if len(expected.PreToolUse) != defaultPTU+1 {
		t.Errorf("expected %d PreToolUse (default %d + crew 1), got %d", defaultPTU+1, defaultPTU, len(expected.PreToolUse))
	}
	// Verify crew-guard is present
	hasCrewGuard := false
	for _, e := range expected.PreToolUse {
		if e.Matcher == "Bash(git*)" && e.Hooks[0].Command == "crew-guard" {
			hasCrewGuard = true
		}
	}
	if !hasCrewGuard {
		t.Error("expected crew PreToolUse guard to be present")
	}
}

// TestComputeExpectedBackfillsSessionStart reproduces gt-y22: on-disk base
// created before SessionStart was added to DefaultBase. SessionStart should
// be backfilled from DefaultBase so settings.json files contain startup hooks.
func TestComputeExpectedBackfillsSessionStart(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	// Simulate a stale hooks-base.json that was created before SessionStart existed.
	// It has Stop, PreCompact, UserPromptSubmit but no SessionStart.
	staleBase := &HooksConfig{
		Stop: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "gt costs record"}}},
		},
		PreCompact: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "gt prime --hook"}}},
		},
		UserPromptSubmit: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "gt mail check --inject"}}},
		},
	}
	if err := SaveBase(staleBase); err != nil {
		t.Fatalf("SaveBase failed: %v", err)
	}

	// All targets should get SessionStart backfilled from DefaultBase
	for _, target := range []string{"mayor", "crew", "witness", "gastown/crew"} {
		expected, err := ComputeExpected(target)
		if err != nil {
			t.Fatalf("ComputeExpected(%s) failed: %v", target, err)
		}
		if len(expected.SessionStart) == 0 {
			t.Errorf("%s: expected SessionStart to be backfilled from DefaultBase, got none", target)
		}
		// Verify the generated hook uses a resolved gt command, not the stale
		// export PATH= marker that causes settings to be treated as out-of-date.
		hasPrime := false
		for _, entry := range expected.SessionStart {
			for _, hook := range entry.Hooks {
				if strings.Contains(hook.Command, "export PATH=") {
					t.Errorf("%s: SessionStart contains stale export PATH marker: %q", target, hook.Command)
				}
				if strings.Contains(hook.Command, "prime --hook") {
					hasPrime = true
				}
			}
		}
		if !hasPrime {
			t.Errorf("%s: expected prime --hook in SessionStart hooks", target)
		}
		// On-disk Stop should be preserved (not overwritten by DefaultBase)
		if len(expected.Stop) == 0 {
			t.Errorf("%s: on-disk Stop should be preserved", target)
		} else if expected.Stop[0].Hooks[0].Command != "gt costs record" {
			t.Errorf("%s: on-disk Stop should take precedence, got %q", target, expected.Stop[0].Hooks[0].Command)
		}
	}
}

func TestComputeExpectedFailsOnDuplicateOverrideMatcher(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	if err := SaveBase(DefaultBase()); err != nil {
		t.Fatalf("SaveBase failed: %v", err)
	}

	overridePath := OverridePath("crew")
	if err := os.MkdirAll(filepath.Dir(overridePath), 0755); err != nil {
		t.Fatalf("creating overrides dir: %v", err)
	}

	raw := `{
  "SessionStart": [
    {"matcher": "", "hooks": [{"type": "command", "command": "first"}]},
    {"matcher": "", "hooks": [{"type": "command", "command": "second"}]}
  ]
}`
	if err := os.WriteFile(overridePath, []byte(raw), 0644); err != nil {
		t.Fatalf("writing override: %v", err)
	}

	_, err := ComputeExpected("crew")
	if err == nil {
		t.Fatal("expected ComputeExpected to fail on duplicate matcher")
	}
	if !strings.Contains(err.Error(), "duplicate matcher") {
		t.Fatalf("expected duplicate matcher error, got: %v", err)
	}
}

func TestComputeExpectedNoBase(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	// Mayor should get DefaultBase (no built-in overrides)
	expected, err := ComputeExpected("mayor")
	if err != nil {
		t.Fatalf("ComputeExpected failed: %v", err)
	}

	defaultBase := DefaultBase()
	if !HooksEqual(expected, defaultBase) {
		t.Error("expected DefaultBase for mayor when no configs exist")
	}

	// Crew should get DefaultBase + built-in crew override (PreCompact)
	crew, err := ComputeExpected("crew")
	if err != nil {
		t.Fatalf("ComputeExpected(crew) failed: %v", err)
	}
	// Crew has a built-in PreCompact override, so it won't equal bare DefaultBase
	if len(crew.PreCompact) == 0 {
		t.Error("expected crew to have PreCompact hook from DefaultOverrides")
	}
	// But it should still have the base SessionStart hooks
	if len(crew.SessionStart) != len(defaultBase.SessionStart) {
		t.Error("expected crew to inherit SessionStart from DefaultBase")
	}

	// Witness should get DefaultBase + built-in patrol-formula-guard (gt-e47hxn)
	witness, err := ComputeExpected("witness")
	if err != nil {
		t.Fatalf("ComputeExpected(witness) failed: %v", err)
	}
	// Witness has built-in PreToolUse overrides for patrol-formula-guard
	if len(witness.PreToolUse) < 4 {
		t.Errorf("expected witness to have at least 4 PreToolUse hooks from DefaultOverrides (patrol-formula-guard), got %d", len(witness.PreToolUse))
	}
	// Should still inherit base SessionStart
	if len(witness.SessionStart) != len(defaultBase.SessionStart) {
		t.Error("expected witness to inherit SessionStart from DefaultBase")
	}
	// Verify patrol matchers are present
	patrolMatchers := map[string]bool{
		"Bash(*bd mol pour*patrol*)":        false,
		"Bash(*bd mol pour *mol-witness*)":  false,
		"Bash(*bd mol pour *mol-deacon*)":   false,
		"Bash(*bd mol pour *mol-refinery*)": false,
	}
	for _, entry := range witness.PreToolUse {
		if _, ok := patrolMatchers[entry.Matcher]; ok {
			patrolMatchers[entry.Matcher] = true
		}
	}
	for matcher, found := range patrolMatchers {
		if !found {
			t.Errorf("witness missing patrol-formula-guard matcher: %s", matcher)
		}
	}

	// Deacon should get DefaultBase + built-in patrol-formula-guard plus anti-batch guards.
	deacon, err := ComputeExpected("deacon")
	if err != nil {
		t.Fatalf("ComputeExpected(deacon) failed: %v", err)
	}
	if len(deacon.PreToolUse) < 7 {
		t.Errorf("expected deacon to have at least 7 PreToolUse hooks from DefaultOverrides (anti-batch + patrol-formula-guard), got %d", len(deacon.PreToolUse))
	}
	if len(deacon.SessionStart) != len(defaultBase.SessionStart) {
		t.Error("expected deacon to inherit SessionStart from DefaultBase")
	}
	deaconPatrolMatchers := map[string]bool{
		"Bash(*for *seq*)":                  false,
		"Bash(*while true*)":                false,
		"Bash(*while :*)":                   false,
		"Bash(*bd mol pour*patrol*)":        false,
		"Bash(*bd mol pour *mol-witness*)":  false,
		"Bash(*bd mol pour *mol-deacon*)":   false,
		"Bash(*bd mol pour *mol-refinery*)": false,
	}
	for _, entry := range deacon.PreToolUse {
		if _, ok := deaconPatrolMatchers[entry.Matcher]; ok {
			deaconPatrolMatchers[entry.Matcher] = true
		}
	}
	for matcher, found := range deaconPatrolMatchers {
		if !found {
			t.Errorf("deacon missing patrol-formula-guard matcher: %s", matcher)
		}
	}

	// Refinery should get DefaultBase + built-in patrol-formula-guard (same as witness)
	refinery, err := ComputeExpected("refinery")
	if err != nil {
		t.Fatalf("ComputeExpected(refinery) failed: %v", err)
	}
	if len(refinery.PreToolUse) < 4 {
		t.Errorf("expected refinery to have at least 4 PreToolUse hooks from DefaultOverrides (patrol-formula-guard), got %d", len(refinery.PreToolUse))
	}
	if len(refinery.SessionStart) != len(defaultBase.SessionStart) {
		t.Error("expected refinery to inherit SessionStart from DefaultBase")
	}
	refineryPatrolMatchers := map[string]bool{
		"Bash(*bd mol pour*patrol*)":        false,
		"Bash(*bd mol pour *mol-witness*)":  false,
		"Bash(*bd mol pour *mol-deacon*)":   false,
		"Bash(*bd mol pour *mol-refinery*)": false,
	}
	for _, entry := range refinery.PreToolUse {
		if _, ok := refineryPatrolMatchers[entry.Matcher]; ok {
			refineryPatrolMatchers[entry.Matcher] = true
		}
	}
	for matcher, found := range refineryPatrolMatchers {
		if !found {
			t.Errorf("refinery missing patrol-formula-guard matcher: %s", matcher)
		}
	}
}

// TestComputeExpectedWitnessRigSpecific verifies patrol-formula-guard propagates
// to rig-specific witness targets (e.g., sky/witness) via the witness role default.
func TestComputeExpectedWitnessRigSpecific(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	// No on-disk overrides — all witnesses should still get patrol-formula-guard
	// from the built-in DefaultOverrides for "witness".
	skyWitness, err := ComputeExpected("sky/witness")
	if err != nil {
		t.Fatalf("ComputeExpected(sky/witness) failed: %v", err)
	}

	// Should have patrol-formula-guard matchers from DefaultOverrides["witness"]
	patrolCount := 0
	for _, entry := range skyWitness.PreToolUse {
		if strings.Contains(entry.Matcher, "bd mol pour") {
			patrolCount++
		}
	}
	if patrolCount < 4 {
		t.Errorf("sky/witness expected 4 patrol-formula-guard matchers, got %d", patrolCount)
	}

	// Should also inherit base hooks (pr-workflow-guard, etc.)
	if len(skyWitness.SessionStart) == 0 {
		t.Error("sky/witness should inherit SessionStart from DefaultBase")
	}
	if len(skyWitness.UserPromptSubmit) != 0 {
		t.Error("sky/witness should disable UserPromptSubmit mail-check from DefaultBase")
	}
}

func TestComputeExpectedPatrolRolesDisableUserPromptMailCheck(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	for _, target := range []string{"witness", "refinery", "deacon", "boot", "sky/witness", "sky/refinery"} {
		t.Run(target, func(t *testing.T) {
			cfg, err := ComputeExpected(target)
			if err != nil {
				t.Fatalf("ComputeExpected(%s): %v", target, err)
			}
			if len(cfg.UserPromptSubmit) != 0 {
				t.Fatalf("%s should disable UserPromptSubmit mail-check, got %+v", target, cfg.UserPromptSubmit)
			}
		})
	}
}

func TestComputeExpectedPolecatsKeepUserPromptMailCheck(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	cfg, err := ComputeExpected("polecats")
	if err != nil {
		t.Fatalf("ComputeExpected(polecats): %v", err)
	}
	if len(cfg.UserPromptSubmit) == 0 {
		t.Fatal("polecats should retain UserPromptSubmit mail-check")
	}
}

// TestComputeExpectedBuiltinPlusOnDisk verifies that on-disk overrides layer
// on top of built-in defaults rather than replacing them.
func TestComputeExpectedBuiltinPlusOnDisk(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	// Save an on-disk mayor override that adds a custom SessionStart hook
	customOverride := &HooksConfig{
		SessionStart: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "custom-mayor-session"}}},
		},
	}
	if err := SaveOverride("mayor", customOverride); err != nil {
		t.Fatalf("SaveOverride failed: %v", err)
	}

	expected, err := ComputeExpected("mayor")
	if err != nil {
		t.Fatalf("ComputeExpected failed: %v", err)
	}

	// Should have the custom SessionStart from on-disk override
	if len(expected.SessionStart) == 0 {
		t.Error("on-disk SessionStart override should be present")
	} else if expected.SessionStart[0].Hooks[0].Command != "custom-mayor-session" {
		t.Errorf("expected custom-mayor-session, got %q", expected.SessionStart[0].Hooks[0].Command)
	}
}

func TestHooksEqual(t *testing.T) {
	a := &HooksConfig{
		SessionStart: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "test"}}},
		},
	}
	b := &HooksConfig{
		SessionStart: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "test"}}},
		},
	}
	c := &HooksConfig{
		SessionStart: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "different"}}},
		},
	}

	if !HooksEqual(a, b) {
		t.Error("identical configs should be equal")
	}
	if HooksEqual(a, c) {
		t.Error("different configs should not be equal")
	}
	if !HooksEqual(&HooksConfig{}, &HooksConfig{}) {
		t.Error("empty configs should be equal")
	}
}

func TestLoadSettings(t *testing.T) {
	tmpDir := t.TempDir()

	// Write raw JSON to test LoadSettings (SettingsJSON uses json:"-" tags)
	settingsJSON := `{
  "editorMode": "vim",
  "hooks": {
    "SessionStart": [
      {"matcher": "", "hooks": [{"type": "command", "command": "test"}]}
    ]
  }
}`
	path := filepath.Join(tmpDir, "settings.json")
	if err := os.WriteFile(path, []byte(settingsJSON), 0644); err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	loaded, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	if loaded.EditorMode != "vim" {
		t.Errorf("expected editorMode vim, got %q", loaded.EditorMode)
	}
	if len(loaded.Hooks.SessionStart) != 1 {
		t.Errorf("expected 1 SessionStart hook, got %d", len(loaded.Hooks.SessionStart))
	}

	// Test loading non-existent file (should return zero-value)
	missing, err := LoadSettings(filepath.Join(tmpDir, "missing.json"))
	if err != nil {
		t.Fatalf("LoadSettings missing file failed: %v", err)
	}
	if missing.EditorMode != "" || len(missing.Hooks.SessionStart) != 0 {
		t.Error("missing file should return zero-value SettingsJSON")
	}
}

func TestLoadSettingsIntegrityError(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"hooks":{"SessionStart":"bad"}}`), 0644); err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	_, err := LoadSettings(path)
	if err == nil {
		t.Fatal("expected integrity error for malformed settings")
	}
	if !IsSettingsIntegrityError(err) {
		t.Fatalf("expected SettingsIntegrityError, got %T: %v", err, err)
	}
}

func TestDiscoverTargets(t *testing.T) {
	tmpDir := t.TempDir()

	os.MkdirAll(filepath.Join(tmpDir, "mayor"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "deacon"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "testrig", "crew", "alice"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "testrig", "crew", "bob"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "testrig", "witness"), 0755)

	targets, err := DiscoverTargets(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverTargets failed: %v", err)
	}

	if len(targets) < 4 {
		t.Errorf("expected at least 4 targets, got %d", len(targets))
		for _, tgt := range targets {
			t.Logf("  target: %s (key=%s)", tgt.DisplayKey(), tgt.Key)
		}
	}

	found := make(map[string]bool)
	for _, tgt := range targets {
		found[tgt.DisplayKey()] = true
	}

	for _, expected := range []string{"mayor", "deacon", "testrig/crew", "testrig/witness"} {
		if !found[expected] {
			t.Errorf("expected target %q not found", expected)
		}
	}
}

func TestDiscoverTargets_RoleNames(t *testing.T) {
	tmpDir := t.TempDir()

	os.MkdirAll(filepath.Join(tmpDir, "mayor"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "deacon"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "rig1", "crew", "alice"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "rig1", "polecats", "toast"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "rig1", "witness"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "rig1", "refinery"), 0755)

	targets, err := DiscoverTargets(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverTargets failed: %v", err)
	}

	// Verify Role field uses singular form (matching RoleSettingsDir conventions)
	roleByKey := make(map[string]string)
	for _, tgt := range targets {
		roleByKey[tgt.Key] = tgt.Role
	}

	expected := map[string]string{
		"mayor":         "mayor",
		"deacon":        "deacon",
		"rig1/crew":     "crew",
		"rig1/polecats": "polecat",
		"rig1/witness":  "witness",
		"rig1/refinery": "refinery",
	}

	for key, wantRole := range expected {
		gotRole, ok := roleByKey[key]
		if !ok {
			t.Errorf("target %q not found", key)
			continue
		}
		if gotRole != wantRole {
			t.Errorf("target %q: Role = %q, want %q", key, gotRole, wantRole)
		}
	}
}

// TestDiscoverTargets_PerRigMayor verifies that <rig>/mayor/rig/ is enumerated
// as a per-rig mayor target. Without this, .claude/settings.json in the rig's
// canonical git clone drifts indefinitely (see gu-jq0q audit and gu-83d5).
func TestDiscoverTargets_PerRigMayor(t *testing.T) {
	tmpDir := t.TempDir()

	os.MkdirAll(filepath.Join(tmpDir, "mayor"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "deacon"), 0755)
	// rig1 has mayor/rig (the canonical checkout); rig2 does not.
	os.MkdirAll(filepath.Join(tmpDir, "rig1", "crew", "alice"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "rig1", "mayor", "rig"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "rig2", "crew", "bob"), 0755)

	targets, err := DiscoverTargets(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverTargets failed: %v", err)
	}

	byKey := make(map[string]Target)
	for _, tgt := range targets {
		byKey[tgt.Key] = tgt
	}

	// rig1 must have a per-rig mayor target with the correct fields.
	tgt, ok := byKey["rig1/mayor"]
	if !ok {
		t.Fatalf("expected target %q for per-rig mayor, not found. All keys: %v",
			"rig1/mayor", keysOf(byKey))
	}
	wantPath := filepath.Join(tmpDir, "rig1", "mayor", "rig", ".claude", "settings.json")
	if tgt.Path != wantPath {
		t.Errorf("rig1/mayor Path = %q, want %q", tgt.Path, wantPath)
	}
	if tgt.Rig != "rig1" {
		t.Errorf("rig1/mayor Rig = %q, want %q", tgt.Rig, "rig1")
	}
	if tgt.Role != "mayor" {
		t.Errorf("rig1/mayor Role = %q, want %q", tgt.Role, "mayor")
	}

	// The town-level mayor target must still exist and be distinct from the
	// per-rig mayor (Key="mayor" vs Key="rig1/mayor").
	townMayor, ok := byKey["mayor"]
	if !ok {
		t.Error("town-level mayor target not found")
	}
	if townMayor.Rig != "" {
		t.Errorf("town-level mayor Rig = %q, want empty string", townMayor.Rig)
	}

	// rig2 lacks mayor/rig/, so no per-rig mayor target should exist for it.
	if _, ok := byKey["rig2/mayor"]; ok {
		t.Errorf("unexpected target %q — rig2 has no mayor/rig directory", "rig2/mayor")
	}
}

// keysOf returns the sorted keys of a map for stable test diagnostics.
func keysOf(m map[string]Target) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestDiscoverTargets_ReturnsOnlyClaude(t *testing.T) {
	tmpDir := t.TempDir()

	os.MkdirAll(filepath.Join(tmpDir, "mayor"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "deacon"), 0755)

	// Create a rig with crew members that have both Claude and Gemini settings.
	// DiscoverTargets should only return Claude targets; non-Claude agents are
	// discovered via DiscoverRoleLocations instead.
	os.MkdirAll(filepath.Join(tmpDir, "rig1", "crew", "alice"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "rig1", "witness"), 0755)

	// Install gemini settings (should NOT appear in DiscoverTargets results)
	geminiDir := filepath.Join(tmpDir, "rig1", "crew", "alice", ".gemini")
	os.MkdirAll(geminiDir, 0755)
	os.WriteFile(filepath.Join(geminiDir, "settings.json"), []byte(`{"hooks":{}}`), 0644)

	targets, err := DiscoverTargets(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverTargets failed: %v", err)
	}

	for _, tgt := range targets {
		if tgt.Provider == "gemini" {
			t.Errorf("DiscoverTargets should not return gemini targets, got: %s", tgt.DisplayKey())
		}
	}
}

func TestDiscoverTargets_BootIncluded(t *testing.T) {
	tmpDir := t.TempDir()

	os.MkdirAll(filepath.Join(tmpDir, "mayor"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "deacon", "dogs", "boot"), 0755)

	targets, err := DiscoverTargets(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverTargets failed: %v", err)
	}

	found := false
	for _, tgt := range targets {
		if tgt.Key == "boot" {
			found = true
			wantPath := filepath.Join(tmpDir, "deacon", "dogs", "boot", ".claude", "settings.json")
			if tgt.Path != wantPath {
				t.Errorf("boot target Path = %q, want %q", tgt.Path, wantPath)
			}
			if tgt.Role != "boot" {
				t.Errorf("boot target Role = %q, want %q", tgt.Role, "boot")
			}
		}
	}
	if !found {
		t.Error("expected boot target when deacon/dogs/boot/ exists, not found")
	}
}

func TestDiscoverTargets_BootAbsent(t *testing.T) {
	tmpDir := t.TempDir()

	os.MkdirAll(filepath.Join(tmpDir, "mayor"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "deacon"), 0755)
	// No deacon/dogs/boot directory

	targets, err := DiscoverTargets(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverTargets failed: %v", err)
	}

	for _, tgt := range targets {
		if tgt.Key == "boot" {
			t.Errorf("expected no boot target when deacon/dogs/boot/ absent, got one: %+v", tgt)
		}
	}
}

func TestDiscoverRoleLocations(t *testing.T) {
	tmpDir := t.TempDir()

	os.MkdirAll(filepath.Join(tmpDir, "mayor"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "deacon"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "rig1", "crew", "alice"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "rig1", "polecats", "toast"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "rig1", "witness"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "rig1", "refinery"), 0755)

	locations, err := DiscoverRoleLocations(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverRoleLocations failed: %v", err)
	}

	// Build lookup by role+rig
	type key struct{ rig, role string }
	found := make(map[key]RoleLocation)
	for _, loc := range locations {
		found[key{loc.Rig, loc.Role}] = loc
	}

	expected := []struct {
		rig, role string
	}{
		{"", "mayor"},
		{"", "deacon"},
		{"rig1", "crew"},
		{"rig1", "polecat"},
		{"rig1", "witness"},
		{"rig1", "refinery"},
	}

	for _, e := range expected {
		loc, ok := found[key{e.rig, e.role}]
		if !ok {
			t.Errorf("expected location rig=%q role=%q not found", e.rig, e.role)
			continue
		}
		if loc.Dir == "" {
			t.Errorf("location rig=%q role=%q has empty Dir", e.rig, e.role)
		}
	}

	if len(locations) != len(expected) {
		t.Errorf("expected %d locations, got %d", len(expected), len(locations))
	}
}

func TestDiscoverRoleLocations_SkipsNonRigs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a directory that isn't a rig (no crew/witness/polecats/refinery subdirs)
	os.MkdirAll(filepath.Join(tmpDir, "notarig", "something"), 0755)
	// Hidden dirs should be skipped
	os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, ".hidden", "crew"), 0755)

	locations, err := DiscoverRoleLocations(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverRoleLocations failed: %v", err)
	}

	for _, loc := range locations {
		if loc.Rig == "notarig" || loc.Rig == ".beads" || loc.Rig == ".hidden" {
			t.Errorf("unexpected location found: rig=%q role=%q", loc.Rig, loc.Role)
		}
	}
}

// TestDiscoverRoleLocations_Dogs verifies that deacon/dogs/* are discovered as
// town-level role="dog" locations. Without this discovery, per-dog agent configs
// (.kiro/agents/gastown.json, .opencode/plugins/gastown.js, etc.) drift
// indefinitely because gt hooks sync never reaches them and InstallForRole
// skips files that already exist. See gu-16md / gt-nadji.
func TestDiscoverRoleLocations_Dogs(t *testing.T) {
	tmpDir := t.TempDir()

	os.MkdirAll(filepath.Join(tmpDir, "mayor"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "deacon", "dogs", "alpha"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "deacon", "dogs", "bravo"), 0755)
	// A hidden entry under dogs/ must be skipped.
	os.MkdirAll(filepath.Join(tmpDir, "deacon", "dogs", ".tmp"), 0755)
	// A project worktree nested inside a dog (separate repo) must not produce
	// its own location — dog discovery returns the dog dir, not its subdirs.
	os.MkdirAll(filepath.Join(tmpDir, "deacon", "dogs", "alpha", "some_repo"), 0755)

	locations, err := DiscoverRoleLocations(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverRoleLocations failed: %v", err)
	}

	var dogs []RoleLocation
	for _, loc := range locations {
		if loc.Role == "dog" {
			dogs = append(dogs, loc)
		}
	}

	if len(dogs) != 2 {
		t.Fatalf("expected 2 dog locations, got %d: %+v", len(dogs), dogs)
	}

	seen := make(map[string]bool)
	for _, loc := range dogs {
		if loc.Rig != "" {
			t.Errorf("dog location should be town-level (Rig=\"\"), got Rig=%q", loc.Rig)
		}
		if loc.Dir == "" {
			t.Errorf("dog location has empty Dir")
		}
		seen[filepath.Base(loc.Dir)] = true
	}

	for _, name := range []string{"alpha", "bravo"} {
		if !seen[name] {
			t.Errorf("expected dog %q in discovered locations, got %v", name, seen)
		}
	}
	if seen[".tmp"] {
		t.Error("hidden dog dir should be skipped")
	}
	if seen["some_repo"] {
		t.Error("nested repo under a dog must not be treated as a dog")
	}
}

// TestDiscoverRoleLocations_NoDogsDir verifies that the deacon/dogs/ directory
// is optional. Workspaces without any dogs should still enumerate the rest of
// the roles without error.
func TestDiscoverRoleLocations_NoDogsDir(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "mayor"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "deacon"), 0755)
	// No deacon/dogs/ subdir at all.

	locations, err := DiscoverRoleLocations(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverRoleLocations failed: %v", err)
	}

	for _, loc := range locations {
		if loc.Role == "dog" {
			t.Errorf("did not expect a dog location when deacon/dogs is absent, got %+v", loc)
		}
	}
}

// TestDiscoverRoleLocations_PerRigMayor verifies that <rig>/mayor/rig/
// directories are enumerated as role="mayor" locations with Rig set to the
// rig name. Without this, gt hooks sync never reaches per-rig mayor
// .kiro/agents/gastown.json (and similar non-Claude agent configs), so they
// drift indefinitely after InstallForRole writes them on first creation.
// See gu-ubtr / gu-jq0q audit.
func TestDiscoverRoleLocations_PerRigMayor(t *testing.T) {
	tmpDir := t.TempDir()

	// Town-level mayor (already covered by other tests, but include for realism).
	os.MkdirAll(filepath.Join(tmpDir, "mayor"), 0755)

	// rig1 has mayor/rig/ — should appear as a per-rig mayor location.
	os.MkdirAll(filepath.Join(tmpDir, "rig1", "crew"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "rig1", "mayor", "rig"), 0755)

	// rig2 has no mayor/rig/ — should NOT produce a per-rig mayor location.
	os.MkdirAll(filepath.Join(tmpDir, "rig2", "crew"), 0755)

	locations, err := DiscoverRoleLocations(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverRoleLocations failed: %v", err)
	}

	var perRigMayors []RoleLocation
	var townMayors []RoleLocation
	for _, loc := range locations {
		if loc.Role != "mayor" {
			continue
		}
		if loc.Rig == "" {
			townMayors = append(townMayors, loc)
		} else {
			perRigMayors = append(perRigMayors, loc)
		}
	}

	// Town-level mayor should still be present and distinct from per-rig mayors.
	if len(townMayors) != 1 {
		t.Errorf("expected exactly 1 town-level mayor location (Rig=\"\"), got %d: %+v",
			len(townMayors), townMayors)
	}

	// Exactly one per-rig mayor: rig1.
	if len(perRigMayors) != 1 {
		t.Fatalf("expected 1 per-rig mayor location, got %d: %+v", len(perRigMayors), perRigMayors)
	}

	got := perRigMayors[0]
	if got.Rig != "rig1" {
		t.Errorf("per-rig mayor Rig = %q, want %q", got.Rig, "rig1")
	}
	wantDir := filepath.Join(tmpDir, "rig1", "mayor", "rig")
	if got.Dir != wantDir {
		t.Errorf("per-rig mayor Dir = %q, want %q", got.Dir, wantDir)
	}

	// Sanity: rig2 (no mayor/rig) must not have produced a mayor entry.
	for _, loc := range perRigMayors {
		if loc.Rig == "rig2" {
			t.Errorf("rig2 has no mayor/rig/ subdir but got per-rig mayor location: %+v", loc)
		}
	}
}

// TestDiscoverRoleLocations_PerRigMayor_FileInsteadOfDir verifies that a
// <rig>/mayor path that exists as a file (or where mayor/rig is a file)
// does not produce a spurious per-rig mayor location. This mirrors the
// IsDir guard on the town-level and rig-role enumerations.
func TestDiscoverRoleLocations_PerRigMayor_FileInsteadOfDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Make rig1 a valid rig (has crew/) but place a FILE at mayor/rig.
	os.MkdirAll(filepath.Join(tmpDir, "rig1", "crew"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "rig1", "mayor"), 0755)
	if err := os.WriteFile(filepath.Join(tmpDir, "rig1", "mayor", "rig"), []byte("not a dir"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	locations, err := DiscoverRoleLocations(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverRoleLocations failed: %v", err)
	}

	for _, loc := range locations {
		if loc.Role == "mayor" && loc.Rig == "rig1" {
			t.Errorf("expected no per-rig mayor location when mayor/rig is a file, got %+v", loc)
		}
	}
}

func TestDiscoverWorktrees(t *testing.T) {
	tmpDir := t.TempDir()

	// Create worktree subdirectories
	os.MkdirAll(filepath.Join(tmpDir, "alice"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "bob"), 0755)
	// Hidden dirs should be skipped
	os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0755)
	// Files should be skipped
	os.WriteFile(filepath.Join(tmpDir, "state.json"), []byte("{}"), 0644)

	dirs := DiscoverWorktrees(tmpDir)

	if len(dirs) != 2 {
		t.Errorf("expected 2 worktrees, got %d: %v", len(dirs), dirs)
	}

	names := make(map[string]bool)
	for _, d := range dirs {
		names[filepath.Base(d)] = true
	}
	if !names["alice"] || !names["bob"] {
		t.Errorf("expected alice and bob, got %v", names)
	}
	if names[".claude"] {
		t.Error("hidden directory should be skipped")
	}
}

func TestDiscoverWorktrees_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	dirs := DiscoverWorktrees(tmpDir)
	if len(dirs) != 0 {
		t.Errorf("expected 0 worktrees, got %d", len(dirs))
	}
}

func TestDiscoverWorktrees_PrefersNestedGitWorktreeRoots(t *testing.T) {
	tmpDir := t.TempDir()

	worktree := filepath.Join(tmpDir, "fury", "gastown")
	if err := os.MkdirAll(filepath.Join(worktree, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "dust"), 0755); err != nil {
		t.Fatal(err)
	}

	dirs := DiscoverWorktrees(tmpDir)

	if len(dirs) != 2 {
		t.Fatalf("expected 2 worktrees, got %d: %v", len(dirs), dirs)
	}

	got := make(map[string]bool)
	for _, dir := range dirs {
		got[dir] = true
	}

	if !got[worktree] {
		t.Fatalf("expected nested worktree root %q, got %v", worktree, dirs)
	}
	if !got[filepath.Join(tmpDir, "dust")] {
		t.Fatalf("expected direct worktree fallback %q, got %v", filepath.Join(tmpDir, "dust"), dirs)
	}
}

func TestDiscoverWorktrees_InvalidDir(t *testing.T) {
	dirs := DiscoverWorktrees("/nonexistent/path/that/does/not/exist")
	if dirs != nil {
		t.Errorf("expected nil for invalid dir, got %v", dirs)
	}
}

func TestDiscoverPolecatWorktrees(t *testing.T) {
	tmpDir := t.TempDir()

	// Typical polecat layout:
	//   polecats/alice/myrig/.git   (git worktree file)
	//   polecats/bob/myrig/.git/    (primary clone, git dir)
	//   polecats/incomplete/        (no worktree yet — should be skipped)
	//   polecats/.hidden/           (hidden — should be skipped)
	//   polecats/state.json         (file — should be skipped)
	aliceWorktree := filepath.Join(tmpDir, "alice", "myrig")
	os.MkdirAll(aliceWorktree, 0755)
	os.WriteFile(filepath.Join(aliceWorktree, ".git"), []byte("gitdir: /somewhere/else"), 0644)
	// Also create a non-worktree sibling under alice/ that must not be picked.
	os.MkdirAll(filepath.Join(tmpDir, "alice", ".runtime"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "alice", "state.json"), []byte("{}"), 0644)

	bobWorktree := filepath.Join(tmpDir, "bob", "myrig")
	os.MkdirAll(filepath.Join(bobWorktree, ".git"), 0755)

	os.MkdirAll(filepath.Join(tmpDir, "incomplete"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, ".hidden", "myrig"), 0755)
	os.WriteFile(filepath.Join(tmpDir, ".hidden", "myrig", ".git"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "state.json"), []byte("{}"), 0644)

	dirs := DiscoverPolecatWorktrees(tmpDir)

	if len(dirs) != 2 {
		t.Fatalf("expected 2 worktrees, got %d: %v", len(dirs), dirs)
	}

	found := make(map[string]bool)
	for _, d := range dirs {
		found[d] = true
	}
	if !found[aliceWorktree] {
		t.Errorf("expected alice worktree %q, got %v", aliceWorktree, dirs)
	}
	if !found[bobWorktree] {
		t.Errorf("expected bob worktree %q, got %v", bobWorktree, dirs)
	}
	// State dir must NOT appear.
	if found[filepath.Join(tmpDir, "alice")] {
		t.Error("polecat state dir should not be returned as a worktree")
	}
}

func TestDiscoverPolecatWorktrees_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	dirs := DiscoverPolecatWorktrees(tmpDir)
	if dirs != nil {
		t.Errorf("expected nil for empty dir, got %v", dirs)
	}
}

func TestDiscoverPolecatWorktrees_InvalidDir(t *testing.T) {
	dirs := DiscoverPolecatWorktrees("/nonexistent/path/that/does/not/exist")
	if dirs != nil {
		t.Errorf("expected nil for invalid dir, got %v", dirs)
	}
}

func TestDiscoverPolecatWorktrees_SkipsWhenNoGitFound(t *testing.T) {
	// A polecat dir without any nested .git should be skipped entirely
	// rather than returning the state dir (which would reintroduce the bug).
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "alice", "myrig"), 0755)
	// No .git anywhere.

	dirs := DiscoverPolecatWorktrees(tmpDir)
	if len(dirs) != 0 {
		t.Errorf("expected 0 worktrees when no .git present, got %v", dirs)
	}
}

func TestDiscoverRoleLocations_ReadError(t *testing.T) {
	_, err := DiscoverRoleLocations("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Error("expected error for nonexistent directory")
	}
}

func TestTargetDisplayKey(t *testing.T) {
	tests := []struct {
		target   Target
		expected string
	}{
		{Target{Key: "mayor", Role: "mayor"}, "mayor"},
		{Target{Key: "gastown/crew", Rig: "gastown", Role: "crew"}, "gastown/crew"},
		{Target{Key: "beads/witness", Rig: "beads", Role: "witness"}, "beads/witness"},
	}

	for _, tt := range tests {
		if got := tt.target.DisplayKey(); got != tt.expected {
			t.Errorf("DisplayKey() = %q, want %q", got, tt.expected)
		}
	}
}

func TestGetSetEntries(t *testing.T) {
	cfg := &HooksConfig{
		SessionStart: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "test"}}},
		},
	}

	entries := cfg.GetEntries("SessionStart")
	if len(entries) != 1 {
		t.Errorf("expected 1 SessionStart entry, got %d", len(entries))
	}

	entries = cfg.GetEntries("PreToolUse")
	if len(entries) != 0 {
		t.Errorf("expected 0 PreToolUse entries, got %d", len(entries))
	}

	entries = cfg.GetEntries("Unknown")
	if entries != nil {
		t.Errorf("expected nil for unknown event type, got %v", entries)
	}

	cfg.SetEntries("PreToolUse", []HookEntry{
		{Matcher: "Bash(*)", Hooks: []Hook{{Type: "command", Command: "guard"}}},
	})
	if len(cfg.PreToolUse) != 1 {
		t.Errorf("expected 1 PreToolUse entry after SetEntries, got %d", len(cfg.PreToolUse))
	}
}

func TestToMap(t *testing.T) {
	cfg := &HooksConfig{
		SessionStart: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "start"}}},
		},
		Stop: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "stop"}}},
		},
	}

	m := cfg.ToMap()
	if len(m) != 2 {
		t.Errorf("expected 2 entries in map, got %d", len(m))
	}
	if _, ok := m["SessionStart"]; !ok {
		t.Error("expected SessionStart in map")
	}
	if _, ok := m["Stop"]; !ok {
		t.Error("expected Stop in map")
	}
	if _, ok := m["PreToolUse"]; ok {
		t.Error("empty PreToolUse should not be in map")
	}
}

func TestAddEntry(t *testing.T) {
	cfg := &HooksConfig{}

	added := cfg.AddEntry("PreToolUse", HookEntry{
		Matcher: "Bash(git*)",
		Hooks:   []Hook{{Type: "command", Command: "guard"}},
	})
	if !added {
		t.Error("expected first entry to be added")
	}
	if len(cfg.PreToolUse) != 1 {
		t.Errorf("expected 1 PreToolUse entry, got %d", len(cfg.PreToolUse))
	}

	added = cfg.AddEntry("PreToolUse", HookEntry{
		Matcher: "Bash(git*)",
		Hooks:   []Hook{{Type: "command", Command: "different"}},
	})
	if added {
		t.Error("expected duplicate matcher to not be added")
	}
	if len(cfg.PreToolUse) != 1 {
		t.Errorf("expected still 1 PreToolUse entry, got %d", len(cfg.PreToolUse))
	}

	added = cfg.AddEntry("PreToolUse", HookEntry{
		Matcher: "Bash(rm*)",
		Hooks:   []Hook{{Type: "command", Command: "block"}},
	})
	if !added {
		t.Error("expected new matcher to be added")
	}
	if len(cfg.PreToolUse) != 2 {
		t.Errorf("expected 2 PreToolUse entries, got %d", len(cfg.PreToolUse))
	}
}

func TestMarshalConfig(t *testing.T) {
	cfg := &HooksConfig{
		SessionStart: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "test"}}},
		},
	}

	data, err := MarshalConfig(cfg)
	if err != nil {
		t.Fatalf("MarshalConfig failed: %v", err)
	}

	if len(data) == 0 {
		t.Error("MarshalConfig returned empty data")
	}

	loaded := &HooksConfig{}
	if err := json.Unmarshal(data, loaded); err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}

	if len(loaded.SessionStart) != 1 {
		t.Errorf("round-trip lost SessionStart hooks")
	}
}

// --- Plugin defaults / town-configurable override layer (gu-eiumt) ---
//
// The Amazon-specific AIM disable-list is no longer hardcoded in shared Go.
// ExpectedPlugins layers the town's on-disk plugin overrides on top of the
// shared neutral default (beads disabled), and ApplyExpectedPlugins/
// HasExpectedPlugins are the write/read sides. The OOM guard (2026-06-05
// post-mortem) is now supplied by a town override file, not the binary.

// TestExpectedPlugins_NeutralDefaultNoOverride verifies that, with no override
// file, every role resolves to just the neutral beads-disabled default and no
// Amazon plugin names appear.
func TestExpectedPlugins_NeutralDefaultNoOverride(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	for _, target := range []string{"witness", "polecats", "crew", "mayor", "gastown/crew"} {
		t.Run(target, func(t *testing.T) {
			got, err := ExpectedPlugins(target)
			if err != nil {
				t.Fatalf("ExpectedPlugins(%q): %v", target, err)
			}
			if v, ok := got[neutralBeadsPlugin]; !ok || v {
				t.Errorf("beads plugin = (%v, present=%v), want explicitly false", v, ok)
			}
			if len(got) != 1 {
				t.Errorf("expected only the neutral default, got %d entries: %v", len(got), got)
			}
		})
	}
}

// TestExpectedPlugins_OverrideLayering verifies that a role-level override
// supplies a plugin policy and that a more-specific rig/role override wins
// per-plugin.
func TestExpectedPlugins_OverrideLayering(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	writePluginOverride(t, "polecats", map[string]bool{
		"SomePlugin-core":      true,
		"SomeOther-all@market": false,
	})
	writePluginOverride(t, "gastown/polecats", map[string]bool{
		"SomeOther-all@market": true, // rig/role flips the role-level value
	})

	got, err := ExpectedPlugins("gastown/polecats")
	if err != nil {
		t.Fatalf("ExpectedPlugins: %v", err)
	}
	if got[neutralBeadsPlugin] != false {
		t.Errorf("beads = %v, want false (neutral default preserved)", got[neutralBeadsPlugin])
	}
	if got["SomePlugin-core"] != true {
		t.Errorf("SomePlugin-core = %v, want true (from role override)", got["SomePlugin-core"])
	}
	if got["SomeOther-all@market"] != true {
		t.Errorf("SomeOther = %v, want true (rig/role override wins)", got["SomeOther-all@market"])
	}
}

// TestApplyAndHasExpectedPlugins verifies the write/read round-trip, including
// the fleet-only enableAllProjectMcpServers pin and additive (extra-entries-ok)
// drift semantics.
func TestApplyAndHasExpectedPlugins(t *testing.T) {
	expected := map[string]bool{
		neutralBeadsPlugin:     false,
		"SomePlugin-core":      true,
		"SomeOther-all@market": false,
	}

	t.Run("fleet", func(t *testing.T) {
		s := &SettingsJSON{}
		ApplyExpectedPlugins(s, constants.RolePolecat, expected)

		for plugin, want := range expected {
			if got, ok := s.EnabledPlugins[plugin]; !ok || got != want {
				t.Errorf("plugin %q = (%v, present=%v), want %v", plugin, got, ok, want)
			}
		}
		raw, ok := s.Extra["enableAllProjectMcpServers"]
		if !ok || string(raw) != "false" {
			t.Errorf("enableAllProjectMcpServers = (%q, present=%v), want false", string(raw), ok)
		}
		if !HasExpectedPlugins(s, constants.RolePolecat, expected) {
			t.Error("HasExpectedPlugins = false after Apply, want true")
		}
		// Extra entries beyond expected are fine (additive policy).
		s.EnabledPlugins["unrelated@x"] = true
		if !HasExpectedPlugins(s, constants.RolePolecat, expected) {
			t.Error("HasExpectedPlugins = false with extra entry, want true (additive)")
		}
	})

	t.Run("interactive", func(t *testing.T) {
		s := &SettingsJSON{}
		ApplyExpectedPlugins(s, constants.RoleCrew, expected)
		// Interactive roles do NOT get enableAllProjectMcpServers pinned.
		if _, ok := s.Extra["enableAllProjectMcpServers"]; ok {
			t.Error("interactive role should not set enableAllProjectMcpServers")
		}
		if !HasExpectedPlugins(s, constants.RoleCrew, expected) {
			t.Error("HasExpectedPlugins = false after Apply, want true")
		}
	})
}

// TestHasExpectedPlugins_DetectsDrift verifies the drift detector flags missing,
// flipped, or (for fleet) an unset enableAllProjectMcpServers.
func TestHasExpectedPlugins_DetectsDrift(t *testing.T) {
	expected := map[string]bool{
		neutralBeadsPlugin: false,
		"X-all@market":     false,
	}

	// nil settings => not configured.
	if HasExpectedPlugins(nil, constants.RoleDog, expected) {
		t.Error("HasExpectedPlugins(nil) = true, want false")
	}

	// Flipped value => drift.
	s := &SettingsJSON{}
	ApplyExpectedPlugins(s, constants.RoleDog, expected)
	s.EnabledPlugins["X-all@market"] = true
	if HasExpectedPlugins(s, constants.RoleDog, expected) {
		t.Error("HasExpectedPlugins with flipped entry = true, want false")
	}

	// Missing entry => drift.
	s2 := &SettingsJSON{}
	ApplyExpectedPlugins(s2, constants.RoleDog, expected)
	delete(s2.EnabledPlugins, "X-all@market")
	if HasExpectedPlugins(s2, constants.RoleDog, expected) {
		t.Error("HasExpectedPlugins missing entry = true, want false")
	}

	// Fleet role missing enableAllProjectMcpServers => drift.
	s3 := &SettingsJSON{}
	ApplyExpectedPlugins(s3, constants.RoleDog, expected)
	delete(s3.Extra, "enableAllProjectMcpServers")
	if HasExpectedPlugins(s3, constants.RoleDog, expected) {
		t.Error("HasExpectedPlugins fleet missing enableAllProjectMcpServers = true, want false")
	}
}

// writePluginOverride writes an enabledPlugins-carrying override file for target
// under the test home's ~/.gt/hooks-overrides/.
func writePluginOverride(t *testing.T, target string, plugins map[string]bool) {
	t.Helper()
	path := OverridePath(target)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir overrides: %v", err)
	}
	wrapper := map[string]interface{}{"enabledPlugins": plugins}
	data, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		t.Fatalf("marshal override: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write override: %v", err)
	}
}

// --- mcpServers managed field / town-configurable override layer (gu-2nmnt) ---
//
// Mirrors the enabledPlugins layer: Amazon-/tool-specific server names
// (builder-mcp, serena) ship in ~/.gt/hooks-overrides/<target>.json, not in
// shared Go. ExpectedMCPServers layers the town's on-disk mcpServers overrides;
// ApplyExpectedMCPServers/HasExpectedMCPServers are the write/read sides.

// TestExpectedMCPServers_EmptyWhenNoOverride verifies that, with no override
// file, every role resolves to an empty map and no server names appear.
func TestExpectedMCPServers_EmptyWhenNoOverride(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	for _, target := range []string{"witness", "polecats", "crew", "mayor", "gastown/crew"} {
		t.Run(target, func(t *testing.T) {
			got, err := ExpectedMCPServers(target)
			if err != nil {
				t.Fatalf("ExpectedMCPServers(%q): %v", target, err)
			}
			if len(got) != 0 {
				t.Errorf("expected empty mcpServers map, got %d entries: %v", len(got), got)
			}
		})
	}
}

// TestExpectedMCPServers_OverrideLayering verifies that a role-level override
// supplies servers and a more-specific rig/role override wins per-server.
func TestExpectedMCPServers_OverrideLayering(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	writeMCPServerOverride(t, "polecats", map[string]string{
		"serena":      `{"command":"uvx","args":["serena"]}`,
		"builder-mcp": `{"command":"builder-mcp","args":[]}`,
	})
	writeMCPServerOverride(t, "gastown/polecats", map[string]string{
		"builder-mcp": `{"command":"builder-mcp","args":["--flag"]}`, // rig/role flips it
	})

	got, err := ExpectedMCPServers("gastown/polecats")
	if err != nil {
		t.Fatalf("ExpectedMCPServers: %v", err)
	}
	if _, ok := got["serena"]; !ok {
		t.Errorf("serena missing (should come from role override)")
	}
	if !rawJSONEqual(got["builder-mcp"], json.RawMessage(`{"command":"builder-mcp","args":["--flag"]}`)) {
		t.Errorf("builder-mcp = %s, want rig/role override value", got["builder-mcp"])
	}
}

// TestApplyAndHasExpectedMCPServers verifies the write/read round-trip and the
// additive (extra-entries-ok) drift semantics.
func TestApplyAndHasExpectedMCPServers(t *testing.T) {
	expected := map[string]json.RawMessage{
		"serena":      json.RawMessage(`{"command":"uvx","args":["serena"]}`),
		"builder-mcp": json.RawMessage(`{"command":"builder-mcp","args":[]}`),
	}

	s := &SettingsJSON{}
	ApplyExpectedMCPServers(s, expected)

	if !HasExpectedMCPServers(s, expected) {
		t.Fatal("HasExpectedMCPServers = false after Apply, want true")
	}

	// Extra servers beyond expected are fine (additive policy).
	current := CurrentMCPServers(s)
	if len(current) != 2 {
		t.Fatalf("expected 2 servers after Apply, got %d", len(current))
	}
	current["unrelated"] = json.RawMessage(`{"command":"x"}`)
	raw, _ := json.Marshal(current)
	s.Extra["mcpServers"] = raw
	if !HasExpectedMCPServers(s, expected) {
		t.Error("HasExpectedMCPServers = false with extra entry, want true (additive)")
	}
}

// TestApplyExpectedMCPServers_PreservesExisting verifies Apply merges into an
// existing mcpServers block rather than replacing it.
func TestApplyExpectedMCPServers_PreservesExisting(t *testing.T) {
	s := &SettingsJSON{Extra: map[string]json.RawMessage{
		"mcpServers": json.RawMessage(`{"operator-added":{"command":"keep"}}`),
	}}
	expected := map[string]json.RawMessage{
		"builder-mcp": json.RawMessage(`{"command":"builder-mcp"}`),
	}
	ApplyExpectedMCPServers(s, expected)

	got := CurrentMCPServers(s)
	if _, ok := got["operator-added"]; !ok {
		t.Error("Apply dropped operator-added server")
	}
	if _, ok := got["builder-mcp"]; !ok {
		t.Error("Apply did not add builder-mcp")
	}
}

// TestApplyExpectedMCPServers_EmptyIsNoOp verifies an empty expected map does
// not touch the settings (towns without MCP config see no change).
func TestApplyExpectedMCPServers_EmptyIsNoOp(t *testing.T) {
	s := &SettingsJSON{}
	ApplyExpectedMCPServers(s, map[string]json.RawMessage{})
	if _, ok := s.Extra["mcpServers"]; ok {
		t.Error("empty expected should not create an mcpServers block")
	}
	// And an empty expected map is trivially satisfied.
	if !HasExpectedMCPServers(s, map[string]json.RawMessage{}) {
		t.Error("HasExpectedMCPServers(empty) should be true")
	}
	if !HasExpectedMCPServers(nil, map[string]json.RawMessage{}) {
		t.Error("HasExpectedMCPServers(nil, empty) should be true")
	}
}

// TestHasExpectedMCPServers_DetectsDrift verifies the drift detector flags
// missing or modified servers.
func TestHasExpectedMCPServers_DetectsDrift(t *testing.T) {
	expected := map[string]json.RawMessage{
		"builder-mcp": json.RawMessage(`{"command":"builder-mcp","args":[]}`),
	}

	// nil settings with a non-empty expectation => not configured.
	if HasExpectedMCPServers(nil, expected) {
		t.Error("HasExpectedMCPServers(nil) = true, want false")
	}

	// Missing server => drift.
	empty := &SettingsJSON{}
	if HasExpectedMCPServers(empty, expected) {
		t.Error("HasExpectedMCPServers with no mcpServers block = true, want false")
	}

	// Modified definition => drift.
	s := &SettingsJSON{}
	ApplyExpectedMCPServers(s, expected)
	current := CurrentMCPServers(s)
	current["builder-mcp"] = json.RawMessage(`{"command":"WRONG"}`)
	raw, _ := json.Marshal(current)
	s.Extra["mcpServers"] = raw
	if HasExpectedMCPServers(s, expected) {
		t.Error("HasExpectedMCPServers with modified server = true, want false")
	}
}

// TestHasExpectedMCPServers_KeyOrderInsensitive verifies a server definition
// with reordered keys is not treated as drift.
func TestHasExpectedMCPServers_KeyOrderInsensitive(t *testing.T) {
	expected := map[string]json.RawMessage{
		"builder-mcp": json.RawMessage(`{"command":"builder-mcp","args":[],"env":{}}`),
	}
	s := &SettingsJSON{Extra: map[string]json.RawMessage{
		"mcpServers": json.RawMessage(`{"builder-mcp":{"env":{},"args":[],"command":"builder-mcp"}}`),
	}}
	if !HasExpectedMCPServers(s, expected) {
		t.Error("reordered keys should not count as drift")
	}
}

// TestLoadOverrideMCPServers_MissingAndEmpty verifies load semantics: missing
// file => ErrNotExist; present file without mcpServers => empty non-nil map.
func TestLoadOverrideMCPServers_MissingAndEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	if _, err := LoadOverrideMCPServers("polecats"); !os.IsNotExist(err) {
		t.Errorf("expected ErrNotExist for missing override, got %v", err)
	}

	// Write an override that carries only enabledPlugins (no mcpServers).
	writePluginOverride(t, "polecats", map[string]bool{"X-core": true})
	got, err := LoadOverrideMCPServers("polecats")
	if err != nil {
		t.Fatalf("LoadOverrideMCPServers: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("expected empty non-nil map for override without mcpServers, got %v", got)
	}
}

// writeMCPServerOverride writes an mcpServers-carrying override file for target
// under the test home's ~/.gt/hooks-overrides/. Values are raw JSON strings.
func writeMCPServerOverride(t *testing.T, target string, servers map[string]string) {
	t.Helper()
	path := OverridePath(target)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir overrides: %v", err)
	}
	mcp := make(map[string]json.RawMessage, len(servers))
	for name, def := range servers {
		mcp[name] = json.RawMessage(def)
	}
	wrapper := map[string]interface{}{"mcpServers": mcp}
	data, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		t.Fatalf("marshal override: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write override: %v", err)
	}
}

// TestDiscoverTargets_DogsIncluded verifies that arbitrary dogs under
// deacon/dogs/* are enumerated as fleet targets (role "dog"), boot keeps its
// dedicated role, and hidden entries are skipped.
func TestDiscoverTargets_DogsIncluded(t *testing.T) {
	tmpDir := t.TempDir()

	os.MkdirAll(filepath.Join(tmpDir, "mayor"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "deacon", "dogs", "boot"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "deacon", "dogs", "charlie"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "deacon", "dogs", "delta"), 0755)
	// Hidden entry must be skipped.
	os.MkdirAll(filepath.Join(tmpDir, "deacon", "dogs", ".tmp"), 0755)

	targets, err := DiscoverTargets(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverTargets failed: %v", err)
	}

	byPath := make(map[string]Target)
	for _, tgt := range targets {
		byPath[tgt.Path] = tgt
	}

	// boot keeps its dedicated role.
	bootPath := filepath.Join(tmpDir, "deacon", "dogs", "boot", ".claude", "settings.json")
	if tgt, ok := byPath[bootPath]; !ok {
		t.Error("expected boot target, not found")
	} else if tgt.Role != constants.RoleBoot {
		t.Errorf("boot target Role = %q, want %q", tgt.Role, constants.RoleBoot)
	}

	// Arbitrary dogs are fleet targets with role "dog".
	for _, name := range []string{"charlie", "delta"} {
		p := filepath.Join(tmpDir, "deacon", "dogs", name, ".claude", "settings.json")
		tgt, ok := byPath[p]
		if !ok {
			t.Errorf("expected dog target for %q, not found", name)
			continue
		}
		if tgt.Role != constants.RoleDog {
			t.Errorf("dog %q Role = %q, want %q", name, tgt.Role, constants.RoleDog)
		}
		if !isFleetRole(tgt.Role) {
			t.Errorf("dog %q role %q not recognized as fleet role", name, tgt.Role)
		}
	}

	// Hidden entry must not appear.
	hidden := filepath.Join(tmpDir, "deacon", "dogs", ".tmp", ".claude", "settings.json")
	if _, ok := byPath[hidden]; ok {
		t.Error("hidden dogs entry .tmp should be skipped, but was included")
	}
}

// settingsWithPermissions builds a SettingsJSON whose Extra carries the given
// permissions map (or no permissions key when perm is nil).
func settingsWithPermissions(t *testing.T, perm map[string]any) *SettingsJSON {
	t.Helper()
	s := &SettingsJSON{Extra: map[string]json.RawMessage{}}
	if perm != nil {
		raw, err := json.Marshal(perm)
		if err != nil {
			t.Fatalf("marshal perm: %v", err)
		}
		s.Extra["permissions"] = raw
	}
	return s
}

func TestRequiredDenyForRole(t *testing.T) {
	tests := []struct {
		role        string
		wantFirst   string // "" means empty
		mustContain []string
		mustOmit    []string
		wantEmpty   bool
	}{
		{role: constants.RoleMayor, wantEmpty: true},
		{
			role:        constants.RoleCrew,
			mustContain: []string{"TodoWrite", "TaskStop"},
			mustOmit:    []string{"AskUserQuestion"},
		},
		{
			role:        constants.RoleWitness,
			wantFirst:   "AskUserQuestion",
			mustContain: []string{"AskUserQuestion", "TodoWrite", "TaskStop"},
		},
		{
			role:        constants.RoleRefinery,
			mustContain: []string{"AskUserQuestion", "TaskList"},
		},
		{
			role:        constants.RoleDeacon,
			mustContain: []string{"AskUserQuestion", "TaskGet"},
		},
		{
			role:        constants.RolePolecat,
			mustContain: []string{"AskUserQuestion", "TaskCreate"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			got := requiredDenyForRole(tt.role)
			if tt.wantEmpty {
				if len(got) != 0 {
					t.Fatalf("requiredDenyForRole(%q) = %v, want empty", tt.role, got)
				}
				return
			}
			if tt.wantFirst != "" && (len(got) == 0 || got[0] != tt.wantFirst) {
				t.Errorf("requiredDenyForRole(%q) first = %v, want %q", tt.role, got, tt.wantFirst)
			}
			has := make(map[string]bool)
			for _, d := range got {
				has[d] = true
			}
			for _, m := range tt.mustContain {
				if !has[m] {
					t.Errorf("requiredDenyForRole(%q) missing %q (got %v)", tt.role, m, got)
				}
			}
			for _, m := range tt.mustOmit {
				if has[m] {
					t.Errorf("requiredDenyForRole(%q) should omit %q (got %v)", tt.role, m, got)
				}
			}
		})
	}
}

func TestEnsurePermissionDefaultsRestoresDenyList(t *testing.T) {
	// Witness with a permissions block that lost its deny list entirely.
	s := settingsWithPermissions(t, map[string]any{"defaultMode": "bypassPermissions"})

	EnsurePermissionDefaults(s, constants.RoleWitness)

	if !HasPermissionDefaults(s, constants.RoleWitness) {
		t.Fatal("witness should have permission defaults after EnsurePermissionDefaults")
	}
	deny := CurrentDenyList(s)
	has := make(map[string]bool)
	for _, d := range deny {
		has[d] = true
	}
	for _, req := range requiredDenyForRole(constants.RoleWitness) {
		if !has[req] {
			t.Errorf("deny list missing required %q after restore (got %v)", req, deny)
		}
	}
}

func TestEnsurePermissionDefaultsPreservesExtraDeny(t *testing.T) {
	// Operator added a custom deny entry; restore must keep it.
	s := settingsWithPermissions(t, map[string]any{
		"defaultMode": "bypassPermissions",
		"deny":        []string{"SomeCustomTool"},
	})

	EnsurePermissionDefaults(s, constants.RoleWitness)

	deny := CurrentDenyList(s)
	has := make(map[string]bool)
	for _, d := range deny {
		has[d] = true
	}
	if !has["SomeCustomTool"] {
		t.Errorf("custom deny entry dropped during restore (got %v)", deny)
	}
	if !has["AskUserQuestion"] {
		t.Errorf("required AskUserQuestion not added (got %v)", deny)
	}
}

func TestEnsurePermissionDefaultsNoPermissionsKey(t *testing.T) {
	// Permissions block entirely absent (worst-case drift).
	s := &SettingsJSON{Extra: map[string]json.RawMessage{}}

	EnsurePermissionDefaults(s, constants.RoleDeacon)

	if !HasPermissionDefaults(s, constants.RoleDeacon) {
		t.Fatal("deacon should have permission defaults after restore from empty")
	}
}

func TestEnsurePermissionDefaultsMayorNoDeny(t *testing.T) {
	s := &SettingsJSON{Extra: map[string]json.RawMessage{}}

	EnsurePermissionDefaults(s, constants.RoleMayor)

	// Mayor must still get defaultMode pinned, but no deny list required.
	if !HasPermissionDefaults(s, constants.RoleMayor) {
		t.Fatal("mayor should satisfy permission defaults (defaultMode only)")
	}
	if len(CurrentDenyList(s)) != 0 {
		t.Errorf("mayor should have no managed deny entries, got %v", CurrentDenyList(s))
	}
}

func TestHasPermissionDefaultsDetectsDrift(t *testing.T) {
	// Witness missing AskUserQuestion → drift.
	s := settingsWithPermissions(t, map[string]any{
		"defaultMode": "bypassPermissions",
		"deny":        []string{"TodoWrite"},
	})
	if HasPermissionDefaults(s, constants.RoleWitness) {
		t.Error("witness missing AskUserQuestion should report drift")
	}

	// Crew with Task tools but no AskUserQuestion → satisfied (crew doesn't require it).
	crew := settingsWithPermissions(t, map[string]any{
		"defaultMode": "bypassPermissions",
		"deny":        []string{"TodoWrite", "TaskCreate", "TaskUpdate", "TaskList", "TaskGet", "TaskOutput", "TaskStop"},
	})
	if !HasPermissionDefaults(crew, constants.RoleCrew) {
		t.Error("crew with full Task deny list should be satisfied")
	}
}

func TestHasPermissionDefaultsWrongDefaultMode(t *testing.T) {
	s := settingsWithPermissions(t, map[string]any{
		"defaultMode": "ask",
		"deny":        requiredDenyForRole(constants.RoleWitness),
	})
	if HasPermissionDefaults(s, constants.RoleWitness) {
		t.Error("wrong defaultMode should report drift")
	}
}

func TestEnsurePermissionDefaultsIdempotent(t *testing.T) {
	s := &SettingsJSON{Extra: map[string]json.RawMessage{}}
	EnsurePermissionDefaults(s, constants.RoleWitness)
	first := CurrentDenyList(s)
	EnsurePermissionDefaults(s, constants.RoleWitness)
	second := CurrentDenyList(s)
	if len(first) != len(second) {
		t.Fatalf("EnsurePermissionDefaults not idempotent: %v vs %v", first, second)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("deny order changed on second call: %v vs %v", first, second)
		}
	}
}
