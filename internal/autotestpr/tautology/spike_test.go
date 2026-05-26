package tautology

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestSubRuleIPrecisionRecall is the Phase 0a-5 spike: run the
// flow-sensitive tautology analyzer against a 50-test corpus
// (25 tautological, 25 good) and measure precision and recall.
//
// Acceptance criteria:
//   - Precision ≥ 85% (≤15% false-positive on known-good)
//   - Recall ≥ 75% (≤25% false-negative on known-tautological)
func TestSubRuleIPrecisionRecall(t *testing.T) {
	// Parse both corpus files.
	tautFindings := analyzeCorpusSource(t, "tautological_test.go", tautologicalCorpus)
	goodFindings := analyzeCorpusSource(t, "good_test.go", goodCorpus)

	// Count results.
	tautFuncs := extractTestFuncs(t, "tautological_test.go", tautologicalCorpus)
	goodFuncs := extractTestFuncs(t, "good_test.go", goodCorpus)

	// Map findings to test function names.
	tautFlagged := findingsToFuncSet(tautFindings)
	goodFlagged := findingsToFuncSet(goodFindings)

	// Calculate metrics.
	truePositives := 0  // tautological funcs correctly flagged
	falseNegatives := 0 // tautological funcs missed
	falsePositives := 0 // good funcs incorrectly flagged
	trueNegatives := 0  // good funcs correctly passed

	t.Log("=== TAUTOLOGICAL CORPUS (expect: flagged) ===")
	for _, fn := range tautFuncs {
		if tautFlagged[fn] {
			truePositives++
			t.Logf("  ✓ TP: %s", fn)
		} else {
			falseNegatives++
			t.Logf("  ✗ FN: %s (MISSED)", fn)
		}
	}

	t.Log("")
	t.Log("=== GOOD CORPUS (expect: not flagged) ===")
	for _, fn := range goodFuncs {
		if goodFlagged[fn] {
			falsePositives++
			t.Logf("  ✗ FP: %s (FALSE ALARM)", fn)
		} else {
			trueNegatives++
			t.Logf("  ✓ TN: %s", fn)
		}
	}

	// Calculate precision and recall.
	totalFlagged := truePositives + falsePositives
	precision := 0.0
	if totalFlagged > 0 {
		precision = float64(truePositives) / float64(totalFlagged)
	}

	recall := 0.0
	totalTautological := truePositives + falseNegatives
	if totalTautological > 0 {
		recall = float64(truePositives) / float64(totalTautological)
	}

	t.Log("")
	t.Log("=== RESULTS ===")
	t.Logf("Corpus: %d tautological, %d good", len(tautFuncs), len(goodFuncs))
	t.Logf("True Positives:  %d", truePositives)
	t.Logf("False Negatives: %d", falseNegatives)
	t.Logf("False Positives: %d", falsePositives)
	t.Logf("True Negatives:  %d", trueNegatives)
	t.Logf("")
	t.Logf("Precision: %.1f%% (threshold: ≥85%%)", precision*100)
	t.Logf("Recall:    %.1f%% (threshold: ≥75%%)", recall*100)

	// Gate check.
	if precision < 0.85 {
		t.Errorf("PRECISION BELOW THRESHOLD: %.1f%% < 85%%", precision*100)
	}
	if recall < 0.75 {
		t.Errorf("RECALL BELOW THRESHOLD: %.1f%% < 75%%", recall*100)
	}

	if precision >= 0.85 && recall >= 0.75 {
		t.Log("")
		t.Log("✓ SPIKE PASSES — sub-rule (i) ships in gate 4d")
	} else {
		t.Log("")
		t.Log("✗ SPIKE FAILS — sub-rule (i) omitted from gate 4d")
	}
}

func analyzeCorpusSource(t *testing.T, filename, src string) []Finding {
	t.Helper()
	findings, err := AnalyzeFile(filename, []byte(src))
	if err != nil {
		t.Fatalf("failed to analyze %s: %v", filename, err)
	}
	return findings
}

func extractTestFuncs(t *testing.T, filename, src string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		t.Fatalf("failed to parse %s: %v", filename, err)
	}

	var funcs []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if strings.HasPrefix(fn.Name.Name, "Test") {
			funcs = append(funcs, fn.Name.Name)
		}
	}
	return funcs
}

