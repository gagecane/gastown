package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
	"github.com/steveyegge/gastown/internal/wisp"
)

// TestShouldReattachFormula verifies the gs-am8 GAP 2 re-attach decision: an
// already-scheduled bead's formula is replaced only under --force with a
// genuinely different formula; otherwise the idempotent no-op stands.
func TestShouldReattachFormula(t *testing.T) {
	ctx := func(f string) *capacity.SlingContextFields {
		return &capacity.SlingContextFields{Formula: f}
	}
	cases := []struct {
		name      string
		force     bool
		requested string
		existing  *capacity.SlingContextFields
		want      bool
	}{
		{"force + different formula re-attaches", true, "mol-pw-adversarial-review", ctx("mol-polecat-work"), true},
		{"force + same formula is a no-op", true, "mol-polecat-work", ctx("mol-polecat-work"), false},
		{"no force never re-attaches", false, "mol-pw-adversarial-review", ctx("mol-polecat-work"), false},
		{"force + clearing formula (to default) re-attaches", true, "", ctx("mol-polecat-work"), true},
		{"nil existing fields never re-attaches", true, "mol-pw-adversarial-review", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldReattachFormula(tc.force, tc.requested, tc.existing); got != tc.want {
				t.Errorf("shouldReattachFormula(%v,%q,%+v) = %v, want %v",
					tc.force, tc.requested, tc.existing, got, tc.want)
			}
		})
	}
}

// TestIsStaleOrFailedContext verifies the gu-rm08l recovery predicate: an open
// sling context is treated as stale/failed (and thus recyclable on re-sling)
// when it recorded any transient dispatch failure OR has aged past the TTL.
// A healthy, fresh, never-failed context must NOT be recycled.
func TestIsStaleOrFailedContext(t *testing.T) {
	now := time.Date(2026, 6, 2, 14, 0, 0, 0, time.UTC)
	fresh := now.Add(-5 * time.Minute).Format(time.RFC3339)
	aged := now.Add(-slingContextTTL - time.Minute).Format(time.RFC3339)

	cases := []struct {
		name   string
		ctx    *beads.Issue
		fields *capacity.SlingContextFields
		want   bool
	}{
		{
			name:   "fresh, no failures — healthy in-flight, keep",
			ctx:    &beads.Issue{CreatedAt: fresh},
			fields: &capacity.SlingContextFields{DispatchFailures: 0},
			want:   false,
		},
		{
			name:   "fresh but one transient failure — recycle",
			ctx:    &beads.Issue{CreatedAt: fresh},
			fields: &capacity.SlingContextFields{DispatchFailures: 1},
			want:   true,
		},
		{
			name:   "aged past TTL, no failures — recycle",
			ctx:    &beads.Issue{CreatedAt: aged},
			fields: &capacity.SlingContextFields{DispatchFailures: 0},
			want:   true,
		},
		{
			name:   "nil fields, fresh — keep (fail-closed on age)",
			ctx:    &beads.Issue{CreatedAt: fresh},
			fields: nil,
			want:   false,
		},
		{
			name:   "nil fields, aged — recycle on age alone",
			ctx:    &beads.Issue{CreatedAt: aged},
			fields: nil,
			want:   true,
		},
		{
			name:   "empty created_at, no failures — keep (unknown age fails closed)",
			ctx:    &beads.Issue{CreatedAt: ""},
			fields: &capacity.SlingContextFields{DispatchFailures: 0},
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isStaleOrFailedContext(tc.ctx, tc.fields, now); got != tc.want {
				t.Errorf("isStaleOrFailedContext(%+v, %+v) = %v, want %v",
					tc.ctx, tc.fields, got, tc.want)
			}
		})
	}
}

// TestStaleContextReslingReason verifies the close reason distinguishes a
// transient-failure expiry from a plain TTL expiry, for operator observability.
func TestStaleContextReslingReason(t *testing.T) {
	if got := staleContextReslingReason(&capacity.SlingContextFields{DispatchFailures: 2}); !strings.Contains(got, "failed-context-resling") || !strings.Contains(got, "dispatch_failures=2") {
		t.Errorf("failure reason = %q, want failed-context-resling with dispatch_failures=2", got)
	}
	if got := staleContextReslingReason(&capacity.SlingContextFields{DispatchFailures: 0}); !strings.Contains(got, "stale-context-resling") || !strings.Contains(got, "ttl-expired") {
		t.Errorf("ttl reason = %q, want stale-context-resling ttl-expired", got)
	}
	if got := staleContextReslingReason(nil); !strings.Contains(got, "stale-context-resling") {
		t.Errorf("nil fields reason = %q, want stale-context-resling", got)
	}
}

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

