package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/polecat"
)

func TestCheckHelpFlag(t *testing.T) {
	// Create a test command
	testCmd := &cobra.Command{
		Use:   "test",
		Short: "Test command",
		Long:  "This is a test command for testing checkHelpFlag.",
	}

	tests := []struct {
		name        string
		args        []string
		wantHelped  bool
		description string
	}{
		{
			name:        "--help as first arg",
			args:        []string{"--help"},
			wantHelped:  true,
			description: "should show help when --help is first argument",
		},
		{
			name:        "-h as first arg",
			args:        []string{"-h"},
			wantHelped:  true,
			description: "should show help when -h is first argument",
		},
		{
			name:        "--help with other args after",
			args:        []string{"--help", "something"},
			wantHelped:  true,
			description: "should show help when --help is first, ignoring rest",
		},
		{
			name:        "no args",
			args:        []string{},
			wantHelped:  false,
			description: "should not show help with no args",
		},
		{
			name:        "regular args",
			args:        []string{"abc123", "--json"},
			wantHelped:  false,
			description: "should not show help with regular args",
		},
		{
			name:        "--help NOT first - false positive prevention",
			args:        []string{"-m", "--help"},
			wantHelped:  false,
			description: "should NOT show help when --help is not first (e.g., commit -m '--help')",
		},
		{
			name:        "-h NOT first - false positive prevention",
			args:        []string{"something", "-h"},
			wantHelped:  false,
			description: "should NOT show help when -h is not first",
		},
		{
			name:        "--help after -- separator",
			args:        []string{"--", "--help"},
			wantHelped:  false,
			description: "should NOT show help when --help is after -- (passed to underlying tool)",
		},
		{
			name:        "similar but not help flag",
			args:        []string{"--helper"},
			wantHelped:  false,
			description: "should not match --helper as help flag",
		},
		{
			name:        "help without dashes",
			args:        []string{"help"},
			wantHelped:  false,
			description: "should not match 'help' without dashes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			helped, err := checkHelpFlag(testCmd, tt.args)
			if err != nil {
				t.Errorf("checkHelpFlag() returned error: %v", err)
			}
			if helped != tt.wantHelped {
				t.Errorf("checkHelpFlag(%v) helped = %v, want %v (%s)",
					tt.args, helped, tt.wantHelped, tt.description)
			}
		})
	}
}

func TestCheckHelpFlag_EdgeCases(t *testing.T) {
	testCmd := &cobra.Command{
		Use:   "test",
		Short: "Test command",
	}

	// Test that we correctly handle edge cases that could cause panics or unexpected behavior
	t.Run("nil-like empty slice", func(t *testing.T) {
		var args []string
		helped, err := checkHelpFlag(testCmd, args)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if helped {
			t.Error("should not show help for nil/empty args")
		}
	})

	t.Run("single empty string arg", func(t *testing.T) {
		args := []string{""}
		helped, err := checkHelpFlag(testCmd, args)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if helped {
			t.Error("should not show help for empty string arg")
		}
	})
}

