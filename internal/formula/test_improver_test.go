package formula

import (
	"strings"
	"testing"
)

// TestTestImproverFormulaStructure verifies that the
// mol-polecat-work-test-improver formula parses correctly and declares
// the expected step DAG, variables, and quality-gate steps (4a-4g).
func TestTestImproverFormulaStructure(t *testing.T) {
	f, err := ParseFile("formulas/mol-polecat-work-test-improver.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	if f.Name != "mol-polecat-work-test-improver" {
		t.Errorf("formula name = %q, want mol-polecat-work-test-improver", f.Name)
	}
	if !f.Type.IsValid() {
		t.Errorf("formula type %q is not valid", f.Type)
	}
	if f.Version < 1 {
		t.Errorf("formula version = %d, want >= 1", f.Version)
	}

	// All expected steps in DAG order.
	wantSteps := []string{
		"load-context",
		"branch-setup",
		"implement",
		"gate-4a-coverage-delta",
		"gate-4b-mutant-sanity",
		"gate-4c-flakiness",
		"gate-4d-tautology",
		"gate-4e-gitleaks",
		"gate-4f-output-allowlist",
		"gate-4g-size-budget",
		"commit-changes",
		"self-review",
		"build-check",
		"pre-verify",
		"submit-and-exit",
	}
	if len(f.Steps) != len(wantSteps) {
		t.Errorf("step count = %d, want %d", len(f.Steps), len(wantSteps))
	}
	for _, id := range wantSteps {
		if f.GetStep(id) == nil {
			t.Errorf("step %q missing", id)
		}
	}
}