func findingsToFuncSet(findings []Finding) map[string]bool {
	set := make(map[string]bool)
	for _, f := range findings {
		set[f.FuncName] = true
	}
	return set
}

// TestDetailedFindings prints detailed findings for debugging.
func TestDetailedFindings(t *testing.T) {
	t.Log("=== Tautological corpus findings ===")
	findings, err := AnalyzeFile("taut.go", []byte(tautologicalCorpus))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Logf("  %s: %s [source: %s]", f.FuncName, f.Message, f.TaintSource)
	}

	t.Log("")
	t.Log("=== Good corpus findings (should be empty) ===")
	findings, err = AnalyzeFile("good.go", []byte(goodCorpus))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Logf("  %s: %s [source: %s]", f.FuncName, f.Message, f.TaintSource)
	}
	if len(findings) == 0 {
		t.Log("  (none — perfect precision)")
	}
}

// Suppress unused import warning.
var _ = fmt.Sprintf

// ============================================================================
// CORPUS: 25 TAUTOLOGICAL TEST FUNCTIONS
// ============================================================================
//
// Each function represents a real pattern found in gastown_upstream tests
// where the assertion is tautological because the expected value depends
// on the function-under-test return.

const tautologicalCorpus = `package corpus

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// T01: Expected derived from same return — both sides of Equal are the same object field.
func TestTaut01_FieldFromSameReturn(t *testing.T) {
	result := ParseConfig("input.json")
	assert.Equal(t, result.Name, result.Name)
}

// T02: Expected stored from FUT return field, then compared back.
func TestTaut02_ExpectedFromFUTField(t *testing.T) {
	cfg := LoadSettings("/path")
	expected := cfg.Timeout
	assert.Equal(t, expected, cfg.Timeout)
}

// T03: String() method on FUT return compared to itself.
func TestTaut03_StringMethodOnReturn(t *testing.T) {
	result := BuildMessage("hello")
	assert.Equal(t, result.String(), result.String())
}

// T04: Multi-return — both sides same variable.
func TestTaut04_MultiReturnSameSource(t *testing.T) {
	name, _ := SplitPath("/usr/local/bin")
	assert.Equal(t, name, name)
}

// T05: Expected from indexing into FUT return slice.
func TestTaut05_IndexIntoFUTSlice(t *testing.T) {
	items := ListItems("query")
	first := items[0]
	assert.Equal(t, first, items[0])
}

// T06: Two calls to same FUT compared (idempotency non-test).
func TestTaut06_TwoCallsSameFUT(t *testing.T) {
	a := Normalize("input")
	b := Normalize("input")
	assert.Equal(t, a, b)
}

// T07: Type assertion on FUT return compared to itself.
func TestTaut07_TypeAssertOnFUT(t *testing.T) {
	result := GetValue("key")
	str := result.(string)
	assert.Equal(t, str, result.(string))
}

// T08: Expected from FUT via intermediate variable chain.
func TestTaut08_IntermediateVarChain(t *testing.T) {
	resp := FetchData("url")
	body := resp.Body
	content := body
	assert.Equal(t, content, resp.Body)
}

// T09: Same method called twice on FUT return.
func TestTaut09_MethodOnFUTObject(t *testing.T) {
	obj := CreateObject("test")
	assert.Equal(t, obj.ID(), obj.ID())
}

// T10: assert.True with comparison of two FUT calls.
func TestTaut10_TrueWithFUTComparison(t *testing.T) {
	a := ComputeHash("data")
	b := ComputeHash("data")
	assert.True(t, a == b)
}

// T11: Range over FUT, assert item field against itself.
func TestTaut11_RangeOverFUT(t *testing.T) {
	items := GetAll()
	for _, item := range items {
		assert.Equal(t, item.Name, item.Name)
	}
}

// T12: Deep selector chain — both sides same root.
func TestTaut12_SelectorChain(t *testing.T) {
	resp := GetResponse()
	assert.Equal(t, resp.Header.ContentType, resp.Header.ContentType)
}

// T13: Variable stored then compared to fresh call (same FUT, same input).
func TestTaut13_StoredVsFreshCall(t *testing.T) {
	stored := Transform("input")
	actual := Transform("input")
	assert.Equal(t, stored, actual)
}

// T14: Error message from FUT compared to itself.
func TestTaut14_ErrorMessageFromFUT(t *testing.T) {
	_, err := Validate("bad input")
	require.Error(t, err)
	msg := err.Error()
	assert.Equal(t, msg, err.Error())
}

// T15: Sub-struct field from FUT compared via different paths.
func TestTaut15_SubStructFromFUT(t *testing.T) {
	result := BuildPlan("spec")
	phase := result.Phases[0]
	assert.Equal(t, phase.Name, result.Phases[0].Name)
}

// T16: Same call twice to verify "consistency" — still tautological.
func TestTaut16_ConsistencyCheck(t *testing.T) {
	first := Serialize("data")
	second := Serialize("data")
	assert.Equal(t, first, second)
}

// T17: Map lookup on FUT return compared to same lookup.
func TestTaut17_MapLookupFromFUT(t *testing.T) {
	m := BuildIndex([]string{"a", "b", "c"})
	val := m["a"]
	assert.Equal(t, val, m["a"])
}

// T18: FUT-return stored conditionally, then asserted.
func TestTaut18_ConditionalFromFUT(t *testing.T) {
	result := Process("data")
	expected := result.Value
	assert.Equal(t, expected, result.Value)
}

// T19: Both sides from Filter (wraps FUT output).
func TestTaut19_BothFromWrappedFUT(t *testing.T) {
	items := GetItems()
	a := Filter(items, "pred")
	b := Filter(items, "pred")
	assert.Equal(t, len(a), len(b))
}

// T20: Same FUT called, results stored in different vars.
func TestTaut20_DifferentVarsSameFUT(t *testing.T) {
	base := GetDefaults()
	all := GetDefaults()
	assert.Equal(t, len(base), len(all))
}

// T21: Struct field compared to itself.
func TestTaut21_StructFieldBothSides(t *testing.T) {
	out := Generate("seed")
	assert.Equal(t, out.Hash, out.Hash)
}

// T22: Method call on FUT interface compared to itself.
func TestTaut22_InterfaceMethodOnFUT(t *testing.T) {
	svc := NewService("config")
	assert.Equal(t, svc.Status(), svc.Status())
}

// T23: Slice operation on FUT result compared to same slice.
func TestTaut23_SliceOpOnFUT(t *testing.T) {
	data := ReadAll("file")
	chunk := data[:10]
	assert.Equal(t, chunk, data[:10])
}

// T24: Same field accessed via different variable paths — both from same root.
func TestTaut24_DifferentAccessPaths(t *testing.T) {
	m := ParseManifest("manifest.yaml")
	name1 := m.Entries[0].Name
	name2 := m.Entries[0].Name
	assert.Equal(t, name1, name2)
}

// T25: Encode → Decode → Encode round-trip, both from Encode.
func TestTaut25_RoundTrip(t *testing.T) {
	raw := Encode("data")
	decoded := Decode(raw)
	reencoded := Encode(decoded)
	assert.Equal(t, raw, reencoded)
}
`

