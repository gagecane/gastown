package autotest

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixturePackage writes one or more files into a fresh tmpdir
// and returns the dir's absolute path. The dir is registered for
// cleanup via t.TempDir().
func writeFixturePackage(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, contents := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}

// allLines returns a LineSet containing every line of src — useful for
// fixture cases where the test wants to assert the runner's choice of
// which lines to emit, not its filtering.
func allLines(src string) LineSet {
	out := LineSet{}
	count := strings.Count(src, "\n") + 1
	for i := 1; i <= count; i++ {
		out = out.Add(i)
	}
	return out
}

// findCandidate returns the first candidate matching m within file or
// fails the test.
func findCandidate(t *testing.T, cands []Candidate, m Mutation, file string) Candidate {
	t.Helper()
	for _, c := range cands {
		if c.Mutation == m && c.File == file {
			return c
		}
	}
	t.Fatalf("no %s candidate in %s; got %d candidates", m, file, len(cands))
	return Candidate{}
}

// assertCompiles parses src to ensure it is still well-formed Go after
// a mutation has been applied. Plan promises offset-correct splices,
// not type-correctness — but unparseable output would mean the runner
// is shipping mutants `go test` cannot even compile, which is a bug.
func assertCompiles(t *testing.T, src []byte) {
	t.Helper()
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "mutated.go", src, parser.AllErrors); err != nil {
		t.Fatalf("mutated source does not parse: %v\n--- src ---\n%s", err, src)
	}
}

// ============================================================
// Grammar form (i): comment-out-line
// ============================================================

const fixtureCommentOut = `package fix

func Add(a, b int) int {
	x := a + b
	return x
}
`

func TestPlan_CommentOutLine_OnCoveredLine(t *testing.T) {
	dir := writeFixturePackage(t, map[string]string{"add.go": fixtureCommentOut})
	target := Target{
		PackageDir:   dir,
		TestName:     "TestAdd",
		FileSHA:      "sha-add",
		CoveredLines: map[string]LineSet{"add.go": {4: {}}},
	}
	cands, err := Plan(target)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	c := findCandidate(t, cands, MutationCommentOutLine, "add.go")
	if c.Line != 4 {
		t.Fatalf("comment-out line = %d, want 4", c.Line)
	}
	src, err := os.ReadFile(filepath.Join(dir, "add.go"))
	if err != nil {
		t.Fatal(err)
	}
	mutated, err := Apply(src, c)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	assertCompiles(t, mutated)
	if !strings.Contains(string(mutated), "_ = struct{}{}") {
		t.Fatalf("comment-out replacement absent\n%s", mutated)
	}
}

func TestPlan_CommentOutLine_OnUncoveredLineSkipped(t *testing.T) {
	dir := writeFixturePackage(t, map[string]string{"add.go": fixtureCommentOut})
	target := Target{
		PackageDir:   dir,
		TestName:     "TestAdd",
		FileSHA:      "sha-add",
		CoveredLines: map[string]LineSet{"add.go": {1: {}}}, // package decl line
	}
	cands, err := Plan(target)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, c := range cands {
		if c.Mutation == MutationCommentOutLine {
			t.Fatalf("expected no comment-out candidate (no covered statement line), got %+v", c)
		}
	}
}

// ============================================================
// Grammar form (ii): negate-boolean
// ============================================================

const fixtureNegate = `package fix

func IsZero(x int) bool {
	if !(x == 0) {
		return false
	}
	return x != 0
}
`

