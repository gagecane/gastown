package autotestpr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConventionsTemplate_GoldenFile is the snapshot test required by the
// Phase 0 task 2d acceptance criteria (round 3 fix #8). It verifies that
// the embedded conventions template byte-for-byte matches the checked-in
// golden file, so that drift in either file fails CI and forces a
// reviewer to acknowledge any change.
//
// To update the golden file after an intentional template edit, set
// AUTOTESTPR_UPDATE_GOLDEN=1 in the environment and re-run the test.
func TestConventionsTemplate_GoldenFile(t *testing.T) {
	goldenPath := filepath.Join("testdata", "conventions_template.golden.md")
	got := ConventionsTemplate()

	if os.Getenv("AUTOTESTPR_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("update golden: %v", err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden file %s: %v", goldenPath, err)
	}
	if string(want) != got {
		t.Errorf("conventions template drifted from golden file %s.\n"+
			"Re-run with AUTOTESTPR_UPDATE_GOLDEN=1 to refresh after an "+
			"intentional edit, then re-verify the required sections (NG2 "+
			"forbid-list, NG5 churn-proximity, D8 marker, D15 approval).",
			goldenPath)
	}
}

// TestConventionsTemplate_RequiredSections verifies the explicit
// acceptance criteria for Phase 0 task 2d (round 3 fix #8): the embedded
// template MUST contain the NG2 forbid-list (Benchmark/Example/Fuzz/
// integration/e2e/load), the NG5 churn-proximity preference paragraph,
// the D8 provenance-marker requirement, and the D15 approval-line
// instruction.
//
// These checks are intentionally redundant with the golden-file check:
// if a future template edit accidentally removes one of these sections
// AND somebody refreshes the golden file with AUTOTESTPR_UPDATE_GOLDEN
// without reading the diff, the golden test will go green but this test
// will go red.
func TestConventionsTemplate_RequiredSections(t *testing.T) {
	tmpl := ConventionsTemplate()

	cases := []struct {
		name     string
		needles  []string
		guidance string
	}{
		{
			name:     "NG2 forbid-list — section header",
			needles:  []string{"NG2:", "Forbidden test forms"},
			guidance: "the NG2 section header must be present so the polecat can locate the forbid-list",
		},
		{
			name:     "NG2 forbid-list — Benchmark forbidden",
			needles:  []string{"Benchmark"},
			guidance: "Benchmarks must be explicitly forbidden (PRD Non-Goal NG2)",
		},
		{
			name:     "NG2 forbid-list — Example forbidden",
			needles:  []string{"Example"},
			guidance: "Examples must be explicitly forbidden (PRD Non-Goal NG2)",
		},
		{
			name:     "NG2 forbid-list — Fuzz forbidden",
			needles:  []string{"Fuzz"},
			guidance: "Fuzz tests must be explicitly forbidden (PRD Non-Goal NG2)",
		},
		{
			name:     "NG2 forbid-list — integration tests forbidden",
			needles:  []string{"Integration tests"},
			guidance: "integration tests must be explicitly forbidden (PRD Non-Goal NG2)",
		},
		{
			name:     "NG2 forbid-list — end-to-end tests forbidden",
			needles:  []string{"End-to-end tests"},
			guidance: "end-to-end tests must be explicitly forbidden (PRD Non-Goal NG2)",
		},
		{
			name:     "NG2 forbid-list — load tests forbidden",
			needles:  []string{"Load tests"},
			guidance: "load tests must be explicitly forbidden (PRD Non-Goal NG2)",
		},
		{
			name:     "NG5 churn-proximity preference",
			needles:  []string{"NG5", "churn", "uncovered_branches"},
			guidance: "the NG5 churn-proximity preference paragraph must direct the polecat to prefer churn-adjacent uncovered branches",
		},
		{
			name:     "D8 provenance marker — header + literal marker",
			needles:  []string{"D8:", "Provenance marker", "// gt:auto-test-pr origin="},
			guidance: "the D8 section must show the literal provenance-marker comment so the polecat can copy it verbatim",
		},
		{
			name: "D15 approval-line instruction",
			needles: []string{
				"D15:",
				"approved-by",
				"bd update",
				"require_review_approval",
			},
			guidance: "the D15 section must spell out the approved-by:<user> label write that unblocks Refinery merge",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, needle := range tc.needles {
				if !strings.Contains(tmpl, needle) {
					t.Errorf("conventions template is missing required substring %q.\n"+
						"Reason: %s", needle, tc.guidance)
				}
			}
		})
	}
}
