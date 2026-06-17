package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

// stubNoOpenChildren replaces the open-children probe with a no-op for the test
// and registers cleanup. The open-children guard is the only guard in
// checkScheduleEligibility that runs a `bd children` subprocess; stubbing it
// keeps the table tests for the cheaper label/title/owner guards hermetic.
func stubNoOpenChildren(t *testing.T) {
	t.Helper()
	prev := hasOpenChildrenFn
	hasOpenChildrenFn = func(string) (bool, error) { return false, nil }
	t.Cleanup(func() { hasOpenChildrenFn = prev })
}

// TestCheckScheduleEligibility_Accepts verifies a plain, dispatchable work bead
// passes every guard in the chain.
func TestCheckScheduleEligibility_Accepts(t *testing.T) {
	stubNoOpenChildren(t)
	info := &beadInfo{
		ID:        "gt-abc123",
		Title:     "Fix the widget",
		Status:    "open",
		IssueType: "task",
	}
	if err := checkScheduleEligibility("gt-abc123", info); err != nil {
		t.Fatalf("expected eligible bead to pass, got: %v", err)
	}
}

// TestCheckScheduleEligibility_Rejects exercises the rejection branch matrix:
// each case trips exactly one guard and asserts the error names that cause. This
// is the coverage the 473-line scheduleBead never had (gu-1sgeb).
func TestCheckScheduleEligibility_Rejects(t *testing.T) {
	stubNoOpenChildren(t)

	cases := []struct {
		name        string
		beadID      string
		info        *beadInfo
		errContains string
	}{
		{
			name:        "closed bead",
			beadID:      "gt-1",
			info:        &beadInfo{Status: "closed", Title: "done work"},
			errContains: "work already completed",
		},
		{
			name:        "tombstone bead",
			beadID:      "gt-2",
			info:        &beadInfo{Status: "tombstone", Title: "gone"},
			errContains: "work already completed",
		},
		{
			name:        "identity bead via gt:agent label",
			beadID:      "gt-3",
			info:        &beadInfo{Status: "open", Title: "polecat rust", Labels: []string{"gt:agent"}},
			errContains: "identity/system bead",
		},
		{
			name:        "epic-like title with task type",
			beadID:      "gt-4",
			info:        &beadInfo{Status: "open", Title: "EPIC: big initiative", IssueType: "task"},
			errContains: "epic-like title",
		},
		{
			name:        "container type=molecule",
			beadID:      "gt-5",
			info:        &beadInfo{Status: "open", Title: "patrol wisp", IssueType: "molecule"},
			errContains: "non-work container",
		},
		{
			name:        "mayor-only label",
			beadID:      "gt-6",
			info:        &beadInfo{Status: "open", Title: "town config edit", Labels: []string{"no-polecat"}},
			errContains: "mayor-only / no-polecat",
		},
		{
			name:        "human-only title tag",
			beadID:      "gt-7",
			info:        &beadInfo{Status: "open", Title: "[HUMAN] run user study"},
			errContains: "human-only",
		},
		{
			name:        "awaiting refinery merge",
			beadID:      "gt-8",
			info:        &beadInfo{Status: "in_progress", Title: "shipped work", Labels: []string{"awaiting_refinery_merge"}},
			errContains: "awaiting refinery merge",
		},
		{
			name:        "notification bead via escalation label",
			beadID:      "gt-9",
			info:        &beadInfo{Status: "open", Title: "escalation", Labels: []string{"gt:escalation"}},
			errContains: "notification / operational-signal",
		},
		{
			name:        "reference tripwire via do-not-dispatch label",
			beadID:      "gt-10",
			info:        &beadInfo{Status: "open", Title: "safety gate", Labels: []string{"do-not-dispatch"}},
			errContains: "do-not-dispatch / pinned reference tripwire",
		},
		{
			name:        "sling-context wrapper",
			beadID:      "gt-11",
			info:        &beadInfo{Status: "open", Title: "sling-context: foo", Labels: []string{capacity.LabelSlingContext}},
			errContains: "sling-context wrapper",
		},
		{
			name:        "polecat-owned bead",
			beadID:      "gt-12",
			info:        &beadInfo{Status: "open", Title: "self-filed", Owner: "gastown/polecats/rust"},
			errContains: "owned by a polecat",
		},
		{
			name:        "refinery-owned bead",
			beadID:      "gt-13",
			info:        &beadInfo{Status: "open", Title: "merge state", Owner: "gastown/refinery"},
			errContains: "owned by a refinery",
		},
		{
			name:        "refinery workflow step id",
			beadID:      "gt-wfs-xyz",
			info:        &beadInfo{Status: "open", Title: "Merge and push"},
			errContains: "refinery workflow step",
		},
		{
			name:        "deferred status",
			beadID:      "gt-14",
			info:        &beadInfo{Status: "deferred", Title: "later work"},
			errContains: "is deferred",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkScheduleEligibility(tc.beadID, tc.info)
			if err == nil {
				t.Fatalf("expected rejection, got nil error")
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("error %q should contain %q", err.Error(), tc.errContains)
			}
		})
	}
}

