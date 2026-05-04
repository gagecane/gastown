package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/wisp"
)

// TestAreScheduledFailClosed verifies that areScheduled fails closed when
// running outside a town root — all requested IDs should be treated as scheduled.
// This prevents false stranded detection and duplicate scheduling on transient errors.
func TestAreScheduledFailClosed(t *testing.T) {
	// Run areScheduled from a temp dir that is NOT a town root.
	// workspace.FindFromCwd will fail, triggering the fail-closed path.
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir to temp dir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	requestedIDs := []string{"bead-1", "bead-2", "bead-3"}
	result := areScheduled(requestedIDs)

	// All IDs should appear as scheduled (fail closed)
	for _, id := range requestedIDs {
		if !result[id] {
			t.Errorf("areScheduled fail-closed: expected %q to be marked as scheduled, but it was not", id)
		}
	}
}

// TestAreScheduledEmptyInput verifies areScheduled returns empty map for no input.
func TestAreScheduledEmptyInput(t *testing.T) {
	result := areScheduled(nil)
	if len(result) != 0 {
		t.Errorf("areScheduled(nil) should return empty map, got %d entries", len(result))
	}
	result = areScheduled([]string{})
	if len(result) != 0 {
		t.Errorf("areScheduled([]) should return empty map, got %d entries", len(result))
	}
}

// TestResolveFormula verifies formula resolution precedence:
// explicit flag > wisp layer > bead layer > system default > settings file > hardcoded fallback.
func TestResolveFormula(t *testing.T) {
	t.Parallel()

	t.Run("explicit flag wins", func(t *testing.T) {
		t.Parallel()
		got := resolveFormula("mol-evolve", false, "/tmp/nonexistent", "myrig")
		if got != "mol-evolve" {
			t.Errorf("got %q, want %q", got, "mol-evolve")
		}
	})

	t.Run("hookRawBead returns empty", func(t *testing.T) {
		t.Parallel()
		got := resolveFormula("mol-evolve", true, "/tmp/nonexistent", "myrig")
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("system default mol-polecat-work", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		rigName := "testrig"
		_ = os.MkdirAll(filepath.Join(tmpDir, rigName), 0o755)
		got := resolveFormula("", false, tmpDir, rigName)
		if got != "mol-polecat-work" {
			t.Errorf("got %q, want %q", got, "mol-polecat-work")
		}
	})

	t.Run("wisp layer overrides system default", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		rigName := "testrig"
		_ = os.MkdirAll(filepath.Join(tmpDir, rigName), 0o755)

		wispCfg := wisp.NewConfig(tmpDir, rigName)
		if err := wispCfg.Set("default_formula", "mol-evolve"); err != nil {
			t.Fatalf("wisp set: %v", err)
		}

		got := resolveFormula("", false, tmpDir, rigName)
		if got != "mol-evolve" {
			t.Errorf("got %q, want %q", got, "mol-evolve")
		}
	})

	t.Run("explicit flag overrides wisp layer", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		rigName := "testrig"
		_ = os.MkdirAll(filepath.Join(tmpDir, rigName), 0o755)

		wispCfg := wisp.NewConfig(tmpDir, rigName)
		if err := wispCfg.Set("default_formula", "mol-evolve"); err != nil {
			t.Fatalf("wisp set: %v", err)
		}

		got := resolveFormula("mol-custom", false, tmpDir, rigName)
		if got != "mol-custom" {
			t.Errorf("got %q, want %q", got, "mol-custom")
		}
	})

	t.Run("empty rigName falls back to hardcoded default", func(t *testing.T) {
		t.Parallel()
		got := resolveFormula("", false, "/tmp/nonexistent", "")
		if got != "mol-polecat-work" {
			t.Errorf("got %q, want %q", got, "mol-polecat-work")
		}
	})
}