func TestPlan_NegateBoolean_AllGrammarForms(t *testing.T) {
	cases := []struct {
		name        string
		src         string
		descPrefix  string
		mustContain string // a substring of the mutated source proving the swap landed
		mustAbsent  string // a substring whose disappearance proves the original was replaced
	}{
		{
			name:        "drop bang",
			src:         "package fix\n\nfunc F(b bool) bool { return !b }\n",
			descPrefix:  "drop `!`",
			mustContain: "return b",
			mustAbsent:  "!b",
		},
		{
			name:        "swap eq to neq",
			src:         "package fix\n\nfunc F(a, b int) bool { return a == b }\n",
			descPrefix:  "swap `==`",
			mustContain: "a != b",
			mustAbsent:  "a == b",
		},
		{
			name:        "swap neq to eq",
			src:         "package fix\n\nfunc F(a, b int) bool { return a != b }\n",
			descPrefix:  "swap `!=`",
			mustContain: "a == b",
			mustAbsent:  "a != b",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeFixturePackage(t, map[string]string{"f.go": tc.src})
			cands, err := Plan(Target{
				PackageDir:   dir,
				TestName:     "TestF",
				FileSHA:      "sha-" + tc.name,
				CoveredLines: map[string]LineSet{"f.go": allLines(tc.src)},
			})
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			var matched *Candidate
			for i := range cands {
				if cands[i].Mutation == MutationNegateBoolean && strings.HasPrefix(cands[i].Description, tc.descPrefix) {
					matched = &cands[i]
					break
				}
			}
			if matched == nil {
				t.Fatalf("no candidate with description prefix %q in %v", tc.descPrefix, cands)
			}
			src := []byte(tc.src)
			mutated, err := Apply(src, *matched)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			assertCompiles(t, mutated)
			if !strings.Contains(string(mutated), tc.mustContain) {
				t.Fatalf("mutated source missing %q:\n%s", tc.mustContain, mutated)
			}
			if strings.Contains(string(mutated), tc.mustAbsent) {
				t.Fatalf("mutated source still contains %q (replacement did not land):\n%s", tc.mustAbsent, mutated)
			}
		})
	}
}

func TestPlan_NegateBoolean_DoesNotTouchOtherOps(t *testing.T) {
	const src = `package fix

func Cmp(a, b int) bool { return a < b }
`
	dir := writeFixturePackage(t, map[string]string{"cmp.go": src})
	target := Target{
		PackageDir:   dir,
		TestName:     "TestCmp",
		FileSHA:      "sha-cmp",
		CoveredLines: map[string]LineSet{"cmp.go": allLines(src)},
	}
	cands, err := Plan(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if c.Mutation == MutationNegateBoolean {
			t.Fatalf("`<` should not produce a negate-boolean mutant; got %+v", c)
		}
	}
}

// ============================================================
// Grammar form (iii): return-zero-value
// ============================================================

const fixtureReturnZero = `package fix

import "errors"

func Lookup(n int) (int, error) {
	if n < 0 {
		return 0, errors.New("negative")
	}
	return n * 2, nil
}
`

func TestPlan_ReturnZeroValue_FirstReturnOnly(t *testing.T) {
	dir := writeFixturePackage(t, map[string]string{"lk.go": fixtureReturnZero})
	target := Target{
		PackageDir:   dir,
		TestName:     "TestLookup",
		FileSHA:      "sha-lk",
		CoveredLines: map[string]LineSet{"lk.go": allLines(fixtureReturnZero)},
	}
	cands, err := Plan(target)
	if err != nil {
		t.Fatal(err)
	}
	var seen int
	for _, c := range cands {
		if c.Mutation == MutationReturnZeroValue {
			seen++
		}
	}
	// One return-zero candidate per function declaration; Lookup is
	// the only function in the file.
	if seen != 1 {
		t.Fatalf("expected 1 return-zero candidate (one per function), got %d", seen)
	}
	c := findCandidate(t, cands, MutationReturnZeroValue, "lk.go")
	src, err := os.ReadFile(filepath.Join(dir, "lk.go"))
	if err != nil {
		t.Fatal(err)
	}
	mutated, err := Apply(src, c)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	assertCompiles(t, mutated)
	// The first ReturnStmt in Lookup is `return 0, errors.New("negative")`
	// on line 7; that is the one we replace.
	if c.Line != 7 {
		t.Fatalf("return-zero line = %d, want 7 (first ReturnStmt)", c.Line)
	}
	if !strings.Contains(string(mutated), "*new(int), *new(error)") {
		t.Fatalf("expected *new(int), *new(error); got\n%s", mutated)
	}
	// The second return on line 9 must remain untouched.
	if !strings.Contains(string(mutated), "return n * 2, nil") {
		t.Fatalf("second return clobbered:\n%s", mutated)
	}
}

