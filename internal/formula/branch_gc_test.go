package formula

import (
	"strings"
	"testing"
)

// TestBranchGCFormulaStructure verifies the mol-auto-test-pr-branch-gc
// patrol formula declares the expected step DAG and variables. The
// patrol must be parseable, well-typed as a workflow, and topologically
// orderable; its key vars (dry_run, stale_days, branch_prefix) must
// exist with the documented defaults so callers / Mayor dispatchers
// don't silently get wrong behavior on a config drift.
func TestBranchGCFormulaStructure(t *testing.T) {
	f, err := ParseFile("formulas/mol-auto-test-pr-branch-gc.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	if f.Name != "mol-auto-test-pr-branch-gc" {
		t.Errorf("formula name = %q, want mol-auto-test-pr-branch-gc", f.Name)
	}
	if !f.Type.IsValid() {
		t.Errorf("formula type %q is not valid", f.Type)
	}
	if f.Version < 1 {
		t.Errorf("formula version = %d, want >= 1", f.Version)
	}

	wantSteps := []string{
		"enumerate-opted-in-rigs",
		"list-auto-test-branches",
		"classify-branches",
		"delete-stale-branches",
		"loop-or-exit",
	}
	if len(f.Steps) != len(wantSteps) {
		t.Errorf("step count = %d, want %d", len(f.Steps), len(wantSteps))
	}
	for _, id := range wantSteps {
		if f.GetStep(id) == nil {
			t.Errorf("step %q missing", id)
		}
	}

	order, err := f.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	pairs := [][2]string{
		{"enumerate-opted-in-rigs", "list-auto-test-branches"},
		{"list-auto-test-branches", "classify-branches"},
		{"classify-branches", "delete-stale-branches"},
		{"delete-stale-branches", "loop-or-exit"},
	}
	for _, p := range pairs {
		if pos[p[0]] >= pos[p[1]] {
			t.Errorf("step %q must precede %q in topological order, got positions %d and %d",
				p[0], p[1], pos[p[0]], pos[p[1]])
		}
	}

	wantVars := map[string]string{
		"wisp_type":             "patrol",
		"dry_run":               "true",
		"stale_days":            "7",
		"branch_prefix":         "auto-test/",
		"auto_test_label":       "gt:auto-test-pr",
		"max_deletes_per_cycle": "50",
	}
	for name, def := range wantVars {
		v, ok := f.Vars[name]
		if !ok {
			t.Errorf("var %q missing", name)
			continue
		}
		if v.Default != def {
			t.Errorf("var %q default = %q, want %q", name, v.Default, def)
		}
	}
}

// TestBranchGCFormulaSafetyContract pins the security-relevant text in
// the formula description and steps. The C-SEC-6 branch-namespace
// contract requires the patrol to operate ONLY on the auto-test/*
// prefix; the dry-run path must remain the documented acceptance gate.
// If a future edit removes either guarantee, this test surfaces it.
func TestBranchGCFormulaSafetyContract(t *testing.T) {
	f, err := ParseFile("formulas/mol-auto-test-pr-branch-gc.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	listStep := f.GetStep("list-auto-test-branches")
	if listStep == nil {
		t.Fatal("list-auto-test-branches step missing")
	}
	// The for-each-ref glob must stay anchored to the configured prefix —
	// widening to other refs would break the C-SEC-6 namespace scope.
	if !strings.Contains(listStep.Description, "{{branch_prefix}}") {
		t.Error("list step does not reference the configured {{branch_prefix}}; widening to a hard-coded prefix breaks the C-SEC-6 contract")
	}
	if !strings.Contains(listStep.Description, "C-SEC-6") {
		t.Error("list step does not name C-SEC-6; the contract reference is required for reviewer-spoting on future edits")
	}

	deleteStep := f.GetStep("delete-stale-branches")
	if deleteStep == nil {
		t.Fatal("delete-stale-branches step missing")
	}
	// Dry-run must be a real branch in the step body — a stale assertion
	// here is what the bead acceptance criterion rests on.
	if !strings.Contains(deleteStep.Description, `"{{dry_run}}" = "true"`) {
		t.Error("delete step lacks dry_run=true short-circuit; acceptance gate (dry-run reports without deleting) would regress")
	}

	classify := f.GetStep("classify-branches")
	if classify == nil {
		t.Fatal("classify-branches step missing")
	}
	// All four documented skip conditions must appear in the step body
	// so reviewers reading the formula see the contract in place.
	skipMarkers := []string{
		"Skip condition 1",
		"Skip condition 2",
		"Skip condition 3",
		"Skip condition 4",
	}
	for _, m := range skipMarkers {
		if !strings.Contains(classify.Description, m) {
			t.Errorf("classify step missing %q marker; the four skip conditions are the patrol's keep/delete contract", m)
		}
	}
}
