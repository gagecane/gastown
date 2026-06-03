package capacity

import (
	"strings"
	"testing"
)

func TestPlanDispatch(t *testing.T) {
	beads := func(n int) []PendingBead {
		result := make([]PendingBead, n)
		for i := range result {
			result[i] = PendingBead{ID: string(rune('a' + i))}
		}
		return result
	}

	tests := []struct {
		name              string
		availableCapacity int
		batchSize         int
		readyCount        int
		wantCount         int
		wantSkipped       int
		wantReason        string
	}{
		{"no ready beads", 5, 3, 0, 0, 0, "none"},
		{"no capacity (negative)", -1, 3, 10, 0, 10, "capacity"},
		{"no capacity (zero)", 0, 3, 10, 0, 10, "capacity"},
		{"capacity constrains", 2, 3, 10, 2, 8, "capacity"},
		{"batch constrains", 10, 3, 10, 3, 7, "batch"},
		{"ready constrains", 10, 5, 2, 2, 0, "ready"},
		{"large capacity, batch constrains", 100, 3, 10, 3, 7, "batch"},
		{"large capacity, ready constrains", 100, 5, 2, 2, 0, "ready"},
		{"all equal", 3, 3, 3, 3, 0, "batch"},
		{"single bead", 10, 3, 1, 1, 0, "ready"},
		{"capacity 1", 1, 3, 10, 1, 9, "capacity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ready := beads(tt.readyCount)
			plan := PlanDispatch(tt.availableCapacity, tt.batchSize, ready)

			if len(plan.ToDispatch) != tt.wantCount {
				t.Errorf("ToDispatch count: got %d, want %d", len(plan.ToDispatch), tt.wantCount)
			}
			if plan.Skipped != tt.wantSkipped {
				t.Errorf("Skipped: got %d, want %d", plan.Skipped, tt.wantSkipped)
			}
			if plan.Reason != tt.wantReason {
				t.Errorf("Reason: got %q, want %q", plan.Reason, tt.wantReason)
			}
		})
	}
}

func TestFilterCircuitBroken(t *testing.T) {
	tests := []struct {
		name        string
		failures    []int // dispatch_failures per bead (-1 = nil context)
		maxFailures int
		wantKept    int
		wantRemoved int
	}{
		{"all healthy", []int{0, 0, 0}, 3, 3, 0},
		{"one at threshold", []int{0, 3, 1}, 3, 2, 1},
		{"one above threshold", []int{0, 5, 1}, 3, 2, 1},
		{"all broken", []int{3, 4, 5}, 3, 0, 3},
		{"nil context passes through", []int{-1, 0, 2}, 3, 3, 0},
		{"empty list", []int{}, 3, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var beads []PendingBead
			for i, f := range tt.failures {
				b := PendingBead{ID: string(rune('a' + i))}
				if f >= 0 {
					b.Context = &SlingContextFields{DispatchFailures: f}
				}
				beads = append(beads, b)
			}

			kept, removed := FilterCircuitBroken(beads, tt.maxFailures)
			if len(kept) != tt.wantKept {
				t.Errorf("kept: got %d, want %d", len(kept), tt.wantKept)
			}
			if removed != tt.wantRemoved {
				t.Errorf("removed: got %d, want %d", removed, tt.wantRemoved)
			}
		})
	}
}

func TestAllReady(t *testing.T) {
	beads := []PendingBead{
		{ID: "a"},
		{ID: "b"},
		{ID: "c"},
	}
	result := AllReady(beads)
	if len(result) != 3 {
		t.Errorf("AllReady should pass all beads through, got %d", len(result))
	}
}

