package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/testutil"
	"github.com/steveyegge/gastown/internal/wisp"
)

// setupTestRigForConfig creates a minimal Gas Town workspace for rig config testing.
// Returns townRoot and rigName.
func setupTestRigForConfig(t *testing.T) (string, string) {
	t.Helper()

	townRoot := t.TempDir()
	rigName := "testrig"

	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}

	townConfig := &config.TownConfig{
		Type:      "town",
		Version:   config.CurrentTownVersion,
		Name:      "test-town",
		CreatedAt: time.Now().Truncate(time.Second),
	}
	if err := config.SaveTownConfig(filepath.Join(mayorDir, "town.json"), townConfig); err != nil {
		t.Fatalf("save town.json: %v", err)
	}

	rigsConfig := &config.RigsConfig{
		Version: 1,
		Rigs: map[string]config.RigEntry{
			rigName: {
				GitURL:  "git@github.com:test/testrig.git",
				AddedAt: time.Now().Truncate(time.Second),
			},
		},
	}
	if err := config.SaveRigsConfig(filepath.Join(mayorDir, "rigs.json"), rigsConfig); err != nil {
		t.Fatalf("save rigs.json: %v", err)
	}

	rigPath := filepath.Join(townRoot, rigName)
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}

	rigConfig := config.NewRigConfig(rigName, "git@github.com:test/testrig.git")
	if err := config.SaveRigConfig(filepath.Join(rigPath, "config.json"), rigConfig); err != nil {
		t.Fatalf("save rig config: %v", err)
	}

	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir to town root: %v", err)
	}
	t.Cleanup(func() { os.Chdir(oldCwd) })

	return townRoot, rigName
}

func TestRigConfigSet_WispLayerWarning(t *testing.T) {
	t.Run("warns about ephemeral when writing to wisp layer", func(t *testing.T) {
		townRoot, rigName := setupTestRigForConfig(t)

		rigConfigSetGlobal = false
		rigConfigSetBlock = false

		stderrOut := captureStderr(t, func() {
			err := runRigConfigSet(rigConfigSetCmd, []string{rigName, "max_polecats", "5"})
			if err != nil {
				t.Fatalf("runRigConfigSet: %v", err)
			}
		})

		if !strings.Contains(stderrOut, "ephemeral") {
			t.Errorf("expected ephemeral warning on stderr, got: %q", stderrOut)
		}
		if !strings.Contains(stderrOut, "--global") {
			t.Errorf("expected --global hint on stderr, got: %q", stderrOut)
		}

		// Verify value was actually stored in wisp layer
		wispCfg := wisp.NewConfig(townRoot, rigName)
		val := wispCfg.Get("max_polecats")
		if val == nil {
			t.Error("expected max_polecats to be set in wisp layer")
		}
	})

	t.Run("warns for string values in wisp layer", func(t *testing.T) {
		_, rigName := setupTestRigForConfig(t)

		rigConfigSetGlobal = false
		rigConfigSetBlock = false

		stderrOut := captureStderr(t, func() {
			err := runRigConfigSet(rigConfigSetCmd, []string{rigName, "default_formula", "mol-custom"})
			if err != nil {
				t.Fatalf("runRigConfigSet: %v", err)
			}
		})

		if !strings.Contains(stderrOut, "ephemeral") {
			t.Errorf("expected ephemeral warning on stderr for string value, got: %q", stderrOut)
		}
	})

	t.Run("warns for boolean values in wisp layer", func(t *testing.T) {
		_, rigName := setupTestRigForConfig(t)

		rigConfigSetGlobal = false
		rigConfigSetBlock = false

		stderrOut := captureStderr(t, func() {
			err := runRigConfigSet(rigConfigSetCmd, []string{rigName, "auto_restart", "false"})
			if err != nil {
				t.Fatalf("runRigConfigSet: %v", err)
			}
		})

		if !strings.Contains(stderrOut, "ephemeral") {
			t.Errorf("expected ephemeral warning on stderr for boolean value, got: %q", stderrOut)
		}
	})

	t.Run("no ephemeral warning when using --block flag", func(t *testing.T) {
		_, rigName := setupTestRigForConfig(t)

		rigConfigSetGlobal = false
		rigConfigSetBlock = true
		t.Cleanup(func() { rigConfigSetBlock = false })

		stderrOut := captureStderr(t, func() {
			err := runRigConfigSet(rigConfigSetCmd, []string{rigName, "auto_restart"})
			if err != nil {
				t.Fatalf("runRigConfigSet with --block: %v", err)
			}
		})

		// --block also writes to wisp but has different UX semantics; no ephemeral warning expected
		if strings.Contains(stderrOut, "ephemeral") {
			t.Errorf("unexpected ephemeral warning for --block operation, got: %q", stderrOut)
		}
	})
}

// Regression test for gu-av1j3:
// setBeadLabel (the --global path) must auto-create the rig identity bead when
// it is missing instead of failing with "rig identity bead ... not found".
func TestSetBeadLabel_AutoCreatesMissingIdentityBead(t *testing.T) {
	requireBdCLI(t)
	testutil.RequireDoltContainer(t)

	townRoot := t.TempDir()
	rigName := "testrig"
	rigPath := filepath.Join(townRoot, rigName)
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatalf("mkdir rig path: %v", err)
	}

	prefix := randomTestPrefix(t)

	// Initialize the rig's beads DB but do NOT create the identity bead —
	// this reproduces a live rig whose identity bead went missing.
	b := setupRigBeadsDB(t, rigPath, prefix)
	rigBeadID := beads.RigBeadIDWithPrefix(prefix, rigName)
	if _, err := b.Show(rigBeadID); err == nil {
		t.Fatalf("precondition failed: rig identity bead %s should not exist yet", rigBeadID)
	}

	// Route the non-isolated beads handle inside setBeadLabel at the test
	// container, matching the DB that setupRigBeadsDB initialized.
	t.Setenv("GT_DOLT_PORT", testutil.DoltContainerPort())

	r := &rig.Rig{
		Name:   rigName,
		Path:   rigPath,
		GitURL: "git@github.com:example/repo.git",
		Config: &config.BeadsConfig{Prefix: prefix},
	}

	if err := setBeadLabel(townRoot, r, "max_polecats", "2"); err != nil {
		t.Fatalf("setBeadLabel should auto-create the identity bead, got error: %v", err)
	}

	// Verify the bead now exists and carries the label.
	issue, err := b.Show(rigBeadID)
	if err != nil {
		t.Fatalf("rig identity bead %s should exist after setBeadLabel: %v", rigBeadID, err)
	}
	found := false
	for _, label := range issue.Labels {
		if label == "max_polecats:2" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected label max_polecats:2 on %s, got labels: %v", rigBeadID, issue.Labels)
	}

	// Second call must be idempotent (bead already exists) and update the value.
	if err := setBeadLabel(townRoot, r, "max_polecats", "4"); err != nil {
		t.Fatalf("setBeadLabel on existing bead failed: %v", err)
	}
	issue, err = b.Show(rigBeadID)
	if err != nil {
		t.Fatalf("show after update: %v", err)
	}
	for _, label := range issue.Labels {
		if label == "max_polecats:2" {
			t.Errorf("stale label max_polecats:2 still present after update, labels: %v", issue.Labels)
		}
	}
}
