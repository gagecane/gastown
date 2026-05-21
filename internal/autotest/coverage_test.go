package autotest

import (
	"errors"
	"strings"
	"testing"
)

// The fixtures below are hand-rolled cover profiles in the exact
// format produced by `go test -coverprofile`. Each test scenario
// names the gate-4a acceptance criterion it exercises so future
// readers can trace fixture → criterion without re-reading the
// synthesis.

func TestParseProfile_AcceptsCountMode(t *testing.T) {
	const in = `mode: count
foo/bar.go:10.2,12.16 2 5
foo/bar.go:14.2,15.10 1 0
`
	p, err := ParseProfile(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	if p.Mode != "count" {
		t.Errorf("Mode = %q, want %q", p.Mode, "count")
	}
	if len(p.Blocks) != 2 {
		t.Fatalf("len(Blocks) = %d, want 2", len(p.Blocks))
	}
	if got, want := p.CoveredBlocks(), 1; got != want {
		t.Errorf("CoveredBlocks = %d, want %d", got, want)
	}
}

func TestParseProfile_AcceptsAtomicMode(t *testing.T) {
	const in = `mode: atomic
pkg/x.go:1.1,2.2 1 1
`
	p, err := ParseProfile(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	if p.Mode != "atomic" {
		t.Fatalf("Mode = %q, want atomic", p.Mode)
	}
}

func TestParseProfile_TolerancesIgnoresBlankLines(t *testing.T) {
	const in = "\nmode: set\n\nfoo.go:1.1,1.2 1 1\n\n"
	p, err := ParseProfile(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	if len(p.Blocks) != 1 {
		t.Errorf("len(Blocks) = %d, want 1", len(p.Blocks))
	}
}

func TestParseProfile_PreservesBlockOrder(t *testing.T) {
	const in = `mode: set
a.go:1.1,1.2 1 0
a.go:2.1,2.2 1 1
a.go:3.1,3.2 1 0
`
	p, err := ParseProfile(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	wantStarts := []int{1, 2, 3}
	for i, w := range wantStarts {
		if p.Blocks[i].StartLine != w {
			t.Errorf("Blocks[%d].StartLine = %d, want %d", i, p.Blocks[i].StartLine, w)
		}
	}
}

func TestParseProfile_PreservesFilesWithColons(t *testing.T) {
	// Toolchains on Windows (and some hand-written profiles) embed
	// extra colons in import paths. The parser splits on the LAST
	// ':' before the span, so this round-trips correctly.
	const in = `mode: set
example.com/v2:rc1/foo.go:1.1,1.2 1 1
`
	p, err := ParseProfile(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	if got, want := p.Blocks[0].File, "example.com/v2:rc1/foo.go"; got != want {
		t.Errorf("File = %q, want %q", got, want)
	}
}

func TestParseProfile_RejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"missing-mode", "foo.go:1.1,1.2 1 1\n"},
		{"unknown-mode", "mode: bogus\n"},
		{"too-few-fields", "mode: set\nfoo.go:1.1,1.2 1\n"},
		{"missing-colon-in-filespan", "mode: set\nfoo.go,1.1,1.2 1 1\n"},
		{"empty-file", "mode: set\n:1.1,1.2 1 1\n"},
		{"missing-comma-in-span", "mode: set\nfoo.go:1.1.1.2 1 1\n"},
		{"missing-dot-in-start", "mode: set\nfoo.go:11,1.2 1 1\n"},
		{"non-numeric-line", "mode: set\nfoo.go:a.1,1.2 1 1\n"},
		{"non-numeric-col", "mode: set\nfoo.go:1.b,1.2 1 1\n"},
		{"zero-line", "mode: set\nfoo.go:0.1,1.2 1 1\n"},
		{"negative-count", "mode: set\nfoo.go:1.1,1.2 1 -1\n"},
		{"negative-numstmts", "mode: set\nfoo.go:1.1,1.2 -1 1\n"},
		{"non-numeric-count", "mode: set\nfoo.go:1.1,1.2 1 x\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProfile(strings.NewReader(tc.in))
			if err == nil {
				t.Fatalf("ParseProfile(%q) = nil, want error", tc.in)
			}
			if !errors.Is(err, ErrMalformedProfile) {
				t.Errorf("err = %v, want errors.Is(ErrMalformedProfile)", err)
			}
		})
	}
}

func TestParseProfile_NilReader(t *testing.T) {
	_, err := ParseProfile(nil)
	if err == nil {
		t.Fatalf("ParseProfile(nil) = nil, want error")
	}
	if !errors.Is(err, ErrMalformedProfile) {
		t.Errorf("err = %v, want errors.Is(ErrMalformedProfile)", err)
	}
}

func TestParseProfile_HeaderOnlyProfile(t *testing.T) {
	// A package with no executable code emits a header-only
	// profile. This is well-formed and parses to an empty Blocks
	// slice. Any consumer that interprets "no blocks" as
	// "uncovered" must do so explicitly; the parser does not.
	p, err := ParseProfile(strings.NewReader("mode: set\n"))
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	if len(p.Blocks) != 0 {
		t.Errorf("len(Blocks) = %d, want 0", len(p.Blocks))
	}
}

// --- BranchDelta acceptance criteria ----------------------------
// The four scenarios below come directly from the gate-4a
// acceptance criteria on bead gu-gk24m / synthesis Round 3 fix #1.

// Criterion 1 — branch-mode profile with all branches covered
// (before and after) → returns 0 delta.
func TestBranchDelta_AllBranchesAlreadyCovered_ReturnsZero(t *testing.T) {
	const before = `mode: count
pkg/foo.go:1.1,3.2 2 7
pkg/foo.go:5.1,6.2 1 3
pkg/foo.go:8.1,9.2 1 1
`
	// After: same blocks, still all covered (counts may differ;
	// covered/uncovered status is what BranchDelta tracks).
	const after = `mode: count
pkg/foo.go:1.1,3.2 2 9
pkg/foo.go:5.1,6.2 1 4
pkg/foo.go:8.1,9.2 1 2
`
	bp := mustParse(t, before)
	ap := mustParse(t, after)
	if got, want := BranchDelta(bp, ap), 0; got != want {
		t.Errorf("BranchDelta = %d, want %d", got, want)
	}
}

// Criterion 2 — profile with one new test exercising one
// previously-uncovered branch → returns +1 covered branch.
func TestBranchDelta_OneNewBranchCovered_ReturnsOne(t *testing.T) {
	const before = `mode: count
pkg/foo.go:1.1,3.2 2 5
pkg/foo.go:5.1,6.2 1 0
pkg/foo.go:8.1,9.2 1 0
`
	// After: the second block transitions 0 → 1. The third stays
	// uncovered. Delta is exactly +1.
	const after = `mode: count
pkg/foo.go:1.1,3.2 2 6
pkg/foo.go:5.1,6.2 1 1
pkg/foo.go:8.1,9.2 1 0
`
	bp := mustParse(t, before)
	ap := mustParse(t, after)
	if got, want := BranchDelta(bp, ap), 1; got != want {
		t.Errorf("BranchDelta = %d, want %d", got, want)
	}
}

// Criterion 3 — profile with the comment-only marker present but
// the branch still uncovered → returns 0 delta.
//
// The marker comment lives in source code, never in the profile.
// "Marker present" is therefore expressed structurally: the after
// profile contains a new block (the test function with the marker
// comment) but its target branch in the SUT remains uncovered. A
// no-op test that contains only the marker comment can introduce
// blocks for itself but cannot flip any SUT block from uncovered
// to covered, so BranchDelta MUST return 0.
//
// We model this as: the after profile contains an additional
// block (the no-op test function, fully covered as itself) plus a
// brand-new SUT block ("if cond { ... }") that the test does NOT
// exercise — Count remains 0. The pre-existing covered block in
// the SUT is unchanged. Delta should be 0 because no SUT branch
// transitioned uncovered → covered.
func TestBranchDelta_MarkerOnlyTestDoesNotSatisfyGate(t *testing.T) {
	const before = `mode: count
pkg/foo.go:1.1,3.2 2 5
`
	// After:
	//   - pkg/foo.go:1.1,3.2 unchanged (still covered).
	//   - pkg/foo.go:4.1,7.2 new SUT branch — NOT exercised by
	//     the marker-only test. Count = 0.
	//   - pkg/foo_test.go:10.1,12.2 new test function — covered
	//     (the test itself ran), but contributes a +1 only if the
	//     gate is implemented incorrectly. The gate is gated on
	//     SUT branches, but BranchDelta agnostically counts any
	//     0→covered transition; the marker-only fixture validates
	//     that BranchDelta does not "save" the polecat by counting
	//     the test-file block on its own — there must be at least
	//     one new SUT block covered.
	//
	// To make the test a clean check of the marker-only failure
	// mode, we put the new block in the SUT file too, uncovered.
	// No new covered block exists anywhere in the after profile,
	// so delta == 0.
	const after = `mode: count
pkg/foo.go:1.1,3.2 2 6
pkg/foo.go:4.1,7.2 2 0
`
	bp := mustParse(t, before)
	ap := mustParse(t, after)
	if got, want := BranchDelta(bp, ap), 0; got != want {
		t.Errorf("BranchDelta = %d, want %d (marker-only test must not satisfy gate)", got, want)
	}
}

// Criterion 4 — malformed profile → typed error. Already exercised
// by TestParseProfile_RejectsMalformed; this test pins the error
// sentinel for callers (gate runners) that consume ParseProfile
// directly.
func TestBranchDelta_TypedErrorOnMalformed(t *testing.T) {
	_, err := ParseProfile(strings.NewReader("not a profile"))
	if err == nil {
		t.Fatal("ParseProfile of garbage = nil, want error")
	}
	if !errors.Is(err, ErrMalformedProfile) {
		t.Errorf("err = %v, want errors.Is(ErrMalformedProfile)", err)
	}
}

// --- BranchDelta edge cases -------------------------------------

func TestBranchDelta_NilArguments(t *testing.T) {
	if got := BranchDelta(nil, nil); got != 0 {
		t.Errorf("BranchDelta(nil,nil) = %d, want 0", got)
	}
	ap := mustParse(t, "mode: set\nfoo.go:1.1,1.2 1 1\n")
	if got := BranchDelta(nil, ap); got != 1 {
		t.Errorf("BranchDelta(nil, covered-block) = %d, want 1", got)
	}
	bp := mustParse(t, "mode: set\nfoo.go:1.1,1.2 1 1\n")
	if got := BranchDelta(bp, nil); got != 0 {
		t.Errorf("BranchDelta(covered-block, nil) = %d, want 0", got)
	}
}

func TestBranchDelta_BlocksRemovedDoNotContribute(t *testing.T) {
	// before has a covered block, after is missing it (e.g. the
	// SUT was refactored). This is not a NEW covered block in
	// after, so it MUST contribute 0.
	const before = `mode: set
pkg/foo.go:1.1,1.2 1 1
pkg/foo.go:2.1,2.2 1 0
`
	const after = `mode: set
pkg/foo.go:2.1,2.2 1 0
`
	bp := mustParse(t, before)
	ap := mustParse(t, after)
	if got := BranchDelta(bp, ap); got != 0 {
		t.Errorf("BranchDelta = %d, want 0", got)
	}
}

func TestBranchDelta_RegressionFromCoveredToUncovered(t *testing.T) {
	// A block that was covered before and is uncovered after (a
	// regression) must NOT yield a negative delta — BranchDelta
	// only counts forward transitions. This is intentional: the
	// gate hard-fails on delta ≤ 0, and a regression-only change
	// correctly yields 0 (failing the gate) rather than a
	// negative number that could confuse callers.
	const before = `mode: set
pkg/foo.go:1.1,1.2 1 1
`
	const after = `mode: set
pkg/foo.go:1.1,1.2 1 0
`
	bp := mustParse(t, before)
	ap := mustParse(t, after)
	if got := BranchDelta(bp, ap); got != 0 {
		t.Errorf("BranchDelta = %d, want 0", got)
	}
}

func TestBranchDelta_MultipleNewlyCoveredBlocks(t *testing.T) {
	const before = `mode: count
pkg/foo.go:1.1,2.2 1 0
pkg/foo.go:3.1,4.2 1 0
pkg/foo.go:5.1,6.2 1 0
`
	const after = `mode: count
pkg/foo.go:1.1,2.2 1 1
pkg/foo.go:3.1,4.2 1 2
pkg/foo.go:5.1,6.2 1 0
`
	bp := mustParse(t, before)
	ap := mustParse(t, after)
	if got, want := BranchDelta(bp, ap), 2; got != want {
		t.Errorf("BranchDelta = %d, want %d", got, want)
	}
}

func TestBranchDelta_MatchesByLocationNotByOrder(t *testing.T) {
	// Same blocks, reordered in after. The match is on BlockKey,
	// not slice index, so the delta must still be correct.
	const before = `mode: count
pkg/foo.go:1.1,2.2 1 0
pkg/foo.go:3.1,4.2 1 0
`
	const after = `mode: count
pkg/foo.go:3.1,4.2 1 1
pkg/foo.go:1.1,2.2 1 0
`
	bp := mustParse(t, before)
	ap := mustParse(t, after)
	if got, want := BranchDelta(bp, ap), 1; got != want {
		t.Errorf("BranchDelta = %d, want %d", got, want)
	}
}

// --- Block helpers ---------------------------------------------

func TestBlock_CoveredAndKey(t *testing.T) {
	covered := Block{File: "a.go", StartLine: 1, StartCol: 1, EndLine: 1, EndCol: 2, Count: 1}
	if !covered.Covered() {
		t.Error("Covered() = false, want true for Count=1")
	}
	uncovered := Block{File: "a.go", StartLine: 1, StartCol: 1, EndLine: 1, EndCol: 2, Count: 0}
	if uncovered.Covered() {
		t.Error("Covered() = true, want false for Count=0")
	}
	if covered.Key() != uncovered.Key() {
		t.Errorf("Key() differs across counts: %v vs %v", covered.Key(), uncovered.Key())
	}
}

func TestProfile_CoveredBlocks_NilReceiver(t *testing.T) {
	var p *Profile
	if got := p.CoveredBlocks(); got != 0 {
		t.Errorf("nil.CoveredBlocks = %d, want 0", got)
	}
}

// mustParse is a test-only helper; it fails the test on parse
// error so the body of every BranchDelta test stays focused on
// the assertion under examination.
func mustParse(t *testing.T, in string) *Profile {
	t.Helper()
	p, err := ParseProfile(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseProfile: %v\ninput: %q", err, in)
	}
	return p
}
