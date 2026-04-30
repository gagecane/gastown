package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Warning detection tests (gt-csl.3.4)
// ---------------------------------------------------------------------------

// U-22: Parked rig detected and warned
// This test uses the isRigParkedFn seam to mock parked rig detection.
func TestDetectWarnings_ParkedRig(t *testing.T) {
	// Set up a temp dir as town root and cd there for workspace.FindFromCwd()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	oldDir, _ := os.Getwd()
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(oldDir) })

	// Override isRigBlockedFn to return true for "parkedrig"
	origFn := isRigBlockedFn
	isRigBlockedFn = func(townRoot, rigName string) (bool, string) {
		if rigName == "parkedrig" {
			return true, "parked"
		}
		return false, ""
	}
	t.Cleanup(func() { isRigBlockedFn = origFn })

	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Type: "task", Rig: "parkedrig"},
		"gt-b": {ID: "gt-b", Type: "task", Rig: "gastown"},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b"}}
	findings := detectWarnings(dag, input)

	var parkedFindings []StagingFinding
	for _, f := range findings {
		if f.Category == "blocked-rig" {
			parkedFindings = append(parkedFindings, f)
		}
	}
	if len(parkedFindings) != 1 {
		t.Fatalf("expected 1 blocked-rig warning, got %d: %+v", len(parkedFindings), findings)
	}
	f := parkedFindings[0]
	if f.Severity != "warning" {
		t.Errorf("severity = %q, want %q", f.Severity, "warning")
	}
	if !sliceContains(f.BeadIDs, "gt-a") {
		t.Errorf("BeadIDs should contain gt-a, got %v", f.BeadIDs)
	}
}

// Regression test for #2120 review item #1: docked rigs should also be detected.
func TestDetectWarnings_DockedRig(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	oldDir, _ := os.Getwd()
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(oldDir) })

	// Override isRigBlockedFn to return docked for "dockedrig"
	origFn := isRigBlockedFn
	isRigBlockedFn = func(townRoot, rigName string) (bool, string) {
		if rigName == "dockedrig" {
			return true, "docked"
		}
		return false, ""
	}
	t.Cleanup(func() { isRigBlockedFn = origFn })

	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Type: "task", Rig: "dockedrig"},
		"gt-b": {ID: "gt-b", Type: "task", Rig: "gastown"},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b"}}
	findings := detectWarnings(dag, input)

	var blockedFindings []StagingFinding
	for _, f := range findings {
		if f.Category == "blocked-rig" {
			blockedFindings = append(blockedFindings, f)
		}
	}
	if len(blockedFindings) != 1 {
		t.Fatalf("expected 1 blocked-rig warning for docked rig, got %d: %+v", len(blockedFindings), findings)
	}
	f := blockedFindings[0]
	if f.Severity != "warning" {
		t.Errorf("severity = %q, want %q", f.Severity, "warning")
	}
	if !sliceContains(f.BeadIDs, "gt-a") {
		t.Errorf("BeadIDs should contain gt-a, got %v", f.BeadIDs)
	}
	if !strings.Contains(f.Message, "docked") {
		t.Errorf("message should mention 'docked', got: %s", f.Message)
	}
	if !strings.Contains(f.SuggestedFix, "undock") {
		t.Errorf("suggested fix should mention 'undock', got: %s", f.SuggestedFix)
	}
}

// U-23: Orphan detection for epic input
func TestDetectWarnings_OrphanEpicInput(t *testing.T) {
	// 3 tasks under an epic: A blocks B (connected), C is isolated.
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1": {ID: "epic-1", Type: "epic", Children: []string{"gt-a", "gt-b", "gt-c"}},
		"gt-a":   {ID: "gt-a", Type: "task", Rig: "gastown", Parent: "epic-1", Blocks: []string{"gt-b"}},
		"gt-b":   {ID: "gt-b", Type: "task", Rig: "gastown", Parent: "epic-1", BlockedBy: []string{"gt-a"}},
		"gt-c":   {ID: "gt-c", Type: "task", Rig: "gastown", Parent: "epic-1"},
	}}
	input := &StageInput{Kind: StageInputEpic, IDs: []string{"epic-1"}}
	findings := detectWarnings(dag, input)

	var orphanFindings []StagingFinding
	for _, f := range findings {
		if f.Category == "orphan" {
			orphanFindings = append(orphanFindings, f)
		}
	}
	if len(orphanFindings) != 1 {
		t.Fatalf("expected 1 orphan warning, got %d: %+v", len(orphanFindings), findings)
	}
	if !sliceContains(orphanFindings[0].BeadIDs, "gt-c") {
		t.Errorf("orphan warning should reference gt-c, got %v", orphanFindings[0].BeadIDs)
	}
}

