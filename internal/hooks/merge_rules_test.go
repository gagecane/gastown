package hooks

import (
	"reflect"
	"testing"
)

// h is a shorthand for creating a Hook.
func h(cmd string) Hook { return Hook{Type: "command", Command: cmd} }

// he is a shorthand for creating a HookEntry.
func he(matcher string, hooks ...Hook) HookEntry { return HookEntry{Matcher: matcher, Hooks: hooks} }

// --- Rule 1: Same type + same matcher → override wins ---

func TestMergeRules_R1_SameTypeSameMatcherOverrideReplaces(t *testing.T) {
	base := &HooksConfig{
		Stop: []HookEntry{he("", h("base-stop"))},
	}
	overrides := map[string]*HooksConfig{
		"polecats": {Stop: []HookEntry{he("", h("polecat-stop"))}},
	}
	got := MergeHooks(base, overrides, "polecats")
	requireEntries(t, got.Stop, []HookEntry{he("", h("polecat-stop"))})
}

func TestMergeRules_R1_MultipleSameMatchersAllReplaced(t *testing.T) {
	base := &HooksConfig{
		PreToolUse: []HookEntry{
			he("Bash", h("base-bash")),
			he("Edit", h("base-edit")),
		},
	}
	overrides := map[string]*HooksConfig{
		"deacon": {PreToolUse: []HookEntry{
			he("Bash", h("deacon-bash")),
			he("Edit", h("deacon-edit")),
		}},
	}
	got := MergeHooks(base, overrides, "deacon")
	requireEntries(t, got.PreToolUse, []HookEntry{
		he("Bash", h("deacon-bash")),
		he("Edit", h("deacon-edit")),
	})
}

// --- Rule 2: Same type + different matcher → additive ---

func TestMergeRules_R2_DifferentMatchersBothKept(t *testing.T) {
	base := &HooksConfig{
		PreToolUse: []HookEntry{he("Bash", h("base-bash"))},
	}
	overrides := map[string]*HooksConfig{
		"witness": {PreToolUse: []HookEntry{he("Write", h("witness-write"))}},
	}
	got := MergeHooks(base, overrides, "witness")
	if len(got.PreToolUse) != 2 {
		t.Fatalf("expected 2 PreToolUse entries, got %d", len(got.PreToolUse))
	}
	requireHasEntry(t, got.PreToolUse, "Bash", "base-bash")
	requireHasEntry(t, got.PreToolUse, "Write", "witness-write")
}

func TestMergeRules_R2_MixedSameAndDifferentMatchers(t *testing.T) {
	base := &HooksConfig{
		PreToolUse: []HookEntry{
			he("Bash", h("base-bash")),
			he("Edit", h("base-edit")),
		},
	}
	overrides := map[string]*HooksConfig{
		"deacon": {PreToolUse: []HookEntry{
			he("Bash", h("deacon-bash")),  // R1: replaces
			he("Write", h("deacon-write")), // R2: adds
		}},
	}
	got := MergeHooks(base, overrides, "deacon")
	if len(got.PreToolUse) != 3 {
		t.Fatalf("expected 3 PreToolUse entries, got %d", len(got.PreToolUse))
	}
	requireHasEntry(t, got.PreToolUse, "Bash", "deacon-bash")   // replaced
	requireHasEntry(t, got.PreToolUse, "Edit", "base-edit")     // preserved
	requireHasEntry(t, got.PreToolUse, "Write", "deacon-write") // added
}

// --- Rule 3: Empty hooks list → explicit disable ---

func TestMergeRules_R3_EmptyListRemovesTypeFromBase(t *testing.T) {
	base := &HooksConfig{
		Stop:       []HookEntry{he("", h("base-stop"))},
		PreCompact: []HookEntry{he("", h("base-precompact"))},
	}
	overrides := map[string]*HooksConfig{
		"mayor": {Stop: []HookEntry{he("", /* empty hooks */)}},
	}
	got := MergeHooks(base, overrides, "mayor")
	if len(got.Stop) != 0 {
		t.Errorf("expected Stop removed (explicit disable), got %d entries", len(got.Stop))
	}
	requireEntries(t, got.PreCompact, []HookEntry{he("", h("base-precompact"))})
}

func TestMergeRules_R3_NilVsEmptySliceDistinction(t *testing.T) {
	// nil override entries = "no opinion, use base" (handled by mergeEntries short-circuit)
	// []HookEntry{he("", /* empty */)} = "explicit disable"
	base := &HooksConfig{
		Stop: []HookEntry{he("", h("base-stop"))},
	}

	// nil override for Stop → base preserved (mergeEntries returns base when override is empty)
	overridesNil := map[string]*HooksConfig{
		"mayor": {Stop: nil},
	}
	gotNil := MergeHooks(base, overridesNil, "mayor")
	if len(gotNil.Stop) != 1 {
		t.Errorf("nil override should preserve base Stop, got %d entries", len(gotNil.Stop))
	}

	// empty slice override for Stop matcher → explicit disable
	overridesEmpty := map[string]*HooksConfig{
		"mayor": {Stop: []HookEntry{he("" /* empty hooks */)}},
	}
	gotEmpty := MergeHooks(base, overridesEmpty, "mayor")
	if len(gotEmpty.Stop) != 0 {
		t.Errorf("empty hooks override should remove Stop, got %d entries", len(gotEmpty.Stop))
	}
}

