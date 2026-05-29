package feed

import (
	"strings"
	"testing"
)

// TestRenderConvoyLineSingleLine verifies the renderer never emits a newline,
// even when the convoy's bead Title contains embedded \n / \n\n. Prior to the
// gu-mz1e fix, design-exploration molecule convoys (whose Title field
// contains a leading paragraph followed by "\n\n" and additional prose)
// rendered as multiple visual rows — each rune of the title broke the
// fixed-height row layout and pushed the trailing counter to a later line.
//
// Regression for: gu-mz1e (gt feed: convoy panel renders multi-line titles
// incorrectly).
func TestRenderConvoyLineSingleLine(t *testing.T) {
	tests := []struct {
		name  string
		title string
	}{
		{
			name:  "double newline mid-title",
			title: "design: Structured design exploration via parallel specialized analysts.\n\nEach analyst produces a perspective.",
		},
		{
			name:  "single newline mid-title",
			title: "feature: Add support for X\nthat does Y",
		},
		{
			name:  "trailing newlines",
			title: "Work: Graph-based ticket-triage pipeline (§6.3)\n\n",
		},
		{
			name:  "leading newlines",
			title: "\n\nWork: Graph-based ticket-triage pipeline",
		},
		{
			name:  "tabs and mixed whitespace",
			title: "feat:\trefactor\tcommand\n\twith tabs",
		},
		{
			name:  "single line baseline",
			title: "Work: Graph-based ticket-triage pipeline (§6.3)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Convoy{
				ID:        "hq-cv-test1",
				Title:     tt.title,
				Status:    "open",
				Completed: 1,
				Total:     3,
			}

			// Pane width wide enough that the title is not truncated.
			line := renderConvoyLine(c, false, 200)

			if strings.Contains(line, "\n") {
				t.Errorf("renderConvoyLine emitted newline; rendered output must be a single visual line.\ntitle=%q\noutput=%q", tt.title, line)
			}

			// The trailing counter must appear on the same (only) line.
			if !strings.Contains(line, "1/3") {
				t.Errorf("renderConvoyLine dropped the counter; expected '1/3' in output: %q", line)
			}

			// The convoy ID must still appear.
			if !strings.Contains(line, c.ID) {
				t.Errorf("renderConvoyLine dropped the convoy ID %q in output: %q", c.ID, line)
			}
		})
	}
}

// TestRenderConvoyLineLandedSingleLine is the landed-section variant — the
// trailing token is a "✓ Xh ago" segment instead of a counter, but the same
// single-line invariant must hold.
func TestRenderConvoyLineLandedSingleLine(t *testing.T) {
	c := Convoy{
		ID:    "hq-cv-test2",
		Title: "design: Structured design exploration via parallel specialized analysts.\n\nFollow-up text.",
	}

	line := renderConvoyLine(c, true, 200)

	if strings.Contains(line, "\n") {
		t.Errorf("landed renderConvoyLine emitted newline; output=%q", line)
	}
	if !strings.Contains(line, c.ID) {
		t.Errorf("landed renderConvoyLine dropped convoy ID; output=%q", line)
	}
}

// TestRenderConvoyLineTruncationAfterSanitize verifies that the truncation
// path operates on the sanitized (single-line) title. Without sanitization,
// truncating a title containing "\n" would produce visually wrong output:
// the truncation would count runes including the newline, and the resulting
// "..." could land in the middle of a paragraph break.
func TestRenderConvoyLineTruncationAfterSanitize(t *testing.T) {
	// Title with a leading short paragraph, blank line, then more prose.
	// After sanitization, this becomes one long line. With a narrow width,
	// it should be truncated with "..." at the end on a single visual line.
	c := Convoy{
		ID:    "hq-cv-test3",
		Title: "First paragraph here.\n\nSecond paragraph that is long enough to overflow available width.",
	}

	// Narrow width forces truncation.
	line := renderConvoyLine(c, false, 60)

	if strings.Contains(line, "\n") {
		t.Fatalf("renderConvoyLine with narrow width emitted newline; output=%q", line)
	}
	if !strings.Contains(line, "...") {
		t.Errorf("expected truncation marker '...' in narrow output: %q", line)
	}
}
