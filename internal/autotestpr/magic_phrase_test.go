package autotestpr

import (
	"testing"
	"time"
)

// TestContainsMagicPhrase_ExactMatch verifies that the exact phrase on
// its own line triggers detection.
func TestContainsMagicPhrase_ExactMatch(t *testing.T) {
	body := "gt auto-test-pr: pause-rig-7d"
	if !ContainsMagicPhrase(body) {
		t.Error("expected true for exact phrase")
	}
}

// TestContainsMagicPhrase_WithSurroundingLines verifies that the phrase
// triggers when it appears among other comment lines.
func TestContainsMagicPhrase_WithSurroundingLines(t *testing.T) {
	body := "Thanks for the PR, but I'd like to pause these for now.\n\ngt auto-test-pr: pause-rig-7d\n\nLet's revisit next sprint."
	if !ContainsMagicPhrase(body) {
		t.Error("expected true when phrase is on its own line amid other text")
	}
}

// TestContainsMagicPhrase_LeadingTrailingWhitespace verifies that
// whitespace around the phrase on the line is tolerated.
func TestContainsMagicPhrase_LeadingTrailingWhitespace(t *testing.T) {
	tests := []string{
		"  gt auto-test-pr: pause-rig-7d  ",
		"\tgt auto-test-pr: pause-rig-7d\t",
		"  gt auto-test-pr: pause-rig-7d",
		"gt auto-test-pr: pause-rig-7d   ",
	}
	for _, body := range tests {
		if !ContainsMagicPhrase(body) {
			t.Errorf("expected true for %q", body)
		}
	}
}

// TestContainsMagicPhrase_NearMisses verifies that typos and partial
// matches do NOT trigger (per acceptance criteria: exact-match only).
func TestContainsMagicPhrase_NearMisses(t *testing.T) {
	nearMisses := []string{
		// Wrong prefix.
		"gt auto-test-pr:pause-rig-7d",
		// Extra text on same line.
		"Please gt auto-test-pr: pause-rig-7d now",
		// Missing colon.
		"gt auto-test-pr pause-rig-7d",
		// Wrong duration.
		"gt auto-test-pr: pause-rig-24h",
		// Case mismatch.
		"GT auto-test-pr: pause-rig-7d",
		"gt Auto-Test-PR: pause-rig-7d",
		"GT AUTO-TEST-PR: PAUSE-RIG-7D",
		// Partial match (substring of a longer line).
		"run gt auto-test-pr: pause-rig-7d immediately",
		// Backtick-wrapped (code block).
		"`gt auto-test-pr: pause-rig-7d`",
		// Empty body.
		"",
		// Only whitespace.
		"   \n\t\n   ",
		// Double colon.
		"gt auto-test-pr:: pause-rig-7d",
		// Trailing garbage.
		"gt auto-test-pr: pause-rig-7d!",
	}
	for _, body := range nearMisses {
		if ContainsMagicPhrase(body) {
			t.Errorf("expected false for near-miss %q", body)
		}
	}
}

// TestContainsMagicPhrase_MultilineWithPhrase verifies detection in a
// realistic multi-line GitHub comment.
func TestContainsMagicPhrase_MultilineWithPhrase(t *testing.T) {
	body := `I've reviewed this auto-test PR and the tests are redundant.
Let's pause the auto-test system for this rig.

gt auto-test-pr: pause-rig-7d

Thanks!`
	if !ContainsMagicPhrase(body) {
		t.Error("expected true for multi-line comment with phrase on its own line")
	}
}

// TestContainsMagicPhrase_WindowsLineEndings verifies CRLF line
// endings are handled correctly.
func TestContainsMagicPhrase_WindowsLineEndings(t *testing.T) {
	body := "some context\r\ngt auto-test-pr: pause-rig-7d\r\nmore text"
	if !ContainsMagicPhrase(body) {
		t.Error("expected true with CRLF line endings")
	}
}

// TestMagicPhrasePauseDuration verifies the constant is 7 days.
func TestMagicPhrasePauseDuration(t *testing.T) {
	want := 7 * 24 * time.Hour
	if MagicPhrasePauseDuration != want {
		t.Errorf("MagicPhrasePauseDuration = %v, want %v", MagicPhrasePauseDuration, want)
	}
}

// TestMagicPhraseConstant verifies the phrase constant matches the
// design doc token.
func TestMagicPhraseConstant(t *testing.T) {
	want := "gt auto-test-pr: pause-rig-7d"
	if MagicPhrase != want {
		t.Errorf("MagicPhrase = %q, want %q", MagicPhrase, want)
	}
}
