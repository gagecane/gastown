package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestUserAgentExtends_ChainResolvesRegardlessOfMapOrder is the direct
// regression for gu-417s. A user agent chain like:
//
//	kiro-opus-46 → kiro-opus → kiro (builtin)
//
// was previously resolved during a `for name := range userRegistry.Agents`
// loop, which iterates in randomized order in Go. If kiro-opus-46 happened
// to iterate before kiro-opus, its extends lookup missed (kiro-opus not
// yet in registry) and fell through to the hyphen-prefix warning path —
// producing a non-deterministic warning on load.
//
// This test loads an agents.json with a 3-deep user→user extends chain
// and verifies every entry inherits the chain correctly. The fix (topo
// sort by extends dependency before merging) makes this deterministic
// regardless of map iteration order.
func TestUserAgentExtends_ChainResolvesRegardlessOfMapOrder(t *testing.T) {
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	// A 3-level chain: z-child → m-mid → a-root → kiro (builtin).
	// Names chosen so alphabetical iteration order (a-root, m-mid,
	// z-child) happens to agree with dependency order — but Go's map
	// range order is randomized per-run, so some iterations would still
	// see z-child before a-root under the old code. With topo sort
	// applied, either order produces the same fully-merged chain.
	data := []byte(`{
		"version": 1,
		"agents": {
			"z-child": {
				"extends": "m-mid",
				"args": ["--model", "z-model"]
			},
			"m-mid": {
				"extends": "a-root",
				"args": ["--model", "m-model"]
			},
			"a-root": {
				"extends": "kiro",
				"args": ["--model", "a-model"]
			}
		}
	}`)

	dir := t.TempDir()
	settings := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(settings, data, 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	if err := LoadAgentRegistry(settings); err != nil {
		t.Fatalf("LoadAgentRegistry: %v", err)
	}

	kiroBuiltin := builtinPresets[AgentKiro]
	cases := []struct {
		name      string
		wantModel string
	}{
		{"a-root", "a-model"},
		{"m-mid", "m-model"},
		{"z-child", "z-model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := GetAgentPresetByName(tc.name)
			if info == nil {
				t.Fatalf("missing agent %q after load", tc.name)
			}
			// User field: args should carry the per-entry model flag.
			if len(info.Args) != 2 || info.Args[0] != "--model" || info.Args[1] != tc.wantModel {
				t.Errorf("Args = %v, want [--model %s]", info.Args, tc.wantModel)
			}
			// Inherited through the chain from builtin kiro: Command
			// should match the builtin, which none of the user entries
			// override.
			if info.Command != kiroBuiltin.Command {
				t.Errorf("Command = %q, want inherited %q", info.Command, kiroBuiltin.Command)
			}
			if info.HooksProvider != kiroBuiltin.HooksProvider {
				t.Errorf("HooksProvider = %q, want inherited %q", info.HooksProvider, kiroBuiltin.HooksProvider)
			}
			if got := info.Env["GIT_TERMINAL_PROMPT"]; got != "0" {
				t.Errorf("Env[GIT_TERMINAL_PROMPT] = %q, want %q (inherited via chain)", got, "0")
			}
		})
	}
}

