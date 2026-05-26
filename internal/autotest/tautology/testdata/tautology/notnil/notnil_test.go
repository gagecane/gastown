// Package notnil_test contains test fixtures for sub-rule (iii):
// reject tests whose only assertions against SUT are NotNil/NotEmpty/truthy.
//
// These tests are INTENTIONALLY weak — they exist as test fixtures
// for the linter, not as real tests.
package notnil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- SHOULD TRIGGER sub-rule (iii) ---

func TestOnlyNotNil(t *testing.T) {
	result := getSomething()
	assert.NotNil(t, result)
}

func TestOnlyNotEmpty(t *testing.T) {
	result := getString()
	assert.NotEmpty(t, result)
}

func TestOnlyTrue(t *testing.T) {
	ok := isValid()
	assert.True(t, ok)
}

func TestMultipleTrivial(t *testing.T) {
	result := getSomething()
	err := doSomething()
	assert.NotNil(t, result)
	assert.NotEmpty(t, result)
	require.NoError(t, err)
}

func TestRequireNotNil(t *testing.T) {
	result := getSomething()
	require.NotNil(t, result)
	require.NotEmpty(t, result)
}

// --- SHOULD NOT TRIGGER (has at least one meaningful assertion) ---

func TestNotNilPlusEquality(t *testing.T) {
	result := getSomething()
	assert.NotNil(t, result)
	assert.Equal(t, "expected", result)
}

func TestNotNilPlusLen(t *testing.T) {
	result := getSlice()
	assert.NotNil(t, result)
	assert.Len(t, result, 3)
}

func TestMeaningfulCheck(t *testing.T) {
	result := getSomething()
	assert.Equal(t, "hello", result)
}

func getSomething() interface{} { return "hello" }
func getString() string         { return "world" }
func isValid() bool             { return true }
func doSomething() error        { return nil }
func getSlice() []int           { return []int{1, 2, 3} }
