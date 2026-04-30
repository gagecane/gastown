package cmd

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// renderDAGTree tests (gt-csl.4.1)
// ---------------------------------------------------------------------------

// U-28: Task-list input renders flat list
func TestRenderDAGTree_TaskListFlat(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Title: "Task A", Type: "task", Status: "open", Rig: "gastown"},
		"gt-b": {ID: "gt-b", Title: "Task B", Type: "task", Status: "open", Rig: "gastown"},
		"gt-c": {ID: "gt-c", Title: "Task C", Type: "bug", Status: "open", Rig: "gastown"},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b", "gt-c"}}
	output := renderDAGTree(dag, input)

	// All 3 IDs must appear
	for _, id := range []string{"gt-a", "gt-b", "gt-c"} {
		if !strings.Contains(output, id) {
			t.Errorf("output should contain %q, got:\n%s", id, output)
		}
	}

	// No tree characters in flat list
	if strings.Contains(output, "├── ") || strings.Contains(output, "└── ") {
		t.Errorf("flat task list should not contain tree characters, got:\n%s", output)
	}
}

// U-29: Epic input renders full tree with indentation
func TestRenderDAGTree_EpicTree(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"root-epic": {ID: "root-epic", Title: "Root Epic", Type: "epic", Status: "open",
			Children: []string{"sub-epic", "task-1"}},
		"sub-epic": {ID: "sub-epic", Title: "Sub Epic", Type: "epic", Status: "open",
			Parent: "root-epic", Children: []string{"task-2", "task-3"}},
		"task-1": {ID: "task-1", Title: "Task One", Type: "task", Status: "open",
			Rig: "gastown", Parent: "root-epic"},
		"task-2": {ID: "task-2", Title: "Task Two", Type: "task", Status: "open",
			Rig: "gastown", Parent: "sub-epic"},
		"task-3": {ID: "task-3", Title: "Task Three", Type: "task", Status: "open",
			Rig: "gastown", Parent: "sub-epic"},
	}}
	input := &StageInput{Kind: StageInputEpic, IDs: []string{"root-epic"}}
	output := renderDAGTree(dag, input)

	// Root epic appears at top level (first line, no tree prefix)
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("expected non-empty output")
	}
	if !strings.HasPrefix(lines[0], "root-epic") {
		t.Errorf("first line should start with root-epic, got: %q", lines[0])
	}

	// sub-epic is indented under root
	if !strings.Contains(output, "sub-epic") {
		t.Error("output should contain sub-epic")
	}

	// task-2 and task-3 are indented under sub-epic
	if !strings.Contains(output, "task-2") || !strings.Contains(output, "task-3") {
		t.Error("output should contain task-2 and task-3")
	}

	// Tree characters must be present
	if !strings.Contains(output, "├── ") && !strings.Contains(output, "└── ") {
		t.Errorf("epic tree should contain tree characters, got:\n%s", output)
	}

	// Verify indentation increases: task-2/task-3 should have more prefix than sub-epic
	subEpicIndent := -1
	task2Indent := -1
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " │├└──")
		indent := len(line) - len(trimmed)
		if strings.Contains(line, "sub-epic") {
			subEpicIndent = indent
		}
		if strings.Contains(line, "task-2") {
			task2Indent = indent
		}
	}
	if subEpicIndent >= 0 && task2Indent >= 0 && task2Indent <= subEpicIndent {
		t.Errorf("task-2 indent (%d) should be greater than sub-epic indent (%d)", task2Indent, subEpicIndent)
	}
}

// U-36: Each node shows ID, type, title, rig, status
func TestRenderDAGTree_NodeInfo(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-abc": {ID: "gt-abc", Title: "My Task", Type: "task", Status: "open", Rig: "gastown"},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-abc"}}
	output := renderDAGTree(dag, input)

	// Verify all fields appear in the output
	for _, want := range []string{"gt-abc", "task", "My Task", "gastown", "open"} {
		if !strings.Contains(output, want) {
			t.Errorf("output should contain %q, got:\n%s", want, output)
		}
	}
}

// U-37: Blocked tasks show blockers inline
func TestRenderDAGTree_BlockedShowsBlockers(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"task-a": {ID: "task-a", Title: "Task A", Type: "task", Status: "open", Rig: "gastown",
			Blocks: []string{"task-b"}},
		"task-b": {ID: "task-b", Title: "Task B", Type: "task", Status: "open", Rig: "gastown",
			BlockedBy: []string{"task-a"}},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"task-a", "task-b"}}
	output := renderDAGTree(dag, input)

	// task-b's line should contain "blocked by" and "task-a"
	lines := strings.Split(output, "\n")
	foundBlocker := false
	for _, line := range lines {
		if strings.Contains(line, "task-b") && strings.Contains(line, "blocked by") && strings.Contains(line, "task-a") {
			foundBlocker = true
		}
	}
	if !foundBlocker {
		t.Errorf("task-b should show 'blocked by' with 'task-a', got:\n%s", output)
	}
}

