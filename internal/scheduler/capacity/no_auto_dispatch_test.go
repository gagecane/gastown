package capacity

import "testing"

// gs-b2a: a bead labeled no-auto-dispatch must never be selected by the
// automatic dispatch pipeline. Before the fix, such a bead was redispatched to
// a polecat because the dispatch pipeline did not consult the label.

func TestIsNoAutoDispatch(t *testing.T) {
	cases := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"no labels", nil, false},
		{"unrelated label", []string{"kind/bug"}, false},
		{"no-auto-dispatch", []string{"no-auto-dispatch"}, true},
		{"no-auto-dispatch among others", []string{"human-investigation", "no-auto-dispatch"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNoAutoDispatch(tc.labels); got != tc.want {
				t.Errorf("IsNoAutoDispatch(%v) = %v, want %v", tc.labels, got, tc.want)
			}
		})
	}
}

func TestFilterNoAutoDispatch(t *testing.T) {
	beads := []PendingBead{
		{ID: "ctx-1", WorkBeadID: "gs-1", Labels: []string{"kind/bug"}},
		{ID: "ctx-2", WorkBeadID: "gs-30s", Labels: []string{"no-auto-dispatch", "human-investigation"}},
		{ID: "ctx-3", WorkBeadID: "gs-3", Labels: nil},
	}
	got, removed := FilterNoAutoDispatch(beads)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if len(got) != 2 {
		t.Fatalf("kept %d beads, want 2", len(got))
	}
	for _, b := range got {
		if b.WorkBeadID == "gs-30s" {
			t.Errorf("no-auto-dispatch bead gs-30s survived filtering: %+v", b)
		}
	}
}

// TestPlanDispatch_SkipsNoAutoDispatch is the regression test for gs-b2a: a
// ready bead carrying no-auto-dispatch must be excluded from the dispatch plan
// even when capacity and batch size would otherwise allow it.
func TestPlanDispatch_SkipsNoAutoDispatch(t *testing.T) {
	ready := []PendingBead{
		{ID: "ctx-1", WorkBeadID: "gs-30s", Labels: []string{"no-auto-dispatch", "human-investigation"}},
	}
	plan := PlanDispatch(10 /*capacity*/, 5 /*batchSize*/, ready)
	if len(plan.ToDispatch) != 0 {
		t.Fatalf("no-auto-dispatch bead was dispatched: %+v", plan.ToDispatch)
	}
	if plan.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", plan.Skipped)
	}
}

// A mixed batch: only the non-labeled bead should be dispatched.
func TestPlanDispatch_MixedNoAutoDispatch(t *testing.T) {
	ready := []PendingBead{
		{ID: "ctx-1", WorkBeadID: "gs-30s", Labels: []string{"no-auto-dispatch"}},
		{ID: "ctx-2", WorkBeadID: "gs-ok", Labels: []string{"kind/bug"}},
	}
	plan := PlanDispatch(10, 5, ready)
	if len(plan.ToDispatch) != 1 {
		t.Fatalf("expected 1 dispatched, got %d: %+v", len(plan.ToDispatch), plan.ToDispatch)
	}
	if plan.ToDispatch[0].WorkBeadID != "gs-ok" {
		t.Errorf("dispatched wrong bead: %+v", plan.ToDispatch[0])
	}
	if plan.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", plan.Skipped)
	}
}
