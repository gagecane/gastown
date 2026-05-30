// Tests for the D15 maintainer-approval merge gate (Phase 0 task 10 / gu-mahth).
//
// The gate refuses to merge an MR bead labeled `gt:auto-test-pr` until a
// maintainer applies an `approved-by:<user>` label, when the source rig has
// `auto_test_pr.require_review_approval=true` (default-true). MR beads
// without the auto-test-pr label are unaffected (backwards-compat).
//
// These tests cover the pure helpers (hasApprovedByLabel,
// shouldHoldForAutoTestPRApproval) plus the rig-settings load path
// (autoTestPRApprovalRequired). End-to-end ListReadyMRs coverage runs
// through the dolt-backed integration suite — the unit-level guarantee
// here is the gate's decision logic and the safe-default behavior on
// missing/garbled settings.

package refinery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/rig"
)

func TestHasApprovedByLabel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		labels []string
		want   bool
	}{
		{
			name:   "nil labels",
			labels: nil,
			want:   false,
		},
		{
			name:   "no approved-by label",
			labels: []string{"gt:auto-test-pr", "gt:merge-request"},
			want:   false,
		},
		{
			name:   "approved-by:user present",
			labels: []string{"gt:auto-test-pr", "approved-by:alice"},
			want:   true,
		},
		{
			name:   "approved-by present alongside other approvals",
			labels: []string{"approved-by:bob", "approved-by:carol"},
			want:   true,
		},
		{
			name: "label that contains approved-by but does not start with it is not a match",
			// Strict prefix: a label like "not-approved-by:alice" must not
			// satisfy the gate. Documents the contract explicitly.
			labels: []string{"not-approved-by:alice", "gt:auto-test-pr"},
			want:   false,
		},
		{
			name:   "approved-by: with empty user is treated as approved",
			labels: []string{"approved-by:"},
			want:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			issue := &beads.Issue{Labels: tc.labels}
			if got := hasApprovedByLabel(issue); got != tc.want {
				t.Errorf("hasApprovedByLabel(%v) = %v; want %v", tc.labels, got, tc.want)
			}
		})
	}

	// Nil-safe: a nil issue must not panic and must report unapproved.
	if got := hasApprovedByLabel(nil); got {
		t.Error("hasApprovedByLabel(nil) = true; want false")
	}
}

func TestShouldHoldForAutoTestPRApproval(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		labels          []string
		requireApproval bool
		want            bool
		why             string
	}{
		{
			name:            "auto-test labeled, no approval, gate on -> hold",
			labels:          []string{"gt:auto-test-pr", "gt:merge-request"},
			requireApproval: true,
			want:            true,
			why:             "D15 acceptance: labeled-and-unapproved MR refuses to merge",
		},
		{
			name:            "auto-test labeled, approved, gate on -> proceed",
			labels:          []string{"gt:auto-test-pr", "approved-by:alice"},
			requireApproval: true,
			want:            false,
			why:             "D15 acceptance: labeled-and-approved MR merges",
		},
		{
			name:            "non-auto-test MR, gate on -> proceed (backwards-compat)",
			labels:          []string{"gt:merge-request"},
			requireApproval: true,
			want:            false,
			why:             "D15: MR beads without gt:auto-test-pr behave unchanged",
		},
		{
			name:            "non-auto-test MR, no labels, gate on -> proceed",
			labels:          nil,
			requireApproval: true,
			want:            false,
			why:             "regression coverage: gate must not affect bare MRs",
		},
		{
			name:            "auto-test labeled, no approval, gate off -> proceed",
			labels:          []string{"gt:auto-test-pr"},
			requireApproval: false,
			want:            false,
			why:             "explicit require_review_approval=false (v2 / fixture path) bypasses",
		},
		{
			name:            "auto-test labeled, approved, gate off -> proceed",
			labels:          []string{"gt:auto-test-pr", "approved-by:alice"},
			requireApproval: false,
			want:            false,
			why:             "with gate off, approval state is irrelevant",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			issue := &beads.Issue{Labels: tc.labels}
			if got := shouldHoldForAutoTestPRApproval(issue, tc.requireApproval); got != tc.want {
				t.Errorf("shouldHoldForAutoTestPRApproval(%v, requireApproval=%v) = %v; want %v (%s)",
					tc.labels, tc.requireApproval, got, tc.want, tc.why)
			}
		})
	}

	// Nil-safe: nil issue is treated as not held even with gate on. The
	// caller never passes nil today (ListReadyMRs always has a real
	// *beads.Issue), but a defensive return here keeps the helper safe
	// against future refactors that might preface the call with a guard.
	if got := shouldHoldForAutoTestPRApproval(nil, true); got {
		t.Error("shouldHoldForAutoTestPRApproval(nil, true) = true; want false")
	}
}

