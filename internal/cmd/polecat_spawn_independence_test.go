package cmd

import (
	"fmt"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// stubShower is a minimal beadShower for guard tests.
type stubShower struct {
	issues map[string]*beads.Issue
}

func (s *stubShower) Show(id string) (*beads.Issue, error) {
	if iss, ok := s.issues[id]; ok {
		return iss, nil
	}
	return nil, fmt.Errorf("issue %s not found", id)
}

func TestPolecatNameFromAgent(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"gastown/polecats/capable", "capable"},
		{"lia_bac/polecats/Toast", "Toast"},
		{"  gastown/polecats/nux  ", "nux"}, // surrounding space on the address
		{"gastown/crew/gagecane", ""},       // crew is not a polecat
		{"gastown/witness", ""},
		{"", ""},
		{"capable", ""}, // bare name without the marker is ambiguous → ignore
	}
	for _, c := range cases {
		if got := polecatNameFromAgent(c.in); got != c.want {
			t.Errorf("polecatNameFromAgent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestBuilderIndependence_ExcludesBuilderOfReviewedWork reproduces the gs-aoz
// incident: review gate lb-0kdn dep-blocks on build leg lb-yuhl, which was
// built (closed, assignee retained) by polecat "capable". When dispatching the
// gate, "capable" must be excluded from idle-polecat reuse.
func TestBuilderIndependence_ExcludesBuilderOfReviewedWork(t *testing.T) {
	shower := &stubShower{issues: map[string]*beads.Issue{
		"lb-0kdn": {ID: "lb-0kdn", Status: "open", DependsOn: []string{"lb-yuhl"}},
		"lb-yuhl": {ID: "lb-yuhl", Status: "closed", Assignee: "lia_bac/polecats/capable"},
	}}

	excl := builderIndependenceExclusions(shower, "lb-0kdn")

	if !excl["capable"] {
		t.Errorf("expected builder 'capable' to be excluded, got %v", excl)
	}
}

// TestBuilderIndependence_ExcludesPriorBuilderOfSameBead covers the self-regrab
// case: a reopened bead whose assignee still records the prior builder must not
// be reused by that same polecat.
func TestBuilderIndependence_ExcludesPriorBuilderOfSameBead(t *testing.T) {
	shower := &stubShower{issues: map[string]*beads.Issue{
		"gs-gate": {ID: "gs-gate", Status: "open", Assignee: "gastown/polecats/rictus"},
	}}

	excl := builderIndependenceExclusions(shower, "gs-gate")

	if !excl["rictus"] {
		t.Errorf("expected prior builder 'rictus' to be excluded, got %v", excl)
	}
}

// TestBuilderIndependence_NoDepsNoAssignee returns an empty set so dispatch
// proceeds normally (no idle polecats are skipped).
func TestBuilderIndependence_NoDepsNoAssignee(t *testing.T) {
	shower := &stubShower{issues: map[string]*beads.Issue{
		"gs-fresh": {ID: "gs-fresh", Status: "open"},
	}}

	excl := builderIndependenceExclusions(shower, "gs-fresh")

	if len(excl) != 0 {
		t.Errorf("expected no exclusions for a bead with no deps/assignee, got %v", excl)
	}
}

// TestBuilderIndependence_CrewBuilderNotExcluded verifies that a dependency
// built by a crew member (not a polecat) produces no exclusion — crew are never
// idle-reuse candidates, and we must not accidentally bar an unrelated polecat
// of the same short name.
func TestBuilderIndependence_CrewBuilderNotExcluded(t *testing.T) {
	shower := &stubShower{issues: map[string]*beads.Issue{
		"gs-gate": {ID: "gs-gate", Status: "open", DependsOn: []string{"gs-dep"}},
		"gs-dep":  {ID: "gs-dep", Status: "closed", Assignee: "gastown/crew/gagecane"},
	}}

	excl := builderIndependenceExclusions(shower, "gs-gate")

	if len(excl) != 0 {
		t.Errorf("expected no exclusions for a crew-built dependency, got %v", excl)
	}
}

// TestBuilderIndependence_MultipleDeps excludes the builders of every reviewed
// dependency, case-insensitively.
func TestBuilderIndependence_MultipleDeps(t *testing.T) {
	shower := &stubShower{issues: map[string]*beads.Issue{
		"gs-gate": {ID: "gs-gate", Status: "open", DependsOn: []string{"gs-a", "gs-b"}},
		"gs-a":    {ID: "gs-a", Status: "closed", Assignee: "gastown/polecats/Furiosa"},
		"gs-b":    {ID: "gs-b", Status: "closed", Assignee: "gastown/polecats/nux"},
	}}

	excl := builderIndependenceExclusions(shower, "gs-gate")

	if !excl["furiosa"] || !excl["nux"] {
		t.Errorf("expected both 'furiosa' and 'nux' excluded, got %v", excl)
	}
}

// TestBuilderIndependence_NilAndEmpty guards the degenerate inputs.
func TestBuilderIndependence_NilAndEmpty(t *testing.T) {
	if got := builderIndependenceExclusions(nil, "gs-x"); len(got) != 0 {
		t.Errorf("nil shower should yield empty set, got %v", got)
	}
	shower := &stubShower{issues: map[string]*beads.Issue{}}
	if got := builderIndependenceExclusions(shower, ""); len(got) != 0 {
		t.Errorf("empty beadID should yield empty set, got %v", got)
	}
	// Unknown bead (Show errors) must not panic and must yield empty set.
	if got := builderIndependenceExclusions(shower, "gs-missing"); len(got) != 0 {
		t.Errorf("unknown bead should yield empty set, got %v", got)
	}
}
