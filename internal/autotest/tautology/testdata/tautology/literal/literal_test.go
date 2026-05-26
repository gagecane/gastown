// Package literal_test contains test fixtures for sub-rule (ii):
// reject tests where every assertion is literal-vs-literal.
//
// These tests are INTENTIONALLY tautological — they exist as test
// fixtures for the linter, not as real tests.
package literal_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- SHOULD TRIGGER sub-rule (ii) ---

func TestAllLiterals_StringEqual(t *testing.T) {
	assert.Equal(t, "hello", "hello")
}

func TestAllLiterals_IntEqual(t *testing.T) {
	assert.Equal(t, 42, 42)
}

func TestAllLiterals_MultipleAssertions(t *testing.T) {
	assert.Equal(t, "a", "a")
	assert.Equal(t, 1, 1)
	assert.Equal(t, true, true)
}

func TestAllLiterals_BoolTrue(t *testing.T) {
	assert.True(t, true)
}

func TestAllLiterals_NilCheck(t *testing.T) {
	assert.Nil(t, nil)
}

func TestAllLiterals_NegativeNumbers(t *testing.T) {
	assert.Equal(t, -1, -1)
	assert.Equal(t, -42, -42)
}

// --- SHOULD NOT TRIGGER (at least one non-literal assertion) ---

func TestMixedLiteralAndVariable(t *testing.T) {
	x := computeSomething()
	assert.Equal(t, "expected", "expected") // literal
	assert.Equal(t, 42, x)                  // non-literal
}

func TestVariableComparison(t *testing.T) {
	result := computeSomething()
	assert.Equal(t, 42, result)
}

func computeSomething() int {
	return 42
}
