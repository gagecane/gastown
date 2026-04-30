package cmd

import "testing"

// ---------------------------------------------------------------------------
// buildConvoyDAG tests (U-15 through U-19)
// ---------------------------------------------------------------------------

// sliceContains checks if a string slice contains a value.
func sliceContains(ss []string, val string) bool {
	for _, s := range ss {
		if s == val {
			return true
		}
	}
	return false
}

// U-15: blocks deps create execution edges
func TestBuildDAG_BlocksCreateEdges(t *testing.T) {
	beads := []BeadInfo{
		{ID: "a", Title: "Task A", Type: "task", Status: "open"},
		{ID: "b", Title: "Task B", Type: "task", Status: "open"},
	}
	deps := []DepInfo{
		{IssueID: "b", DependsOnID: "a", Type: "blocks"},
	}
	dag := buildConvoyDAG(beads, deps)
	if dag == nil {
		t.Fatal("expected non-nil DAG")
	}
	nodeA := dag.Nodes["a"]
	nodeB := dag.Nodes["b"]
	if nodeA == nil || nodeB == nil {
		t.Fatal("expected both nodes to exist")
	}
	if !sliceContains(nodeA.Blocks, "b") {
		t.Errorf("a.Blocks should contain 'b', got %v", nodeA.Blocks)
	}
	if !sliceContains(nodeB.BlockedBy, "a") {
		t.Errorf("b.BlockedBy should contain 'a', got %v", nodeB.BlockedBy)
	}
}

// U-16: conditional-blocks create execution edges (same as blocks for DAG purposes)
func TestBuildDAG_ConditionalBlocksCreateEdges(t *testing.T) {
	beads := []BeadInfo{
		{ID: "a", Title: "Task A", Type: "task", Status: "open"},
		{ID: "b", Title: "Task B", Type: "task", Status: "open"},
	}
	deps := []DepInfo{
		{IssueID: "b", DependsOnID: "a", Type: "conditional-blocks"},
	}
	dag := buildConvoyDAG(beads, deps)
	nodeA := dag.Nodes["a"]
	nodeB := dag.Nodes["b"]
	if !sliceContains(nodeA.Blocks, "b") {
		t.Errorf("a.Blocks should contain 'b' for conditional-blocks, got %v", nodeA.Blocks)
	}
	if !sliceContains(nodeB.BlockedBy, "a") {
		t.Errorf("b.BlockedBy should contain 'a' for conditional-blocks, got %v", nodeB.BlockedBy)
	}
}

// U-17: waits-for creates execution edges
func TestBuildDAG_WaitsForCreateEdges(t *testing.T) {
	beads := []BeadInfo{
		{ID: "x", Title: "Task X", Type: "task", Status: "open"},
		{ID: "y", Title: "Task Y", Type: "task", Status: "open"},
	}
	deps := []DepInfo{
		{IssueID: "y", DependsOnID: "x", Type: "waits-for"},
	}
	dag := buildConvoyDAG(beads, deps)
	nodeX := dag.Nodes["x"]
	nodeY := dag.Nodes["y"]
	if !sliceContains(nodeX.Blocks, "y") {
		t.Errorf("x.Blocks should contain 'y' for waits-for, got %v", nodeX.Blocks)
	}
	if !sliceContains(nodeY.BlockedBy, "x") {
		t.Errorf("y.BlockedBy should contain 'x' for waits-for, got %v", nodeY.BlockedBy)
	}
}

// U-18: parent-child recorded as hierarchy but NO execution edge
func TestBuildDAG_ParentChildNoExecutionEdge(t *testing.T) {
	beads := []BeadInfo{
		{ID: "epic-1", Title: "Root", Type: "epic", Status: "open"},
		{ID: "task-1", Title: "Child", Type: "task", Status: "open"},
	}
	deps := []DepInfo{
		{IssueID: "task-1", DependsOnID: "epic-1", Type: "parent-child"},
	}
	dag := buildConvoyDAG(beads, deps)
	epicNode := dag.Nodes["epic-1"]
	taskNode := dag.Nodes["task-1"]
	// Hierarchy should be set
	if !sliceContains(epicNode.Children, "task-1") {
		t.Errorf("epic-1.Children should contain 'task-1', got %v", epicNode.Children)
	}
	if taskNode.Parent != "epic-1" {
		t.Errorf("task-1.Parent should be 'epic-1', got %q", taskNode.Parent)
	}
	// Execution edges must NOT be set
	if len(epicNode.Blocks) != 0 {
		t.Errorf("epic-1.Blocks should be empty for parent-child, got %v", epicNode.Blocks)
	}
	if len(taskNode.BlockedBy) != 0 {
		t.Errorf("task-1.BlockedBy should be empty for parent-child, got %v", taskNode.BlockedBy)
	}
}

// U-19: related/tracks deps ignored entirely
func TestBuildDAG_RelatedTracksIgnored(t *testing.T) {
	beads := []BeadInfo{
		{ID: "a", Title: "A", Type: "task", Status: "open"},
		{ID: "b", Title: "B", Type: "task", Status: "open"},
	}
	deps := []DepInfo{
		{IssueID: "a", DependsOnID: "b", Type: "related"},
		{IssueID: "a", DependsOnID: "b", Type: "tracks"},
	}
	dag := buildConvoyDAG(beads, deps)
	nodeA := dag.Nodes["a"]
	nodeB := dag.Nodes["b"]
	if len(nodeA.BlockedBy) != 0 || len(nodeA.Blocks) != 0 {
		t.Errorf("related/tracks should not create edges on a: BlockedBy=%v Blocks=%v", nodeA.BlockedBy, nodeA.Blocks)
	}
	if len(nodeB.BlockedBy) != 0 || len(nodeB.Blocks) != 0 {
		t.Errorf("related/tracks should not create edges on b: BlockedBy=%v Blocks=%v", nodeB.BlockedBy, nodeB.Blocks)
	}
	// Also no hierarchy
	if len(nodeA.Children) != 0 || nodeA.Parent != "" {
		t.Error("related/tracks should not set hierarchy on a")
	}
	if len(nodeB.Children) != 0 || nodeB.Parent != "" {
		t.Error("related/tracks should not set hierarchy on b")
	}
}
