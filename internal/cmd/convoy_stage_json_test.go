package cmd

import (
	"encoding/json"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// JSON output mode tests (gt-csl.4.3)
// ---------------------------------------------------------------------------

// U-31: JSON output: valid JSON with all required fields present.
// Build a clean DAG (no errors, no warnings), call the JSON rendering
// function, verify valid JSON with all fields.
func TestJSONOutput_ValidWithAllFields(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Title: "Task A", Type: "task", Status: "open", Rig: "gastown",
			Blocks: []string{"gt-b"}},
		"gt-b": {ID: "gt-b", Title: "Task B", Type: "task", Status: "open", Rig: "gastown",
			BlockedBy: []string{"gt-a"}},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b"}}

	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	result := StageResult{
		Status:   "staged_ready",
		ConvoyID: "hq-cv-test1",
		Errors:   buildFindingsJSON(nil),
		Warnings: buildFindingsJSON(nil),
		Waves:    buildWavesJSON(waves, dag),
		Tree:     buildTreeJSON(dag, input),
	}

	out, err := renderJSON(result)
	if err != nil {
		t.Fatalf("renderJSON: %v", err)
	}

	// Must be valid JSON.
	var parsed StageResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\nraw:\n%s", err, out)
	}

	// All required fields present.
	if parsed.Status != "staged_ready" {
		t.Errorf("status = %q, want %q", parsed.Status, "staged_ready")
	}
	if parsed.ConvoyID != "hq-cv-test1" {
		t.Errorf("convoy_id = %q, want %q", parsed.ConvoyID, "hq-cv-test1")
	}
	if parsed.Errors == nil {
		t.Error("errors should not be nil (should be empty array)")
	}
	if parsed.Warnings == nil {
		t.Error("warnings should not be nil (should be empty array)")
	}
	if len(parsed.Waves) == 0 {
		t.Error("waves should not be empty")
	}
	if len(parsed.Tree) == 0 {
		t.Error("tree should not be empty")
	}

	// Verify waves contain task details.
	foundA := false
	foundB := false
	for _, w := range parsed.Waves {
		for _, task := range w.Tasks {
			if task.ID == "gt-a" {
				foundA = true
				if task.Title != "Task A" {
					t.Errorf("gt-a title = %q, want %q", task.Title, "Task A")
				}
				if task.Rig != "gastown" {
					t.Errorf("gt-a rig = %q, want %q", task.Rig, "gastown")
				}
			}
			if task.ID == "gt-b" {
				foundB = true
				if len(task.BlockedBy) == 0 || task.BlockedBy[0] != "gt-a" {
					t.Errorf("gt-b blocked_by = %v, want [gt-a]", task.BlockedBy)
				}
			}
		}
	}
	if !foundA {
		t.Error("wave tasks should contain gt-a")
	}
	if !foundB {
		t.Error("wave tasks should contain gt-b")
	}
}

// U-32: JSON output: errors array populated on failure.
// Build a DAG with a cycle, verify the errors array has the cycle finding.
func TestJSONOutput_ErrorsPopulatedOnCycle(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Type: "task", Rig: "gastown",
			Blocks: []string{"gt-b"}, BlockedBy: []string{"gt-b"}},
		"gt-b": {ID: "gt-b", Type: "task", Rig: "gastown",
			Blocks: []string{"gt-a"}, BlockedBy: []string{"gt-a"}},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b"}}

	errFindings := detectErrors(dag)
	warnFindings := detectWarnings(dag, input)
	errs, warns := categorizeFindings(append(errFindings, warnFindings...))

	if len(errs) == 0 {
		t.Fatal("expected cycle error")
	}

	result := StageResult{
		Status:   "error",
		Errors:   buildFindingsJSON(errs),
		Warnings: buildFindingsJSON(warns),
		Waves:    []WaveJSON{},
		Tree:     buildTreeJSON(dag, input),
	}

	out, err := renderJSON(result)
	if err != nil {
		t.Fatalf("renderJSON: %v", err)
	}

	var parsed StageResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if len(parsed.Errors) == 0 {
		t.Fatal("errors array should not be empty for cycle DAG")
	}

	foundCycle := false
	for _, e := range parsed.Errors {
		if e.Category == "cycle" {
			foundCycle = true
			if len(e.BeadIDs) == 0 {
				t.Error("cycle error should have bead_ids")
			}
			if e.Message == "" {
				t.Error("cycle error should have message")
			}
		}
	}
	if !foundCycle {
		t.Errorf("expected cycle error in errors array, got: %+v", parsed.Errors)
	}
}

