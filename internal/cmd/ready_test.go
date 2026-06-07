package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func issueIDs(issues []*beads.Issue) []string {
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	return ids
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

func TestGetWispIDsUsesBdMolWispList(t *testing.T) {
	beadsPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(`{"id":"stale-jsonl-wisp"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	bdPath := filepath.Join(binDir, "bd")
	bdScript := `#!/bin/sh
if [ "$1" = "mol" ] && [ "$2" = "wisp" ] && [ "$3" = "list" ] && [ "$4" = "--json" ]; then
  printf '{"wisps":[{"id":"dolt-wisp-1"},{"id":"dolt-wisp-2"}],"count":2}\n'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ids := getWispIDs(beadsPath)
	if !ids["dolt-wisp-1"] || !ids["dolt-wisp-2"] {
		t.Fatalf("expected IDs from bd mol wisp list, got %#v", ids)
	}
	if ids["stale-jsonl-wisp"] {
		t.Fatalf("getWispIDs read stale issues.jsonl; got %#v", ids)
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

		// gu-smr1: EPIC-titled task is filtered (data-hygiene guard).
		{
			name:     "EPIC: task title filtered",
			input:    &beads.Issue{ID: "gu-ta823", Title: "EPIC: Triage Queue", Type: "task"},
			filtered: true,
		},
		{
			name:     "Epic: task title filtered",
			input:    &beads.Issue{ID: "gu-ta824", Title: "Epic: cleanup pass", Type: "task"},
			filtered: true,
		},

		// gu-fs88: phase:epic label marks a container even without EPIC: title.
		{
			name:     "phase:epic label on task filtered",
			input:    &beads.Issue{ID: "gu-ta825", Title: "Triage Queue", Type: "task", Labels: []string{"phase:epic"}},
			filtered: true,
		},
		{
			name:     "phase:epic label on bug filtered",
			input:    &beads.Issue{ID: "gu-ta826", Title: "Bug backlog", Type: "bug", Labels: []string{"phase:epic"}},
			filtered: true,
		},
		{
			name:     "phase:epic label with other labels filtered",
			input:    &beads.Issue{ID: "gu-ta827", Title: "Multi-phase work", Type: "task", Labels: []string{"gt:coord", "phase:epic"}},
			filtered: true,
		},
		// gu-9j93s: real epics (type=epic) are NOT filtered by bd ready —
		// the old "caught upstream" assumption was false, so they surfaced as
		// phantom ready work that `gt sling` then refused. filterIdentityBeads
		// now drops them itself via dispatch.IsContainerBeadInfo.
		{
			name:     "real epic (type=epic) filtered",
			input:    &beads.Issue{ID: "gu-real-epic", Title: "Real epic", Type: "epic"},
			filtered: true,
		},
		{
			name:     "real epic with phase:epic label filtered",
			input:    &beads.Issue{ID: "gu-real-epic2", Title: "Real epic", Type: "epic", Labels: []string{"phase:epic"}},
			filtered: true,
		},
		{
			name:     "convoy container (type=convoy) filtered",
			input:    &beads.Issue{ID: "gu-cv-x", Title: "Convoy container", Type: "convoy"},
			filtered: true,
		},
		{
			name:     "gt:epic label on task filtered",
			input:    &beads.Issue{ID: "gu-lblepic", Title: "Plain title", Type: "task", Labels: []string{"gt:epic"}},
			filtered: true,
		},
		{
			name:     "gt:convoy label on task filtered",
			input:    &beads.Issue{ID: "gu-lblcv", Title: "Plain title", Type: "task", Labels: []string{"gt:convoy"}},
			filtered: true,
		},
		// Near-miss label must not filter.
		{
			name:     "phase:epics (plural) does NOT filter",
			input:    &beads.Issue{ID: "gu-plural", Title: "Normal work", Type: "task", Labels: []string{"phase:epics"}},
			filtered: false,
		},
		// gu-ea25u: a source bead with an MR in flight (awaiting_refinery_merge)
		// is work-already-done, not ready work — bd ready does not exclude it, so
		// the auto-dispatcher re-slung it every cycle. filterIdentityBeads now
		// drops it via dispatch.IsAwaitingMergeBeadInfo.
		{
			name:     "awaiting_refinery_merge label filtered",
			input:    &beads.Issue{ID: "gu-inflight", Title: "Fix dispatcher", Type: "bug", Labels: []string{"awaiting_refinery_merge"}},
			filtered: true,
		},
		{
			name:     "awaiting_refinery_merge among other labels filtered",
			input:    &beads.Issue{ID: "gu-inflight2", Title: "Fix dispatcher", Type: "bug", Labels: []string{"bug", "awaiting_refinery_merge"}},
			filtered: true,
		},
		{
			name:     "awaiting_refinery_recovery does NOT filter here",
			input:    &beads.Issue{ID: "gu-recov", Title: "Normal work", Type: "task", Labels: []string{"awaiting_refinery_recovery"}},
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

func TestFilterReadyIssuesByRoute(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("creating town beads dir: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"hq-","path":"."}`,
		`{"prefix":"hq-cv-","path":"."}`,
		`{"prefix":"bds-","path":"bd_symphony/mayor/rig"}`,
		`{"prefix":"gt-","path":"gastown/mayor/rig"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("writing routes: %v", err)
	}

	issues := []*beads.Issue{
		{ID: "hq-123", Title: "town work"},
		{ID: "hq-cv-123", Title: "town convoy"},
		{ID: "bds-town-stale", Title: "wrongly-created town bds row"},
		{ID: "unknown-123", Title: "unknown route"},
	}
	filtered := filterReadyIssuesByRoute(townRoot, "town", issues)
	if got, want := issueIDs(filtered), []string{"hq-123", "hq-cv-123"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("town filtered IDs = %v, want %v", got, want)
	}

	issues = []*beads.Issue{
		{ID: "bds-123", Title: "bd_symphony work"},
		{ID: "hq-123", Title: "town work in rig result"},
		{ID: "gt-123", Title: "other rig work"},
		{ID: "unknown-123", Title: "unknown route"},
	}
	filtered = filterReadyIssuesByRoute(townRoot, "bd_symphony", issues)
	if got, want := issueIDs(filtered), []string{"bds-123"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rig filtered IDs = %v, want %v", got, want)
	}
}
