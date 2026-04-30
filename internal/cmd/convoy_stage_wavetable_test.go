package cmd

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// renderWaveTable tests (U-30, U-38, gt-csl.4.2)
// ---------------------------------------------------------------------------

// U-30: Wave table includes blockers column
func TestRenderWaveTable_IncludesBlockers(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Title: "Task A", Type: "task", Rig: "gastown", Blocks: []string{"gt-b"}},
		"gt-b": {ID: "gt-b", Title: "Task B", Type: "task", Rig: "gastown", BlockedBy: []string{"gt-a"}},
	}}
	waves := []Wave{
		{Number: 1, Tasks: []string{"gt-a"}},
		{Number: 2, Tasks: []string{"gt-b"}},
	}
	output := renderWaveTable(waves, dag)
	if !strings.Contains(output, "gt-a") {
		t.Error("should show gt-a")
	}
	if !strings.Contains(output, "gt-b") {
		t.Error("should show gt-b")
	}
	// gt-b's row should show gt-a as blocker
	// The table should contain "gt-a" in the blocked-by column for gt-b
}

// U-38: Summary line shows totals
func TestRenderWaveTable_SummaryLine(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Title: "A", Type: "task", Rig: "gst"},
		"b": {ID: "b", Title: "B", Type: "task", Rig: "gst"},
		"c": {ID: "c", Title: "C", Type: "task", Rig: "gst"},
	}}
	waves := []Wave{
		{Number: 1, Tasks: []string{"a", "b"}},
		{Number: 2, Tasks: []string{"c"}},
	}
	output := renderWaveTable(waves, dag)
	if !strings.Contains(output, "3 tasks") {
		t.Error("should show 3 tasks")
	}
	if !strings.Contains(output, "2 waves") {
		t.Error("should show 2 waves")
	}
	if !strings.Contains(output, "max parallelism: 2") {
		t.Error("should show max parallelism 2")
	}
}

// Test empty waves
func TestRenderWaveTable_Empty(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{}}
	output := renderWaveTable(nil, dag)
	if !strings.Contains(output, "0 tasks") {
		t.Error("should show 0 tasks")
	}
}

// Test wave table with multiple rigs
func TestRenderWaveTable_MultipleRigs(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Title: "Task A", Type: "task", Rig: "gastown"},
		"bd-b": {ID: "bd-b", Title: "Task B", Type: "task", Rig: "beads"},
	}}
	waves := []Wave{
		{Number: 1, Tasks: []string{"bd-b", "gt-a"}},
	}
	output := renderWaveTable(waves, dag)
	if !strings.Contains(output, "gastown") {
		t.Error("should show gastown rig")
	}
	if !strings.Contains(output, "beads") {
		t.Error("should show beads rig")
	}
}

// Test wave table preserves multi-byte UTF-8 characters during title truncation.
// Regression test: byte-based truncation split em-dashes (U+2014, 3 bytes)
// mid-character, producing mojibake like "â" in the wave table output.
func TestRenderWaveTable_UTF8Truncation(t *testing.T) {
	// Title with em-dash that would be split by byte-based title[:28]
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Title: "F.2: Beads for Optuna rig \u2014 extra", Type: "task", Rig: "gst"},
	}}
	waves := []Wave{
		{Number: 1, Tasks: []string{"gt-a"}},
	}
	output := renderWaveTable(waves, dag)

	// Must not contain the mojibake byte 0xE2 without its continuation bytes.
	// If truncation splits the em-dash, the output will contain an isolated
	// 0xE2 byte which displays as "â".
	for i := 0; i < len(output); i++ {
		if output[i] == 0xE2 {
			// Verify the full 3-byte em-dash sequence is present
			if i+2 >= len(output) || output[i+1] != 0x80 || output[i+2] != 0x94 {
				// Could be a different 3-byte char (like box-drawing "─")
				// Check if it's a valid UTF-8 start byte with proper continuation
				if i+1 >= len(output) || (output[i+1]&0xC0) != 0x80 {
					t.Errorf("found isolated 0xE2 byte at position %d — UTF-8 truncation bug", i)
				}
			}
		}
	}

	// The truncated title should end with ".." and be valid UTF-8
	if !strings.Contains(output, "..") {
		t.Error("long title should be truncated with '..'")
	}
}
