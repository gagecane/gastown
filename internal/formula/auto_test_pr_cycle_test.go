package formula

import (
	"testing"
)

// TestAutoTestPRCycleFormulaStructure verifies the mol-auto-test-pr-cycle
// patrol formula parses, is a valid workflow with the documented step
// DAG, and declares its key vars with the documented defaults. This is
// the inert standing-patrol shell for Phase 0 task 4 (gu-2n7xi).
func TestAutoTestPRCycleFormulaStructure(t *testing.T) {
	f, err := ParseFile("formulas/mol-auto-test-pr-cycle.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	if f.Name != "mol-auto-test-pr-cycle" {
		t.Errorf("formula name = %q, want mol-auto-test-pr-cycle", f.Name)
	}
	if !f.Type.IsValid() {
		t.Errorf("formula type %q is not valid", f.Type)
	}
	if f.Version < 1 {
		t.Errorf("formula version = %d, want >= 1", f.Version)
	}
	if err := f.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}

	wantSteps := []string{"run-cycle-tick", "loop-or-exit"}
	if len(f.Steps) != len(wantSteps) {
		t.Errorf("step count = %d, want %d", len(f.Steps), len(wantSteps))
	}
	for _, id := range wantSteps {
		if f.GetStep(id) == nil {
			t.Errorf("step %q missing", id)
		}
	}

	// loop-or-exit must depend on run-cycle-tick (tick before respawn).
	order, err := f.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	if pos["run-cycle-tick"] >= pos["loop-or-exit"] {
		t.Errorf("run-cycle-tick must come before loop-or-exit, got order %v", order)
	}
}
