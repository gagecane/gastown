package cmd

import "testing"

// ---------------------------------------------------------------------------
// Gated task tests — non-slingable blockers
// ---------------------------------------------------------------------------

// Task blocked by open decision → excluded from waves, returned as gated.
func TestComputeWaves_GatedByDecision(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"dec-1":  {ID: "dec-1", Type: "decision", Status: "open", Blocks: []string{"task-1"}},
		"task-1": {ID: "task-1", Type: "task", Status: "open", BlockedBy: []string{"dec-1"}},
		"task-2": {ID: "task-2", Type: "task", Status: "open"},
	}}
	waves, gated, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// task-2 should be in waves, task-1 should be gated
	if len(waves) != 1 || len(waves[0].Tasks) != 1 || waves[0].Tasks[0] != "task-2" {
		t.Errorf("expected wave 1 = [task-2], got %+v", waves)
	}
	if len(gated) != 1 || gated[0].TaskID != "task-1" {
		t.Errorf("expected gated = [task-1], got %+v", gated)
	}
	if len(gated[0].GatedBy) != 1 || gated[0].GatedBy[0] != "dec-1" {
		t.Errorf("expected gated by dec-1, got %v", gated[0].GatedBy)
	}
}

// task-A gated by decision, task-B depends on task-A → both gated.
func TestComputeWaves_GatedTransitive(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"dec-1":  {ID: "dec-1", Type: "decision", Status: "open", Blocks: []string{"task-a"}},
		"task-a": {ID: "task-a", Type: "task", Status: "open", BlockedBy: []string{"dec-1"}, Blocks: []string{"task-b"}},
		"task-b": {ID: "task-b", Type: "task", Status: "open", BlockedBy: []string{"task-a"}},
		"task-c": {ID: "task-c", Type: "task", Status: "open"},
	}}
	waves, gated, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// task-c in waves, task-a and task-b gated
	if len(waves) != 1 || len(waves[0].Tasks) != 1 || waves[0].Tasks[0] != "task-c" {
		t.Errorf("expected wave 1 = [task-c], got %+v", waves)
	}
	if len(gated) != 2 {
		t.Fatalf("expected 2 gated tasks, got %d: %+v", len(gated), gated)
	}
	gatedIDs := map[string]bool{}
	for _, g := range gated {
		gatedIDs[g.TaskID] = true
	}
	if !gatedIDs["task-a"] || !gatedIDs["task-b"] {
		t.Errorf("expected task-a and task-b gated, got %v", gatedIDs)
	}
	// task-a should have direct gate, task-b should have empty GatedBy (transitive)
	for _, g := range gated {
		if g.TaskID == "task-a" && (len(g.GatedBy) != 1 || g.GatedBy[0] != "dec-1") {
			t.Errorf("task-a should be gated by dec-1, got %v", g.GatedBy)
		}
		if g.TaskID == "task-b" && len(g.GatedBy) != 0 {
			t.Errorf("task-b should be transitively gated (empty GatedBy), got %v", g.GatedBy)
		}
	}
}

// Task blocked by closed decision → in waves (gate resolved).
func TestComputeWaves_ResolvedDecision(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"dec-1":  {ID: "dec-1", Type: "decision", Status: "closed", Blocks: []string{"task-1"}},
		"task-1": {ID: "task-1", Type: "task", Status: "open", BlockedBy: []string{"dec-1"}},
	}}
	waves, gated, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gated) != 0 {
		t.Errorf("expected no gated tasks (decision closed), got %+v", gated)
	}
	if len(waves) != 1 || len(waves[0].Tasks) != 1 || waves[0].Tasks[0] != "task-1" {
		t.Errorf("expected wave 1 = [task-1], got %+v", waves)
	}
}

// Task blocked by tombstoned decision → in waves.
func TestComputeWaves_TombstoneDecision(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"dec-1":  {ID: "dec-1", Type: "decision", Status: "tombstone", Blocks: []string{"task-1"}},
		"task-1": {ID: "task-1", Type: "task", Status: "open", BlockedBy: []string{"dec-1"}},
	}}
	waves, gated, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gated) != 0 {
		t.Errorf("expected no gated tasks (decision tombstoned), got %+v", gated)
	}
	if len(waves) != 1 || len(waves[0].Tasks) != 1 || waves[0].Tasks[0] != "task-1" {
		t.Errorf("expected wave 1 = [task-1], got %+v", waves)
	}
}

// Task blocked by open epic → gated.
func TestComputeWaves_GatedByEpic(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1": {ID: "epic-1", Type: "epic", Status: "open", Blocks: []string{"task-1"}},
		"task-1": {ID: "task-1", Type: "task", Status: "open", BlockedBy: []string{"epic-1"}},
		"task-2": {ID: "task-2", Type: "task", Status: "open"},
	}}
	waves, gated, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gated) != 1 || gated[0].TaskID != "task-1" {
		t.Errorf("expected task-1 gated by epic, got %+v", gated)
	}
	if len(waves) != 1 || waves[0].Tasks[0] != "task-2" {
		t.Errorf("expected wave 1 = [task-2], got %+v", waves)
	}
}

// All slingable tasks gated → empty waves, all returned as gated.
func TestComputeWaves_AllGated(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"dec-1":  {ID: "dec-1", Type: "decision", Status: "open", Blocks: []string{"task-1"}},
		"task-1": {ID: "task-1", Type: "task", Status: "open", BlockedBy: []string{"dec-1"}},
	}}
	waves, gated, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) != 0 {
		t.Errorf("expected 0 waves when all tasks gated, got %d", len(waves))
	}
	if len(gated) != 1 {
		t.Errorf("expected 1 gated task, got %d", len(gated))
	}
}

// merge-blocks creates execution edge in DAG.
func TestBuildConvoyDAG_MergeBlocks(t *testing.T) {
	beads := []BeadInfo{
		{ID: "mr-1", Title: "MR", Type: "task", Status: "open"},
		{ID: "task-1", Title: "Task", Type: "task", Status: "open"},
	}
	deps := []DepInfo{
		{IssueID: "task-1", DependsOnID: "mr-1", Type: "merge-blocks"},
	}
	dag := buildConvoyDAG(beads, deps)

	if node := dag.Nodes["task-1"]; node == nil {
		t.Fatal("task-1 not in DAG")
	} else if len(node.BlockedBy) != 1 || node.BlockedBy[0] != "mr-1" {
		t.Errorf("expected task-1 blocked by mr-1, got %v", node.BlockedBy)
	}
	if node := dag.Nodes["mr-1"]; node == nil {
		t.Fatal("mr-1 not in DAG")
	} else if len(node.Blocks) != 1 || node.Blocks[0] != "task-1" {
		t.Errorf("expected mr-1 blocks task-1, got %v", node.Blocks)
	}
}

// Task blocked by decision → not flagged as orphan.
func TestDetectOrphans_DecisionGatedNotOrphan(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"dec-1":  {ID: "dec-1", Type: "decision", Status: "open", Blocks: []string{"task-1"}},
		"task-1": {ID: "task-1", Type: "task", Status: "open", BlockedBy: []string{"dec-1"}},
	}}
	input := &StageInput{Kind: StageInputEpic}
	findings := detectOrphans(dag, input)
	for _, f := range findings {
		if f.Category == "orphan" && f.BeadIDs[0] == "task-1" {
			t.Error("task-1 should not be flagged as orphan — it is blocked by decision dec-1")
		}
	}
}

