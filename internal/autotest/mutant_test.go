package autotest

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/autotest/sandbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Hand-rolled fixture: simple Go source with all three mutation
// grammar forms present and exercisable.
const fixtureSource = `package fixture

import "errors"

func Add(a, b int) int {
	result := a + b
	return result
}

func IsPositive(n int) bool {
	if !isValid(n) {
		return false
	}
	return n > 0
}

func isValid(n int) bool {
	return n != 0
}

func Divide(a, b int) (int, error) {
	if b == 0 {
		return 0, errors.New("division by zero")
	}
	return a / b, nil
}

func Greet(name string) string {
	prefix := "Hello, "
	return prefix + name
}
`

// TestFindMutants_CommentOutLine verifies grammar (i): expression and
// assignment statements on covered lines produce comment-out-line
// mutants.
func TestFindMutants_CommentOutLine(t *testing.T) {
	dir := setupFixture(t)

	cfg := &RunConfig{
		PkgDir:   dir,
		TestName: "TestAdd",
		CoveredLines: map[string]map[int]bool{
			"fixture.go": {6: true}, // result := a + b (AssignStmt)
		},
		FileSHA: sha256.Sum256([]byte(fixtureSource)),
	}

	mutants, err := FindMutants(cfg)
	require.NoError(t, err)

	var found bool
	for _, m := range mutants {
		if m.Kind == MutCommentOutLine && m.Line == 6 {
			found = true
			assert.Contains(t, m.Original, "result := a + b")
			assert.Contains(t, m.Mutated, "// mutant: removed")
			assert.Equal(t, "fixture.go", m.File)
			break
		}
	}
	assert.True(t, found, "expected comment-out-line mutant on line 6")
}

// TestFindMutants_NegateBoolean verifies grammar (ii): comparison
// operators and ! unary operators on covered lines produce
// negate-boolean mutants.
func TestFindMutants_NegateBoolean(t *testing.T) {
	dir := setupFixture(t)

	cfg := &RunConfig{
		PkgDir:   dir,
		TestName: "TestIsPositive",
		CoveredLines: map[string]map[int]bool{
			"fixture.go": {
				11: true, // if !isValid(n) — UnaryExpr NOT
				14: true, // return n > 0 — BinaryExpr GTR
				18: true, // return n != 0 — BinaryExpr NEQ
			},
		},
		FileSHA: sha256.Sum256([]byte(fixtureSource)),
	}

	mutants, err := FindMutants(cfg)
	require.NoError(t, err)

	var foundNot, foundGTR, foundNEQ bool
	for _, m := range mutants {
		if m.Kind != MutNegateBoolean {
			continue
		}
		switch m.Line {
		case 11:
			// Flip ! — the '!' should be removed.
			foundNot = true
			assert.NotContains(t, m.Mutated, "!")
		case 14:
			// n > 0 → n <= 0
			foundGTR = true
			assert.Contains(t, m.Mutated, "<=")
		case 18:
			// n != 0 → n == 0
			foundNEQ = true
			assert.Contains(t, m.Mutated, "==")
		}
	}
	assert.True(t, foundNot, "expected negate-boolean mutant on line 11 (flip !)")
	assert.True(t, foundGTR, "expected negate-boolean mutant on line 14 (> → <=)")
	assert.True(t, foundNEQ, "expected negate-boolean mutant on line 18 (!= → ==)")
}

// TestFindMutants_ReturnZeroValue verifies grammar (iii): return
// statements with results on covered lines produce return-zero-value
// mutants.
func TestFindMutants_ReturnZeroValue(t *testing.T) {
	dir := setupFixture(t)

	cfg := &RunConfig{
		PkgDir:   dir,
		TestName: "TestDivide",
		CoveredLines: map[string]map[int]bool{
			"fixture.go": {
				7:  true, // return result (single int return)
				23: true, // return 0, errors.New(...) (multi-return)
				25: true, // return a / b, nil (multi-return)
			},
		},
		FileSHA: sha256.Sum256([]byte(fixtureSource)),
	}

	mutants, err := FindMutants(cfg)
	require.NoError(t, err)

	var foundSingle, foundMultiErr, foundMultiNil bool
	for _, m := range mutants {
		if m.Kind != MutReturnZeroValue {
			continue
		}
		switch m.Line {
		case 7:
			// return result → return nil (or 0)
			foundSingle = true
			assert.Contains(t, m.Mutated, "return")
		case 23:
			// return 0, errors.New(...) → return 0, nil
			foundMultiErr = true
			assert.Contains(t, m.Mutated, "return")
			assert.Contains(t, m.Mutated, "nil")
		case 25:
			// return a / b, nil → return 0, nil
			foundMultiNil = true
			assert.Contains(t, m.Mutated, "return")
			assert.Contains(t, m.Mutated, "0")
		}
	}
	assert.True(t, foundSingle, "expected return-zero-value mutant on line 7")
	assert.True(t, foundMultiErr, "expected return-zero-value mutant on line 23")
	assert.True(t, foundMultiNil, "expected return-zero-value mutant on line 25")
}