func TestAutoTestPRApprovalRequired_DefaultTrueOnMissingSettings(t *testing.T) {
	// D15 default-true: a rig with no settings file must surface
	// requireApproval=true so the gate is the safe default. Builds a
	// real Engineer rooted at a temp dir with no settings/ subdirectory.
	rigPath := t.TempDir()
	e := &Engineer{rig: &rig.Rig{Name: "test-rig", Path: rigPath}}

	if got := e.autoTestPRApprovalRequired(); !got {
		t.Errorf("autoTestPRApprovalRequired() with no settings file = false; want true (D15 default-true)")
	}
}

func TestAutoTestPRApprovalRequired_DefaultTrueOnAbsentBlock(t *testing.T) {
	// D15 default-true also covers the case where a settings file
	// exists but has no auto_test_pr block: opting in to anything else
	// (e.g. a merge_queue config) must NOT silently disable the gate.
	rigPath := t.TempDir()
	settingsDir := filepath.Join(rigPath, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}
	body := `{"type":"rig-settings","version":1,"merge_queue":{"enabled":true}}`
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	e := &Engineer{rig: &rig.Rig{Name: "test-rig", Path: rigPath}}
	if got := e.autoTestPRApprovalRequired(); !got {
		t.Errorf("autoTestPRApprovalRequired() with absent auto_test_pr block = false; want true")
	}
}

func TestAutoTestPRApprovalRequired_TrueWhenExplicitlyTrue(t *testing.T) {
	// Explicit true: opted-in rigs should round-trip cleanly.
	rigPath := t.TempDir()
	settingsDir := filepath.Join(rigPath, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}
	cfg := map[string]interface{}{
		"type":    "rig-settings",
		"version": 1,
		"auto_test_pr": map[string]interface{}{
			"enabled":                 true,
			"language":                "go",
			"require_review_approval": true,
		},
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	e := &Engineer{rig: &rig.Rig{Name: "test-rig", Path: rigPath}}
	if got := e.autoTestPRApprovalRequired(); !got {
		t.Errorf("autoTestPRApprovalRequired() with explicit require_review_approval=true = false; want true")
	}
}

func TestAutoTestPRApprovalRequired_FalseOnlyWhenExplicitlyFalse(t *testing.T) {
	// Only an explicit false disables the gate. This is the v2 /
	// fixture path; the test here documents the contract so a future
	// "default-flip" refactor surfaces immediately.
	rigPath := t.TempDir()
	settingsDir := filepath.Join(rigPath, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}
	cfg := map[string]interface{}{
		"type":    "rig-settings",
		"version": 1,
		"auto_test_pr": map[string]interface{}{
			"enabled":                 true,
			"language":                "go",
			"require_review_approval": false,
		},
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	e := &Engineer{rig: &rig.Rig{Name: "test-rig", Path: rigPath}}
	if got := e.autoTestPRApprovalRequired(); got {
		t.Errorf("autoTestPRApprovalRequired() with explicit require_review_approval=false = true; want false")
	}
}

func TestAutoTestPRApprovalRequired_DefaultTrueOnMalformedSettings(t *testing.T) {
	// Safety guarantee: a settings file that fails to load (parse
	// error, validation error, unreadable) must NOT cause the gate to
	// silently disable. The rig's intent is "opted in to auto-test-pr",
	// so the safe default is to hold.
	rigPath := t.TempDir()
	settingsDir := filepath.Join(rigPath, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}
	// Invalid language triggers ErrInvalidAutoTestPRLanguage; the
	// loader returns an error and a nil settings struct.
	body := `{"type":"rig-settings","version":1,"auto_test_pr":{"language":"rust"}}`
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	e := &Engineer{rig: &rig.Rig{Name: "test-rig", Path: rigPath}}
	if got := e.autoTestPRApprovalRequired(); !got {
		t.Errorf("autoTestPRApprovalRequired() with malformed settings = false; want true (safe default)")
	}
}

func TestAutoTestPRApprovalRequired_NilSafe(t *testing.T) {
	// Defensive: a partly-constructed Engineer (e.g. zero-value rig
	// path) must not panic, and must surface the safe default.
	if got := (*Engineer)(nil).autoTestPRApprovalRequired(); !got {
		t.Error("(*Engineer)(nil).autoTestPRApprovalRequired() = false; want true")
	}
	e := &Engineer{}
	if got := e.autoTestPRApprovalRequired(); !got {
		t.Error("Engineer{}.autoTestPRApprovalRequired() = false; want true")
	}
	e2 := &Engineer{rig: &rig.Rig{Path: ""}}
	if got := e2.autoTestPRApprovalRequired(); !got {
		t.Error("Engineer with empty rig path autoTestPRApprovalRequired() = false; want true")
	}
}