// U-33: JSON output: convoy_id empty when errors found.
func TestJSONOutput_ConvoyIDEmptyOnErrors(t *testing.T) {
	result := StageResult{
		Status:   "error",
		ConvoyID: "", // no convoy created
		Errors: []FindingJSON{
			{Category: "cycle", BeadIDs: []string{"a", "b"}, Message: "cycle detected"},
		},
		Warnings: []FindingJSON{},
		Waves:    []WaveJSON{},
		Tree:     []TreeNodeJSON{},
	}

	out, err := renderJSON(result)
	if err != nil {
		t.Fatalf("renderJSON: %v", err)
	}

	var parsed StageResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if parsed.ConvoyID != "" {
		t.Errorf("convoy_id should be empty on error, got %q", parsed.ConvoyID)
	}
	if parsed.Status != "error" {
		t.Errorf("status should be 'error', got %q", parsed.Status)
	}
}

// IT-21: --json flag outputs valid JSON to stdout.
// Verifies the flag is registered on the command.
func TestJSONFlag_RegisteredOnCommand(t *testing.T) {
	flag := convoyStageCmd.Flags().Lookup("json")
	if flag == nil {
		t.Fatal("--json flag not registered on convoyStageCmd")
	}
	if flag.DefValue != "false" {
		t.Errorf("--json default should be false, got %q", flag.DefValue)
	}
}

// IT-22: --json output: no human-readable text on stdout.
// Verify JSON mode suppresses tree/table/error output.
// Note: rigFromBeadID() is a stub returning "", so tasks get no-rig errors.
// This test verifies that even on error, JSON mode outputs JSON (not human text).
func TestJSONOutput_NoHumanReadableText(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	testDAG := newTestDAG(t).
		Task("gt-j1", "JSON Task 1", withRig("gastown")).
		Task("gt-j2", "JSON Task 2", withRig("gastown")).BlockedBy("gt-j1")

	testDAG.Setup(t)

	// Capture stdout by setting convoyStageJSON and running the pipeline.
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Also capture stderr to verify no human-readable errors go there.
	oldStderr := os.Stderr
	rErr, wErr, _ := os.Pipe()
	os.Stderr = wErr

	// Enable JSON mode.
	convoyStageJSON = true
	defer func() { convoyStageJSON = false }()

	_ = runConvoyStage(nil, []string{"gt-j1", "gt-j2"})
	w.Close()
	wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	outBytes, _ := io.ReadAll(r)
	output := string(outBytes)

	errBytes, _ := io.ReadAll(rErr)
	stderrOutput := string(errBytes)

	// Stdout should be valid JSON.
	var parsed StageResult
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nraw:\n%s", err, output)
	}

	// Should NOT contain human-readable markers on stdout.
	if strings.Contains(output, "├── ") || strings.Contains(output, "└── ") {
		t.Errorf("JSON output should not contain tree characters, got:\n%s", output)
	}
	if strings.Contains(output, "Convoy created:") || strings.Contains(output, "Convoy updated:") {
		t.Errorf("JSON output should not contain human-readable convoy message, got:\n%s", output)
	}
	// The "Errors:" header from renderErrors should NOT appear in JSON mode.
	if strings.Contains(output, "Errors:\n") {
		t.Errorf("JSON output should not contain human-readable error header, got:\n%s", output)
	}
	// Stderr should be empty in JSON mode (errors go into JSON, not stderr).
	if stderrOutput != "" {
		t.Errorf("stderr should be empty in JSON mode, got:\n%s", stderrOutput)
	}
}

// IT-34: --json with errors: non-zero exit code.
func TestJSONOutput_ErrorsReturnNonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	// Create a DAG with a no-rig error (task without rig).
	// Use "zz-" prefix which won't be in routes.jsonl, so rigFromBeadID returns "".
	testDAG := newTestDAG(t).
		Task("zz-norig", "No Rig Task", "") // unmapped prefix → no-rig error

	testDAG.Setup(t)

	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	convoyStageJSON = true
	defer func() { convoyStageJSON = false }()

	err := runConvoyStage(nil, []string{"zz-norig"})
	w.Close()
	os.Stdout = old

	// Should return an error (non-zero exit code).
	if err == nil {
		t.Fatal("expected error for DAG with no-rig, got nil")
	}

	// But stdout should still contain valid JSON.
	outBytes, _ := io.ReadAll(r)
	output := string(outBytes)

	var parsed StageResult
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("error output should still be valid JSON: %v\nraw:\n%s", err, output)
	}

	if parsed.Status != "error" {
		t.Errorf("status should be 'error', got %q", parsed.Status)
	}
	if parsed.ConvoyID != "" {
		t.Errorf("convoy_id should be empty on error, got %q", parsed.ConvoyID)
	}
	if len(parsed.Errors) == 0 {
		t.Error("errors array should not be empty")
	}
}

