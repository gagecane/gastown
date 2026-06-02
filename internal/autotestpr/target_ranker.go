// Target picking for the auto-test-pr cycle (synthesis §Key Components
// §1 step 4).
//
// Phase 0 task 4 (gu-2n7xi). The cycle computes candidate files from
// recent churn × coverage profile, ranks them by
// (churn × uncovered_branches), drops any file under a per-file
// rejection cooldown, and — once a file is chosen — orders that file's
// uncovered branches by line-distance to recent-churn ranges (PRD
// Non-Goal NG5: prefer recently-changed uncovered branches over legacy
// untouched code).
//
// These functions are intentionally pure: they take pre-computed churn
// and coverage inputs and return ranked candidates. The git/coverage
// IO that produces those inputs lives in the Phase 1 wiring; isolating
// the ranking logic here keeps it unit-testable without a worktree.
//
// Design context:
//   - .designs/auto-test-pr/synthesis.md §"Proposed Design component 1"
//     steps 4-5, R21 (within-file churn-proximity ranking).
package autotestpr

import (
	"sort"
	"time"
)

// UncoveredBranch is a single uncovered basic block in a target file,
// as carried in the dispatch envelope's targets[].uncovered_branches[].
type UncoveredBranch struct {
	// Line is the 1-based source line where the branch begins.
	Line int `json:"line"`

	// Kind is a short tag describing the branch form (e.g. "if-true",
	// "switch-case-default"). Free-form; produced by the coverage
	// classifier in Phase 1 wiring.
	Kind string `json:"kind"`
}

// ChurnRange is an inclusive line range [Start, End] that was touched
// by a commit within the churn window (git log -L / blame). Used by
// the NG5 within-file proximity ranking.
type ChurnRange struct {
	Start int
	End   int
}

// TargetCandidate is a single candidate file for test improvement,
// pre-populated with its churn count and uncovered branches. Ranking
// is a pure function of these fields plus the rejection log.
type TargetCandidate struct {
	// Path is the repo-relative file path (e.g. "internal/cmd/foo.go").
	Path string `json:"path"`

	// Churn is the number of commits touching this file within the
	// churn window (default 30 days).
	Churn int `json:"-"`

	// UncoveredBranches are the file's uncovered basic blocks.
	UncoveredBranches []UncoveredBranch `json:"uncovered_branches"`

	// CoveragePctBefore is the file's branch-coverage fraction before
	// this cycle, surfaced in the dispatch envelope for the MR banner.
	CoveragePctBefore float64 `json:"coverage_pct_before"`

	// ChurnRanges are the line ranges touched within the churn window,
	// used to order UncoveredBranches by proximity. Not serialized.
	ChurnRanges []ChurnRange `json:"-"`
}

// score is the ranking key: churn × number of uncovered branches.
// Higher is better. A file with no uncovered branches scores 0 and is
// effectively never picked (nothing to improve).
func (c TargetCandidate) score() int {
	return c.Churn * len(c.UncoveredBranches)
}

// RankCandidates returns the candidates ranked best-first by
// (churn × uncovered_branches), after dropping any candidate whose
// path is under an active per-file rejection cooldown.
//
// A path is under cooldown if the rejection log contains a record for
// that exact path whose CooldownUntil is strictly after now. The
// rejection log is the materialized output of the OQ4-fallback
// attachment beads (see attachments.MaterializeAutoTestState).
//
// Ties (equal score) are broken by path for deterministic output.
// Candidates that score 0 (no uncovered branches) are dropped — there
// is nothing to test.
//
// The returned slice is a fresh allocation; the input is not mutated.
func RankCandidates(candidates []TargetCandidate, rejections []RejectionRecord, now time.Time) []TargetCandidate {
	// Build the set of paths under active cooldown.
	cooled := make(map[string]struct{}, len(rejections))
	for _, r := range rejections {
		if r.CooldownUntil.After(now) {
			cooled[r.File] = struct{}{}
		}
	}

	out := make([]TargetCandidate, 0, len(candidates))
	for _, c := range candidates {
		if _, blocked := cooled[c.Path]; blocked {
			continue
		}
		if c.score() == 0 {
			continue
		}
		out = append(out, c)
	}

	sort.SliceStable(out, func(i, j int) bool {
		si, sj := out[i].score(), out[j].score()
		if si != sj {
			return si > sj // higher score first
		}
		return out[i].Path < out[j].Path // stable tiebreak
	})

	return out
}

// OrderUncoveredByChurnProximity returns a copy of branches ordered by
// ascending line-distance to the nearest churn range (NG5). Branches
// inside a churn range have distance 0 and sort first; branches far
// from any recent change sort last. This biases the polecat toward
// testing recently-changed code rather than backfilling legacy
// untouched branches in the same file.
//
// Ties (equal distance) preserve ascending line order for determinism.
// When churnRanges is empty, branches are returned in ascending line
// order (no proximity signal — fall back to source order).
//
// The input slice is not mutated.
func OrderUncoveredByChurnProximity(branches []UncoveredBranch, churnRanges []ChurnRange) []UncoveredBranch {
	out := make([]UncoveredBranch, len(branches))
	copy(out, branches)

	dist := func(line int) int {
		if len(churnRanges) == 0 {
			return 0
		}
		best := -1
		for _, r := range churnRanges {
			var d int
			switch {
			case line < r.Start:
				d = r.Start - line
			case line > r.End:
				d = line - r.End
			default:
				d = 0 // inside the range
			}
			if best == -1 || d < best {
				best = d
			}
		}
		return best
	}

	sort.SliceStable(out, func(i, j int) bool {
		di, dj := dist(out[i].Line), dist(out[j].Line)
		if di != dj {
			return di < dj // closer to churn first
		}
		return out[i].Line < out[j].Line // stable tiebreak
	})

	return out
}