// TestSelectMutants_MaxFive verifies the ≤5 mutant cap (D11) and
// deterministic selection.
func TestSelectMutants_MaxFive(t *testing.T) {
	// Create 10 candidate mutants with varying blast radius.
	candidates := make([]Mutant, 10)
	for i := range candidates {
		candidates[i] = Mutant{
			Kind:        MutCommentOutLine,
			File:        "test.go",
			Line:        i + 1,
			Original:    "x := 1",
			Mutated:     "// mutant: removed",
			BlastRadius: i, // 0..9
		}
	}

	sha := sha256.Sum256([]byte("test-file-content"))
	selected := SelectMutants(candidates, sha, "TestFoo")

	assert.Len(t, selected, MaxMutantsPerTest)

	// Top 5 blast radius values should be selected (5,6,7,8,9).
	for _, m := range selected {
		assert.GreaterOrEqual(t, m.BlastRadius, 5,
			"selected mutant should have blast radius >= 5")
	}
}

// TestSelectMutants_Deterministic verifies same inputs produce same
// selection.
func TestSelectMutants_Deterministic(t *testing.T) {
	candidates := make([]Mutant, 8)
	for i := range candidates {
		candidates[i] = Mutant{
			Kind:        MutNegateBoolean,
			File:        "foo.go",
			Line:        i + 1,
			Original:    "x == y",
			Mutated:     "x != y",
			BlastRadius: 3, // All tied — shuffle is deterministic.
		}
	}

	sha := sha256.Sum256([]byte("deterministic-test"))
	sel1 := SelectMutants(candidates, sha, "TestBar")
	sel2 := SelectMutants(candidates, sha, "TestBar")

	require.Len(t, sel1, MaxMutantsPerTest)
	require.Len(t, sel2, MaxMutantsPerTest)

	for i := range sel1 {
		assert.Equal(t, sel1[i].Line, sel2[i].Line,
			"selection must be deterministic (index %d)", i)
	}
}

// TestSelectMutants_BelowCap verifies that fewer candidates than the
// cap returns all of them.
func TestSelectMutants_BelowCap(t *testing.T) {
	candidates := []Mutant{
		{Kind: MutCommentOutLine, File: "a.go", Line: 1, BlastRadius: 1},
		{Kind: MutNegateBoolean, File: "a.go", Line: 2, BlastRadius: 2},
	}
	sha := sha256.Sum256([]byte("small"))
	selected := SelectMutants(candidates, sha, "TestSmall")
	assert.Len(t, selected, 2)
}

// TestRunMutants_RejectOverCap verifies RunMutants refuses more than
// MaxMutantsPerTest.
func TestRunMutants_RejectOverCap(t *testing.T) {
	mutants := make([]Mutant, MaxMutantsPerTest+1)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "dummy.go"), []byte("package dummy\n"), 0644)
	sb, err := sandbox.New(dir)
	require.NoError(t, err)
	cfg := &RunConfig{
		PkgDir:   dir,
		TestName: "TestX",
		Sandbox:  sb,
	}
	_, err = RunMutants(cfg, mutants)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds cap")
}

// TestApplyMutation verifies file rewriting.
func TestApplyMutation(t *testing.T) {
	dir := t.TempDir()
	src := "line1\nline2\nline3\n"
	fpath := filepath.Join(dir, "test.go")
	require.NoError(t, os.WriteFile(fpath, []byte(src), 0644))

	m := Mutant{
		Line:    2,
		Mutated: "REPLACED",
	}
	require.NoError(t, applyMutation(fpath, m))

	got, err := os.ReadFile(fpath)
	require.NoError(t, err)
	assert.Contains(t, string(got), "REPLACED")
	assert.Contains(t, string(got), "line1")
	assert.Contains(t, string(got), "line3")
}

// TestCopyDir verifies recursive directory copy.
func TestCopyDir(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "sub"), 0755)
	os.WriteFile(filepath.Join(src, "a.go"), []byte("package a"), 0644)
	os.WriteFile(filepath.Join(src, "sub", "b.go"), []byte("package b"), 0644)

	dst := t.TempDir()
	require.NoError(t, copyDir(src, dst))

	got, err := os.ReadFile(filepath.Join(dst, "a.go"))
	require.NoError(t, err)
	assert.Equal(t, "package a", string(got))

	got, err = os.ReadFile(filepath.Join(dst, "sub", "b.go"))
	require.NoError(t, err)
	assert.Equal(t, "package b", string(got))
}

// TestCommentOutLine verifies indentation preservation.
func TestCommentOutLine(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"	x := 1", "	// mutant: removed"},
		{"    y = 2", "    // mutant: removed"},
		{"doSomething()", "// mutant: removed"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, commentOutLine(tt.input))
	}
}

// TestFlipNot verifies ! removal at correct column.
func TestFlipNot(t *testing.T) {
	line := "	if !valid {"
	// Column 5 (1-based): the '!' is at index 4 in the string.
	result := flipNot(line, 5)
	assert.Equal(t, "	if valid {", result)
}

// TestDeterministicSeed verifies reproducibility.
func TestDeterministicSeed(t *testing.T) {
	sha := sha256.Sum256([]byte("hello"))
	s1 := deterministicSeed(sha, "TestFoo")
	s2 := deterministicSeed(sha, "TestFoo")
	assert.Equal(t, s1, s2)

	s3 := deterministicSeed(sha, "TestBar")
	assert.NotEqual(t, s1, s3, "different test names should produce different seeds")
}

// setupFixture creates a temp directory with the fixture source file.
func setupFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(fixtureSource), 0644)
	require.NoError(t, err)
	return dir
}
