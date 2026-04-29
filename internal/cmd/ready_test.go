package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
)

func TestGetFormulaNames(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()
	formulasDir := filepath.Join(tmpDir, "formulas")
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		t.Fatalf("creating formulas dir: %v", err)
	}

	// Create some formula files
	formulas := []string{
		constants.MolDeaconPatrol + ".formula.toml",
		constants.MolWitnessPatrol + ".formula.toml",
		"shiny.formula.toml",
	}
	for _, f := range formulas {
		path := filepath.Join(formulasDir, f)
		if err := os.WriteFile(path, []byte("# test"), 0644); err != nil {
			t.Fatalf("writing %s: %v", f, err)
		}
	}

	// Also create a non-formula file (should be ignored)
	if err := os.WriteFile(filepath.Join(formulasDir, ".installed.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("writing .installed.json: %v", err)
	}

	// Test
	names := getFormulaNames(tmpDir)
	if names == nil {
		t.Fatal("getFormulaNames returned nil")
	}

	expected := []string{constants.MolDeaconPatrol, constants.MolWitnessPatrol, "shiny"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("expected formula name %q not found", name)
		}
	}

	// Should not include the .installed.json file
	if names[".installed"] {
		t.Error(".installed should not be in formula names")
	}

	if len(names) != len(expected) {
		t.Errorf("got %d formula names, want %d", len(names), len(expected))
	}
}

func TestGetFormulaNames_NonexistentDir(t *testing.T) {
	names := getFormulaNames("/nonexistent/path")
	if names != nil {
		t.Error("expected nil for nonexistent directory")
	}
}

func TestFilterFormulaScaffolds(t *testing.T) {
	formulaNames := map[string]bool{
		constants.MolDeaconPatrol:  true,
		constants.MolWitnessPatrol: true,
	}

	issues := []*beads.Issue{
		{ID: constants.MolDeaconPatrol, Title: constants.MolDeaconPatrol},
		{ID: constants.MolDeaconPatrol + ".inbox-check", Title: "Handle callbacks"},
		{ID: constants.MolDeaconPatrol + ".health-scan", Title: "Check health"},
		{ID: constants.MolWitnessPatrol, Title: constants.MolWitnessPatrol},
		{ID: constants.MolWitnessPatrol + ".loop-or-exit", Title: "Loop or exit"},
		{ID: "hq-123", Title: "Real work item"},
		{ID: "hq-wisp-abc", Title: "Actual wisp"},
		{ID: "gt-456", Title: "Project issue"},
	}

	filtered := filterFormulaScaffolds(issues, formulaNames)

	// Should only have the non-scaffold issues
	if len(filtered) != 3 {
		t.Errorf("got %d filtered issues, want 3", len(filtered))
	}

	expectedIDs := map[string]bool{
		"hq-123":      true,
		"hq-wisp-abc": true,
		"gt-456":      true,
	}
	for _, issue := range filtered {
		if !expectedIDs[issue.ID] {
			t.Errorf("unexpected issue in filtered result: %s", issue.ID)
		}
	}
}

func TestFilterFormulaScaffolds_NilFormulaNames(t *testing.T) {
	issues := []*beads.Issue{
		{ID: "hq-123", Title: "Real work"},
		{ID: constants.MolDeaconPatrol, Title: "Would be filtered"},
	}

	// With nil formula names, should return all issues unchanged
	filtered := filterFormulaScaffolds(issues, nil)
	if len(filtered) != len(issues) {
		t.Errorf("got %d issues, want %d (nil formulaNames should return all)", len(filtered), len(issues))
	}
}

func TestFilterFormulaScaffolds_EmptyFormulaNames(t *testing.T) {
	issues := []*beads.Issue{
		{ID: "hq-123", Title: "Real work"},
		{ID: constants.MolDeaconPatrol, Title: "Would be filtered"},
	}

	// With empty formula names, should return all issues unchanged
	filtered := filterFormulaScaffolds(issues, map[string]bool{})
	if len(filtered) != len(issues) {
		t.Errorf("got %d issues, want %d (empty formulaNames should return all)", len(filtered), len(issues))
	}
}

func TestFilterFormulaScaffolds_EmptyIssues(t *testing.T) {
	formulaNames := map[string]bool{constants.MolDeaconPatrol: true}
	filtered := filterFormulaScaffolds([]*beads.Issue{}, formulaNames)
	if len(filtered) != 0 {
		t.Errorf("got %d issues, want 0", len(filtered))
	}
}

