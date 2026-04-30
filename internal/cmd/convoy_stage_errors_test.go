package cmd

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Error detection + categorization tests (gt-csl.3.3)
// ---------------------------------------------------------------------------

// U-20: Cycle is categorized as error, not warning
func TestCategorize_CycleIsError(t *testing.T) {
	findings := []StagingFinding{
		{Severity: "error", Category: "cycle", BeadIDs: []string{"a", "b"}, Message: "cycle"},
	}
	errs, warns := categorizeFindings(findings)
	if len(errs) != 1 {
		t.Errorf("expected 1 error, got %d", len(errs))
	}
	if len(warns) != 0 {
		t.Errorf("expected 0 warnings, got %d", len(warns))
	}
}

// U-21: No-rig is categorized as error
func TestCategorize_NoRigIsError(t *testing.T) {
	findings := []StagingFinding{
		{Severity: "error", Category: "no-rig", BeadIDs: []string{"gt-xyz"}, Message: "no rig"},
	}
	errs, warns := categorizeFindings(findings)
	if len(errs) != 1 {
		t.Errorf("expected 1 error, got %d", len(errs))
	}
	if len(warns) != 0 {
		t.Errorf("expected 0 warnings, got %d", len(warns))
	}
}

// U-25: No errors + no warnings → staged_ready
func TestChooseStatus_Ready(t *testing.T) {
	status := chooseStatus(nil, nil)
	if status != "staged_ready" {
		t.Errorf("expected staged_ready, got %q", status)
	}
}

// U-26: Warnings only → staged_warnings
func TestChooseStatus_Warnings(t *testing.T) {
	warns := []StagingFinding{{Severity: "warning", Category: "blocked-rig"}}
	status := chooseStatus(nil, warns)
	if status != "staged_warnings" {
		t.Errorf("expected staged_warnings, got %q", status)
	}
}

// U-27: Any errors → no creation (empty string)
func TestChooseStatus_Errors(t *testing.T) {
	errs := []StagingFinding{{Severity: "error", Category: "cycle"}}
	status := chooseStatus(errs, nil)
	if status != "" {
		t.Errorf("expected empty (no creation), got %q", status)
	}
}

// U-39: Error output includes bead IDs and suggested fix
func TestRenderErrors_IncludesFixAndIDs(t *testing.T) {
	findings := []StagingFinding{
		{Severity: "error", Category: "cycle", BeadIDs: []string{"a", "b"},
			Message:      "cycle detected: a → b → a",
			SuggestedFix: "remove one blocking dep"},
	}
	output := renderErrors(findings)
	if !strings.Contains(output, "a, b") {
		t.Error("should include bead IDs")
	}
	if !strings.Contains(output, "remove one blocking dep") {
		t.Error("should include suggested fix")
	}
	if !strings.Contains(output, "cycle") {
		t.Error("should include category")
	}
}

// Test detectErrors with cycle
func TestErrorDetection_CycleDetected(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Type: "task", Rig: "gastown", Blocks: []string{"b"}, BlockedBy: []string{"b"}},
		"b": {ID: "b", Type: "task", Rig: "gastown", BlockedBy: []string{"a"}, Blocks: []string{"a"}},
	}}

	findings := detectErrors(dag)
	errs, _ := categorizeFindings(findings)
	if len(errs) == 0 {
		t.Fatal("expected cycle error")
	}
	if errs[0].Category != "cycle" {
		t.Errorf("expected cycle, got %s", errs[0].Category)
	}
}

// Test detectErrors with no rig
func TestErrorDetection_NoRig(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Type: "task", Rig: ""}, // no rig!
	}}
	findings := detectErrors(dag)
	errs, _ := categorizeFindings(findings)
	if len(errs) == 0 {
		t.Fatal("expected no-rig error")
	}
	if errs[0].Category != "no-rig" {
		t.Errorf("expected no-rig, got %s", errs[0].Category)
	}
}

// Test detectErrors clean DAG → no errors
func TestErrorDetection_Clean(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Type: "task", Rig: "gastown", Blocks: []string{"b"}},
		"b": {ID: "b", Type: "task", Rig: "gastown", BlockedBy: []string{"a"}},
	}}
	findings := detectErrors(dag)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(findings))
	}
}

