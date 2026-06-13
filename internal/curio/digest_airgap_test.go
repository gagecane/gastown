package curio

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExcludeSelfReferential_DropsAndKeeps proves the Q5 layer-1 air-gap: a
// candidate set mixing self-referential findings (a Curio proposal-derived
// rule_id, a Curio-telemetry series) with a parallel non-Curio finding renders
// with the self-refs excluded and the non-Curio one surviving.
func TestExcludeSelfReferential_DropsAndKeeps(t *testing.T) {
	cands := []Candidate{
		// Self-ref (a): rule_id prefixed proposed_ — a prior/pending proposal.
		newCandidate("w1", ProposedRulePrefix+"alarm_rate_spike", "sling", "", "sling", 9, "proposal-about-a-proposal"),
		// Self-ref (b): Curio's own telemetry series (CurioSeriesPrefix).
		newCandidate("w1", "alarm_rate_spike", CurioSeriesPrefix+"patrol_cycle", "", CurioSeriesPrefix+"patrol_cycle", 99, "curio's own series"),
		// Parallel NON-Curio finding — must survive.
		newCandidate("w1", "alarm_rate_spike", "sling", "", "sling", 450, `series "sling" rate 450 exceeds threshold 350`),
	}

	got := ExcludeSelfReferential(cands)

	if len(got) != 1 {
		t.Fatalf("ExcludeSelfReferential kept %d candidates, want 1\ngot: %+v", len(got), got)
	}
	if got[0].Series != "sling" || got[0].RuleID != "alarm_rate_spike" {
		t.Errorf("surviving candidate = %+v, want the non-Curio sling finding", got[0])
	}

	// The filter must not mutate or alias the input slice's contents.
	if len(cands) != 3 {
		t.Errorf("input slice mutated: len = %d, want 3", len(cands))
	}
}

// TestExcludeSelfReferential_EmptyAndAllDropped covers the boundary cases.
func TestExcludeSelfReferential_EmptyAndAllDropped(t *testing.T) {
	if got := ExcludeSelfReferential(nil); len(got) != 0 {
		t.Errorf("nil input → %d candidates, want 0", len(got))
	}
	allSelf := []Candidate{
		newCandidate("w1", ProposedRulePrefix+"x", "t", "", "s", 1, "proposal"),
		newCandidate("w1", "alarm_rate_spike", CurioSeriesPrefix+"x", "", CurioSeriesPrefix+"x", 1, "telemetry"),
	}
	if got := ExcludeSelfReferential(allSelf); len(got) != 0 {
		t.Errorf("all-self-ref input → %d candidates, want 0", len(got))
	}
}

// TestRenderedDigestNeverShowsSelfRef wires the filter the way the caller does
// (ExcludeSelfReferential before RenderDigest) and asserts the rendered artifact
// — the thing the agent actually reads — carries no self-referential cluster.
func TestRenderedDigestNeverShowsSelfRef(t *testing.T) {
	cands := []Candidate{
		newCandidate("w1", ProposedRulePrefix+"alarm_rate_spike", "sling", "", "sling", 9, "PROPOSED-SELFREF-MARKER"),
		newCandidate("w1", "alarm_rate_spike", CurioSeriesPrefix+"patrol", "", CurioSeriesPrefix+"patrol", 99, "CURIO-SERIES-MARKER"),
		newCandidate("w1", "kill_signal_near_dolt", "deacon#0", "", "dog.log.kill_signal", 1, "real finding"),
	}
	digest := RenderDigest(fixedCutoff, ExcludeSelfReferential(cands), nil)

	for _, marker := range []string{"PROPOSED-SELFREF-MARKER", "CURIO-SERIES-MARKER", ProposedRulePrefix, CurioSeriesPrefix} {
		if strings.Contains(digest, marker) {
			t.Errorf("rendered digest leaked self-referential token %q:\n%s", marker, digest)
		}
	}
	if !strings.Contains(digest, "dog.log.kill_signal") {
		t.Errorf("real finding missing from digest:\n%s", digest)
	}
}

// TestCurioSeriesPrefix_SingleSourced is the mechanical single-sourcing
// invariant from the bead: the CurioSeriesPrefix membership check must have ONE
// definition (isCurioSeries), not be re-implemented as an inline
// strings.HasPrefix(..., CurioSeriesPrefix) at each call site. It parses the
// package's non-test .go files and asserts no inline HasPrefix call references
// CurioSeriesPrefix — every site must route through isCurioSeries instead.
func TestCurioSeriesPrefix_SingleSourced(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "strings" || sel.Sel.Name != "HasPrefix" {
				return true
			}
			// An inline strings.HasPrefix referencing CurioSeriesPrefix is a
			// re-implementation of the air-gap predicate. The only allowed one is
			// inside isCurioSeries itself.
			for _, arg := range call.Args {
				if id, ok := arg.(*ast.Ident); ok && id.Name == "CurioSeriesPrefix" {
					pos := fset.Position(call.Pos())
					offenders = append(offenders, filepath.Base(pos.Filename)+":"+itoa(pos.Line))
				}
			}
			return true
		})
	}

	// isCurioSeries holds the single allowed inline check; everything else must
	// call it. Assert exactly one occurrence, and that it is in rules.go (where
	// isCurioSeries is defined).
	if len(offenders) != 1 {
		t.Fatalf("CurioSeriesPrefix HasPrefix check found at %v; want exactly 1 (inside isCurioSeries). "+
			"Route every other site through isCurioSeries to keep the air-gap single-sourced.", offenders)
	}
	if !strings.HasPrefix(offenders[0], "rules.go:") {
		t.Errorf("the single CurioSeriesPrefix check is at %s; want it inside isCurioSeries (rules.go)", offenders[0])
	}
}
