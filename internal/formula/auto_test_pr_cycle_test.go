package formula

import (
	"testing"
)

// TestAutoTestPRCycleFormulaStructure verifies the mol-auto-test-pr-cycle
// patrol formula parses, is a valid workflow with the documented step
// DAG, and declares its key vars with the documented defaults.
//
// gu-wfs-56hza rewrote this formula from the old in-process RunCycle
// state-machine shell (steps: run-cycle-tick -> loop-or-exit, which
// invoked the now-deleted internal/autotestpr/cycle.go) into a thin
// standing patrol: read opt-in -> check in-flight MR -> check cadence
// -> sling mol-auto-test-pr-pipeline -> sleep+respawn. The step DAG and
// vars asserted here track that new design.
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

	// The thin-patrol step DAG: each gating step feeds the next, the
	// sling step consumes all gates, and loop-or-exit respawns last.
	wantSteps := []string{
		"check-enabled",
		"check-in-flight",
		"check-cadence",
		"sling-pipeline",
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

	// Steps form a linear gating chain; verify topological order matches
	// the documented sequence (each step before the one that needs it).
	order, err := f.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	for i := 1; i < len(wantSteps); i++ {
		prev, cur := wantSteps[i-1], wantSteps[i]
		if pos[prev] >= pos[cur] {
			t.Errorf("%s must come before %s, got order %v", prev, cur, order)
		}
	}

	// Key vars the patrol depends on at runtime must be declared.
	wantVars := []string{"rig", "auto_test_label", "pipeline_formula", "scan_interval_seconds"}
	for _, name := range wantVars {
		if _, ok := f.Vars[name]; !ok {
			t.Errorf("var %q not declared", name)
		}
	}

	// The pipeline this patrol slings must default to the pipeline formula.
	if v, ok := f.Vars["pipeline_formula"]; ok {
		if v.Default != "mol-auto-test-pr-pipeline" {
			t.Errorf("pipeline_formula default = %q, want mol-auto-test-pr-pipeline", v.Default)
		}
	}
}