// TestCheckScheduleEligibility_WorkflowPoolStepCarveOut verifies the gu-fxyuz
// carve-out: a `-wfs-` step the formula engine deliberately routed to the rig
// pool (carrying gt:workflow-pool-step) is allowed through, while a bare `-wfs-`
// step is still refused.
func TestCheckScheduleEligibility_WorkflowPoolStepCarveOut(t *testing.T) {
	stubNoOpenChildren(t)

	poolStep := &beadInfo{Status: "open", Title: "review leg", Labels: []string{labelWorkflowPoolStep}}
	if err := checkScheduleEligibility("gt-wfs-pool1", poolStep); err != nil {
		t.Errorf("engine-routed pool step should be eligible, got: %v", err)
	}

	bareStep := &beadInfo{Status: "open", Title: "role step"}
	if err := checkScheduleEligibility("gt-wfs-role1", bareStep); err == nil {
		t.Error("bare -wfs- step should be refused")
	}
}

// TestCheckScheduleEligibility_OpenChildrenGuard verifies the open-children
// guard fires when the injected probe reports open children, and the error
// names the bead — covering the one guard that depends on a subprocess.
func TestCheckScheduleEligibility_OpenChildrenGuard(t *testing.T) {
	prev := hasOpenChildrenFn
	hasOpenChildrenFn = func(string) (bool, error) { return true, nil }
	t.Cleanup(func() { hasOpenChildrenFn = prev })

	info := &beadInfo{Status: "open", Title: "container", IssueType: "task"}
	err := checkScheduleEligibility("gt-parent", info)
	if err == nil {
		t.Fatal("expected rejection for bead with open children")
	}
	if !strings.Contains(err.Error(), "has open children") {
		t.Errorf("error %q should mention open children", err.Error())
	}
}

// TestCheckScheduleEligibility_GuardOrder verifies the deliberate ordering: a
// closed bead that is ALSO an identity bead reports the closed cause, not the
// more general identity message (gu-3znx). The closed check must run first.
func TestCheckScheduleEligibility_GuardOrder(t *testing.T) {
	stubNoOpenChildren(t)
	// gt:agent label makes this an identity bead AND status=closed trips the
	// closed guard. The closed guard runs first, so its message wins.
	info := &beadInfo{Status: "closed", Title: "polecat rust", Labels: []string{"gt:agent"}}
	err := checkScheduleEligibility("gt-x", info)
	if err == nil {
		t.Fatal("expected rejection")
	}
	if !strings.Contains(err.Error(), "work already completed") {
		t.Errorf("closed guard should win over identity guard, got: %q", err.Error())
	}
}

