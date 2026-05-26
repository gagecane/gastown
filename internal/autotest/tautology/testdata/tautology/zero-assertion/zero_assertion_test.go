// Package zeroassertion_test contains test fixtures for sub-rule (iv):
// reject assert(true) / expect(x).toBe(x) / zero-assertion tests.
//
// These tests are INTENTIONALLY tautological — they exist as test fixtures
// for the linter, not as real tests.
package zeroassertion_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- SHOULD TRIGGER sub-rule (iv): zero assertions ---

func TestEmptyBody(t *testing.T) {
	// No assertions at all.
}

func TestOnlySetup(t *testing.T) {
	_ = "setup"
	x := 42
	_ = x
}

func TestOnlyLogging(t *testing.T) {
	t.Log("this test does nothing meaningful")
}

// --- SHOULD TRIGGER sub-rule (iv): assert(true) ---

func TestAssertTrue(t *testing.T) {
	assert.True(t, true)
}

func TestAssertFalseFalse(t *testing.T) {
	assert.False(t, false)
}

// --- SHOULD TRIGGER sub-rule (iv): self-equal ---

func TestSelfEqual_Variable(t *testing.T) {
	x := computeResult()
	assert.Equal(t, x, x)
}

func TestSelfEqual_Literal(t *testing.T) {
	assert.Equal(t, "hello", "hello")
}

// --- SHOULD NOT TRIGGER ---

func TestValidAssertion(t *testing.T) {
	result := computeResult()
	assert.Equal(t, 42, result)
}

func TestValidNotEqual(t *testing.T) {
	a := computeResult()
	b := computeOther()
	assert.NotEqual(t, a, b)
}

func TestValidTrue(t *testing.T) {
	ok := isReady()
	assert.True(t, ok)
}

func computeResult() int { return 42 }
func computeOther() int  { return 99 }
func isReady() bool      { return true }