func TestBlockerAware(t *testing.T) {
	beads := []PendingBead{
		{ID: "ctx-a", WorkBeadID: "a"},
		{ID: "ctx-b", WorkBeadID: "b"},
		{ID: "ctx-c", WorkBeadID: "c"},
		{ID: "ctx-d", WorkBeadID: "d"},
	}

	readyIDs := map[string]bool{"a": true, "c": true}
	filter := BlockerAware(readyIDs)
	result := filter(beads)

	if len(result) != 2 {
		t.Fatalf("BlockerAware should return 2 beads, got %d", len(result))
	}
	if result[0].WorkBeadID != "a" || result[1].WorkBeadID != "c" {
		t.Errorf("BlockerAware returned wrong beads: %v, %v", result[0].WorkBeadID, result[1].WorkBeadID)
	}
}

func TestBlockerAware_EmptySet(t *testing.T) {
	beads := []PendingBead{{ID: "a", WorkBeadID: "wa"}, {ID: "b", WorkBeadID: "wb"}}
	readyIDs := map[string]bool{}
	filter := BlockerAware(readyIDs)
	result := filter(beads)
	if len(result) != 0 {
		t.Errorf("BlockerAware with empty readyIDs should return 0 beads, got %d", len(result))
	}
}

func TestCircuitBreakerPolicy(t *testing.T) {
	policy := CircuitBreakerPolicy(3)

	tests := []struct {
		failures int
		want     FailureAction
	}{
		{0, FailureRetry},
		{1, FailureRetry},
		{2, FailureRetry},
		{3, FailureQuarantine},
		{5, FailureQuarantine},
	}
	for _, tt := range tests {
		got := policy(tt.failures)
		if got != tt.want {
			t.Errorf("CircuitBreakerPolicy(3)(%d) = %v, want %v", tt.failures, got, tt.want)
		}
	}
}

func TestNoRetryPolicy(t *testing.T) {
	policy := NoRetryPolicy()
	for _, failures := range []int{0, 1, 5} {
		if got := policy(failures); got != FailureQuarantine {
			t.Errorf("NoRetryPolicy()(%d) = %v, want FailureQuarantine", failures, got)
		}
	}
}

func TestReconstructFromContext(t *testing.T) {
	ctx := &SlingContextFields{
		WorkBeadID:  "bead-123",
		TargetRig:   "prod-rig",
		Formula:     "mol-polecat-work",
		Args:        "do stuff",
		Vars:        "x=1\ny=2",
		Merge:       "mr",
		BaseBranch:  "main",
		Account:     "acme",
		Agent:       "codex",
		Mode:        "ralph",
		NoMerge:     true,
		HookRawBead: true,
	}

	params := ReconstructFromContext(ctx)

	if params.BeadID != "bead-123" {
		t.Errorf("BeadID: got %q, want %q", params.BeadID, "bead-123")
	}
	if params.RigName != "prod-rig" {
		t.Errorf("RigName: got %q, want %q", params.RigName, "prod-rig")
	}
	if params.FormulaName != "mol-polecat-work" {
		t.Errorf("FormulaName: got %q, want %q", params.FormulaName, "mol-polecat-work")
	}
	if params.Args != "do stuff" {
		t.Errorf("Args: got %q, want %q", params.Args, "do stuff")
	}
	if len(params.Vars) != 2 || params.Vars[0] != "x=1" || params.Vars[1] != "y=2" {
		t.Errorf("Vars: got %v, want [x=1 y=2]", params.Vars)
	}
	if params.Merge != "mr" {
		t.Errorf("Merge: got %q, want %q", params.Merge, "mr")
	}
	if params.BaseBranch != "main" {
		t.Errorf("BaseBranch: got %q, want %q", params.BaseBranch, "main")
	}
	if params.Account != "acme" {
		t.Errorf("Account: got %q, want %q", params.Account, "acme")
	}
	if params.Agent != "codex" {
		t.Errorf("Agent: got %q, want %q", params.Agent, "codex")
	}
	if params.Mode != "ralph" {
		t.Errorf("Mode: got %q, want %q", params.Mode, "ralph")
	}
	if !params.NoMerge {
		t.Error("NoMerge: expected true")
	}
	if !params.HookRawBead {
		t.Error("HookRawBead: expected true")
	}
}