func TestPersistentPreRunLoadsAgentRegistry(t *testing.T) {
	// Regression test: persistentPreRun must load settings/agents.json so that
	// GetProcessNames (used by IsAgentAlive, daemon heartbeat, cleanup) respects
	// user-configured process_names overrides.
	//
	// Without this, NixOS users whose Claude binary is ".claude-unwrapped" get
	// their sessions killed every 3 minutes because the builtin preset only
	// lists ["node", "claude"].
	//
	// NOTE: cannot use t.Parallel() — mutates cwd and global agent registry.
	config.ResetRegistryForTesting()
	t.Cleanup(config.ResetRegistryForTesting)

	// Build a minimal fake town root with mayor/town.json (PrimaryMarker)
	// and settings/agents.json containing a process_names override.
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "settings"), 0755); err != nil {
		t.Fatal(err)
	}

	registry := config.AgentRegistry{
		Version: config.CurrentAgentRegistryVersion,
		Agents: map[string]*config.AgentPresetInfo{
			"claude": {
				Name:         "claude",
				Command:      "claude",
				Args:         []string{"--dangerously-skip-permissions"},
				ProcessNames: []string{"node", "claude", ".claude-unwrapped"},
			},
		},
	}
	data, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "settings", "agents.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// cd into the fake town root so workspace.FindFromCwd() finds it.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	// Run persistentPreRun (the function under test).
	cmd := &cobra.Command{Use: "version"}
	if err := persistentPreRun(cmd, nil); err != nil {
		t.Fatalf("persistentPreRun: %v", err)
	}

	// Verify GetProcessNames returns the override from settings/agents.json.
	got := config.GetProcessNames("claude")
	want := []string{"node", "claude", ".claude-unwrapped"}
	if len(got) != len(want) {
		t.Fatalf("GetProcessNames(claude) = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("GetProcessNames(claude)[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPersistentPreRunMalformedAgentRegistry(t *testing.T) {
	// Verify that malformed settings/agents.json does not block persistentPreRun
	// and that the builtin defaults are preserved (graceful fallback).
	//
	// NOTE: cannot use t.Parallel() — mutates cwd and global agent registry.
	config.ResetRegistryForTesting()
	t.Cleanup(config.ResetRegistryForTesting)

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "settings"), 0755); err != nil {
		t.Fatal(err)
	}
	// Write invalid JSON to settings/agents.json.
	if err := os.WriteFile(filepath.Join(townRoot, "settings", "agents.json"), []byte("{malformed"), 0644); err != nil {
		t.Fatal(err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	// persistentPreRun should succeed despite malformed agents.json.
	cmd := &cobra.Command{Use: "version"}
	if err := persistentPreRun(cmd, nil); err != nil {
		t.Fatalf("persistentPreRun should not fail on malformed agents.json: %v", err)
	}

	// Builtin defaults should still be in effect.
	got := config.GetProcessNames("claude")
	if len(got) < 2 || got[0] != "node" || got[1] != "claude" {
		t.Fatalf("GetProcessNames(claude) after malformed registry = %v, want builtin [node claude ...]", got)
	}
}

// TestTouchAgentHeartbeat_RoleAllowlist pins the role allowlist for the
// per-command heartbeat producer (cv-p3fem Phase 1: closes gu-rh0g). Without
// witness/refinery in this list, their heartbeats never refresh and the
// daemon reaper falls back to the legacy 2h updated_at proxy — exactly the
// failure mode that lost a refinery for 28h. The polecat/crew/dog/deacon
// entries existed pre-cv-p3fem; the test pins them too so a future tidy-up
// doesn't accidentally narrow coverage.
//
// We exercise touchAgentHeartbeat (not the persistentPreRun layer) because
// the rest of persistentPreRun's surface is already covered above and the
// allowlist behaviour we care about lives entirely in touchAgentHeartbeat.
func TestTouchAgentHeartbeat_RoleAllowlist(t *testing.T) {
	cases := []struct {
		role      string
		wantWrite bool
	}{
		// Roles that MUST produce heartbeats.
		{"gastown_upstream/polecats/dust", true},
		{"gastown_upstream/crew/canewiw", true},
		{"gastown_upstream/dog", true}, // stuck-agent-dog and friends
		{"gastown_upstream/deacon", true},
		{"gastown_upstream/witness", true},
		{"gastown_upstream/refinery", true},
		// Roles that intentionally skip — overseer is human, mayor is town-level
		// coordination not subject to per-rig liveness. Empty/unknown roles are
		// also skipped to avoid stray writes from `gt` invoked outside an agent
		// context.
		{"gastown_upstream/overseer", false},
		{"mayor", false},
		{"", false},
		{"random/unknown", false},
	}

	// Each subtest needs an isolated townRoot to avoid state leaking between
	// runs. We use t.Setenv so cleanup is automatic.
	for _, tc := range cases {
		tc := tc
		t.Run("role="+tc.role, func(t *testing.T) {
			townRoot := t.TempDir()
			// Plant a workspace marker so detectTownRootFromCwd succeeds.
			if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
				t.Fatal(err)
			}

			origDir, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(townRoot); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chdir(origDir) })

			t.Setenv("GT_SESSION", "gt-test-allowlist")
			t.Setenv("GT_ROLE", tc.role)

			touchAgentHeartbeat()

			hb := polecat.ReadSessionHeartbeat(townRoot, "gt-test-allowlist")
			if tc.wantWrite && hb == nil {
				t.Errorf("role %q: expected heartbeat write, got none", tc.role)
			}
			if !tc.wantWrite && hb != nil {
				t.Errorf("role %q: expected NO heartbeat write, got %+v", tc.role, hb)
			}
		})
	}
}