// TestBuildSlingContextFields_Defaults verifies the minimal mapping: required
// fields are set, optional fields stay zero-valued, and the enqueue time is
// formatted as UTC RFC3339 from the injected clock.
func TestBuildSlingContextFields_Defaults(t *testing.T) {
	enq := time.Date(2026, 6, 17, 19, 30, 0, 0, time.UTC)
	got := buildSlingContextFields("gt-abc", "gastown", ScheduleOptions{}, enq)

	if got.Version != 1 {
		t.Errorf("Version = %d, want 1", got.Version)
	}
	if got.WorkBeadID != "gt-abc" {
		t.Errorf("WorkBeadID = %q, want gt-abc", got.WorkBeadID)
	}
	if got.TargetRig != "gastown" {
		t.Errorf("TargetRig = %q, want gastown", got.TargetRig)
	}
	if got.EnqueuedAt != "2026-06-17T19:30:00Z" {
		t.Errorf("EnqueuedAt = %q, want 2026-06-17T19:30:00Z", got.EnqueuedAt)
	}
	// Unset options must leave optional fields zero-valued.
	if got.Formula != "" || got.Args != "" || got.Vars != "" || got.Merge != "" ||
		got.BaseBranch != "" || got.ResumeBranch != "" || got.Account != "" ||
		got.Agent != "" || got.Mode != "" || got.PriorityFloor != 0 ||
		got.NoMerge || got.ReviewOnly || got.HookRawBead || got.Owned {
		t.Errorf("unset options should leave optional fields zero, got %+v", got)
	}
}

// TestBuildSlingContextFields_NonLocalClock verifies the enqueue time is
// normalized to UTC even when a non-UTC clock value is passed.
func TestBuildSlingContextFields_NonLocalClock(t *testing.T) {
	loc := time.FixedZone("PST", -8*3600)
	enq := time.Date(2026, 6, 17, 11, 30, 0, 0, loc) // 19:30 UTC
	got := buildSlingContextFields("gt-abc", "gastown", ScheduleOptions{}, enq)
	if got.EnqueuedAt != "2026-06-17T19:30:00Z" {
		t.Errorf("EnqueuedAt = %q, want UTC-normalized 2026-06-17T19:30:00Z", got.EnqueuedAt)
	}
}

// TestBuildSlingContextFields_AllOptions verifies every option maps onto the
// matching context field, including the Ralph→Mode and Vars-join translations.
func TestBuildSlingContextFields_AllOptions(t *testing.T) {
	enq := time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)
	opts := ScheduleOptions{
		Formula:       "mol-evolve",
		Args:          "fix it",
		Vars:          []string{"a=1", "b=2"},
		Merge:         "mr",
		BaseBranch:    "integration/v3",
		ResumeBranch:  "polecat/rust/foo",
		NoMerge:       true,
		ReviewOnly:    true,
		Account:       "acct-1",
		Agent:         "codex",
		HookRawBead:   true,
		Ralph:         true,
		Owned:         true,
		PriorityFloor: 2,
	}
	got := buildSlingContextFields("gt-z", "lia", opts, enq)

	checks := map[string]struct{ got, want string }{
		"Formula":      {got.Formula, "mol-evolve"},
		"Args":         {got.Args, "fix it"},
		"Vars":         {got.Vars, "a=1\nb=2"},
		"Merge":        {got.Merge, "mr"},
		"BaseBranch":   {got.BaseBranch, "integration/v3"},
		"ResumeBranch": {got.ResumeBranch, "polecat/rust/foo"},
		"Account":      {got.Account, "acct-1"},
		"Agent":        {got.Agent, "codex"},
		"Mode":         {got.Mode, "ralph"},
	}
	for field, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", field, c.got, c.want)
		}
	}
	if !got.NoMerge || !got.ReviewOnly || !got.HookRawBead || !got.Owned {
		t.Errorf("bool flags not all set: %+v", got)
	}
	if got.PriorityFloor != 2 {
		t.Errorf("PriorityFloor = %d, want 2", got.PriorityFloor)
	}
}

// TestBuildSlingContextFields_PriorityFloorZeroOmitted verifies a zero priority
// floor (the normal default) is left unset rather than written explicitly.
func TestBuildSlingContextFields_PriorityFloorZeroOmitted(t *testing.T) {
	enq := time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)
	got := buildSlingContextFields("gt-z", "lia", ScheduleOptions{PriorityFloor: 0}, enq)
	if got.PriorityFloor != 0 {
		t.Errorf("PriorityFloor = %d, want 0 (default)", got.PriorityFloor)
	}
}