func TestReconstructFromContext_EmptyVars(t *testing.T) {
	ctx := &SlingContextFields{
		WorkBeadID: "bead-1",
		TargetRig:  "rig1",
	}
	params := ReconstructFromContext(ctx)
	if params.Vars != nil {
		t.Errorf("Vars should be nil when ctx.Vars is empty, got %v", params.Vars)
	}
}

func TestIsMessagingBead(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"gt:message alone", []string{"gt:message"}, true},
		{"gt:handoff alone", []string{"gt:handoff"}, true},
		{"gt:merge-request alone", []string{"gt:merge-request"}, true},
		{"gt:message with another label", []string{"gt:message", "from:foo"}, true},
		{"sling-context label is not messaging", []string{"gt:sling-context"}, false},
		{"agent label is not messaging", []string{"gt:agent"}, false},
		{"empty slice", []string{}, false},
		{"nil slice", nil, false},
		{"plain work label", []string{"area/dog", "kind/bug"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsMessagingBead(tt.labels); got != tt.want {
				t.Errorf("IsMessagingBead(%v) = %v, want %v", tt.labels, got, tt.want)
			}
		})
	}
}

func TestFilterMessagingBeads(t *testing.T) {
	beads := []PendingBead{
		{ID: "ctx-1", WorkBeadID: "gt-1", Labels: []string{"area/dog"}},
		{ID: "ctx-2", WorkBeadID: "hq-1", Labels: []string{"gt:message"}},
		{ID: "ctx-3", WorkBeadID: "gt-2", Labels: []string{"gt:handoff"}},
		{ID: "ctx-4", WorkBeadID: "gt-3", Labels: []string{"gt:merge-request"}},
		{ID: "ctx-5", WorkBeadID: "gt-4", Labels: []string{"kind/bug"}},
	}
	kept, removed := FilterMessagingBeads(beads)
	if len(kept) != 2 {
		t.Errorf("kept = %d, want 2", len(kept))
	}
	if removed != 3 {
		t.Errorf("removed = %d, want 3", removed)
	}
	for _, b := range kept {
		if IsMessagingBead(b.Labels) {
			t.Errorf("kept slice contains messaging bead %s", b.ID)
		}
	}
}

func TestIsHandoffTitle(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  bool
	}{
		{"refinery context handoff", "🤝 HANDOFF: Refinery context handoff", true},
		{"no space after emoji", "🤝HANDOFF: foo", true},
		{"leading whitespace", "  🤝 HANDOFF: bar", true},
		{"plain handoff word is not enough", "HANDOFF: needs the emoji", false},
		{"emoji elsewhere is not a prefix", "fix: 🤝 HANDOFF mentioned", false},
		{"plain work title", "fix: dispatch bug", false},
		{"empty title", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsHandoffTitle(tt.title); got != tt.want {
				t.Errorf("IsHandoffTitle(%q) = %v, want %v", tt.title, got, tt.want)
			}
		})
	}
}

