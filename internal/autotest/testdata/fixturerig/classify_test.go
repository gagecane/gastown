package fixturerig

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestClassify_Baseline exercises only the "neg" and "pos" branches,
// leaving "zero" (n == 0) and "big" (n > 100) uncovered. The cycle's
// Targets hook reports those uncovered branches and the in-process
// polecat writes a follow-up test that covers them.
func TestClassify_Baseline(t *testing.T) {
	assert.Equal(t, "neg", Classify(-5))
	assert.Equal(t, "pos", Classify(7))
}
