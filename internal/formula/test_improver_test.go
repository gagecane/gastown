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

	// All expected steps in DAG order. The d19-reply step (Phase 0 task 3b,
	// gu-75jja) sits between pre-verify and submit-and-exit; it is a no-op
	// in mode=create and emits a reviewer-comment-thread reply in mode=revise.
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
		"d19-reply",
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
		{"pre-verify", "d19-reply"},
		{"d19-reply", "submit-and-exit"},
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

// TestTestImproverFormulaModeReviseDocumented verifies that the formula's
// description and load-context step both teach the polecat how to read
// the mode=revise dispatch envelope (args.revision shape, the
// comment_id targeted vs most-recent fallback paths). Without these,
// the dispatched polecat has no contract telling it which fields are
// available and which paths apply (Phase 0 task 3b: gu-75jja).
func TestTestImproverFormulaModeReviseDocumented(t *testing.T) {
	f, err := ParseFile("formulas/mol-polecat-work-test-improver.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	// Top-level description must teach both modes.
	for _, s := range []string{"mode=create", "mode=revise", "args.revision", "D19"} {
		if !strings.Contains(f.Description, s) {
			t.Errorf("formula description missing %q; mode=revise contract is incomplete", s)
		}
	}

	loadCtx := f.GetStep("load-context")
	if loadCtx == nil {
		t.Fatal("load-context step missing")
	}
	for _, s := range []string{
		"args.revision",
		"branch",
		"last_commit_sha",
		"comment_id",
		"comments[]",
	} {
		if !strings.Contains(loadCtx.Description, s) {
			t.Errorf("load-context step missing %q; revise envelope shape not documented", s)
		}
	}
}

// TestTestImproverFormulaD19ReplyStep verifies that the D19 reply step
// is wired between pre-verify and submit-and-exit, declares its
// transport options (Refinery bead-comment in v1, GH review-reply in
// v2), and references the SelectReplyTargets / RenderD19Reply helpers
// shared with the manual CLI (Phase 0 task 3b: gu-75jja).
func TestTestImproverFormulaD19ReplyStep(t *testing.T) {
	f, err := ParseFile("formulas/mol-polecat-work-test-improver.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	step := f.GetStep("d19-reply")
	if step == nil {
		t.Fatal("d19-reply step missing — Phase 0 task 3b not implemented")
	}

	// Step must depend on pre-verify so gates have run before reply.
	if len(step.Needs) == 0 || step.Needs[0] != "pre-verify" {
		t.Errorf("d19-reply needs = %v; want first dep to be pre-verify (gates run before reply)",
			step.Needs)
	}

	// submit-and-exit must depend on d19-reply (not pre-verify directly).
	submit := f.GetStep("submit-and-exit")
	if submit == nil {
		t.Fatal("submit-and-exit step missing")
	}
	foundD19Dep := false
	for _, n := range submit.Needs {
		if n == "d19-reply" {
			foundD19Dep = true
			break
		}
	}
	if !foundD19Dep {
		t.Errorf("submit-and-exit needs = %v; must include d19-reply so the reply happens before push",
			submit.Needs)
	}

	// The reply step body must teach both targeted and fallback paths
	// AND name the helper symbols so polecats know where the logic lives.
	// (Acceptance criteria: tests cover both --comment-id-targeted and
	// most-recent-thread fallback paths.)
	for _, s := range []string{
		"mode=revise",
		"comment_id",
		"most-recent",
		"non-resolved",
		"manual",
		"SelectReplyTargets",
		"RenderD19Reply",
	} {
		if !strings.Contains(step.Description, s) {
			t.Errorf("d19-reply step missing %q; D19 contract is incomplete", s)
		}
	}

	// Transport options for v1 (Refinery / bead-comment) AND v2 (GH PR).
	for _, s := range []string{"bead-comment", "gh pr review"} {
		if !strings.Contains(step.Description, s) {
			t.Errorf("d19-reply step missing transport %q; both Refinery and GH paths must be documented", s)
		}
	}

	// The step must explicitly mention skipping when mode=create so a
	// future polecat reading the formula does not post a banner against
	// a non-existent reviewer thread on the original create cycle.
	if !strings.Contains(step.Description, "create") {
		t.Errorf("d19-reply step does not document the mode=create skip semantics")
	}

	// Failure handling must NOT swallow transport errors silently — the
	// whole reason D19 was added (R23 in the risk register) is to
	// prevent silent reply-skips.
	for _, s := range []string{"escalate", "DEFERRED"} {
		// At least one of these must be referenced; check both.
		_ = s // (informational — actual assertion below)
	}
	if !strings.Contains(step.Description, "escalate") {
		t.Errorf("d19-reply step does not document escalation on failure; silent reply-skip is the failure mode D19 prevents")
	}
}

// TestTestImproverFormulaImplementCoversReviseMode verifies that the
// implement step teaches the polecat how to address reviewer feedback
// (mode=revise) in addition to writing new tests (mode=create). The
// summary-string contract is critical — the D19 reply banner uses it
// verbatim, so the implement step must explicitly tell the polecat to
// produce one and persist it on the bead.
func TestTestImproverFormulaImplementCoversReviseMode(t *testing.T) {
	f, err := ParseFile("formulas/mol-polecat-work-test-improver.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	impl := f.GetStep("implement")
	if impl == nil {
		t.Fatal("implement step missing")
	}

	for _, s := range []string{
		"mode=revise",
		"args.revision.comments",
		"D19-summary",       // the persisted-on-bead key the d19-reply step reads back
		"one-line summary",  // contract phrase
	} {
		if !strings.Contains(impl.Description, s) {
			t.Errorf("implement step missing %q; mode=revise path is under-documented", s)
		}
	}

	// Production-source-edit ban must be explicit — the output-allow-list
	// gate (4f) catches it but the implement step needs to warn polecats
	// up front so they don't write source-fix code that gate-4f rejects
	// at the end of a long cycle.
	if !strings.Contains(impl.Description, "production source") &&
		!strings.Contains(impl.Description, "production code") {
		t.Errorf("implement step does not warn against editing production source in revise mode")
	}
}

// TestTestImproverFormulaBranchSetupCoversReviseCheckout verifies that
// the branch-setup step teaches the polecat to check out the existing
// MR branch (rather than create a new one) when args.mode == "revise",
// and that it validates HEAD against args.revision.last_commit_sha so a
// stale dispatch envelope does not silently land a revision against
// obsolete feedback.
func TestTestImproverFormulaBranchSetupCoversReviseCheckout(t *testing.T) {
	f, err := ParseFile("formulas/mol-polecat-work-test-improver.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	bs := f.GetStep("branch-setup")
	if bs == nil {
		t.Fatal("branch-setup step missing")
	}

	for _, s := range []string{
		"mode=revise",
		"args.revision.branch",
		"args.revision.last_commit_sha",
		"git checkout",
		"git rev-parse HEAD",
	} {
		if !strings.Contains(bs.Description, s) {
			t.Errorf("branch-setup step missing %q; revise checkout flow is incomplete", s)
		}
	}

	// Stale-SHA escalation must be explicit so a polecat reading the
	// formula does not silently rebase past last_commit_sha.
	if !strings.Contains(bs.Description, "stale") {
		t.Errorf("branch-setup step does not document stale-SHA escalation; concurrent push race is unhandled")
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
		"base_branch":            "main",
		"size_budget_max_files":  "3",
		"size_budget_max_loc":    "200",
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