// TestTouchAgentHeartbeat_NoSession verifies that without GT_SESSION the
// function silently no-ops, regardless of GT_ROLE. This protects against
// stray heartbeat files when developers run `gt` interactively outside any
// agent session.
func TestTouchAgentHeartbeat_NoSession(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	t.Setenv("GT_SESSION", "")
	t.Setenv("GT_ROLE", "gastown_upstream/witness")

	touchAgentHeartbeat()

	// Heartbeats dir might exist (other tests in same package), but no file
	// for an empty session name should be there.
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		t.Errorf("touchAgentHeartbeat with empty GT_SESSION wrote %q", e.Name())
	}
}

// TestCheckStaleBinaryWarning_SuppressedOnNonTTY pins the regression fix for
// gu-hmsv: when stderr is captured (not a TTY), the stale-binary banner must
// NOT be written. Otherwise it leaks into convoy failure logs, refinery gate
// output, and other capture-stderr code paths where it masquerades as the
// actual error message.
//
// Test plan: redirect os.Stderr to a pipe (which is non-TTY), call
// checkStaleBinaryWarning, and assert nothing was written. We don't need to
// stage a stale binary — the function must short-circuit on the TTY check
// before it runs `version.CheckStaleBinary`.
func TestCheckStaleBinaryWarning_SuppressedOnNonTTY(t *testing.T) {
	// Reset the once-per-session sentinel so this test is hermetic regardless
	// of test ordering. The function early-returns if either staleBinaryWarned
	// is set or GT_STALE_WARNED=1; clear both.
	prevWarned := staleBinaryWarned
	staleBinaryWarned = false
	t.Cleanup(func() { staleBinaryWarned = prevWarned })
	t.Setenv("GT_STALE_WARNED", "")
	t.Setenv("GT_STALE_WARN", "") // ensure debug override is off

	// Swap os.Stderr for a pipe (pipes are NOT terminals).
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	// Drain the read end in a goroutine so writes never block (defensive: the
	// function under test should write nothing, but if a regression
	// reintroduces the leak we still want the test to fail cleanly rather
	// than deadlock).
	captured := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(r)
		captured <- data
	}()

	checkStaleBinaryWarning()

	// Close the writer so the reader returns.
	_ = w.Close()
	got := <-captured

	if len(got) != 0 {
		t.Errorf("checkStaleBinaryWarning wrote %q to stderr when stderr is non-TTY; want no output (gu-hmsv)", string(got))
	}
}

// TestCheckStaleBinaryWarning_DebugOverride verifies that GT_STALE_WARN=1 lets
// the staleness check proceed even when stderr is non-TTY. We can't easily
// stage a stale binary in a unit test, so we assert that the function reaches
// the version.CheckStaleBinary path (not the TTY short-circuit) by observing
// that it does NOT early-return — i.e. setting staleBinaryWarned=true would
// make a second call a no-op even with the override. That's a weak signal,
// so instead we just assert no panic and rely on the fact that
// CheckStaleBinary returns Error!=nil from a non-git temp dir, taking the
// silent-skip branch. The regression we care about — stderr leak on non-TTY
// — is pinned by the test above.
func TestCheckStaleBinaryWarning_DebugOverride(t *testing.T) {
	prevWarned := staleBinaryWarned
	staleBinaryWarned = false
	t.Cleanup(func() { staleBinaryWarned = prevWarned })
	t.Setenv("GT_STALE_WARNED", "")
	t.Setenv("GT_STALE_WARN", "1")

	// Redirect stderr so any output during the test doesn't pollute test logs.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })
	go func() { _, _ = io.ReadAll(r) }()

	// Just verify it doesn't panic. With GT_STALE_WARN=1 the function takes
	// the version.CheckStaleBinary path; from a non-git cwd that returns
	// Error!=nil and the function silently skips.
	checkStaleBinaryWarning()
	_ = w.Close()
}