func TestPlan_ReturnZeroValue_VoidFunctionSkipped(t *testing.T) {
	const src = `package fix

func DoNothing() {
	return
}
`
	dir := writeFixturePackage(t, map[string]string{"v.go": src})
	target := Target{
		PackageDir:   dir,
		TestName:     "TestDoNothing",
		FileSHA:      "sha-v",
		CoveredLines: map[string]LineSet{"v.go": allLines(src)},
	}
	cands, err := Plan(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if c.Mutation == MutationReturnZeroValue {
			t.Fatalf("void function should not produce zero-return mutant; got %+v", c)
		}
	}
}

// ============================================================
// ≤5-mutants enforcement
// ============================================================

// fixtureBig builds a function with N covered if-statements so that a
// generous coverage profile produces well over MaxMutantsPerTest
// candidates across all grammar forms.
const fixtureBig = `package fix

func Many(a int) int {
	if a == 1 { return 1 }
	if a == 2 { return 2 }
	if a == 3 { return 3 }
	if a == 4 { return 4 }
	if a == 5 { return 5 }
	if a == 6 { return 6 }
	if a == 7 { return 7 }
	return 0
}
`

func TestPlan_FiveMutantCap(t *testing.T) {
	dir := writeFixturePackage(t, map[string]string{"big.go": fixtureBig})
	target := Target{
		PackageDir:   dir,
		TestName:     "TestMany",
		FileSHA:      "sha-big",
		CoveredLines: map[string]LineSet{"big.go": allLines(fixtureBig)},
	}
	cands, err := Plan(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) > MaxMutantsPerTest {
		t.Fatalf("Plan returned %d candidates, exceeds MaxMutantsPerTest=%d", len(cands), MaxMutantsPerTest)
	}
	if len(cands) == 0 {
		t.Fatalf("Plan returned 0 candidates on a fixture with many covered lines")
	}
}

// ============================================================
// Determinism
// ============================================================

func TestPlan_DeterministicSameSeed(t *testing.T) {
	dir := writeFixturePackage(t, map[string]string{"big.go": fixtureBig})
	mk := func() Target {
		return Target{
			PackageDir:   dir,
			TestName:     "TestMany",
			FileSHA:      "sha-big",
			CoveredLines: map[string]LineSet{"big.go": allLines(fixtureBig)},
		}
	}
	a, err := Plan(mk())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Plan(mk())
	if err != nil {
		t.Fatal(err)
	}
	if !candidatesEqual(a, b) {
		t.Fatalf("Plan is not deterministic for identical inputs:\nrun 1: %v\nrun 2: %v", a, b)
	}
}

