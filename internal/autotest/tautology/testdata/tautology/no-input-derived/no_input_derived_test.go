// Package noinputderived_test contains test fixtures for sub-rule (i):
// at least one assertion must depend on the function-under-test's return
// value or observable side effect.
//
// These tests are INTENTIONALLY tautological — they exist as test fixtures
// for the linter, not as real tests.
package noinputderived_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
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

// --- SHOULD NOT TRIGGER (assertion depends on FUT output) ---

func TestWithFUTOutput(t *testing.T) {
	result := computeValue()
	assert.Equal(t, 42, result)
}

func TestWithFUTOutputDerived(t *testing.T) {
	result := computeValue()
	doubled := result * 2
	assert.Equal(t, 84, doubled)
}

func TestWithFUTMethod(t *testing.T) {
	svc := NewService()
	output := svc.Process("input")
	assert.Equal(t, "processed: input", output)
}

func TestWithFUTSlice(t *testing.T) {
	items := fetchItems()
	assert.Len(t, items, 3)
}

func TestWithFUTError(t *testing.T) {
	_, err := riskyOperation()
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "failed")
}

// Stubs for the FUT functions.
func computeValue() int               { return 42 }
func fetchItems() []string            { return []string{"a", "b", "c"} }
func riskyOperation() (string, error) { return "", nil }

type Service struct{}

func NewService() *Service                  { return &Service{} }
func (s *Service) Process(in string) string { return "processed: " + in }