func TestFilterFormulaScaffolds_DotInNonScaffold(t *testing.T) {
	// Issue ID has a dot but prefix is not a formula name
	formulaNames := map[string]bool{constants.MolDeaconPatrol: true}

	issues := []*beads.Issue{
		{ID: "hq-cv.synthesis-step", Title: "Convoy synthesis"},
		{ID: "some.other.thing", Title: "Random dotted ID"},
	}

	filtered := filterFormulaScaffolds(issues, formulaNames)
	if len(filtered) != 2 {
		t.Errorf("got %d issues, want 2 (non-formula dots should not filter)", len(filtered))
	}
}

// TestFilterIdentityBeads verifies that gt ready strips agent/role/rig
// identity beads so that polecats and dog dispatchers never see them as
// selectable work. Covers label, type, ID-suffix, and title-regex paths
// (see gu-huta — widen filter to cover witness/crew/dog/mayor/deacon).
func TestFilterIdentityBeads(t *testing.T) {
	tests := []struct {
		name     string
		input    *beads.Issue
		filtered bool // true = should be removed from ready output
	}{
		// Real work bead — must pass through.
		{
			name:     "plain task bead passes",
			input:    &beads.Issue{ID: "gu-abc123", Title: "Fix parser bug", Type: "task"},
			filtered: false,
		},
		{
			name:     "bug bead passes",
			input:    &beads.Issue{ID: "gu-def456", Title: "Dispatcher drops ready beads", Type: "bug"},
			filtered: false,
		},

		// Label / type criteria (already covered by IsAgentBead).
		{
			name:     "gt:agent label filtered",
			input:    &beads.Issue{ID: "gu-xyz", Title: "Random title", Labels: []string{"gt:agent"}},
			filtered: true,
		},
		{
			name:     "legacy type=agent filtered",
			input:    &beads.Issue{ID: "gu-xyz", Title: "Random title", Type: "agent"},
			filtered: true,
		},
		{
			name:     "gt:role label filtered",
			input:    &beads.Issue{ID: "gu-xyz", Title: "Role doc", Labels: []string{"gt:role"}},
			filtered: true,
		},
		{
			name:     "gt:rig label filtered",
			input:    &beads.Issue{ID: "gu-xyz", Title: "Rig tracker", Labels: []string{"gt:rig"}},
			filtered: true,
		},

		// ID-based criteria.
		{
			name:     "role suffix filtered",
			input:    &beads.Issue{ID: "hq-crew-role", Title: "Crew role definition"},
			filtered: true,
		},
		{
			name:     "-rig- ID filtered",
			input:    &beads.Issue{ID: "gt-rig-gastown", Title: "Gastown rig identity"},
			filtered: true,
		},

		// Title-regex criteria (gu-huta extensions).
		{
			name:     "polecat title filtered",
			input:    &beads.Issue{ID: "gu-abc", Title: "gu-gastown-polecat-guzzle", Type: "task"},
			filtered: true,
		},
		{
			name:     "witness title filtered",
			input:    &beads.Issue{ID: "gu-def", Title: "gu-gastown-witness", Type: "task"},
			filtered: true,
		},
		{
			name:     "refinery title filtered",
			input:    &beads.Issue{ID: "gu-ghi", Title: "gu-gastown-refinery", Type: "task"},
			filtered: true,
		},
		{
			name:     "crew title filtered",
			input:    &beads.Issue{ID: "gu-jkl", Title: "gu-gastown-crew-joe", Type: "task"},
			filtered: true,
		},
		{
			name:     "mayor title filtered",
			input:    &beads.Issue{ID: "hq-mno", Title: "hq-mayor", Type: "task"},
			filtered: true,
		},
		{
			name:     "dog title filtered",
			input:    &beads.Issue{ID: "hq-pqr", Title: "hq-dog-alpha", Type: "task"},
			filtered: true,
		},

		// Near miss — role keyword mid-title should NOT filter.
		{
			name:     "refinery mid-title passes",
			input:    &beads.Issue{ID: "gu-work", Title: "af-refinery-feature-work", Type: "task"},
			filtered: false,
		},
		{
			name:     "witness in sentence passes",
			input:    &beads.Issue{ID: "gu-work2", Title: "Add witness support to feature", Type: "task"},
			filtered: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := []*beads.Issue{tt.input}
			out := filterIdentityBeads(in)
			gotFiltered := len(out) == 0
			if gotFiltered != tt.filtered {
				t.Errorf("filterIdentityBeads(%+v): filtered=%v, want %v", tt.input, gotFiltered, tt.filtered)
			}
		})
	}
}
