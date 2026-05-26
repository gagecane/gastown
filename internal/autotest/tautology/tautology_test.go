package tautology

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSubRuleII_Literal verifies that the linter detects tests where every
// assertion is literal-vs-literal.
func TestSubRuleII_Literal(t *testing.T) {
	src := readFixture(t, "literal/literal_test.go")
	findings, err := AnalyzeFile("literal_test.go", src)
	if err != nil {
		t.Fatalf("AnalyzeFile: %v", err)
	}

	// Expected: trigger for functions that only have literal assertions.
	shouldTrigger := map[string]bool{
		"TestAllLiterals_StringEqual":        true,
		"TestAllLiterals_IntEqual":           true,
		"TestAllLiterals_MultipleAssertions": true,
		"TestAllLiterals_BoolTrue":           true,
		"TestAllLiterals_NilCheck":           true,
		"TestAllLiterals_NegativeNumbers":    true,
	}
	shouldNotTrigger := map[string]bool{
		"TestMixedLiteralAndVariable": true,
		"TestVariableComparison":      true,
	}

	literalFindings := filterByRule(findings, RuleLiteral)
	flagged := findingFuncSet(literalFindings)

	for fn := range shouldTrigger {
		if !flagged[fn] {
			t.Errorf("sub-rule (ii) missed %s (false negative)", fn)
		}
	}
	for fn := range shouldNotTrigger {
		if flagged[fn] {
			t.Errorf("sub-rule (ii) falsely flagged %s (false positive)", fn)
		}
	}
}

// TestSubRuleIII_NotNilOnly verifies that the linter detects tests whose
// only assertions are trivial (NotNil/NotEmpty/True).
func TestSubRuleIII_NotNilOnly(t *testing.T) {
	src := readFixture(t, "notnil/notnil_test.go")
	findings, err := AnalyzeFile("notnil_test.go", src)
	if err != nil {
		t.Fatalf("AnalyzeFile: %v", err)
	}

	shouldTrigger := map[string]bool{
		"TestOnlyNotNil":      true,
		"TestOnlyNotEmpty":    true,
		"TestOnlyTrue":        true,
		"TestMultipleTrivial": true,
		"TestRequireNotNil":   true,
	}
	shouldNotTrigger := map[string]bool{
		"TestNotNilPlusEquality": true,
		"TestNotNilPlusLen":      true,
		"TestMeaningfulCheck":    true,
	}

	notnilFindings := filterByRule(findings, RuleNotNilOnly)
	flagged := findingFuncSet(notnilFindings)

	for fn := range shouldTrigger {
		if !flagged[fn] {
			t.Errorf("sub-rule (iii) missed %s (false negative)", fn)
		}
	}
	for fn := range shouldNotTrigger {
		if flagged[fn] {
			t.Errorf("sub-rule (iii) falsely flagged %s (false positive)", fn)
		}
	}
}

// TestSubRuleIV_ZeroAssertion verifies that the linter detects tests
// with zero assertions or tautological assert(true)/self-equal patterns.
func TestSubRuleIV_ZeroAssertion(t *testing.T) {
	src := readFixture(t, "zero-assertion/zero_assertion_test.go")
	findings, err := AnalyzeFile("zero_assertion_test.go", src)
	if err != nil {
		t.Fatalf("AnalyzeFile: %v", err)
	}

	// Functions that should trigger zero-assertion rule.
	shouldTrigger := map[string]bool{
		"TestEmptyBody":           true,
		"TestOnlySetup":           true,
		"TestOnlyLogging":         true,
		"TestAssertTrue":          true,
		"TestAssertFalseFalse":    true,
		"TestSelfEqual_Variable":  true,
		"TestSelfEqual_Literal":   true,
	}
	shouldNotTrigger := map[string]bool{
		"TestValidAssertion": true,
		"TestValidNotEqual":  true,
		"TestValidTrue":      true,
	}

	zeroFindings := filterByRule(findings, RuleZeroAssertion)
	flagged := findingFuncSet(zeroFindings)

	for fn := range shouldTrigger {
		if !flagged[fn] {
			t.Errorf("sub-rule (iv) missed %s (false negative)", fn)
		}
	}
	for fn := range shouldNotTrigger {
		if flagged[fn] {
			t.Errorf("sub-rule (iv) falsely flagged %s (false positive)", fn)
		}
	}
}

// TestSubRuleI_NoInputDerived verifies that the linter detects tests
// where no assertion depends on FUT output.
func TestSubRuleI_NoInputDerived(t *testing.T) {
	src := readFixture(t, "no-input-derived/no_input_derived_test.go")
	findings, err := AnalyzeFile("no_input_derived_test.go", src)
	if err != nil {
		t.Fatalf("AnalyzeFile: %v", err)
	}

	shouldTrigger := map[string]bool{
		"TestNoFUTDependency_HardcodedExpected":   true,
		"TestNoFUTDependency_SetupOnly":           true,
		"TestNoFUTDependency_ConstantComparison":  true,
	}
	shouldNotTrigger := map[string]bool{
		"TestWithFUTOutput":        true,
		"TestWithFUTOutputDerived": true,
		"TestWithFUTMethod":        true,
		"TestWithFUTSlice":         true,
		"TestWithFUTError":         true,
	}

	noInputFindings := filterByRule(findings, RuleNoInputDerived)
	flagged := findingFuncSet(noInputFindings)

	for fn := range shouldTrigger {
		if !flagged[fn] {
			t.Errorf("sub-rule (i) missed %s (false negative)", fn)
		}
	}
	for fn := range shouldNotTrigger {
		if flagged[fn] {
			t.Errorf("sub-rule (i) falsely flagged %s (false positive)", fn)
		}
	}
}

// TestAnalyzeFileParseError ensures parse errors are returned properly.
func TestAnalyzeFileParseError(t *testing.T) {
	_, err := AnalyzeFile("bad.go", []byte("not valid go"))
	if err == nil {
		t.Error("expected parse error for invalid Go source")
	}
}

// TestAnalyzeFileEmpty ensures empty files produce no findings.
func TestAnalyzeFileEmpty(t *testing.T) {
	src := []byte("package empty\n")
	findings, err := AnalyzeFile("empty_test.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(findings))
	}
}

// --- Helpers ---

func readFixture(t *testing.T, rel string) []byte {
	t.Helper()
	path := filepath.Join("testdata", "tautology", rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", rel, err)
	}
	return data
}

func filterByRule(findings []Finding, rule Rule) []Finding {
	var result []Finding
	for _, f := range findings {
		if f.Rule == rule {
			result = append(result, f)
		}
	}
	return result
}

func findingFuncSet(findings []Finding) map[string]bool {
	s := make(map[string]bool)
	for _, f := range findings {
		s[f.FuncName] = true
	}
	return s
}
