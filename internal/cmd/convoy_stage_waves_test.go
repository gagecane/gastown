package cmd

import (
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// computeWaves tests (U-06 through U-14)
// ---------------------------------------------------------------------------

// helper: collect all task IDs across all waves
func allWaveTaskIDs(waves []Wave) []string {
	var all []string
	for _, w := range waves {
		all = append(all, w.Tasks...)
	}
	return all
}

// helper: find which wave a task is in (returns -1 if not found)
func waveOf(waves []Wave, taskID string) int {
	for _, w := range waves {
		for _, id := range w.Tasks {
			if id == taskID {
				return w.Number
			}
		}
	}
	return -1
}

// U-06: 3 independent tasks (no deps) → all Wave 1
func TestComputeWaves_AllIndependent(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Type: "task"},
		"b": {ID: "b", Type: "task"},
		"c": {ID: "c", Type: "task"},
	}}
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) != 1 {
		t.Fatalf("expected 1 wave, got %d: %+v", len(waves), waves)
	}
	if waves[0].Number != 1 {
		t.Fatalf("expected wave number 1, got %d", waves[0].Number)
	}
	if len(waves[0].Tasks) != 3 {
		t.Fatalf("expected 3 tasks in wave 1, got %d: %v", len(waves[0].Tasks), waves[0].Tasks)
	}
	// Tasks should be sorted for determinism
	expected := []string{"a", "b", "c"}
	for i, id := range waves[0].Tasks {
		if id != expected[i] {
			t.Errorf("wave 1 task[%d] = %q, want %q", i, id, expected[i])
		}
	}
}

// U-07: Linear chain A→B→C → 3 waves
func TestComputeWaves_LinearChain(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Type: "task", Blocks: []string{"b"}},
		"b": {ID: "b", Type: "task", BlockedBy: []string{"a"}, Blocks: []string{"c"}},
		"c": {ID: "c", Type: "task", BlockedBy: []string{"b"}},
	}}
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) != 3 {
		t.Fatalf("expected 3 waves, got %d: %+v", len(waves), waves)
	}
	// Wave 1=[a], Wave 2=[b], Wave 3=[c]
	checks := []struct {
		waveNum int
		tasks   []string
	}{
		{1, []string{"a"}},
		{2, []string{"b"}},
		{3, []string{"c"}},
	}
	for _, c := range checks {
		w := waves[c.waveNum-1]
		if w.Number != c.waveNum {
			t.Errorf("wave %d: got number %d", c.waveNum, w.Number)
		}
		if fmt.Sprintf("%v", w.Tasks) != fmt.Sprintf("%v", c.tasks) {
			t.Errorf("wave %d: got tasks %v, want %v", c.waveNum, w.Tasks, c.tasks)
		}
	}
}

// U-08: Diamond deps → correct waves. A→B, A→C, B→D, C→D = 3 waves: [A], [B,C], [D]
func TestComputeWaves_Diamond(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Type: "task", Blocks: []string{"b", "c"}},
		"b": {ID: "b", Type: "task", BlockedBy: []string{"a"}, Blocks: []string{"d"}},
		"c": {ID: "c", Type: "task", BlockedBy: []string{"a"}, Blocks: []string{"d"}},
		"d": {ID: "d", Type: "task", BlockedBy: []string{"b", "c"}},
	}}
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) != 3 {
		t.Fatalf("expected 3 waves, got %d: %+v", len(waves), waves)
	}
	// Wave 1=[a], Wave 2=[b,c], Wave 3=[d]
	if fmt.Sprintf("%v", waves[0].Tasks) != "[a]" {
		t.Errorf("wave 1: got %v, want [a]", waves[0].Tasks)
	}
	if fmt.Sprintf("%v", waves[1].Tasks) != "[b c]" {
		t.Errorf("wave 2: got %v, want [b c]", waves[1].Tasks)
	}
	if fmt.Sprintf("%v", waves[2].Tasks) != "[d]" {
		t.Errorf("wave 3: got %v, want [d]", waves[2].Tasks)
	}
}

// U-09: Mixed parallel + serial. A→B, C (independent), B→D = waves: [A,C], [B], [D]
func TestComputeWaves_MixedParallelSerial(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Type: "task", Blocks: []string{"b"}},
		"b": {ID: "b", Type: "task", BlockedBy: []string{"a"}, Blocks: []string{"d"}},
		"c": {ID: "c", Type: "task"},
		"d": {ID: "d", Type: "task", BlockedBy: []string{"b"}},
	}}
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) != 3 {
		t.Fatalf("expected 3 waves, got %d: %+v", len(waves), waves)
	}
	// Wave 1=[a,c], Wave 2=[b], Wave 3=[d]
	if fmt.Sprintf("%v", waves[0].Tasks) != "[a c]" {
		t.Errorf("wave 1: got %v, want [a c]", waves[0].Tasks)
	}
	if fmt.Sprintf("%v", waves[1].Tasks) != "[b]" {
		t.Errorf("wave 2: got %v, want [b]", waves[1].Tasks)
	}
	if fmt.Sprintf("%v", waves[2].Tasks) != "[d]" {
		t.Errorf("wave 3: got %v, want [d]", waves[2].Tasks)
	}
}