// TestCheckSchedulePrefixParity verifies the enqueue-time cross-rig-prefix
// guard mirrors the dispatcher's unconditional AcceptsPrefix check. This
// is the parity fix for gu-5ooj: without this, `gt sling <bead> <rig>
// --force` for a mismatched prefix created a sling-context that the
// dispatcher would refuse on every heartbeat until the circuit breaker
// silently closed it.
func TestCheckSchedulePrefixParity(t *testing.T) {
	tmpDir := t.TempDir()

	// Set up a rig named "gastown" with prefix "gt" via mayor/rigs.json.
	// rigBeadsPrefix prefers mayor/rigs.json over the rig's config.json.
	// Note: the prefix in rigs.json is stored WITHOUT the trailing hyphen
	// (matching BeadIDPrefix which returns substring-before-first-hyphen).
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	rigsJSON := `{"version":1,"rigs":{"gastown":{"beads":{"prefix":"gt"}},"beads":{"beads":{"prefix":"bd"}}}}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatalf("write rigs.json: %v", err)
	}

	tests := []struct {
		name        string
		rigName     string
		beadID      string
		wantErr     bool
		errContains string
	}{
		{
			name:    "matching prefix: gt bead to gastown rig",
			rigName: "gastown",
			beadID:  "gt-abc123",
			wantErr: false,
		},
		{
			name:    "matching prefix: bd bead to beads rig",
			rigName: "beads",
			beadID:  "bd-xyz",
			wantErr: false,
		},
		{
			name:        "cross-rig: bd bead to gastown rig (dispatcher would refuse)",
			rigName:     "gastown",
			beadID:      "bd-xyz",
			wantErr:     true,
			errContains: "cross-rig prefix",
		},
		{
			name:        "cross-rig: gt bead to beads rig (dispatcher would refuse)",
			rigName:     "beads",
			beadID:      "gt-abc123",
			wantErr:     true,
			errContains: "cross-rig prefix",
		},
		{
			name:        "town-root: hq bead to gastown rig (dispatcher would refuse)",
			rigName:     "gastown",
			beadID:      "hq-foo",
			wantErr:     true,
			errContains: "cross-rig prefix",
		},
		{
			name:    "unknown rig prefix: fails open (matches dispatcher AcceptsPrefix)",
			rigName: "unregistered",
			beadID:  "anything-foo",
			wantErr: false,
		},
		{
			name:    "empty bead prefix: fails open (BeadIDPrefix returns \"\" matches empty)",
			rigName: "unregistered",
			beadID:  "nohyphen",
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSchedulePrefixParity(tmpDir, tc.rigName, tc.beadID)
			gotErr := err != nil
			if gotErr != tc.wantErr {
				t.Errorf("checkSchedulePrefixParity(%q, %q) error = %v, wantErr %v",
					tc.rigName, tc.beadID, err, tc.wantErr)
				return
			}
			if !tc.wantErr {
				return
			}
			msg := err.Error()
			if tc.errContains != "" && !strings.Contains(msg, tc.errContains) {
				t.Errorf("error should contain %q, got: %v", tc.errContains, err)
			}
			// Error must mention --force cannot override and bd create guidance —
			// these are the operator affordances that justify the guard.
			if !strings.Contains(msg, "--force") {
				t.Errorf("error should mention --force cannot override, got: %v", err)
			}
			if !strings.Contains(msg, "bd create") {
				t.Errorf("error should mention bd create, got: %v", err)
			}
			if !strings.Contains(msg, "dispatcher") {
				t.Errorf("error should mention dispatcher invariant, got: %v", err)
			}
		})
	}
}

// TestCheckSchedulePrefixParity_EmptyRigPrefixFailsOpen verifies that when
// a rig has no registered prefix (missing from rigs.json AND from its own
// config.json), the guard fails OPEN — mirroring capacity.AcceptsPrefix.
// This keeps enqueue-time and dispatch-time behavior identical in the
// "unknown rig config" edge case so the enqueue side never refuses
// something the dispatcher would accept.
func TestCheckSchedulePrefixParity_EmptyRigPrefixFailsOpen(t *testing.T) {
	tmpDir := t.TempDir()
	// Do NOT create mayor/rigs.json or rig config.json — rigBeadsPrefix returns "".
	if err := checkSchedulePrefixParity(tmpDir, "norig", "gt-abc"); err != nil {
		t.Errorf("empty rig prefix should fail open, got error: %v", err)
	}
}
