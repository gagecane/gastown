package formula

import (
	"strings"
	"testing"
)

// TestMolPolecatWorkTestImprover_Resolves is the formula-skeleton test
// required by Phase 0 task 3a (per `.designs/auto-test-pr/synthesis.md`).
// It verifies that:
//
//  1. The skeleton parses cleanly from the embedded FS.
//  2. It declares `extends = ["mol-polecat-work"]` so step inheritance is
//     in play (the synthesis explicitly requires this — "extends
//     mol-polecat-work, idiomatic per mol-polecat-work-monorepo-tdd").
//  3. After Resolve(), the step list contains the seven gates 4a-4g plus
//     the bug-discovery NOTES protocol step, in the order specified by
//     the synthesis design (table 4a-4g + §3a NOTES protocol).
//  4. The submit step reaches a state where the polecat passes the two
//     auto-test-pr labels (`gt:auto-test-pr` and `rig:<target_rig>`) to
//     `gt done` via `--label` — Round 3 fix #6.
//  5. The `target_rig` variable is declared as required so a misconfigured
//     dispatch envelope cannot silently produce an unlabeled MR bead.
//
// This is the single integrating test for the formula skeleton; the
// per-step content is locked in by the description itself plus the
// mol-polecat-work parent's existing tests.
func TestMolPolecatWorkTestImprover_Resolves(t *testing.T) {
	data, err := GetEmbeddedFormulaContent("mol-polecat-work-test-improver")
	if err != nil {
		t.Fatalf("GetEmbeddedFormulaContent: %v", err)
	}

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got := f.Name; got != "mol-polecat-work-test-improver" {
		t.Errorf("formula name = %q, want mol-polecat-work-test-improver", got)
	}

	// (2) Extends mol-polecat-work.
	if len(f.Extends) != 1 || f.Extends[0] != "mol-polecat-work" {
		t.Errorf("Extends = %v, want [mol-polecat-work] (synthesis: extends mol-polecat-work, idiomatic per mol-polecat-work-monorepo-tdd)", f.Extends)
	}

	// (5) target_rig declared and required.
	if v, ok := f.Vars["target_rig"]; !ok {
		t.Errorf("vars.target_rig not declared; the formula's submit step needs it for --label rig:{{target_rig}} (Round 3 fix #6)")
	} else if !v.Required {
		t.Errorf("vars.target_rig.required = false, want true; an empty target_rig would produce an unlabeled MR bead and break the 3c cycle-close handler O(1) lookup")
	}

	resolved, err := Resolve(f, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// (3) The seven gates 4a-4g + bug-discovery NOTES step are present.
	wantGateIDs := []string{
		"implement.write-tests",
		"implement.gate-coverage-delta",
		"implement.gate-synthetic-mutant",
		"implement.gate-flakiness",
		"implement.gate-tautology-linter",
		"implement.gate-gitleaks",
		"implement.gate-output-allowlist",
		"implement.gate-size-budget",
		"implement.bug-discovered",
	}
	gotIDs := stepIDs(resolved)
	for _, wantID := range wantGateIDs {
		if !sliceContainsString(gotIDs, wantID) {
			t.Errorf("resolved formula missing step %q (got: %v) — the seven 4a-4g gates plus bug-discovery NOTES step are mandated by synthesis.md §Polecat formula table 4a-g + §3a", wantID, gotIDs)
		}
	}

	// Verify gate ordering matches the synthesis (4a → 4b → 4c → 4d → 4e
	// → 4f → 4g → bug-discovered). Each gate must list the previous one
	// in its `needs`.
	wantOrder := []string{
		"implement.write-tests",
		"implement.gate-coverage-delta",
		"implement.gate-synthetic-mutant",
		"implement.gate-flakiness",
		"implement.gate-tautology-linter",
		"implement.gate-gitleaks",
		"implement.gate-output-allowlist",
		"implement.gate-size-budget",
		"implement.bug-discovered",
	}
	for i := 1; i < len(wantOrder); i++ {
		step := findStep(resolved, wantOrder[i])
		if step == nil {
			continue // already reported above
		}
		prev := wantOrder[i-1]
		if !sliceContainsString(step.Needs, prev) {
			t.Errorf("step %q.Needs = %v, want it to include %q so gates execute in synthesis 4a→4g order", step.ID, step.Needs, prev)
		}
	}

	// (4) The submit step (which replaces parent's submit-and-exit via
	// compose.expand on test-improver-submit) instructs the polecat to
	// pass both auto-test-pr labels via --label. The expanded step ID
	// retains the parent target name "submit-and-exit" because the single
	// template entry uses id = "{target}".
	submit := findStep(resolved, "submit-and-exit")
	if submit == nil {
		t.Fatalf("submit-and-exit step missing after Resolve; expected test-improver-submit expansion to retain the original target ID. Got steps: %v", gotIDs)
	}
	if !strings.Contains(submit.Description, "--label gt:auto-test-pr") {
		t.Errorf("submit step description missing `--label gt:auto-test-pr`; the polecat will not label the MR bead with the auto-test-pr marker (Round 3 fix #6 — branch-GC scope and audit-trail backstop)")
	}
	if !strings.Contains(submit.Description, "--label rig:{{target_rig}}") {
		t.Errorf("submit step description missing `--label rig:{{target_rig}}`; the 3c cycle-close handler relies on this label for O(1) state-bead lookup (Round 3 fix #6)")
	}
}

// TestMolPolecatWorkTestImprover_NotRegisteredByMolecule is the bead's
// second acceptance criterion: "Formula skeleton is registered in the
// formula registry but not attached to any molecule." We test "not
// attached" by walking every other embedded formula and verifying none of
// them list our skeleton in `extends` or in a `compose.expand.with`.
//
// This is a forward-looking guard. Phase 0 task 4 will land
// `mol-auto-test-pr-cycle`, which dispatches polecats with
// mol-polecat-work-test-improver via the dispatch bead's
// `args.formula` field — NOT via formula composition. The composition
// path is reserved for legitimate workflow inheritance (e.g. a future
// rig-specific test-improver variant), and the bead acceptance pins the
// no-composition state at 3a-landing time.
func TestMolPolecatWorkTestImprover_NotRegisteredByMolecule(t *testing.T) {
	const skeletonName = "mol-polecat-work-test-improver"

	embedded, err := getEmbeddedFormulas()
	if err != nil {
		t.Fatalf("getEmbeddedFormulas: %v", err)
	}

	for filename := range embedded {
		// Skip ourselves.
		if strings.HasPrefix(filename, skeletonName) {
			continue
		}
		data, err := GetEmbeddedFormulaContent(strings.TrimSuffix(filename, ".formula.toml"))
		if err != nil {
			t.Errorf("GetEmbeddedFormulaContent(%s): %v", filename, err)
			continue
		}
		f, err := Parse(data)
		if err != nil {
			// Some formulas may have parse-time issues (e.g. expansions
			// without all vars); not our concern here.
			continue
		}
		for _, parent := range f.Extends {
			if parent == skeletonName {
				t.Errorf("formula %q lists %q in extends; skeleton must not be attached to any molecule per Phase 0 task 3a acceptance criteria",
					f.Name, skeletonName)
			}
		}
		if f.Compose != nil {
			for _, rule := range f.Compose.Expand {
				if rule.With == skeletonName {
					t.Errorf("formula %q has compose.expand.with = %q; skeleton must not be referenced by any composition rule per Phase 0 task 3a acceptance criteria",
						f.Name, skeletonName)
				}
			}
		}
	}
}

func findStep(f *Formula, id string) *Step {
	for i, s := range f.Steps {
		if s.ID == id {
			return &f.Steps[i]
		}
	}
	return nil
}

func sliceContainsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
