package curio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Fixture is one replay window: a normalized Input plus the grading metadata
// that says whether this window is an anchor incident (must fire) or a normal
// window (bounded candidate volume). Fixtures are checked-in, REDACTED captures
// of historical incidents — they are the replay harness corpus and the CI gate.
type Fixture struct {
	// Name is a human label for the window.
	Name string `json:"name"`
	// Anchor, when set, names the incident this window must reproduce. Empty
	// for normal/held-out windows.
	Anchor string `json:"anchor,omitempty"`
	// ExpectRules lists rule IDs that MUST fire on this window (anchors only).
	ExpectRules []string `json:"expect_rules,omitempty"`
	// Input is the normalized observation bundle for the window.
	Input Input `json:"input"`
}

// fixtureJSON mirrors Fixture but with a serializable Input. Window times are
// omitted from fixtures (rules don't read them); only Window.ID matters.
type fixtureJSON struct {
	Name        string   `json:"name"`
	Anchor      string   `json:"anchor,omitempty"`
	ExpectRules []string `json:"expect_rules,omitempty"`
	WindowID    string   `json:"window_id"`
	Beads       []BeadRecord
	LogLines    []LogLine
	EventCounts []SeriesCount
	Admissions  []AdmissionRecord
	// CurioBeads lists bead IDs Curio itself filed (Call 1(A) air-gap). A
	// fixture record whose CausalRoot is in this list is a self-reaction the
	// loop-breaker must suppress. Optional; omitted in most fixtures.
	CurioBeads []string
}

// LoadFixtures reads all *.json fixtures from dir, sorted by filename for
// deterministic ordering.
func LoadFixtures(dir string) ([]Fixture, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading fixture dir %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var fixtures []Fixture
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // G304: test fixture path
		if err != nil {
			return nil, fmt.Errorf("reading fixture %s: %w", name, err)
		}
		var fj fixtureJSON
		if err := json.Unmarshal(data, &fj); err != nil {
			return nil, fmt.Errorf("parsing fixture %s: %w", name, err)
		}
		var curioBeads map[string]bool
		if len(fj.CurioBeads) > 0 {
			curioBeads = make(map[string]bool, len(fj.CurioBeads))
			for _, id := range fj.CurioBeads {
				curioBeads[id] = true
			}
		}
		fixtures = append(fixtures, Fixture{
			Name:        fj.Name,
			Anchor:      fj.Anchor,
			ExpectRules: fj.ExpectRules,
			Input: Input{
				Window:      Window{ID: fj.WindowID},
				Beads:       fj.Beads,
				LogLines:    fj.LogLines,
				EventCounts: fj.EventCounts,
				Admissions:  fj.Admissions,
				CurioBeads:  curioBeads,
			},
		})
	}
	return fixtures, nil
}

// GradeReport summarizes a replay run.
type GradeReport struct {
	// AnchorsHit maps anchor name → whether all its expected rules fired.
	AnchorsHit map[string]bool
	// MissingRules maps anchor name → expected rule IDs that did NOT fire.
	MissingRules map[string][]string
	// NormalCandidates is the max candidate count over any single normal window
	// (the precision-proxy volume metric).
	NormalCandidates int
	// WorstNormalWindow is the name of the window with NormalCandidates.
	WorstNormalWindow string
}

// Grade runs the rules over every fixture and grades recall (anchors fire) and
// the precision proxy (candidate volume on normal windows).
func Grade(rules []Rule, fixtures []Fixture) GradeReport {
	rep := GradeReport{
		AnchorsHit:   map[string]bool{},
		MissingRules: map[string][]string{},
	}
	for _, f := range fixtures {
		cands := Evaluate(rules, f.Input)
		fired := map[string]bool{}
		for _, c := range cands {
			fired[c.RuleID] = true
		}

		if f.Anchor != "" {
			var missing []string
			for _, want := range f.ExpectRules {
				if !fired[want] {
					missing = append(missing, want)
				}
			}
			rep.AnchorsHit[f.Anchor] = len(missing) == 0
			if len(missing) > 0 {
				rep.MissingRules[f.Anchor] = missing
			}
		} else {
			// Normal window: track the worst-case candidate volume.
			if len(cands) > rep.NormalCandidates {
				rep.NormalCandidates = len(cands)
				rep.WorstNormalWindow = f.Name
			}
		}
	}
	return rep
}
