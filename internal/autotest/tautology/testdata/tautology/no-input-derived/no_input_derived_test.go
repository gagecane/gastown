// Package noinputderived_test contains test fixtures for sub-rule (i):
// at least one assertion must depend on the function-under-test's return
// value or observable side effect.
//
// These tests are INTENTIONALLY tautological — they exist as test fixtures
// for the linter, not as real tests.
//
// Note: the production functions referenced here (svc.Process,
// pkg.ComputeValue, etc.) are deliberately NOT declared in this file. The
// analyzer treats functions declared in *_test.go files as fixtures /
// helpers (not FUT), so any "should not trigger" case must reference a
// FUT defined in production code. Tests that derive their values entirely
// from in-file helpers are the helper-built-taint pattern that gu-lem36
// is meant to flag.
package noinputderived_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"example.com/sut"
)

// --- SHOULD TRIGGER sub-rule (i) ---

func TestNoFUTDependency_HardcodedExpected(t *testing.T) {
	// Tests that never call the FUT — assertions don't depend on any function output.
	x := 42
	assert.Equal(t, 42, x)
}

func TestNoFUTDependency_SetupOnly(t *testing.T) {
	// Only stdlib setup, no FUT call.
	s := "hello"
	assert.Equal(t, "hello", s)
}

func TestNoFUTDependency_ConstantComparison(t *testing.T) {
	expected := "foo"
	actual := "foo"
	assert.Equal(t, expected, actual)
}

// TestNoFUTDependency_HelperProvidedTaint exercises the gu-lem36 fix:
// the value being asserted on comes from a test-local helper (buildTaint),
// not from any real FUT. Pre-fix, the analyzer treated the helper as a
// potential FUT and the test slipped through. Post-fix, helpers declared
// in this file are excluded from FUT classification, so this case is
// correctly flagged.
func TestNoFUTDependency_HelperProvidedTaint(t *testing.T) {
	v := buildTaint("seed")
	assert.Equal(t, "seed", v)
}

// TestNoFUTDependency_HelperFactoryStruct: factory helper produces a
// struct whose field is then asserted on. Same shape as
// HelperProvidedTaint but exercises the SelectorExpr / CompositeLit
// recursion paths.
func TestNoFUTDependency_HelperFactoryStruct(t *testing.T) {
	got := newFixture()
	assert.Equal(t, "fixture", got.Name)
}

// --- SHOULD NOT TRIGGER (assertion depends on FUT output) ---

func TestWithFUTOutput(t *testing.T) {
	result := sut.ComputeValue()
	assert.Equal(t, 42, result)
}

func TestWithFUTOutputDerived(t *testing.T) {
	result := sut.ComputeValue()
	doubled := result * 2
	assert.Equal(t, 84, doubled)
}

func TestWithFUTMethod(t *testing.T) {
	svc := sut.NewService()
	output := svc.Process("input")
	assert.Equal(t, "processed: input", output)
}

func TestWithFUTSlice(t *testing.T) {
	items := sut.FetchItems()
	assert.Len(t, items, 3)
}

func TestWithFUTError(t *testing.T) {
	_, err := sut.RiskyOperation()
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "failed")
}

// TestWithFUTWrappedByHelper exercises the propagation case: a real FUT
// call (sut.ComputeValue) flows through a test-local helper (passthrough).
// The helper itself is not FUT, but it must propagate the wrapped FUT
// taint — otherwise we'd lose recall on a common pattern.
func TestWithFUTWrappedByHelper(t *testing.T) {
	got := passthrough(sut.ComputeValue())
	assert.Equal(t, 42, got)
}

// --- Test-local helpers (NOT FUT — declared in this _test.go file). ---

type fixture struct{ Name string }

func buildTaint(s string) string { return s }
func newFixture() fixture        { return fixture{Name: "fixture"} }
func passthrough(x int) int      { return x }