// --- Rule 4: Type in base, not in override → preserved ---

func TestMergeRules_R4_BaseOnlyPreserved(t *testing.T) {
	base := &HooksConfig{
		SessionStart: []HookEntry{he("", h("base-start"))},
		Stop:         []HookEntry{he("", h("base-stop"))},
	}
	overrides := map[string]*HooksConfig{
		"mayor": {SessionStart: []HookEntry{he("", h("mayor-start"))}},
		// no Stop override
	}
	got := MergeHooks(base, overrides, "mayor")
	requireEntries(t, got.SessionStart, []HookEntry{he("", h("mayor-start"))}) // R1
	requireEntries(t, got.Stop, []HookEntry{he("", h("base-stop"))})           // R4
}

// --- Edge cases ---

func TestMergeRules_Edge_RoleNotInOverrides(t *testing.T) {
	base := &HooksConfig{
		Stop: []HookEntry{he("", h("base-stop"))},
	}
	overrides := map[string]*HooksConfig{
		"polecats": {Stop: []HookEntry{he("", h("polecat-stop"))}},
	}
	got := MergeHooks(base, overrides, "unknown-role")
	requireEntries(t, got.Stop, []HookEntry{he("", h("base-stop"))})
}

func TestMergeRules_Edge_EmptyBaseEmptyOverrides(t *testing.T) {
	got := MergeHooks(&HooksConfig{}, map[string]*HooksConfig{}, "any")
	if !reflect.DeepEqual(got, &HooksConfig{}) {
		t.Errorf("expected empty config, got %+v", got)
	}
}

func TestMergeRules_Edge_EmptyBaseWithOverrides(t *testing.T) {
	overrides := map[string]*HooksConfig{
		"polecats": {Stop: []HookEntry{he("", h("new"))}},
	}
	got := MergeHooks(&HooksConfig{}, overrides, "polecats")
	requireEntries(t, got.Stop, []HookEntry{he("", h("new"))})
}

func TestMergeRules_Edge_OrderingStableForSameType(t *testing.T) {
	// Base-order preserved first, then override-only appended
	base := &HooksConfig{
		PreToolUse: []HookEntry{
			he("Bash", h("base-bash")),
			he("Edit", h("base-edit")),
		},
	}
	overrides := map[string]*HooksConfig{
		"deacon": {PreToolUse: []HookEntry{
			he("Write", h("deacon-write")), // new
			he("Bash", h("deacon-bash")),   // replaces
		}},
	}
	got := MergeHooks(base, overrides, "deacon")

	// Expected: base order preserved (Bash replaced in-place, Edit kept), then new Write appended
	if len(got.PreToolUse) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got.PreToolUse))
	}
	if got.PreToolUse[0].Matcher != "Bash" || got.PreToolUse[0].Hooks[0].Command != "deacon-bash" {
		t.Errorf("entry[0]: expected Bash/deacon-bash, got %s/%s", got.PreToolUse[0].Matcher, got.PreToolUse[0].Hooks[0].Command)
	}
	if got.PreToolUse[1].Matcher != "Edit" || got.PreToolUse[1].Hooks[0].Command != "base-edit" {
		t.Errorf("entry[1]: expected Edit/base-edit, got %s/%s", got.PreToolUse[1].Matcher, got.PreToolUse[1].Hooks[0].Command)
	}
	if got.PreToolUse[2].Matcher != "Write" || got.PreToolUse[2].Hooks[0].Command != "deacon-write" {
		t.Errorf("entry[2]: expected Write/deacon-write, got %s/%s", got.PreToolUse[2].Matcher, got.PreToolUse[2].Hooks[0].Command)
	}
}

// --- Property tests ---

func TestMergeRules_Property_Idempotent(t *testing.T) {
	// Merging twice with the same overrides should produce the same result
	base := &HooksConfig{
		SessionStart: []HookEntry{he("", h("base-start"))},
		PreToolUse:   []HookEntry{he("Bash", h("base-bash"))},
	}
	overrides := map[string]*HooksConfig{
		"crew": {
			SessionStart: []HookEntry{he("", h("crew-start"))},
			PreToolUse:   []HookEntry{he("Write", h("crew-write"))},
		},
	}
	once := MergeHooks(base, overrides, "crew")
	twice := MergeHooks(once, overrides, "crew")
	if !HooksEqual(once, twice) {
		t.Errorf("merge is not idempotent:\nonce:  %+v\ntwice: %+v", once, twice)
	}
}