func TestPlan_DifferentSeedDifferentOrder(t *testing.T) {
	// Two functionally identical targets that differ only in
	// FileSHA / TestName must produce a different shuffle within
	// blast-radius bands. We cannot guarantee a different *set* (the
	// cap may keep the same five items even when the band is fully
	// resorted), but for a pool wider than the cap with all-tied
	// scores the chosen members usually differ. We assert at least
	// one of "set differs" or "order differs" — both pass the
	// determinism contract while letting either kind of difference
	// surface.
	dir := writeFixturePackage(t, map[string]string{"big.go": fixtureBig})
	a, err := Plan(Target{
		PackageDir:   dir,
		TestName:     "TestMany",
		FileSHA:      "seed-A",
		CoveredLines: map[string]LineSet{"big.go": allLines(fixtureBig)},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Plan(Target{
		PackageDir:   dir,
		TestName:     "TestMany",
		FileSHA:      "seed-B",
		CoveredLines: map[string]LineSet{"big.go": allLines(fixtureBig)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if candidatesEqual(a, b) {
		t.Fatalf("Plan output is identical across seeds — RNG is not engaged")
	}
}

func candidatesEqual(a, b []Candidate) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Mutation != b[i].Mutation ||
			a[i].File != b[i].File ||
			a[i].StartOffset != b[i].StartOffset ||
			a[i].EndOffset != b[i].EndOffset ||
			string(a[i].Replacement) != string(b[i].Replacement) {
			return false
		}
	}
	return true
}

// ============================================================
// Blast-radius ranking
// ============================================================

func TestPlan_HigherBlastRadiusRanksFirst(t *testing.T) {
	dir := writeFixturePackage(t, map[string]string{"big.go": fixtureBig})
	hits := map[string]map[int]int{
		"big.go": {
			// Line 4 is `if a == 1 { return 1 }` — give it a huge
			// blast radius so its candidates rank ahead of every
			// other line's.
			4:  100,
			5:  1,
			6:  1,
			7:  1,
			8:  1,
			9:  1,
			10: 1,
		},
	}
	cands, err := Plan(Target{
		PackageDir:     dir,
		TestName:       "TestMany",
		FileSHA:        "sha-big",
		CoveredLines:   map[string]LineSet{"big.go": allLines(fixtureBig)},
		GlobalLineHits: hits,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) == 0 {
		t.Fatal("no candidates")
	}
	if cands[0].Line != 4 {
		t.Fatalf("highest-blast candidate first; got line %d (blast=%d) — full slice: %+v", cands[0].Line, cands[0].BlastRadius, cands)
	}
}

// ============================================================
// File filtering
// ============================================================

func TestPlan_SkipsTestFiles(t *testing.T) {
	dir := writeFixturePackage(t, map[string]string{
		"impl.go":      fixtureCommentOut,
		"impl_test.go": fixtureCommentOut,
	})
	cov := allLines(fixtureCommentOut)
	cands, err := Plan(Target{
		PackageDir: dir,
		TestName:   "TestAdd",
		FileSHA:    "sha-add",
		CoveredLines: map[string]LineSet{
			"impl.go":      cov,
			"impl_test.go": cov,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if strings.HasSuffix(c.File, "_test.go") {
			t.Fatalf("Plan should not mutate _test.go files; got %+v", c)
		}
	}
}

func TestPlan_SkipsBuildIgnoreFile(t *testing.T) {
	const src = `//go:build ignore

package fix

func Helper() int { return 42 }
`
	dir := writeFixturePackage(t, map[string]string{"helper.go": src})
	cands, err := Plan(Target{
		PackageDir:   dir,
		TestName:     "TestHelper",
		FileSHA:      "sha-h",
		CoveredLines: map[string]LineSet{"helper.go": allLines(src)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 0 {
		t.Fatalf("//go:build ignore file should be skipped; got %d candidates", len(cands))
	}
}

// ============================================================
// CopyPackage and Stage
// ============================================================

func TestCopyPackage_OnlyTopLevelFiles(t *testing.T) {
	dir := writeFixturePackage(t, map[string]string{"a.go": "package fix\n"})
	if err := os.Mkdir(filepath.Join(dir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "testdata", "x.txt"), []byte("ignore me"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst, err := CopyPackage(dir)
	if err != nil {
		t.Fatalf("CopyPackage: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dst) })
	if _, err := os.Stat(filepath.Join(dst, "a.go")); err != nil {
		t.Fatalf("a.go not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "testdata")); !os.IsNotExist(err) {
		t.Fatalf("testdata/ should not be copied; stat err = %v", err)
	}
}

func TestCopyPackage_RejectsRelative(t *testing.T) {
	if _, err := CopyPackage("relative/path"); err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestStage_AppliesMutationToCopy(t *testing.T) {
	dir := writeFixturePackage(t, map[string]string{"add.go": fixtureCommentOut})
	cands, err := Plan(Target{
		PackageDir:   dir,
		TestName:     "TestAdd",
		FileSHA:      "sha-stage",
		CoveredLines: map[string]LineSet{"add.go": allLines(fixtureCommentOut)},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := findCandidate(t, cands, MutationCommentOutLine, "add.go")
	staged, err := Stage(dir, c)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(staged) })
	mutated, err := os.ReadFile(filepath.Join(staged, "add.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mutated), "_ = struct{}{}") {
		t.Fatalf("stage did not apply mutation: %s", mutated)
	}
	// And the worktree itself must be unchanged (synthesis D6).
	original, err := os.ReadFile(filepath.Join(dir, "add.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != fixtureCommentOut {
		t.Fatalf("Stage modified the worktree (D6 violation):\n%s", original)
	}
}

func TestStage_RejectsPathSeparators(t *testing.T) {
	dir := writeFixturePackage(t, map[string]string{"add.go": fixtureCommentOut})
	if _, err := Stage(dir, Candidate{File: "sub/add.go", StartOffset: 0, EndOffset: 0}); err == nil {
		t.Fatal("expected error for non-basename File")
	}
}

// ============================================================
// Apply error path
// ============================================================

func TestApply_RejectsOutOfRangeOffsets(t *testing.T) {
	src := []byte("package x\n")
	if _, err := Apply(src, Candidate{StartOffset: -1}); err == nil {
		t.Fatal("expected error for negative offset")
	}
	if _, err := Apply(src, Candidate{StartOffset: 5, EndOffset: 100}); err == nil {
		t.Fatal("expected error for past-EOF offset")
	}
	if _, err := Apply(src, Candidate{StartOffset: 5, EndOffset: 2}); err == nil {
		t.Fatal("expected error for inverted offsets")
	}
}

// ============================================================
// Mutation.String stability
// ============================================================

func TestMutation_StringStable(t *testing.T) {
	cases := map[Mutation]string{
		MutationCommentOutLine:  "comment-out-line",
		MutationNegateBoolean:   "negate-boolean",
		MutationReturnZeroValue: "return-zero-value",
	}
	for m, want := range cases {
		if got := m.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", int(m), got, want)
		}
	}
}

// ============================================================
// LineSet
// ============================================================

func TestLineSet_NilSafe(t *testing.T) {
	var s LineSet
	if s.Has(1) {
		t.Fatal("nil LineSet.Has(1) = true, want false")
	}
	s = s.Add(7)
	if !s.Has(7) {
		t.Fatal("Add did not insert")
	}
}

// ============================================================
// Empty inputs
// ============================================================

func TestPlan_EmptyPackage(t *testing.T) {
	dir := t.TempDir()
	cands, err := Plan(Target{
		PackageDir:   dir,
		TestName:     "TestNothing",
		FileSHA:      "sha-empty",
		CoveredLines: map[string]LineSet{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 0 {
		t.Fatalf("expected 0 candidates from empty pkg, got %d", len(cands))
	}
}

func TestPlan_RequiresPackageDir(t *testing.T) {
	if _, err := Plan(Target{TestName: "TestX"}); err == nil {
		t.Fatal("expected error for empty PackageDir")
	}
}

func TestPlan_RequiresTestName(t *testing.T) {
	dir := writeFixturePackage(t, map[string]string{"a.go": "package fix\n"})
	if _, err := Plan(Target{PackageDir: dir}); err == nil {
		t.Fatal("expected error for empty TestName")
	}
}

func TestPlan_RequiresAbsolutePackageDir(t *testing.T) {
	if _, err := Plan(Target{PackageDir: "rel", TestName: "TestX"}); err == nil {
		t.Fatal("expected error for relative PackageDir")
	}
}

// ============================================================
// Candidate ordering helper used by other tests
// ============================================================

func TestPlan_CandidatesAreSortedDeterministically(t *testing.T) {
	dir := writeFixturePackage(t, map[string]string{"big.go": fixtureBig})
	cands, err := Plan(Target{
		PackageDir:   dir,
		TestName:     "TestMany",
		FileSHA:      "stable-seed",
		CoveredLines: map[string]LineSet{"big.go": allLines(fixtureBig)},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The candidates must be a permutation of themselves under the
	// (BlastRadius desc, File asc, StartOffset asc, Mutation asc)
	// stable sort applied before the band shuffle. We weakly assert
	// the BlastRadius column is non-increasing — that part of the
	// order is post-shuffle stable because the shuffle is band-local.
	for i := 1; i < len(cands); i++ {
		if cands[i].BlastRadius > cands[i-1].BlastRadius {
			t.Fatalf("candidates not blast-sorted: %d=%+v vs %d=%+v", i-1, cands[i-1], i, cands[i])
		}
	}
}

