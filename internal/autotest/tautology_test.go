package autotest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readSUTAnnotation extracts the `// SUT: <Name>` annotation from line 1
// of a fixture. The annotation isolates the linter's precision/recall
// from SUT-detection error in the fixture corpus, matching the
// convention established by the Phase 0a-5 spike.
func readSUTAnnotation(src []byte) string {
	const prefix = "// SUT:"
	first, _, _ := strings.Cut(string(src), "\n")
	first = strings.TrimSpace(first)
	if !strings.HasPrefix(first, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(first, prefix))
}

// runFixtures reads every *.txt fixture in dir and returns the
// CheckSource Violations for each, keyed by basename.
func runFixtures(t *testing.T, dir string) map[string][]Violation {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	out := map[string][]Violation{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		sut := readSUTAnnotation(src)
		if sut == "" {
			t.Fatalf("%s: missing // SUT: annotation on line 1", path)
		}
		// CheckSource expects a *.go filename (parser.ParseFile uses
		// it for diagnostic positions only — the actual parsing is
		// driven by src). Synthesize one so positions report
		// nicely.
		fakeName := strings.TrimSuffix(e.Name(), ".txt") + ".go"
		viols, err := CheckSource(fakeName, src, []string{sut})
		if err != nil {
			t.Fatalf("CheckSource %s: %v", path, err)
		}
		out[e.Name()] = viols
	}
	return out
}

// hasSubRule reports whether viols contains at least one violation of
// rule.
func hasSubRule(viols []Violation, rule SubRule) bool {
	for _, v := range viols {
		if v.SubRule == rule {
			return true
		}
	}
	return false
}

func TestCheckSource_zeroAssertionFixturesAllFlagged(t *testing.T) {
	dir := filepath.Join("testdata", "tautology", "zero-assertion")
	all := runFixtures(t, dir)
	if len(all) == 0 {
		t.Fatalf("no fixtures discovered in %s", dir)
	}
	for name, viols := range all {
		if !hasSubRule(viols, SubRuleZeroAssertion) {
			t.Errorf("%s: expected sub-rule (iv) violation, got %v", name, viols)
		}
	}
}

func TestCheckSource_literalFixturesAllFlagged(t *testing.T) {
	dir := filepath.Join("testdata", "tautology", "literal")
	all := runFixtures(t, dir)
	if len(all) == 0 {
		t.Fatalf("no fixtures discovered in %s", dir)
	}
	for name, viols := range all {
		if !hasSubRule(viols, SubRuleLiteralVsLiteral) {
			t.Errorf("%s: expected sub-rule (ii) violation, got %v", name, viols)
		}
	}
}

func TestCheckSource_notNilFixturesAllFlagged(t *testing.T) {
	dir := filepath.Join("testdata", "tautology", "notnil")
	all := runFixtures(t, dir)
	if len(all) == 0 {
		t.Fatalf("no fixtures discovered in %s", dir)
	}
	for name, viols := range all {
		if !hasSubRule(viols, SubRuleOnlyNotNil) {
			t.Errorf("%s: expected sub-rule (iii) violation, got %v", name, viols)
		}
	}
}

func TestCheckSource_noInputDerivedFixturesAllFlagged(t *testing.T) {
	dir := filepath.Join("testdata", "tautology", "no-input-derived")
	all := runFixtures(t, dir)
	if len(all) == 0 {
		t.Fatalf("no fixtures discovered in %s", dir)
	}
	for name, viols := range all {
		if !hasSubRule(viols, SubRuleNoSUTDependence) {
			t.Errorf("%s: expected sub-rule (i) violation, got %v", name, viols)
		}
	}
}

func TestCheckSource_goodFixturesPass(t *testing.T) {
	dir := filepath.Join("testdata", "tautology", "good")
	all := runFixtures(t, dir)
	if len(all) == 0 {
		t.Fatalf("no fixtures discovered in %s", dir)
	}
	for name, viols := range all {
		if len(viols) != 0 {
			t.Errorf("%s: expected no violations, got %v", name, viols)
		}
	}
}

func TestCheckSource_emptySrcReturnsError(t *testing.T) {
	_, err := CheckSource("foo.go", nil, nil)
	if err == nil {
		t.Fatal("expected error for empty src")
	}
}

func TestCheckSource_emptyFilenameReturnsError(t *testing.T) {
	_, err := CheckSource("", []byte("package x\n"), nil)
	if err == nil {
		t.Fatal("expected error for empty filename")
	}
}

func TestCheckSource_parseErrorIsReported(t *testing.T) {
	_, err := CheckSource("bad.go", []byte("package\n@@@invalid"), nil)
	if err == nil {
		t.Fatal("expected parse error to surface")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("expected error to mention parse, got %v", err)
	}
}

func TestCheckSource_subRuleStringForm(t *testing.T) {
	cases := []struct {
		rule SubRule
		want string
	}{
		{SubRuleNoSUTDependence, "i"},
		{SubRuleLiteralVsLiteral, "ii"},
		{SubRuleOnlyNotNil, "iii"},
		{SubRuleZeroAssertion, "iv"},
	}
	for _, c := range cases {
		if got := c.rule.String(); got != c.want {
			t.Errorf("SubRule(%d).String()=%q, want %q", int(c.rule), got, c.want)
		}
	}
}

func TestCheckSource_emptySUTSetSkipsRulesIAndIII(t *testing.T) {
	// A test with only NotNil on a SUT call would normally fire
	// sub-rule (iii); without an SUT name the linter can't taint, so
	// (i) and (iii) are skipped.
	src := []byte(`package x

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestX(t *testing.T) {
	c := NewClient("")
	assert.NotNil(t, c)
}
`)
	viols, err := CheckSource("x.go", src, nil)
	if err != nil {
		t.Fatalf("CheckSource: %v", err)
	}
	for _, v := range viols {
		if v.SubRule == SubRuleNoSUTDependence || v.SubRule == SubRuleOnlyNotNil {
			t.Errorf("with no SUT names, sub-rule (i)/(iii) must be skipped; got %v", v)
		}
	}
}

func TestCheckSource_zeroSubsumesOthersForSameTest(t *testing.T) {
	// A single test that has only a trivially-true assertion fires
	// sub-rule (iv); we don't ALSO emit (ii) for the same test.
	src := []byte(`package x

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestX(t *testing.T) {
	assert.True(t, true)
}
`)
	viols, err := CheckSource("x.go", src, []string{"X"})
	if err != nil {
		t.Fatalf("CheckSource: %v", err)
	}
	if !hasSubRule(viols, SubRuleZeroAssertion) {
		t.Fatalf("expected (iv), got %v", viols)
	}
	if hasSubRule(viols, SubRuleLiteralVsLiteral) {
		t.Errorf("(iv) should subsume (ii) for the same test; got %v", viols)
	}
}

func TestViolation_String(t *testing.T) {
	src := []byte(`package x

import "testing"

func TestX(t *testing.T) {
}
`)
	viols, err := CheckSource("x.go", src, nil)
	if err != nil {
		t.Fatalf("CheckSource: %v", err)
	}
	if len(viols) != 1 {
		t.Fatalf("want 1 violation, got %d: %v", len(viols), viols)
	}
	got := viols[0].String()
	if !strings.Contains(got, "x.go") || !strings.Contains(got, "TestX") || !strings.Contains(got, "iv") {
		t.Errorf("Violation.String() = %q, missing expected fields", got)
	}
}