func TestMergeRules_Property_EmptyOverrideIsIdentity(t *testing.T) {
	base := &HooksConfig{
		SessionStart: []HookEntry{he("", h("base-start"))},
		Stop:         []HookEntry{he("", h("base-stop"))},
		PreToolUse: []HookEntry{
			he("Bash", h("base-bash")),
			he("Edit", h("base-edit")),
		},
	}
	result := MergeHooks(base, map[string]*HooksConfig{}, "any-role")
	if !HooksEqual(base, result) {
		t.Errorf("empty override should return base unchanged")
	}
}

func TestMergeRules_Property_NoGhostHooks(t *testing.T) {
	// Every hook in the output must come from either base or override
	base := &HooksConfig{
		SessionStart: []HookEntry{he("", h("base-start"))},
		PreToolUse:   []HookEntry{he("Bash", h("base-bash"))},
	}
	override := &HooksConfig{
		Stop:       []HookEntry{he("", h("crew-stop"))},
		PreToolUse: []HookEntry{he("Write", h("crew-write"))},
	}
	overrides := map[string]*HooksConfig{"crew": override}
	result := MergeHooks(base, overrides, "crew")

	allSourceCmds := make(map[string]bool)
	for _, entries := range [][]HookEntry{base.SessionStart, base.PreToolUse, base.Stop,
		override.SessionStart, override.PreToolUse, override.Stop} {
		for _, e := range entries {
			for _, hook := range e.Hooks {
				allSourceCmds[hook.Command] = true
			}
		}
	}

	for _, entries := range [][]HookEntry{result.SessionStart, result.PreToolUse, result.Stop,
		result.PreCompact, result.PostToolUse, result.UserPromptSubmit} {
		for _, e := range entries {
			for _, hook := range e.Hooks {
				if !allSourceCmds[hook.Command] {
					t.Errorf("ghost hook found in output: %q", hook.Command)
				}
			}
		}
	}
}

// --- Real-world scenario tests using DefaultBase/DefaultOverrides ---

func TestMergeRules_Scenario_PolecatGetsStopFromOverride(t *testing.T) {
	base := DefaultBase()
	overrides := DefaultOverrides()
	result := MergeHooks(base, overrides, "polecats")

	// Polecats should have the polecat-specific Stop hook (overrides base Stop)
	if len(result.Stop) == 0 {
		t.Fatal("polecats should have Stop hooks")
	}
	// Base SessionStart should be preserved
	if len(result.SessionStart) == 0 {
		t.Fatal("polecats should inherit base SessionStart")
	}
}

func TestMergeRules_Scenario_CrewGetsPreCompactOverride(t *testing.T) {
	base := DefaultBase()
	overrides := DefaultOverrides()
	result := MergeHooks(base, overrides, "crew")

	// Crew should have PreCompact from override
	if len(result.PreCompact) == 0 {
		t.Fatal("crew should have PreCompact hooks")
	}
	// Base Stop should be preserved (crew has no Stop override)
	if len(result.Stop) == 0 {
		t.Fatal("crew should inherit base Stop")
	}
}

func TestMergeRules_Scenario_UnknownRoleGetsOnlyBase(t *testing.T) {
	base := DefaultBase()
	overrides := DefaultOverrides()
	result := MergeHooks(base, overrides, "nonexistent-role")

	// Should get exactly base hooks, nothing more
	if !HooksEqual(base, result) {
		t.Error("unknown role should get only base hooks")
	}
}

// --- Helpers ---

func requireEntries(t *testing.T, got, want []HookEntry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("expected %d entries, got %d\n  got:  %+v\n  want: %+v", len(want), len(got), got, want)
	}
	for i := range want {
		if got[i].Matcher != want[i].Matcher {
			t.Errorf("entry[%d] matcher: got %q, want %q", i, got[i].Matcher, want[i].Matcher)
		}
		if len(got[i].Hooks) != len(want[i].Hooks) {
			t.Errorf("entry[%d] hooks count: got %d, want %d", i, len(got[i].Hooks), len(want[i].Hooks))
			continue
		}
		for j := range want[i].Hooks {
			if got[i].Hooks[j].Command != want[i].Hooks[j].Command {
				t.Errorf("entry[%d].hooks[%d] command: got %q, want %q",
					i, j, got[i].Hooks[j].Command, want[i].Hooks[j].Command)
			}
		}
	}
}

func requireHasEntry(t *testing.T, entries []HookEntry, matcher, command string) {
	t.Helper()
	for _, e := range entries {
		if e.Matcher == matcher && len(e.Hooks) > 0 && e.Hooks[0].Command == command {
			return
		}
	}
	t.Errorf("missing entry with matcher=%q command=%q in %+v", matcher, command, entries)
}
