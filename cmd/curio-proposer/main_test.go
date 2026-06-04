package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestClosedWindowCursor asserts the closed-window invariant: the read cursor
// is ALWAYS strictly behind now by at least closedWindowMargin. Live-tailing is
// forbidden — the proposer can never observe a candidate written "now". This is
// the binary's own tested invariant (per the bead): max read timestamp strictly
// behind now by a margin.
func TestClosedWindowCursor(t *testing.T) {
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	cursor := closedWindowCursor(now)

	if !cursor.Before(now) {
		t.Fatalf("cursor %s is not strictly before now %s", cursor, now)
	}
	if gap := now.Sub(cursor); gap < closedWindowMargin {
		t.Fatalf("cursor gap %s is smaller than the closed-window margin %s", gap, closedWindowMargin)
	}
	if got, want := now.Sub(cursor), closedWindowMargin; got != want {
		t.Fatalf("cursor gap = %s, want exactly the margin %s", got, want)
	}
}

// TestClosedWindowCursor_StrictlyBehindAcrossClocks checks the strict-behind
// property holds for arbitrary clock values, not just one fixed instant.
func TestClosedWindowCursor_StrictlyBehindAcrossClocks(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 100; i++ {
		now := base.Add(time.Duration(i) * 7 * time.Minute)
		if cursor := closedWindowCursor(now); !cursor.Before(now) {
			t.Fatalf("cursor %s not strictly before now %s", cursor, now)
		}
	}
}

// TestKillSwitchIsolation asserts that curio.llm.enabled is read INDEPENDENTLY
// of curio.enabled. Toggling the live Patrol switch must not move the
// Retrospect lane switch, and vice-versa — the kill-switch isolation invariant.
func TestKillSwitchIsolation(t *testing.T) {
	cases := []struct {
		name        string
		patrolOn    bool
		llmOn       bool
		wantLLMLane bool
	}{
		{"both off", false, false, false},
		{"patrol on, llm off — Retrospect stays OFF", true, false, false},
		{"patrol off, llm on — Retrospect runs without Patrol", false, true, true},
		{"both on", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cfg proposerConfig
			cfg.Patrols.Curio.Enabled = tc.patrolOn
			cfg.Patrols.Curio.LLM.Enabled = tc.llmOn

			if got := cfg.llmEnabled(); got != tc.wantLLMLane {
				t.Fatalf("llmEnabled() = %v, want %v (patrol=%v llm=%v)",
					got, tc.wantLLMLane, tc.patrolOn, tc.llmOn)
			}
			// Isolation: the LLM lane decision must equal the LLM switch
			// regardless of the Patrol switch.
			if cfg.llmEnabled() != tc.llmOn {
				t.Fatalf("llmEnabled() depends on curio.enabled — not isolated")
			}
		})
	}
}

// TestLoadProposerConfig_KillSwitchParsing verifies the kill-switch projection
// parses real mayor/daemon.json shapes, and that a missing file reads as
// LLM-off (not an error).
func TestLoadProposerConfig_KillSwitchParsing(t *testing.T) {
	t.Run("missing file -> llm off, no error", func(t *testing.T) {
		cfg, err := loadProposerConfig(t.TempDir())
		if err != nil {
			t.Fatalf("missing config should not error: %v", err)
		}
		if cfg.llmEnabled() {
			t.Fatal("missing config should read as llm disabled")
		}
	})

	t.Run("llm.enabled=true parsed", func(t *testing.T) {
		root := t.TempDir()
		writeDaemonJSON(t, root, `{"patrols":{"curio":{"enabled":false,"llm":{"enabled":true}}}}`)
		cfg, err := loadProposerConfig(root)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if !cfg.llmEnabled() {
			t.Fatal("expected llm enabled")
		}
	})

	t.Run("llm absent -> off even when patrol on", func(t *testing.T) {
		root := t.TempDir()
		writeDaemonJSON(t, root, `{"patrols":{"curio":{"enabled":true}}}`)
		cfg, err := loadProposerConfig(root)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.llmEnabled() {
			t.Fatal("llm should default off when key absent")
		}
	})
}

// TestImportGraph_NoWritePath is the core write-incapability invariant: the
// curio-proposer binary's transitive dependencies must EXCLUDE internal/beads
// (bead mutation) and internal/daemon (which imports beads). The mutation
// capability is physically absent from the binary, not merely unused. This
// proves the import graph has no write path, as the bead requires.
func TestImportGraph_NoWritePath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go list import-graph check in short mode")
	}

	forbidden := []string{
		"github.com/steveyegge/gastown/internal/beads",
		"github.com/steveyegge/gastown/internal/daemon",
	}

	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps failed: %v\n%s", err, out)
	}
	deps := string(out)
	for _, pkg := range forbidden {
		if strings.Contains(deps, pkg) {
			t.Errorf("curio-proposer transitively imports %s — write path present, "+
				"violating the write-incapable invariant", pkg)
		}
	}
}

func writeDaemonJSON(t *testing.T, townRoot, body string) {
	t.Helper()
	dir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "daemon.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