// U-24: Missing integration branch warning
func TestDetectWarnings_MissingBranch(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"root-epic": {ID: "root-epic", Type: "epic", Children: []string{"sub-epic"}},
		"sub-epic":  {ID: "sub-epic", Type: "epic", Parent: "root-epic", Children: []string{"gt-a", "gt-b"}},
		"gt-a":      {ID: "gt-a", Type: "task", Rig: "gastown", Parent: "sub-epic"},
		"gt-b":      {ID: "gt-b", Type: "task", Rig: "gastown", Parent: "sub-epic"},
	}}
	input := &StageInput{Kind: StageInputEpic, IDs: []string{"root-epic"}}
	findings := detectWarnings(dag, input)

	var branchFindings []StagingFinding
	for _, f := range findings {
		if f.Category == "missing-branch" {
			branchFindings = append(branchFindings, f)
		}
	}
	if len(branchFindings) != 1 {
		t.Fatalf("expected 1 missing-branch warning, got %d: %+v", len(branchFindings), findings)
	}
	f := branchFindings[0]
	if f.Severity != "warning" {
		t.Errorf("severity = %q, want %q", f.Severity, "warning")
	}
	if !sliceContains(f.BeadIDs, "sub-epic") {
		t.Errorf("BeadIDs should contain sub-epic, got %v", f.BeadIDs)
	}
	if !strings.Contains(f.SuggestedFix, "sub-epic") {
		t.Errorf("SuggestedFix should mention sub-epic, got %q", f.SuggestedFix)
	}
}

// U-34: Cross-rig routing mismatch warned
func TestDetectWarnings_CrossRig(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Type: "task", Rig: "gastown"},
		"gt-b": {ID: "gt-b", Type: "task", Rig: "gastown"},
		"bd-c": {ID: "bd-c", Type: "task", Rig: "beads"},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b", "bd-c"}}
	findings := detectWarnings(dag, input)

	var crossFindings []StagingFinding
	for _, f := range findings {
		if f.Category == "cross-rig" {
			crossFindings = append(crossFindings, f)
		}
	}
	if len(crossFindings) != 1 {
		t.Fatalf("expected 1 cross-rig warning, got %d: %+v", len(crossFindings), findings)
	}
	f := crossFindings[0]
	if f.Severity != "warning" {
		t.Errorf("severity = %q, want %q", f.Severity, "warning")
	}
	if !sliceContains(f.BeadIDs, "bd-c") {
		t.Errorf("BeadIDs should contain bd-c, got %v", f.BeadIDs)
	}
	if !strings.Contains(f.Message, "gastown") {
		t.Errorf("Message should mention primary rig gastown, got %q", f.Message)
	}
}

// U-35: Capacity estimation
func TestDetectWarnings_Capacity(t *testing.T) {
	// Create a DAG where wave 1 has 6 independent tasks (all in-degree 0).
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"t1": {ID: "t1", Type: "task", Rig: "gastown"},
		"t2": {ID: "t2", Type: "task", Rig: "gastown"},
		"t3": {ID: "t3", Type: "task", Rig: "gastown"},
		"t4": {ID: "t4", Type: "task", Rig: "gastown"},
		"t5": {ID: "t5", Type: "task", Rig: "gastown"},
		"t6": {ID: "t6", Type: "task", Rig: "gastown"},
	}}

	// Verify computeWaves puts them all in wave 1.
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}
	if len(waves) != 1 || len(waves[0].Tasks) != 6 {
		t.Fatalf("expected 1 wave with 6 tasks, got %d waves with tasks: %+v", len(waves), waves)
	}

	input := &StageInput{Kind: StageInputTasks, IDs: []string{"t1", "t2", "t3", "t4", "t5", "t6"}}
	findings := detectWarnings(dag, input)

	var capFindings []StagingFinding
	for _, f := range findings {
		if f.Category == "capacity" {
			capFindings = append(capFindings, f)
		}
	}
	if len(capFindings) != 1 {
		t.Fatalf("expected 1 capacity warning, got %d: %+v", len(capFindings), findings)
	}
	f := capFindings[0]
	if f.Severity != "warning" {
		t.Errorf("severity = %q, want %q", f.Severity, "warning")
	}
	if !strings.Contains(f.Message, "wave 1") {
		t.Errorf("Message should mention wave 1, got %q", f.Message)
	}
	if !strings.Contains(f.Message, "6 tasks") {
		t.Errorf("Message should mention 6 tasks, got %q", f.Message)
	}
}