// TestTestImproverFormulaTopology verifies the step DAG is valid and
// respects the correct ordering: implement → gates 4a-4g → commit.
func TestTestImproverFormulaTopology(t *testing.T) {
	f, err := ParseFile("formulas/mol-polecat-work-test-improver.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	order, err := f.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}

	// The critical ordering: implement → gates → commit → review → build → pre-verify → submit
	pairs := [][2]string{
		{"load-context", "branch-setup"},
		{"branch-setup", "implement"},
		{"implement", "gate-4a-coverage-delta"},
		{"gate-4a-coverage-delta", "gate-4b-mutant-sanity"},
		{"gate-4b-mutant-sanity", "gate-4c-flakiness"},
		{"gate-4c-flakiness", "gate-4d-tautology"},
		{"gate-4d-tautology", "gate-4e-gitleaks"},
		{"gate-4e-gitleaks", "gate-4f-output-allowlist"},
		{"gate-4f-output-allowlist", "gate-4g-size-budget"},
		{"gate-4g-size-budget", "commit-changes"},
		{"commit-changes", "self-review"},
		{"self-review", "build-check"},
		{"build-check", "pre-verify"},
		{"pre-verify", "submit-and-exit"},
	}
	for _, p := range pairs {
		posA, okA := pos[p[0]]
		posB, okB := pos[p[1]]
		if !okA {
			t.Errorf("step %q not in topological order", p[0])
			continue
		}
		if !okB {
			t.Errorf("step %q not in topological order", p[1])
			continue
		}
		if posA >= posB {
			t.Errorf("step %q must precede %q in topological order, got positions %d and %d",
				p[0], p[1], posA, posB)
		}
	}
}

// TestTestImproverFormulaMRLabels verifies that the submit step
// references BOTH required MR-bead labels (Round 3 fix #6):
// - gt:auto-test-pr (identifies the MR)
// - rig:<target_rig> (O(1) linkage to per-rig state bead)
//
// Without these labels, the 3c cycle-close handler cannot resolve
// which rig the MR belongs to without walking the bead graph.
func TestTestImproverFormulaMRLabels(t *testing.T) {
	f, err := ParseFile("formulas/mol-polecat-work-test-improver.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	submit := f.GetStep("submit-and-exit")
	if submit == nil {
		t.Fatal("submit-and-exit step missing")
	}

	// Both labels must appear in the step description as gt done flags.
	if !strings.Contains(submit.Description, "gt:auto-test-pr") {
		t.Error("submit step does not reference gt:auto-test-pr label; " +
			"MR-bead will lack the auto-test-pr identifier required by cycle-close handler")
	}
	if !strings.Contains(submit.Description, "rig:{{target_rig}}") {
		t.Error("submit step does not reference rig:{{target_rig}} label; " +
			"MR-bead will lack the O(1) rig linkage required by 3c cycle-close handler")
	}

	// The --label flags must be present in the gt done command.
	if !strings.Contains(submit.Description, "--label gt:auto-test-pr") {
		t.Error("submit step does not include --label gt:auto-test-pr in gt done command")
	}
	if !strings.Contains(submit.Description, "--label rig:{{target_rig}}") {
		t.Error("submit step does not include --label rig:{{target_rig}} in gt done command")
	}
}

// TestTestImproverFormulaQualityGates verifies all five quality-gate
// steps are present and declare hard-fail semantics.
func TestTestImproverFormulaQualityGates(t *testing.T) {
	f, err := ParseFile("formulas/mol-polecat-work-test-improver.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	gates := []struct {
		id       string
		contains string // Key phrase that must appear in description
	}{
		{"gate-4a-coverage-delta", "HARD FAIL if branch delta"},
		{"gate-4b-mutant-sanity", "HARD FAIL if any new test still passes"},
		{"gate-4c-flakiness", "HARD FAIL if any run produces a failure"},
		{"gate-4d-tautology", "HARD FAIL if any sub-rule triggers"},
		{"gate-4e-gitleaks", "HARD FAIL if any secret is detected"},
		{"gate-4f-output-allowlist", "HARD FAIL if any rule is violated"},
		{"gate-4g-size-budget", "HARD FAIL if"},
	}

	for _, g := range gates {
		step := f.GetStep(g.id)
		if step == nil {
			t.Errorf("gate step %q missing", g.id)
			continue
		}
		if !strings.Contains(step.Description, g.contains) {
			t.Errorf("gate step %q does not contain hard-fail phrase %q", g.id, g.contains)
		}
	}
}

// TestTestImproverFormulaBugDiscoveryProtocol verifies the implement
// step documents the BUG-DISCOVERED: NOTES protocol per synthesis.
func TestTestImproverFormulaBugDiscoveryProtocol(t *testing.T) {
	f, err := ParseFile("formulas/mol-polecat-work-test-improver.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	impl := f.GetStep("implement")
	if impl == nil {
		t.Fatal("implement step missing")
	}

	requiredPhrases := []string{
		"BUG-DISCOVERED:",
		"Do NOT push the test",
		"Do NOT fix the source",
	}
	for _, phrase := range requiredPhrases {
		if !strings.Contains(impl.Description, phrase) {
			t.Errorf("implement step does not contain %q; bug-discovery protocol is incomplete", phrase)
		}
	}
}

// TestTestImproverFormulaSandboxIntegration verifies that gate steps
// reference the sandbox wrapper and credential stripping.
func TestTestImproverFormulaSandboxIntegration(t *testing.T) {
	f, err := ParseFile("formulas/mol-polecat-work-test-improver.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	// Gate 4a is the first gate that explicitly documents running through sandbox.
	gate4a := f.GetStep("gate-4a-coverage-delta")
	if gate4a == nil {
		t.Fatal("gate-4a-coverage-delta step missing")
	}
	if !strings.Contains(gate4a.Description, "sandbox") {
		t.Error("gate-4a does not reference sandbox wrapper; gates must run sandboxed")
	}

	// The formula description should mention sandbox integration.
	if !strings.Contains(f.Description, "sandbox") {
		t.Error("formula description does not mention sandbox integration")
	}
}

// TestTestImproverFormulaVars verifies required variables are declared
// with correct defaults.
func TestTestImproverFormulaVars(t *testing.T) {
	f, err := ParseFile("formulas/mol-polecat-work-test-improver.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	// Required vars (no default).
	requiredVars := []string{"issue", "target_rig"}
	for _, name := range requiredVars {
		v, ok := f.Vars[name]
		if !ok {
			t.Errorf("required var %q missing", name)
			continue
		}
		if !v.Required {
			t.Errorf("var %q should be required", name)
		}
	}

	// Vars with defaults.
	wantDefaults := map[string]string{
		"base_branch":          "main",
		"size_budget_max_files": "3",
		"size_budget_max_loc":  "200",
		"conventions_sheet_path": ".gt/auto-test-pr/conventions.md",
	}
	for name, want := range wantDefaults {
		v, ok := f.Vars[name]
		if !ok {
			t.Errorf("var %q missing", name)
			continue
		}
		if v.Default != want {
			t.Errorf("var %q default = %q, want %q", name, v.Default, want)
		}
	}
}