// TestResolveFormulaForBead verifies the gs-zq0 / gs-am8 GAP 1 precedence:
// explicit --formula > bead 'gt:formula:<name>' label > rig/system default.
func TestResolveFormulaForBead(t *testing.T) {
	t.Parallel()

	const label = "gt:formula:mol-pw-adversarial-review"
	const labelFormula = "mol-pw-adversarial-review"

	t.Run("explicit flag wins over label", func(t *testing.T) {
		t.Parallel()
		got := resolveFormulaForBead("mol-evolve", false, "/tmp/nonexistent", "myrig", []string{label})
		if got != "mol-evolve" {
			t.Errorf("got %q, want %q", got, "mol-evolve")
		}
	})

	t.Run("label wins over system default", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		rigName := "testrig"
		_ = os.MkdirAll(filepath.Join(tmpDir, rigName), 0o755)
		got := resolveFormulaForBead("", false, tmpDir, rigName, []string{"bug", label})
		if got != labelFormula {
			t.Errorf("got %q, want %q", got, labelFormula)
		}
	})

	t.Run("no label falls back to system default", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		rigName := "testrig"
		_ = os.MkdirAll(filepath.Join(tmpDir, rigName), 0o755)
		got := resolveFormulaForBead("", false, tmpDir, rigName, []string{"bug"})
		if got != "mol-polecat-work" {
			t.Errorf("got %q, want %q", got, "mol-polecat-work")
		}
	})

	t.Run("hookRawBead ignores label", func(t *testing.T) {
		t.Parallel()
		got := resolveFormulaForBead("", true, "/tmp/nonexistent", "myrig", []string{label})
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

// TestResolveFormulaForBeadWithBase verifies the gs-njym main-target override:
// a bead resolving to base=main on a rig whose default_formula is an
// integration-branch dev-work workflow is dispatched with the rig's configured
// main_target_formula instead, while explicit flags and labels keep precedence.
func TestResolveFormulaForBeadWithBase(t *testing.T) {
	t.Parallel()

	const devFormula = "mol-lia-dev-work"
	const prFormula = "mol-lia-pr-work"
	const label = "gt:formula:mol-pw-adversarial-review"
	const labelFormula = "mol-pw-adversarial-review"

	// newRig builds a temp rig whose default formula is the dev-work workflow
	// and (optionally) sets a main_target_formula override.
	newRig := func(t *testing.T, mainTarget string) (townRoot, rigName string) {
		t.Helper()
		townRoot = t.TempDir()
		rigName = "lia"
		_ = os.MkdirAll(filepath.Join(townRoot, rigName), 0o755)
		wispCfg := wisp.NewConfig(townRoot, rigName)
		if err := wispCfg.Set("default_formula", devFormula); err != nil {
			t.Fatalf("set default_formula: %v", err)
		}
		if mainTarget != "" {
			if err := wispCfg.Set("main_target_formula", mainTarget); err != nil {
				t.Fatalf("set main_target_formula: %v", err)
			}
		}
		return townRoot, rigName
	}

	t.Run("base=main selects main_target_formula over dev-work default", func(t *testing.T) {
		t.Parallel()
		townRoot, rigName := newRig(t, prFormula)
		got := resolveFormulaForBeadWithBase("", false, townRoot, rigName, nil, "main")
		if got != prFormula {
			t.Errorf("got %q, want %q", got, prFormula)
		}
	})

	t.Run("base=main without main_target_formula keeps dev-work default", func(t *testing.T) {
		t.Parallel()
		townRoot, rigName := newRig(t, "")
		got := resolveFormulaForBeadWithBase("", false, townRoot, rigName, nil, "main")
		if got != devFormula {
			t.Errorf("got %q, want %q", got, devFormula)
		}
	})

	t.Run("non-main base keeps dev-work default even with override set", func(t *testing.T) {
		t.Parallel()
		townRoot, rigName := newRig(t, prFormula)
		got := resolveFormulaForBeadWithBase("", false, townRoot, rigName, nil, "integration/v3")
		if got != devFormula {
			t.Errorf("got %q, want %q", got, devFormula)
		}
	})

	t.Run("empty base keeps dev-work default", func(t *testing.T) {
		t.Parallel()
		townRoot, rigName := newRig(t, prFormula)
		got := resolveFormulaForBeadWithBase("", false, townRoot, rigName, nil, "")
		if got != devFormula {
			t.Errorf("got %q, want %q", got, devFormula)
		}
	})

	t.Run("explicit flag wins over main-target override", func(t *testing.T) {
		t.Parallel()
		townRoot, rigName := newRig(t, prFormula)
		got := resolveFormulaForBeadWithBase("mol-custom", false, townRoot, rigName, nil, "main")
		if got != "mol-custom" {
			t.Errorf("got %q, want %q", got, "mol-custom")
		}
	})

	t.Run("gt:formula label wins over main-target override", func(t *testing.T) {
		t.Parallel()
		townRoot, rigName := newRig(t, prFormula)
		got := resolveFormulaForBeadWithBase("", false, townRoot, rigName, []string{label}, "main")
		if got != labelFormula {
			t.Errorf("got %q, want %q", got, labelFormula)
		}
	})

	t.Run("hookRawBead ignores main-target override", func(t *testing.T) {
		t.Parallel()
		townRoot, rigName := newRig(t, prFormula)
		got := resolveFormulaForBeadWithBase("", true, townRoot, rigName, nil, "main")
		if got != "" {
			t.Errorf("got %q, want empty", got)
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