// ============================================================================
// CORPUS: 25 WELL-FORMED (GOOD) TEST FUNCTIONS
// ============================================================================
//
// Each function has assertions with independent expected values (literals,
// table-driven, input-derived).

const goodCorpus = `package corpus

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// G01: Expected is a string literal.
func TestGood01_LiteralExpected(t *testing.T) {
	result := ParseConfig("test.json")
	assert.Equal(t, "myapp", result.Name)
}

// G02: Table-driven test with independent expected.
func TestGood02_TableDriven(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"hello", 5},
		{"world", 5},
		{"", 0},
	}
	for _, tt := range tests {
		result := ComputeLength(tt.input)
		assert.Equal(t, tt.expected, result)
	}
}

// G03: Expected is a numeric literal.
func TestGood03_NumericLiteral(t *testing.T) {
	count := CountItems("bucket")
	assert.Equal(t, 42, count)
}

// G04: Expected from independently constructed struct.
func TestGood04_IndependentFixture(t *testing.T) {
	actual := LoadSettings("/path/to/config")
	assert.Equal(t, "test", actual.Name)
	assert.Equal(t, 8080, actual.Port)
}

// G05: Expected is a boolean via assert.True.
func TestGood05_BoolLiteral(t *testing.T) {
	valid := IsValid("good-input")
	assert.True(t, valid)
}

// G06: Expected is a hardcoded string, actual from FUT.
func TestGood06_HardcodedExpected(t *testing.T) {
	result := ToUpper("hello world")
	assert.Equal(t, "HELLO WORLD", result)
}

// G07: Error assertion — require.NoError is NOT an equality assertion.
func TestGood07_NoError(t *testing.T) {
	_, err := Process("valid-input")
	require.NoError(t, err)
}

// G08: Expected from package-level constant.
func TestGood08_Constant(t *testing.T) {
	result := GetStatus()
	assert.Equal(t, StatusActive, result)
}

// G09: Assert.Empty — single arg (FUT return), not comparing to FUT.
func TestGood09_EmptySlice(t *testing.T) {
	items := Filter(GetItems(), "nonexistent")
	assert.Empty(t, items)
}

// G10: Assert.Len with literal length count.
func TestGood10_LenLiteral(t *testing.T) {
	items := ListAll()
	assert.Len(t, items, 3)
}

// G11: Expected from environment variable (independent source).
func TestGood11_EnvVar(t *testing.T) {
	t.Setenv("MY_VAR", "expected_value")
	result := ReadEnv("MY_VAR")
	assert.Equal(t, "expected_value", result)
}

// G12: Expected from literal, FUT reads test fixture.
func TestGood12_FileFixture(t *testing.T) {
	result := ReadFile("/tmp/test-input.txt")
	assert.Equal(t, "test content", result)
}

// G13: Assert.Contains with literal substring.
func TestGood13_ContainsLiteral(t *testing.T) {
	msg := FormatError("missing field")
	assert.Contains(t, msg, "missing field")
}

// G14: Expected is the original INPUT, not a FUT output.
func TestGood14_InputAsExpected(t *testing.T) {
	encoded := Encode("hello")
	decoded := Decode(encoded)
	assert.Equal(t, "hello", decoded)
}

// G15: Assert.NotNil — single arg assertion.
func TestGood15_NotNil(t *testing.T) {
	obj := CreateObject("test")
	assert.NotNil(t, obj)
}

// G16: Comparison with hardcoded year.
func TestGood16_TimeComparison(t *testing.T) {
	result := GetTimestamp()
	assert.True(t, result.Year() >= 2020)
}

// G17: Expected from input struct field.
func TestGood17_InputFieldAsExpected(t *testing.T) {
	input := "/api"
	result := RouteRequest(input)
	assert.Equal(t, "/api", result.MatchedPath)
}

// G18: Expected is joined string literal.
func TestGood18_ComputedFromInput(t *testing.T) {
	result := JoinStrings([]string{"a", "b", "c"}, ",")
	assert.Equal(t, "a,b,c", result)
}

// G19: Error message assertion with literal.
func TestGood19_ErrorMessage(t *testing.T) {
	_, err := Validate("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be empty")
}

// G20: HTTP status code as numeric literal.
func TestGood20_HTTPStatus(t *testing.T) {
	resp := MakeRequest("GET", "/health")
	assert.Equal(t, 200, resp.StatusCode)
}

// G21: Table-driven with error and name checks.
func TestGood21_TableStructComparison(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantErr  bool
		wantName string
	}{
		{"valid", "good", false, "good"},
		{"empty", "", true, ""},
	}
	for _, tt := range tests {
		result, err := ParseName(tt.input)
		if tt.wantErr {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
			assert.Equal(t, tt.wantName, result)
		}
	}
}

// G22: Assert.False with FUT result — well-formed boolean assertion.
func TestGood22_FalseLiteral(t *testing.T) {
	result := IsExpired("valid-token")
	assert.False(t, result)
}

// G23: Expected from map literal (independent construction).
func TestGood23_MapLiteral(t *testing.T) {
	result := CountChars("aabb")
	assert.Equal(t, 2, result["a"])
	assert.Equal(t, 2, result["b"])
}

// G24: Expected from len of INPUT (not output).
func TestGood24_LenOfInput(t *testing.T) {
	input := []string{"x", "y", "z"}
	result := CopySlice(input)
	assert.Equal(t, 3, len(result))
}

// G25: Expected from slice literal.
func TestGood25_ElementsMatch(t *testing.T) {
	result := SortAndDedupe([]string{"b", "a", "b", "c"})
	assert.ElementsMatch(t, []string{"a", "b", "c"}, result)
}
`
