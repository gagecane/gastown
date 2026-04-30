package cmd

import (
	"fmt"
	"runtime"
	"sort"
	"testing"
)

// ---------------------------------------------------------------------------
// collectBeads tests — Epic DAG walking (IT-01 through IT-04)
// ---------------------------------------------------------------------------

// IT-01: Epic walk collects all descendants across 3 levels.
// Tree: gt-epic → {gt-sub (epic), gt-task1 (task)}
//
//	gt-sub → {gt-task2 (task), gt-task3 (task)}
func TestEpicWalk_CollectsAllDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Epic("gt-epic", "Root Epic").
		Epic("gt-sub", "Sub Epic").ParentOf("gt-epic").
		Task("gt-task1", "Task 1", withRig("gastown")).ParentOf("gt-epic").
		Task("gt-task2", "Task 2", withRig("gastown")).ParentOf("gt-sub").
		Task("gt-task3", "Task 3", withRig("gastown")).ParentOf("gt-sub")

	dag.Setup(t)

	input := &StageInput{Kind: StageInputEpic, IDs: []string{"gt-epic"}}
	beads, _, err := collectBeads(input)
	if err != nil {
		t.Fatalf("collectBeads: %v", err)
	}

	// Should have 5 beads: epic, sub, task1, task2, task3
	if len(beads) != 5 {
		ids := make([]string, len(beads))
		for i, b := range beads {
			ids[i] = b.ID
		}
		t.Errorf("expected 5 beads, got %d: %v", len(beads), ids)
	}

	// Verify all expected IDs present.
	idSet := make(map[string]bool)
	for _, b := range beads {
		idSet[b.ID] = true
	}
	for _, want := range []string{"gt-epic", "gt-sub", "gt-task1", "gt-task2", "gt-task3"} {
		if !idSet[want] {
			t.Errorf("missing bead %q in collected set", want)
		}
	}
}

// IT-02: Nonexistent epic bead returns error.
func TestEpicWalk_NonexistentBeadErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	// Set up a DAG with only one bead so "gt-missing" doesn't exist.
	dag := newTestDAG(t).
		Task("gt-exists", "Existing task", withRig("gastown"))
	dag.Setup(t)

	input := &StageInput{Kind: StageInputEpic, IDs: []string{"gt-missing"}}
	_, _, err := collectBeads(input)
	if err == nil {
		t.Fatal("expected error for nonexistent epic, got nil")
	}
}

// IT-03: Task list analyzes only given tasks.
func TestTaskListWalk_AnalyzesOnlyGiven(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Task("gt-a", "Task A", withRig("gastown")).
		Task("gt-b", "Task B", withRig("gastown")).BlockedBy("gt-a").
		Task("gt-c", "Task C", withRig("gastown")) // not requested
	dag.Setup(t)

	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b"}}
	beads, deps, err := collectBeads(input)
	if err != nil {
		t.Fatalf("collectBeads: %v", err)
	}

	// Should have exactly 2 beads.
	if len(beads) != 2 {
		ids := make([]string, len(beads))
		for i, b := range beads {
			ids[i] = b.ID
		}
		t.Errorf("expected 2 beads, got %d: %v", len(beads), ids)
	}

	// Verify only gt-a and gt-b.
	idSet := make(map[string]bool)
	for _, b := range beads {
		idSet[b.ID] = true
	}
	if !idSet["gt-a"] || !idSet["gt-b"] {
		t.Errorf("expected gt-a and gt-b, got %v", idSet)
	}
	if idSet["gt-c"] {
		t.Error("gt-c should not be in collected beads")
	}

	// gt-b should have a dep on gt-a.
	foundDep := false
	for _, d := range deps {
		if d.IssueID == "gt-b" && d.DependsOnID == "gt-a" && d.Type == "blocks" {
			foundDep = true
		}
	}
	if !foundDep {
		t.Errorf("expected dep gt-b blocked-by gt-a, got deps: %+v", deps)
	}
}

// IT-04: Convoy reads tracked beads.
func TestConvoyWalk_ReadsTrackedBeads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Convoy("gt-convoy", "Test Convoy").
		Task("gt-t1", "Tracked 1", withRig("gastown")).TrackedBy("gt-convoy").
		Task("gt-t2", "Tracked 2", withRig("gastown")).TrackedBy("gt-convoy")
	dag.Setup(t)

	input := &StageInput{Kind: StageInputConvoy, IDs: []string{"gt-convoy"}}
	beads, _, err := collectBeads(input)
	if err != nil {
		t.Fatalf("collectBeads: %v", err)
	}

	// Should have 2 tracked beads (convoy itself is not returned as a bead to stage).
	if len(beads) != 2 {
		ids := make([]string, len(beads))
		for i, b := range beads {
			ids[i] = b.ID
		}
		t.Errorf("expected 2 beads, got %d: %v", len(beads), ids)
	}

	idSet := make(map[string]bool)
	for _, b := range beads {
		idSet[b.ID] = true
	}
	if !idSet["gt-t1"] || !idSet["gt-t2"] {
		t.Errorf("expected gt-t1 and gt-t2 in tracked beads, got %v", idSet)
	}
}

// IT-05: Epic walk collects deps across the tree.
func TestEpicWalk_CollectsDeps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Epic("gt-epic", "Root Epic").
		Task("gt-t1", "Task 1", withRig("gastown")).ParentOf("gt-epic").
		Task("gt-t2", "Task 2", withRig("gastown")).ParentOf("gt-epic").BlockedBy("gt-t1")
	dag.Setup(t)

	input := &StageInput{Kind: StageInputEpic, IDs: []string{"gt-epic"}}
	beads, deps, err := collectBeads(input)
	if err != nil {
		t.Fatalf("collectBeads: %v", err)
	}
	if len(beads) != 3 {
		t.Fatalf("expected 3 beads, got %d", len(beads))
	}

	// Should find the blocks dep and the parent-child deps.
	var depTypes []string
	for _, d := range deps {
		depTypes = append(depTypes, fmt.Sprintf("%s→%s(%s)", d.IssueID, d.DependsOnID, d.Type))
	}
	sort.Strings(depTypes)

	// Expect parent-child deps for gt-t1 and gt-t2, plus blocks dep gt-t2→gt-t1.
	foundBlocks := false
	for _, d := range deps {
		if d.IssueID == "gt-t2" && d.DependsOnID == "gt-t1" && d.Type == "blocks" {
			foundBlocks = true
		}
	}
	if !foundBlocks {
		t.Errorf("expected blocks dep gt-t2→gt-t1, got: %v", depTypes)
	}
}