// SN-01: Full tree for nested epic structure (3-level deep)
func TestRenderDAGTree_NestedEpic(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"root-epic": {ID: "root-epic", Title: "Root", Type: "epic", Status: "open",
			Children: []string{"sub-epic"}},
		"sub-epic": {ID: "sub-epic", Title: "Sub", Type: "epic", Status: "open",
			Parent: "root-epic", Children: []string{"sub-sub-epic"}},
		"sub-sub-epic": {ID: "sub-sub-epic", Title: "SubSub", Type: "epic", Status: "open",
			Parent: "sub-epic", Children: []string{"deep-task"}},
		"deep-task": {ID: "deep-task", Title: "Deep Task", Type: "task", Status: "open",
			Rig: "gastown", Parent: "sub-sub-epic"},
	}}
	input := &StageInput{Kind: StageInputEpic, IDs: []string{"root-epic"}}
	output := renderDAGTree(dag, input)

	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines (root + 3 descendants), got %d:\n%s", len(lines), output)
	}

	// Verify indentation increases at each level.
	// Root has no indent (line 0), sub-epic has some, sub-sub-epic more, deep-task most.
	// We measure indent by counting leading non-alpha chars.
	indentOf := func(line string) int {
		for i, ch := range line {
			if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') {
				return i
			}
		}
		return len(line)
	}

	indent0 := indentOf(lines[0]) // root-epic
	indent1 := indentOf(lines[1]) // sub-epic
	indent2 := indentOf(lines[2]) // sub-sub-epic
	indent3 := indentOf(lines[3]) // deep-task

	if indent1 <= indent0 {
		t.Errorf("sub-epic indent (%d) should be > root indent (%d)", indent1, indent0)
	}
	if indent2 <= indent1 {
		t.Errorf("sub-sub-epic indent (%d) should be > sub-epic indent (%d)", indent2, indent1)
	}
	if indent3 <= indent2 {
		t.Errorf("deep-task indent (%d) should be > sub-sub-epic indent (%d)", indent3, indent2)
	}
}

// IT-40: Tree displayed before wave table (ordering contract)
func TestRenderDAGTree_OutputOrdering(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Title: "Task A", Type: "task", Status: "open", Rig: "gastown",
			Blocks: []string{"gt-b"}},
		"gt-b": {ID: "gt-b", Title: "Task B", Type: "task", Status: "open", Rig: "gastown",
			BlockedBy: []string{"gt-a"}},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b"}}

	treeOutput := renderDAGTree(dag, input)

	// Tree output should NOT contain wave table markers (header separator, wave numbers in columns)
	// The wave table uses "─" separator lines and columnar "Wave" header.
	if strings.Contains(treeOutput, "──────") {
		t.Errorf("tree output should not contain wave table separator, got:\n%s", treeOutput)
	}
	if strings.Contains(treeOutput, "Wave") && strings.Contains(treeOutput, "Blocked By") {
		t.Errorf("tree output should not contain wave table header, got:\n%s", treeOutput)
	}

	// Verify tree output is non-empty and contains expected bead IDs
	if !strings.Contains(treeOutput, "gt-a") || !strings.Contains(treeOutput, "gt-b") {
		t.Errorf("tree output should contain task IDs, got:\n%s", treeOutput)
	}

	// Simulate the full output: tree + wave table concatenation
	waves := []Wave{
		{Number: 1, Tasks: []string{"gt-a"}},
		{Number: 2, Tasks: []string{"gt-b"}},
	}
	waveOutput := renderWaveTable(waves, dag)

	fullOutput := treeOutput + "\n" + waveOutput

	// Tree content (task IDs without wave table formatting) appears before wave table content
	treeFirstID := strings.Index(fullOutput, "gt-a")
	waveTableStart := strings.Index(fullOutput, "Wave")
	if treeFirstID < 0 || waveTableStart < 0 {
		t.Fatalf("expected both tree content and wave table in full output:\n%s", fullOutput)
	}
	if treeFirstID >= waveTableStart {
		t.Errorf("tree content (at %d) should appear before wave table (at %d) in full output", treeFirstID, waveTableStart)
	}
}