// SN-06: JSON output: full structure snapshot.
// Build a representative DAG and verify the full JSON output structure
// matches expected field names, nesting, and types.
func TestJSONOutput_FullStructureSnapshot(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1": {ID: "epic-1", Title: "Root Epic", Type: "epic", Status: "open",
			Children: []string{"gt-a", "gt-b"}},
		"gt-a": {ID: "gt-a", Title: "Task A", Type: "task", Status: "open", Rig: "gastown",
			Parent: "epic-1", Blocks: []string{"gt-b"}},
		"gt-b": {ID: "gt-b", Title: "Task B", Type: "task", Status: "open", Rig: "gastown",
			Parent: "epic-1", BlockedBy: []string{"gt-a"}},
	}}
	input := &StageInput{Kind: StageInputEpic, IDs: []string{"epic-1"}}

	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	// Build a warning for cross-rig (simulated).
	warns := []StagingFinding{
		{Severity: "warning", Category: "orphan", BeadIDs: []string{"gt-a"},
			Message: "task gt-a isolated", SuggestedFix: "add dep"},
	}

	result := StageResult{
		Status:   "staged_warnings",
		ConvoyID: "hq-cv-snap1",
		Errors:   buildFindingsJSON(nil),
		Warnings: buildFindingsJSON(warns),
		Waves:    buildWavesJSON(waves, dag),
		Tree:     buildTreeJSON(dag, input),
	}

	out, err := renderJSON(result)
	if err != nil {
		t.Fatalf("renderJSON: %v", err)
	}

	// Parse into raw map to verify exact field names.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// All top-level fields must be present.
	requiredFields := []string{"status", "convoy_id", "errors", "warnings", "waves", "tree"}
	for _, field := range requiredFields {
		if _, ok := raw[field]; !ok {
			t.Errorf("missing top-level field %q in JSON output", field)
		}
	}

	// Parse fully and verify tree structure (epic input → nested).
	var parsed StageResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Tree should have 1 root node (epic-1) with 2 children.
	if len(parsed.Tree) != 1 {
		t.Fatalf("tree should have 1 root, got %d", len(parsed.Tree))
	}
	root := parsed.Tree[0]
	if root.ID != "epic-1" {
		t.Errorf("tree root ID = %q, want %q", root.ID, "epic-1")
	}
	if root.Type != "epic" {
		t.Errorf("tree root type = %q, want %q", root.Type, "epic")
	}
	if len(root.Children) != 2 {
		t.Fatalf("tree root should have 2 children, got %d", len(root.Children))
	}

	// Children should be sorted by ID (gt-a before gt-b).
	if root.Children[0].ID != "gt-a" {
		t.Errorf("first child = %q, want gt-a", root.Children[0].ID)
	}
	if root.Children[1].ID != "gt-b" {
		t.Errorf("second child = %q, want gt-b", root.Children[1].ID)
	}

	// Children should have rig set.
	if root.Children[0].Rig != "gastown" {
		t.Errorf("gt-a rig = %q, want gastown", root.Children[0].Rig)
	}

	// Waves should have task details.
	if len(parsed.Waves) != 2 {
		t.Fatalf("expected 2 waves, got %d", len(parsed.Waves))
	}
	if parsed.Waves[0].Number != 1 {
		t.Errorf("wave 1 number = %d, want 1", parsed.Waves[0].Number)
	}

	// Warnings should be populated.
	if len(parsed.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(parsed.Warnings))
	}
	if parsed.Warnings[0].Category != "orphan" {
		t.Errorf("warning category = %q, want orphan", parsed.Warnings[0].Category)
	}
	if parsed.Warnings[0].SuggestedFix != "add dep" {
		t.Errorf("warning suggested_fix = %q, want 'add dep'", parsed.Warnings[0].SuggestedFix)
	}

	// Errors should be empty array, not null.
	if string(raw["errors"]) == "null" {
		t.Error("errors should be [] not null")
	}
}

// Test buildTreeJSON for flat (task-list) input.
func TestBuildTreeJSON_FlatInput(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-x": {ID: "gt-x", Title: "X", Type: "task", Status: "open", Rig: "gastown"},
		"gt-y": {ID: "gt-y", Title: "Y", Type: "bug", Status: "open", Rig: "beads"},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-x", "gt-y"}}

	tree := buildTreeJSON(dag, input)

	if len(tree) != 2 {
		t.Fatalf("expected 2 tree nodes, got %d", len(tree))
	}

	// Flat → no children.
	for _, node := range tree {
		if len(node.Children) != 0 {
			t.Errorf("flat tree node %q should have no children", node.ID)
		}
	}

	// Sorted by ID.
	if tree[0].ID != "gt-x" || tree[1].ID != "gt-y" {
		t.Errorf("tree should be sorted by ID: got [%s, %s]", tree[0].ID, tree[1].ID)
	}
}