// TestUserAgentExtends_ReverseAlphabeticalChain exercises the fix against
// the scenario where alphabetical order would DISAGREE with dependency
// order. With the old map-iteration code, this fails roughly 50% of runs
// because the Go runtime randomizes map ranges. With topo sort, the emit
// order follows dependencies regardless of name ordering. (gu-417s)
func TestUserAgentExtends_ReverseAlphabeticalChain(t *testing.T) {
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	// z is the root (extends builtin), a extends z. Alphabetical order
	// would process a before z, which would leave a without its parent
	// present under the old code if the lookup fell back to the registry
	// state mid-loop. Topo sort must process z first.
	data := []byte(`{
		"version": 1,
		"agents": {
			"a-leaf": {
				"extends": "z-root",
				"args": ["--model", "leaf"]
			},
			"z-root": {
				"extends": "kiro",
				"args": ["--model", "root"]
			}
		}
	}`)

	dir := t.TempDir()
	settings := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(settings, data, 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	if err := LoadAgentRegistry(settings); err != nil {
		t.Fatalf("LoadAgentRegistry: %v", err)
	}

	aLeaf := GetAgentPresetByName("a-leaf")
	if aLeaf == nil {
		t.Fatal("missing a-leaf")
	}
	zRoot := GetAgentPresetByName("z-root")
	if zRoot == nil {
		t.Fatal("missing z-root")
	}

	kiroBuiltin := builtinPresets[AgentKiro]
	// a-leaf must have inherited Command from kiro through z-root.
	if aLeaf.Command != kiroBuiltin.Command {
		t.Errorf("a-leaf.Command = %q, want inherited %q", aLeaf.Command, kiroBuiltin.Command)
	}
	// Extends field should be preserved as declared, not rewritten.
	if aLeaf.Extends != "z-root" {
		t.Errorf("a-leaf.Extends = %q, want %q", aLeaf.Extends, "z-root")
	}
	if zRoot.Extends != "kiro" {
		t.Errorf("z-root.Extends = %q, want %q", zRoot.Extends, "kiro")
	}
}

// TestUserAgentExtends_CycleDetected verifies that the topo sort rejects
// cyclic user→user extends with a clear error listing the involved
// entries, rather than producing an arbitrary partial ordering or
// infinite-looping. (gu-417s)
func TestUserAgentExtends_CycleDetected(t *testing.T) {
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	// a-loop → b-loop → a-loop. Both user entries, both participating
	// in the cycle. Neither can have indegree zero at start, so Kahn's
	// algorithm leaves both unemitted and we surface a cycle error.
	data := []byte(`{
		"version": 1,
		"agents": {
			"a-loop": {"extends": "b-loop"},
			"b-loop": {"extends": "a-loop"}
		}
	}`)

	dir := t.TempDir()
	settings := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(settings, data, 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	err := LoadAgentRegistry(settings)
	if err == nil {
		t.Fatal("LoadAgentRegistry succeeded on cyclic extends; want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "cycle") {
		t.Errorf("error = %q, want containing %q", msg, "cycle")
	}
	if !strings.Contains(msg, "a-loop") || !strings.Contains(msg, "b-loop") {
		t.Errorf("error = %q, want listing both cycle members (a-loop, b-loop)", msg)
	}
}

// TestUserAgentExtends_SelfLoopDetected checks the degenerate case where
// an entry declares extends equal to its own name. It's a trivial cycle,
// but handling it explicitly means the error message names the single
// offender clearly instead of reporting a generic two-node cycle. (gu-417s)
func TestUserAgentExtends_SelfLoopDetected(t *testing.T) {
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	data := []byte(`{
		"version": 1,
		"agents": {
			"me-me": {"extends": "me-me"}
		}
	}`)

	dir := t.TempDir()
	settings := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(settings, data, 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	err := LoadAgentRegistry(settings)
	if err == nil {
		t.Fatal("LoadAgentRegistry succeeded on self-loop extends; want error")
	}
	if !strings.Contains(err.Error(), "self-loop") {
		t.Errorf("error = %q, want mentioning %q", err.Error(), "self-loop")
	}
	if !strings.Contains(err.Error(), "me-me") {
		t.Errorf("error = %q, want naming offender %q", err.Error(), "me-me")
	}
}

// TestTopoSortUserAgents_Deterministic exercises the helper directly with
// several configurations to pin down both correctness and the
// alphabetical-tie-breaker contract. Determinism matters because the
// emitted order governs the order warnings print on load, and
// non-deterministic warnings are confusing to operators debugging
// agents.json changes. (gu-417s)
func TestTopoSortUserAgents_Deterministic(t *testing.T) {
	cases := []struct {
		description string
		extends     map[string]string
		want        []string
	}{
		{
			description: "no user-to-user edges — alphabetical",
			extends:     map[string]string{"c": "kiro", "a": "kiro", "b": ""},
			want:        []string{"a", "b", "c"},
		},
		{
			description: "single chain — follows dependency not alpha",
			extends:     map[string]string{"z-root": "kiro", "a-leaf": "z-root"},
			want:        []string{"z-root", "a-leaf"},
		},
		{
			description: "fan-out from shared root — alpha among siblings",
			extends: map[string]string{
				"root":   "kiro",
				"beta":   "root",
				"alpha":  "root",
				"gamma":  "root",
			},
			want: []string{"root", "alpha", "beta", "gamma"},
		},
		{
			description: "ExtendsNone doesn't create a dep — a still depends on b",
			extends:     map[string]string{"b": "none", "a": "b"},
			// a extends b (user entry) → b first. ExtendsNone on b just
			// says b has no user-entry parent, not that entries extending
			// b are exempt from the ordering constraint.
			want: []string{"b", "a"},
		},
		{
			description: "unknown extends (typo) doesn't block other entries",
			extends:     map[string]string{"typoed": "kiro-doesnotexist", "fine": "kiro"},
			want:        []string{"fine", "typoed"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.description, func(t *testing.T) {
			got, err := topoSortUserAgents(tc.extends)
			if err != nil {
				t.Fatalf("topoSortUserAgents: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("order = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("order[%d] = %q, want %q (full: got %v, want %v)",
						i, got[i], tc.want[i], got, tc.want)
				}
			}
		})
	}
}

// TestTopoSortUserAgents_RepeatedRuns hammers the sorter 100 times on the
// same input to catch any residual nondeterminism from the helper itself
// (e.g., if an internal loop iterated over a map without explicit
// sorting). Every run must produce byte-identical output. (gu-417s)
func TestTopoSortUserAgents_RepeatedRuns(t *testing.T) {
	input := map[string]string{
		"delta":   "gamma",
		"gamma":   "beta",
		"beta":    "alpha",
		"alpha":   "kiro",
		"orphan":  "",
		"optout":  "none",
		"typoed":  "does-not-exist",
	}

	first, err := topoSortUserAgents(input)
	if err != nil {
		t.Fatalf("first run failed: %v", err)
	}

	for i := 0; i < 100; i++ {
		got, err := topoSortUserAgents(input)
		if err != nil {
			t.Fatalf("run %d failed: %v", i, err)
		}
		if !equalStrings(got, first) {
			t.Fatalf("run %d: order changed: got %v, first %v", i, got, first)
		}
	}

	// Spot-check the dependency ordering is actually respected.
	positions := make(map[string]int, len(first))
	for i, n := range first {
		positions[n] = i
	}
	chain := []string{"alpha", "beta", "gamma", "delta"}
	for i := 0; i+1 < len(chain); i++ {
		if positions[chain[i]] >= positions[chain[i+1]] {
			t.Errorf("chain order violated: %q (pos %d) should precede %q (pos %d)",
				chain[i], positions[chain[i]], chain[i+1], positions[chain[i+1]])
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestUserAgentExtends_LoadedFieldsStable is a belt-and-suspenders run of
// the main regression: load the same agents.json many times (each load
// spawns fresh map iteration) and assert the resulting preset fields are
// identical every run. Without the fix, the prefix-match warning path
// would occasionally rewrite Args/Command when a user→user extends
// missed its parent. (gu-417s)
func TestUserAgentExtends_LoadedFieldsStable(t *testing.T) {
	data := []byte(`{
		"version": 1,
		"agents": {
			"leaf": {
				"extends": "middle",
				"args": ["--leaf"]
			},
			"middle": {
				"extends": "root",
				"args": ["--middle"]
			},
			"root": {
				"extends": "kiro",
				"args": ["--root"]
			}
		}
	}`)

	var baseline string
	for i := 0; i < 20; i++ {
		ResetRegistryForTesting()

		dir := t.TempDir()
		settings := filepath.Join(dir, "agents.json")
		if err := os.WriteFile(settings, data, 0o644); err != nil {
			t.Fatalf("iter %d: write settings: %v", i, err)
		}

		if err := LoadAgentRegistry(settings); err != nil {
			t.Fatalf("iter %d: LoadAgentRegistry: %v", i, err)
		}

		// Snapshot key fields across all three user agents, sorted by
		// name so the comparison is deterministic regardless of map
		// range order at snapshot time.
		names := []string{"leaf", "middle", "root"}
		sort.Strings(names)
		var snap strings.Builder
		for _, n := range names {
			info := GetAgentPresetByName(n)
			if info == nil {
				t.Fatalf("iter %d: missing %q", i, n)
			}
			fmt.Fprintf(&snap, "%s|cmd=%s|args=%v|ext=%s|hp=%s\n",
				n, info.Command, info.Args, info.Extends, info.HooksProvider)
		}

		if i == 0 {
			baseline = snap.String()
			continue
		}
		if snap.String() != baseline {
			t.Fatalf("iter %d snapshot differs:\nbaseline:\n%s\nactual:\n%s",
				i, baseline, snap.String())
		}
	}
	ResetRegistryForTesting()
}