func TestFilterMessagingBeads_UnlabeledHandoffTitle(t *testing.T) {
	// gu-a76gk: an agent-authored handoff memo lands as a bare type=task with
	// NO messaging label. It must still be filtered out by its title.
	beads := []PendingBead{
		{ID: "ctx-1", WorkBeadID: "gt-1", Title: "fix: real work", Labels: []string{"kind/bug"}},
		{ID: "ctx-2", WorkBeadID: "cae2-w22", Title: "🤝 HANDOFF: Refinery context handoff", Labels: nil},
	}
	kept, removed := FilterMessagingBeads(beads)
	if len(kept) != 1 {
		t.Fatalf("kept = %d, want 1", len(kept))
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if kept[0].ID != "ctx-1" {
		t.Errorf("kept the wrong bead: %s", kept[0].ID)
	}
}

func TestPlanDispatch_FiltersUnlabeledHandoffTitle(t *testing.T) {
	candidates := []PendingBead{
		{ID: "ctx-1", WorkBeadID: "gt-1", Title: "fix: real work", Labels: []string{"kind/bug"}},
		{ID: "ctx-2", WorkBeadID: "cae2-w22", Title: "🤝 HANDOFF: Refinery context handoff", Labels: nil},
	}
	plan := PlanDispatch(100, 10, candidates)
	if len(plan.ToDispatch) != 1 {
		t.Fatalf("ToDispatch = %d, want 1", len(plan.ToDispatch))
	}
	if plan.ToDispatch[0].ID != "ctx-1" {
		t.Errorf("dispatched the wrong bead: %s", plan.ToDispatch[0].ID)
	}
}

func TestPlanDispatch_FiltersMessagingBeads(t *testing.T) {
	// Mix 3 messaging-labeled beads and 2 plain work beads. PlanDispatch must
	// keep only the 2 plain beads; Skipped must reflect the 3 messaging skips.
	candidates := []PendingBead{
		{ID: "ctx-1", WorkBeadID: "gt-1", Labels: []string{"area/dog"}},
		{ID: "ctx-2", WorkBeadID: "hq-1", Labels: []string{"gt:message"}},
		{ID: "ctx-3", WorkBeadID: "gt-2", Labels: []string{"kind/bug"}},
		{ID: "ctx-4", WorkBeadID: "hq-2", Labels: []string{"gt:handoff"}},
		{ID: "ctx-5", WorkBeadID: "hq-3", Labels: []string{"gt:merge-request"}},
	}
	plan := PlanDispatch(100, 10, candidates)
	if len(plan.ToDispatch) != 2 {
		t.Errorf("ToDispatch = %d, want 2 (only plain beads)", len(plan.ToDispatch))
	}
	if plan.Skipped < 3 {
		t.Errorf("Skipped = %d, want >= 3 (messaging skips)", plan.Skipped)
	}
	if !strings.Contains(plan.Reason, "messaging-filtered") {
		t.Errorf("Reason = %q, want to contain %q", plan.Reason, "messaging-filtered")
	}
	for _, b := range plan.ToDispatch {
		if IsMessagingBead(b.Labels) {
			t.Errorf("ToDispatch contains messaging bead %s", b.ID)
		}
	}
}

func TestPlanDispatch_OnlyMessagingBeads(t *testing.T) {
	candidates := []PendingBead{
		{ID: "ctx-1", WorkBeadID: "hq-1", Labels: []string{"gt:message"}},
		{ID: "ctx-2", WorkBeadID: "hq-2", Labels: []string{"gt:handoff"}},
	}
	plan := PlanDispatch(100, 10, candidates)
	if len(plan.ToDispatch) != 0 {
		t.Errorf("ToDispatch = %d, want 0", len(plan.ToDispatch))
	}
	if plan.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2", plan.Skipped)
	}
	if plan.Reason != "messaging-filtered" {
		t.Errorf("Reason = %q, want %q", plan.Reason, "messaging-filtered")
	}
}