// Test buildTreeJSON for epic input with nested children.
func TestBuildTreeJSON_EpicInput(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1": {ID: "epic-1", Title: "Root", Type: "epic", Status: "open",
			Children: []string{"sub-epic", "task-1"}},
		"sub-epic": {ID: "sub-epic", Title: "Sub", Type: "epic", Status: "open",
			Parent: "epic-1", Children: []string{"task-2"}},
		"task-1": {ID: "task-1", Title: "T1", Type: "task", Status: "open",
			Rig: "gastown", Parent: "epic-1"},
		"task-2": {ID: "task-2", Title: "T2", Type: "task", Status: "open",
			Rig: "gastown", Parent: "sub-epic"},
	}}
	input := &StageInput{Kind: StageInputEpic, IDs: []string{"epic-1"}}

	tree := buildTreeJSON(dag, input)

	if len(tree) != 1 {
		t.Fatalf("expected 1 root tree node, got %d", len(tree))
	}

	root := tree[0]
	if root.ID != "epic-1" {
		t.Errorf("root ID = %q, want epic-1", root.ID)
	}
	if len(root.Children) != 2 {
		t.Fatalf("root should have 2 children, got %d", len(root.Children))
	}

	// Children sorted: sub-epic < task-1
	if root.Children[0].ID != "sub-epic" {
		t.Errorf("first child = %q, want sub-epic", root.Children[0].ID)
	}
	if root.Children[1].ID != "task-1" {
		t.Errorf("second child = %q, want task-1", root.Children[1].ID)
	}

	// sub-epic has 1 child: task-2
	if len(root.Children[0].Children) != 1 {
		t.Fatalf("sub-epic should have 1 child, got %d", len(root.Children[0].Children))
	}
	if root.Children[0].Children[0].ID != "task-2" {
		t.Errorf("sub-epic child = %q, want task-2", root.Children[0].Children[0].ID)
	}
}

// Test buildFindingsJSON with empty input.
func TestBuildFindingsJSON_Empty(t *testing.T) {
	out := buildFindingsJSON(nil)
	if out == nil {
		t.Fatal("buildFindingsJSON(nil) should return empty slice, not nil")
	}
	if len(out) != 0 {
		t.Errorf("expected 0 findings, got %d", len(out))
	}
}

// Test buildWavesJSON with task details.
func TestBuildWavesJSON_TaskDetails(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Title: "A", Type: "task", Rig: "gst",
			Blocks: []string{"b"}},
		"b": {ID: "b", Title: "B", Type: "task", Rig: "gst",
			BlockedBy: []string{"a"}},
	}}
	waves := []Wave{
		{Number: 1, Tasks: []string{"a"}},
		{Number: 2, Tasks: []string{"b"}},
	}

	wj := buildWavesJSON(waves, dag)
	if len(wj) != 2 {
		t.Fatalf("expected 2 waves, got %d", len(wj))
	}

	// Wave 1: task a, no blockers.
	if wj[0].Number != 1 {
		t.Errorf("wave 1 number = %d", wj[0].Number)
	}
	if len(wj[0].Tasks) != 1 || wj[0].Tasks[0].ID != "a" {
		t.Errorf("wave 1 tasks = %+v", wj[0].Tasks)
	}
	if len(wj[0].Tasks[0].BlockedBy) != 0 {
		t.Errorf("task a should have no blockers, got %v", wj[0].Tasks[0].BlockedBy)
	}

	// Wave 2: task b, blocked by a.
	if wj[1].Tasks[0].ID != "b" {
		t.Errorf("wave 2 task = %q", wj[1].Tasks[0].ID)
	}
	if len(wj[1].Tasks[0].BlockedBy) != 1 || wj[1].Tasks[0].BlockedBy[0] != "a" {
		t.Errorf("task b blocked_by = %v", wj[1].Tasks[0].BlockedBy)
	}
}

