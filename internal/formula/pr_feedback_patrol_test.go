package formula

import (
	"strings"
	"testing"
)

// TestPRFeedbackPatrolFormulaStructure verifies that
// mol-pr-feedback-patrol parses correctly and has the expected steps.
func TestPRFeedbackPatrolFormulaStructure(t *testing.T) {
	f, err := ParseFile("formulas/mol-pr-feedback-patrol.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	if f.Name != "mol-pr-feedback-patrol" {
		t.Errorf("formula name = %q, want mol-pr-feedback-patrol", f.Name)
	}

	wantSteps := []string{
		"list-open-prs",
		"check-review-status",
		"check-ci-status",
		"dispatch-work",
		"loop-or-exit",
	}

	if len(f.Steps) != len(wantSteps) {
		t.Fatalf("step count = %d, want %d", len(f.Steps), len(wantSteps))
	}

	for i, want := range wantSteps {
		if f.Steps[i].ID != want {
			t.Errorf("step[%d].ID = %q, want %q", i, f.Steps[i].ID, want)
		}
	}
}

// TestPRFeedbackPatrolDispatchStepContainsLabelKeyedDispatch verifies
// that the dispatch-work step references label-keyed dispatch for
// gt:auto-test-pr (Phase 2 task 19, gu-vvl4y).
func TestPRFeedbackPatrolDispatchStepContainsLabelKeyedDispatch(t *testing.T) {
	f, err := ParseFile("formulas/mol-pr-feedback-patrol.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	var dispatchStep *Step
	for i := range f.Steps {
		if f.Steps[i].ID == "dispatch-work" {
			dispatchStep = &f.Steps[i]
			break
		}
	}
	if dispatchStep == nil {
		t.Fatal("dispatch-work step not found")
	}

	// Verify the step description includes label-keyed dispatch keywords.
	checks := []string{
		"gt:auto-test-pr",
		"auto_test_pr_revision_routing",
		"mol-polecat-work-test-improver",
		"mode=revise",
		"Label-Keyed Dispatch",
	}
	for _, want := range checks {
		if !strings.Contains(dispatchStep.Description, want) {
			t.Errorf("dispatch-work description missing %q", want)
		}
	}
}

// TestPRFeedbackPatrolDispatchStepPreservesGenericPath verifies that
// the generic (non-label-keyed) dispatch path still exists for PRs
// without the gt:auto-test-pr label (regression coverage).
func TestPRFeedbackPatrolDispatchStepPreservesGenericPath(t *testing.T) {
	f, err := ParseFile("formulas/mol-pr-feedback-patrol.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	var dispatchStep *Step
	for i := range f.Steps {
		if f.Steps[i].ID == "dispatch-work" {
			dispatchStep = &f.Steps[i]
			break
		}
	}
	if dispatchStep == nil {
		t.Fatal("dispatch-work step not found")
	}

	// The generic path must still create beads and sling polecats for
	// review-feedback and ci-failure types.
	genericChecks := []string{
		"review-feedback",
		"ci-failure",
		"gt sling {{rig}}",
		"bd create",
		"Safety valve",
	}
	for _, want := range genericChecks {
		if !strings.Contains(dispatchStep.Description, want) {
			t.Errorf("dispatch-work description missing generic-path keyword %q", want)
		}
	}
}