func TestParsePriorityFloor(t *testing.T) {
	tests := []struct {
		input  string
		want   int
		wantOK bool
	}{
		{"normal", PriorityFloorNormal, true},
		{"Normal", PriorityFloorNormal, true},
		{"NORMAL", PriorityFloorNormal, true},
		{"", PriorityFloorNormal, true},
		{"low", PriorityFloorLow, true},
		{"Low", PriorityFloorLow, true},
		{"lowest", PriorityFloorLowest, true},
		{"Lowest", PriorityFloorLowest, true},
		{"LOWEST", PriorityFloorLowest, true},
		{"invalid", 0, false},
		{"high", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := ParsePriorityFloor(tt.input)
			if ok != tt.wantOK {
				t.Errorf("ParsePriorityFloor(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("ParsePriorityFloor(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestPriorityFloorName(t *testing.T) {
	tests := []struct {
		floor int
		want  string
	}{
		{0, "normal"},
		{-1, "normal"},
		{1, "low"},
		{2, "low"},
		{3, "lowest"},
		{4, "lowest"},
		{99, "lowest"},
	}
	for _, tt := range tests {
		got := PriorityFloorName(tt.floor)
		if got != tt.want {
			t.Errorf("PriorityFloorName(%d) = %q, want %q", tt.floor, got, tt.want)
		}
	}
}

func TestSortByPriorityFloor(t *testing.T) {
	beads := []PendingBead{
		{ID: "ctx-1", WorkBeadID: "gt-low", Context: &SlingContextFields{PriorityFloor: PriorityFloorLowest}},
		{ID: "ctx-2", WorkBeadID: "gt-normal", Context: &SlingContextFields{PriorityFloor: PriorityFloorNormal}},
		{ID: "ctx-3", WorkBeadID: "gt-also-normal", Context: &SlingContextFields{PriorityFloor: PriorityFloorNormal}},
		{ID: "ctx-4", WorkBeadID: "gt-medium", Context: &SlingContextFields{PriorityFloor: PriorityFloorLow}},
	}
	SortByPriorityFloor(beads)

	// Expect: normal (0), normal (0), low (2), lowest (4)
	// Within same priority, original order preserved (stable sort)
	expected := []string{"gt-normal", "gt-also-normal", "gt-medium", "gt-low"}
	for i, want := range expected {
		if beads[i].WorkBeadID != want {
			t.Errorf("position %d: got %s, want %s", i, beads[i].WorkBeadID, want)
		}
	}
}

func TestSortByPriorityFloor_NilContext(t *testing.T) {
	beads := []PendingBead{
		{ID: "ctx-1", WorkBeadID: "gt-lowest", Context: &SlingContextFields{PriorityFloor: PriorityFloorLowest}},
		{ID: "ctx-2", WorkBeadID: "gt-nil"}, // nil context → treated as normal (0)
		{ID: "ctx-3", WorkBeadID: "gt-normal", Context: &SlingContextFields{PriorityFloor: PriorityFloorNormal}},
	}
	SortByPriorityFloor(beads)

	// nil context and PriorityFloor=0 should both come before lowest
	if beads[0].WorkBeadID != "gt-nil" {
		t.Errorf("position 0: got %s, want gt-nil (nil context = normal priority)", beads[0].WorkBeadID)
	}
	if beads[1].WorkBeadID != "gt-normal" {
		t.Errorf("position 1: got %s, want gt-normal", beads[1].WorkBeadID)
	}
	if beads[2].WorkBeadID != "gt-lowest" {
		t.Errorf("position 2: got %s, want gt-lowest", beads[2].WorkBeadID)
	}
}

// TestPlanDispatch_PriorityFloorDispatchesUserFirst is the integration test for
// the priority-floor mechanism (gu-k1ub acceptance criteria #9):
//
//	Two beads are enqueued: an auto-test bead with --priority-floor=lowest and a
//	fixture user bead with normal priority. The dispatcher must return the user
//	bead first regardless of submission order.
func TestPlanDispatch_PriorityFloorDispatchesUserFirst(t *testing.T) {
	// Simulate: auto-test bead enqueued FIRST (earlier enqueue time),
	// user bead enqueued SECOND. With priority floor, user bead dispatches first.
	candidates := []PendingBead{
		{
			ID:         "ctx-autotest",
			WorkBeadID: "gt-autotest",
			Context:    &SlingContextFields{PriorityFloor: PriorityFloorLowest, EnqueuedAt: "2026-01-01T00:00:00Z"},
		},
		{
			ID:         "ctx-user",
			WorkBeadID: "gt-user",
			Context:    &SlingContextFields{PriorityFloor: PriorityFloorNormal, EnqueuedAt: "2026-01-01T00:01:00Z"},
		},
	}

	// Only 1 slot available — must pick the user bead (normal priority)
	plan := PlanDispatch(1, 10, candidates)

	if len(plan.ToDispatch) != 1 {
		t.Fatalf("expected 1 dispatched, got %d", len(plan.ToDispatch))
	}
	if plan.ToDispatch[0].WorkBeadID != "gt-user" {
		t.Errorf("expected user bead (gt-user) to be dispatched first, got %s", plan.ToDispatch[0].WorkBeadID)
	}
	if plan.Skipped != 1 {
		t.Errorf("expected 1 skipped (auto-test bead), got %d", plan.Skipped)
	}
}

// TestPlanDispatch_PriorityFloorDoesNotStarve verifies that lowest-priority
// beads are eventually dispatched when capacity is available.
func TestPlanDispatch_PriorityFloorDoesNotStarve(t *testing.T) {
	candidates := []PendingBead{
		{
			ID:         "ctx-autotest",
			WorkBeadID: "gt-autotest",
			Context:    &SlingContextFields{PriorityFloor: PriorityFloorLowest},
		},
		{
			ID:         "ctx-user",
			WorkBeadID: "gt-user",
			Context:    &SlingContextFields{PriorityFloor: PriorityFloorNormal},
		},
	}

	// 2 slots available — both should dispatch, user first
	plan := PlanDispatch(2, 10, candidates)

	if len(plan.ToDispatch) != 2 {
		t.Fatalf("expected 2 dispatched, got %d", len(plan.ToDispatch))
	}
	if plan.ToDispatch[0].WorkBeadID != "gt-user" {
		t.Errorf("expected user bead first, got %s", plan.ToDispatch[0].WorkBeadID)
	}
	if plan.ToDispatch[1].WorkBeadID != "gt-autotest" {
		t.Errorf("expected auto-test bead second, got %s", plan.ToDispatch[1].WorkBeadID)
	}
}

// TestPlanDispatch_PriorityFloorFIFOWithinSameLevel verifies FIFO ordering
// is preserved among beads with the same priority floor.
func TestPlanDispatch_PriorityFloorFIFOWithinSameLevel(t *testing.T) {
	candidates := []PendingBead{
		{ID: "ctx-1", WorkBeadID: "gt-first", Context: &SlingContextFields{PriorityFloor: PriorityFloorLowest}},
		{ID: "ctx-2", WorkBeadID: "gt-second", Context: &SlingContextFields{PriorityFloor: PriorityFloorLowest}},
		{ID: "ctx-3", WorkBeadID: "gt-third", Context: &SlingContextFields{PriorityFloor: PriorityFloorLowest}},
	}

	plan := PlanDispatch(2, 10, candidates)
	if len(plan.ToDispatch) != 2 {
		t.Fatalf("expected 2 dispatched, got %d", len(plan.ToDispatch))
	}
	// Within the same floor, FIFO order is preserved
	if plan.ToDispatch[0].WorkBeadID != "gt-first" {
		t.Errorf("expected gt-first at position 0, got %s", plan.ToDispatch[0].WorkBeadID)
	}
	if plan.ToDispatch[1].WorkBeadID != "gt-second" {
		t.Errorf("expected gt-second at position 1, got %s", plan.ToDispatch[1].WorkBeadID)
	}
}

func TestSplitVars(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", nil},
		{"single", "a=1", []string{"a=1"}},
		{"two newline-separated", "a=1\nb=2", []string{"a=1", "b=2"}},
		{"three newline-separated", "x=hello\ny=world\nz=42", []string{"x=hello", "y=world", "z=42"}},
		{"blank lines filtered", "a=1\n\nb=2\n", []string{"a=1", "b=2"}},
		{"whitespace trimmed", "  a=1  \n  b=2  ", []string{"a=1", "b=2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitVars(tt.input)
			if tt.want == nil {
				if got != nil {
					t.Errorf("splitVars(%q) = %v, want nil", tt.input, got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("splitVars(%q) = %v (len %d), want %v (len %d)",
					tt.input, got, len(got), tt.want, len(tt.want))
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("splitVars(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}