// TestAppendValidationWave_CreatesCapstoneWave verifies that appendValidationWave
// creates a validation bead blocked by all slingable tasks and appends it as the
// final wave.
func TestAppendValidationWave_CreatesCapstoneWave(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	testDAG := newTestDAG(t).
		Epic("epic-1", "Test Epic").
		Task("gt-a", "Task A", withRig("gastown")).ParentOf("epic-1").
		Task("gt-b", "Task B", withRig("gastown")).ParentOf("epic-1").BlockedBy("gt-a")

	_, logPath := testDAG.Setup(t)

	// Build the ConvoyDAG.
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1": {ID: "epic-1", Title: "Test Epic", Type: "epic", Status: "open"},
		"gt-a":   {ID: "gt-a", Title: "Task A", Type: "task", Status: "open", Rig: "gastown", Blocks: []string{"gt-b"}},
		"gt-b":   {ID: "gt-b", Title: "Task B", Type: "task", Status: "open", Rig: "gastown", BlockedBy: []string{"gt-a"}},
	}}

	// Compute waves first.
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}
	if len(waves) != 2 {
		t.Fatalf("expected 2 waves before validation, got %d", len(waves))
	}

	// Append validation wave.
	waves, validationID, err := appendValidationWave(dag, waves, "epic-1")
	if err != nil {
		t.Fatalf("appendValidationWave: %v", err)
	}

	// Verify validation bead was created.
	if validationID == "" {
		t.Fatal("expected non-empty validation bead ID")
	}
	if !strings.HasPrefix(validationID, "hq-") {
		t.Errorf("validation bead ID should start with hq-, got %q", validationID)
	}

	// Verify waves: should now have 3 waves (original 2 + validation).
	if len(waves) != 3 {
		t.Fatalf("expected 3 waves after validation, got %d", len(waves))
	}
	if waves[2].Number != 3 {
		t.Errorf("validation wave number = %d, want 3", waves[2].Number)
	}
	if len(waves[2].Tasks) != 1 || waves[2].Tasks[0] != validationID {
		t.Errorf("validation wave tasks = %v, want [%s]", waves[2].Tasks, validationID)
	}

	// Verify the validation bead was added to the DAG.
	valNode, ok := dag.Nodes[validationID]
	if !ok {
		t.Fatal("validation bead not found in DAG")
	}
	if valNode.Type != "task" {
		t.Errorf("validation bead type = %q, want task", valNode.Type)
	}
	if valNode.Parent != "epic-1" {
		t.Errorf("validation bead parent = %q, want epic-1", valNode.Parent)
	}

	// Verify it's blocked by all slingable beads.
	blockedBy := make(map[string]bool)
	for _, id := range valNode.BlockedBy {
		blockedBy[id] = true
	}
	if !blockedBy["gt-a"] || !blockedBy["gt-b"] {
		t.Errorf("validation bead should be blocked by gt-a and gt-b, got %v", valNode.BlockedBy)
	}

	// Verify slingable nodes now block the validation bead.
	if nodeA, ok := dag.Nodes["gt-a"]; ok {
		found := false
		for _, id := range nodeA.Blocks {
			if id == validationID {
				found = true
			}
		}
		if !found {
			t.Errorf("gt-a should block validation bead, Blocks = %v", nodeA.Blocks)
		}
	}

	// Verify bd commands were logged: create, dep add parent-child, dep add blocks.
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd.log: %v", err)
	}
	logContent := string(logBytes)

	if !strings.Contains(logContent, "create") {
		t.Errorf("bd.log should contain 'create' command")
	}
	if !strings.Contains(logContent, "--type=task") {
		t.Errorf("bd.log should contain '--type=task'")
	}
	if !strings.Contains(logContent, "mol-validate-prd") {
		t.Errorf("bd.log should contain 'mol-validate-prd' in description")
	}
	if !strings.Contains(logContent, "dep add epic-1 "+validationID+" --type=parent-child") {
		t.Errorf("bd.log should contain parent-child dep add, got:\n%s", logContent)
	}
	for _, beadID := range []string{"gt-a", "gt-b"} {
		if !strings.Contains(logContent, "dep add "+beadID+" "+validationID+" --type=blocks") {
			t.Errorf("bd.log should contain 'dep add %s %s --type=blocks', got:\n%s", beadID, validationID, logContent)
		}
	}
}

// TestAppendValidationWave_NoSlingableBeads verifies that appendValidationWave
// returns early when there are no slingable beads (e.g., epic-only DAG).
func TestAppendValidationWave_NoSlingableBeads(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1": {ID: "epic-1", Title: "Test Epic", Type: "epic", Status: "open"},
	}}

	waves, validationID, err := appendValidationWave(dag, nil, "epic-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if validationID != "" {
		t.Errorf("expected empty validation ID for no slingable beads, got %q", validationID)
	}
	if len(waves) != 0 {
		t.Errorf("expected 0 waves, got %d", len(waves))
	}
}