// IT-43: Orphan detection skipped for task-list input
func TestDetectWarnings_NoOrphansForTaskList(t *testing.T) {
	// Same DAG as orphan test but with task-list input.
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Type: "task", Rig: "gastown", Blocks: []string{"gt-b"}},
		"gt-b": {ID: "gt-b", Type: "task", Rig: "gastown", BlockedBy: []string{"gt-a"}},
		"gt-c": {ID: "gt-c", Type: "task", Rig: "gastown"}, // isolated
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b", "gt-c"}}
	findings := detectWarnings(dag, input)

	for _, f := range findings {
		if f.Category == "orphan" {
			t.Errorf("task-list input should NOT produce orphan warnings, got: %+v", f)
		}
	}
}

// Test renderWarnings output format
func TestRenderWarnings_Format(t *testing.T) {
	findings := []StagingFinding{
		{
			Severity:     "warning",
			Category:     "blocked-rig",
			BeadIDs:      []string{"gt-a"},
			Message:      "task gt-a is assigned to parked rig \"gastown.parked\"",
			SuggestedFix: "reassign gt-a to an active rig",
		},
		{
			Severity: "warning",
			Category: "capacity",
			BeadIDs:  []string{"t1", "t2", "t3", "t4", "t5", "t6"},
			Message:  "wave 1 has 6 tasks (threshold: 5) — may exceed parallel capacity",
		},
		{
			Severity:     "warning",
			Category:     "cross-rig",
			BeadIDs:      []string{"bd-c"},
			Message:      "task bd-c is on rig \"beads\" (primary rig is \"gastown\")",
			SuggestedFix: "verify cross-rig routing for bd-c or reassign to gastown",
		},
	}

	output := renderWarnings(findings)

	// Must start with "Warnings:" header
	if !strings.HasPrefix(output, "Warnings:\n") {
		t.Errorf("output should start with 'Warnings:\\n', got:\n%s", output)
	}

	// Must include categories
	for _, cat := range []string{"blocked-rig", "capacity", "cross-rig"} {
		if !strings.Contains(output, cat) {
			t.Errorf("output should contain category %q, got:\n%s", cat, output)
		}
	}

	// Must include bead IDs
	for _, id := range []string{"gt-a", "bd-c"} {
		if !strings.Contains(output, id) {
			t.Errorf("output should contain bead ID %q, got:\n%s", id, output)
		}
	}

	// Must include suggested fixes
	if !strings.Contains(output, "reassign gt-a") {
		t.Errorf("output should contain suggested fix, got:\n%s", output)
	}

	// Numbered items
	if !strings.Contains(output, "1.") || !strings.Contains(output, "2.") || !strings.Contains(output, "3.") {
		t.Errorf("output should contain numbered items 1-3, got:\n%s", output)
	}
}

// Test detectWarnings clean DAG — no warnings
func TestDetectWarnings_Clean(t *testing.T) {
	// Override isRigBlockedFn so the test doesn't depend on real rig state.
	origFn := isRigBlockedFn
	isRigBlockedFn = func(townRoot, rigName string) (bool, string) { return false, "" }
	t.Cleanup(func() { isRigBlockedFn = origFn })

	// All tasks on same rig, all have deps between them, epic input.
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1": {ID: "epic-1", Type: "epic", Children: []string{"gt-a", "gt-b", "gt-c"}},
		"gt-a":   {ID: "gt-a", Type: "task", Rig: "gastown", Parent: "epic-1", Blocks: []string{"gt-b"}},
		"gt-b":   {ID: "gt-b", Type: "task", Rig: "gastown", Parent: "epic-1", BlockedBy: []string{"gt-a"}, Blocks: []string{"gt-c"}},
		"gt-c":   {ID: "gt-c", Type: "task", Rig: "gastown", Parent: "epic-1", BlockedBy: []string{"gt-b"}},
	}}
	input := &StageInput{Kind: StageInputEpic, IDs: []string{"epic-1"}}
	findings := detectWarnings(dag, input)
	if len(findings) != 0 {
		t.Errorf("expected 0 warnings for clean DAG, got %d: %+v", len(findings), findings)
	}
}

// Test renderWarnings with empty findings
func TestRenderWarnings_Empty(t *testing.T) {
	output := renderWarnings(nil)
	if output != "" {
		t.Errorf("expected empty string for nil findings, got %q", output)
	}
	output = renderWarnings([]StagingFinding{})
	if output != "" {
		t.Errorf("expected empty string for empty findings, got %q", output)
	}
}

