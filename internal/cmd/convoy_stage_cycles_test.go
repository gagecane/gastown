package cmd

import "testing"

// U-01: Simple 2-node cycle A→B→A
func TestDetectCycles_Simple2NodeCycle(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", BlockedBy: []string{"b"}, Blocks: []string{"b"}},
		"b": {ID: "b", BlockedBy: []string{"a"}, Blocks: []string{"a"}},
	}}
	cycle := detectCycles(dag)
	if cycle == nil {
		t.Fatal("expected cycle, got nil")
	}
	// Cycle should contain both "a" and "b"
	if len(cycle) < 2 {
		t.Fatalf("cycle too short: %v", cycle)
	}
}

// U-02: No cycle - linear chain A→B→C
func TestDetectCycles_NoCycleLinearChain(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Blocks: []string{"b"}},
		"b": {ID: "b", BlockedBy: []string{"a"}, Blocks: []string{"c"}},
		"c": {ID: "c", BlockedBy: []string{"b"}},
	}}
	cycle := detectCycles(dag)
	if cycle != nil {
		t.Fatalf("expected no cycle, got: %v", cycle)
	}
}

// U-03: Self-loop A blocks A
func TestDetectCycles_SelfLoop(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", BlockedBy: []string{"a"}, Blocks: []string{"a"}},
	}}
	cycle := detectCycles(dag)
	if cycle == nil {
		t.Fatal("expected cycle for self-loop, got nil")
	}
}

// U-04: Diamond shape (no cycle) - A→B, A→C, B→D, C→D
func TestDetectCycles_DiamondNoCycle(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Blocks: []string{"b", "c"}},
		"b": {ID: "b", BlockedBy: []string{"a"}, Blocks: []string{"d"}},
		"c": {ID: "c", BlockedBy: []string{"a"}, Blocks: []string{"d"}},
		"d": {ID: "d", BlockedBy: []string{"b", "c"}},
	}}
	cycle := detectCycles(dag)
	if cycle != nil {
		t.Fatalf("expected no cycle in diamond, got: %v", cycle)
	}
}

// U-05: Long chain with back-edge - A→B→C→D→B (cycle: B→C→D→B)
func TestDetectCycles_LongChainWithBackEdge(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Blocks: []string{"b"}},
		"b": {ID: "b", BlockedBy: []string{"a", "d"}, Blocks: []string{"c"}},
		"c": {ID: "c", BlockedBy: []string{"b"}, Blocks: []string{"d"}},
		"d": {ID: "d", BlockedBy: []string{"c"}, Blocks: []string{"b"}},
	}}
	cycle := detectCycles(dag)
	if cycle == nil {
		t.Fatal("expected cycle, got nil")
	}
	// Cycle should include b, c, d
	if len(cycle) < 3 {
		t.Fatalf("cycle too short, expected at least b,c,d: %v", cycle)
	}
}