// U-11: Excludes epics from waves
func TestComputeWaves_ExcludesEpics(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1": {ID: "epic-1", Type: "epic"},
		"task-1": {ID: "task-1", Type: "task"},
	}}
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) != 1 {
		t.Fatalf("expected 1 wave, got %d", len(waves))
	}
	if len(waves[0].Tasks) != 1 || waves[0].Tasks[0] != "task-1" {
		t.Errorf("wave 1: got %v, want [task-1]", waves[0].Tasks)
	}
	// epic should not appear in any wave
	if waveOf(waves, "epic-1") != -1 {
		t.Error("epic-1 should not be in any wave")
	}
}

// U-12: Excludes non-slingable types (decision, epic, etc.)
func TestComputeWaves_ExcludesNonSlingable(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"d1":     {ID: "d1", Type: "decision"},
		"e1":     {ID: "e1", Type: "epic"},
		"task-1": {ID: "task-1", Type: "task"},
		"bug-1":  {ID: "bug-1", Type: "bug"},
		"feat-1": {ID: "feat-1", Type: "feature"},
		"ch-1":   {ID: "ch-1", Type: "chore"},
	}}
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) != 1 {
		t.Fatalf("expected 1 wave, got %d", len(waves))
	}
	// Only slingable types in the wave
	all := allWaveTaskIDs(waves)
	if len(all) != 4 {
		t.Fatalf("expected 4 slingable tasks, got %d: %v", len(all), all)
	}
	// decision and epic should not appear
	for _, id := range all {
		if id == "d1" || id == "e1" {
			t.Errorf("non-slingable %q should not appear in waves", id)
		}
	}
}

// #2141: decision beads block downstream tasks even though decisions aren't slingable.
// A task blocked by an open decision must NOT appear in Wave 1.
func TestComputeWaves_DecisionBlocksTask(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"d1":     {ID: "d1", Type: "decision", Status: "open", Blocks: []string{"task-1"}},
		"task-1": {ID: "task-1", Type: "task", Status: "open", BlockedBy: []string{"d1"}},
		"task-2": {ID: "task-2", Type: "task", Status: "open"},
	}}
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) < 1 {
		t.Fatalf("expected at least 1 wave, got %d", len(waves))
	}
	wave1Tasks := waves[0].Tasks
	for _, id := range wave1Tasks {
		if id == "task-1" {
			t.Errorf("task-1 should NOT be in Wave 1 — it's blocked by decision d1")
		}
		if id == "d1" {
			t.Errorf("decision d1 should NOT appear in any wave (not slingable)")
		}
	}
	found := false
	for _, id := range wave1Tasks {
		if id == "task-2" {
			found = true
		}
	}
	if !found {
		t.Errorf("task-2 should be in Wave 1, got: %v", wave1Tasks)
	}
	for _, w := range waves {
		for _, id := range w.Tasks {
			if id == "d1" {
				t.Errorf("decision d1 should not appear in wave %d", w.Number)
			}
		}
	}
}

// #2141: closed decision beads do NOT block downstream tasks.
func TestComputeWaves_ClosedDecisionDoesNotBlock(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"d1":     {ID: "d1", Type: "decision", Status: "closed", Blocks: []string{"task-1"}},
		"task-1": {ID: "task-1", Type: "task", Status: "open", BlockedBy: []string{"d1"}},
	}}
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) != 1 {
		t.Fatalf("expected 1 wave, got %d", len(waves))
	}
	if len(waves[0].Tasks) != 1 || waves[0].Tasks[0] != "task-1" {
		t.Errorf("task-1 should be in Wave 1 (decision is closed), got: %v", waves[0].Tasks)
	}
}

// U-13: parent-child deps don't create execution edges
func TestComputeWaves_ParentChildNotExecution(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1": {ID: "epic-1", Type: "epic", Children: []string{"task-1", "task-2"}},
		"task-1": {ID: "task-1", Type: "task", Parent: "epic-1"},
		"task-2": {ID: "task-2", Type: "task", Parent: "epic-1"},
	}}
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) != 1 {
		t.Fatalf("expected 1 wave, got %d: %+v", len(waves), waves)
	}
	// Both tasks in Wave 1 (parent-child doesn't block)
	if len(waves[0].Tasks) != 2 {
		t.Fatalf("expected 2 tasks in wave 1, got %d: %v", len(waves[0].Tasks), waves[0].Tasks)
	}
	if waveOf(waves, "task-1") != 1 || waveOf(waves, "task-2") != 1 {
		t.Errorf("both tasks should be in wave 1, got task-1=%d, task-2=%d",
			waveOf(waves, "task-1"), waveOf(waves, "task-2"))
	}
}

// U-14: Empty DAG (no slingable tasks) → error
func TestComputeWaves_EmptyDAG(t *testing.T) {
	// Completely empty
	dag1 := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{}}
	_, _, err := computeWaves(dag1)
	if err == nil {
		t.Error("expected error for empty DAG, got nil")
	}

	// Only non-slingable types
	dag2 := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1":     {ID: "epic-1", Type: "epic"},
		"decision-1": {ID: "decision-1", Type: "decision"},
	}}
	_, _, err = computeWaves(dag2)
	if err == nil {
		t.Error("expected error for DAG with only non-slingable types, got nil")
	}
}
