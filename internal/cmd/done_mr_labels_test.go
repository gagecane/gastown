package cmd

import (
	"reflect"
	"strings"
	"testing"
)

// TestBuildMRLabels_BaseLabelAlwaysPresent locks in the contract that every
// merge-request bead created by `gt done` carries the "gt:merge-request"
// queue marker. The refinery polls Beads with this label as its work queue;
// dropping it would silently make work invisible to the merge pipeline. This
// test guards against future refactors that try to "clean up" the constant
// out of existence.
func TestBuildMRLabels_BaseLabelAlwaysPresent(t *testing.T) {
	cases := []struct {
		name   string
		extras []string
	}{
		{name: "no extras", extras: nil},
		{name: "empty extras slice", extras: []string{}},
		{name: "one extra", extras: []string{"gt:auto-test-pr"}},
		{name: "multiple extras", extras: []string{"gt:auto-test-pr", "rig:gastown_upstream"}},
		{name: "extras with whitespace-only", extras: []string{"  ", "gt:auto-test-pr"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildMRLabels(tc.extras)
			if len(got) == 0 {
				t.Fatalf("buildMRLabels returned empty slice; gt:merge-request must always be present")
			}
			if got[0] != MRBaseLabel {
				t.Errorf("buildMRLabels(%v)[0] = %q, want %q at position 0", tc.extras, got[0], MRBaseLabel)
			}
			if !labelSliceContains(got, MRBaseLabel) {
				t.Errorf("buildMRLabels(%v) = %v, missing required label %q", tc.extras, got, MRBaseLabel)
			}
		})
	}
}

// TestBuildMRLabels_AutoTestPRLabelsRoundTrip is the unit test required by
// Phase 0 task 3a Round 3 fix #6 (per .designs/auto-test-pr/synthesis.md):
// when the auto-test-pr polecat invokes `gt done --label gt:auto-test-pr
// --label rig:<target_rig>`, the resulting MR bead MUST carry both labels
// alongside the base merge-request marker. The 3c cycle-close handler relies
// on the rig:<target_rig> label for O(1) lookup of the per-rig state bead;
// the gt:auto-test-pr label is the audit-trail backstop and the cycle filter
// for the branch GC patrol (mol-auto-test-pr-branch-gc).
func TestBuildMRLabels_AutoTestPRLabelsRoundTrip(t *testing.T) {
	const targetRig = "gastown_upstream"
	rigLabel := "rig:" + targetRig

	got := buildMRLabels([]string{"gt:auto-test-pr", rigLabel})

	want := []string{MRBaseLabel, "gt:auto-test-pr", rigLabel}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildMRLabels = %v, want %v", got, want)
	}

	// Pin the contract called out in the synthesis design: the cycle-close
	// handler reads the rig:<target_rig> label off the MR bead, so it MUST
	// be present and findable by HasLabel-style scans (Round 3 fix #6).
	if !labelSliceContains(got, "gt:auto-test-pr") {
		t.Errorf("buildMRLabels did not include gt:auto-test-pr; cycle-close audit trail / branch-GC filter would break")
	}
	if !labelSliceContains(got, rigLabel) {
		t.Errorf("buildMRLabels did not include %q; 3c cycle-close handler O(1) state-bead lookup would fall back to a graph walk", rigLabel)
	}
}

// TestBuildMRLabels_DedupsAndDropsEmpty exercises the input-hygiene path. A
// formula may pass an unset template variable (e.g. --label "rig:" if the
// dispatch envelope omits target_rig), and we never want a half-formed label
// like "rig:" landing on a queue bead. Likewise, a formula that
// double-registers gt:merge-request must not produce a duplicate entry.
func TestBuildMRLabels_DedupsAndDropsEmpty(t *testing.T) {
	got := buildMRLabels([]string{
		"",                  // empty
		"   ",               // whitespace
		"gt:merge-request",  // dup of base
		"gt:auto-test-pr",
		"gt:auto-test-pr",   // dup of extra
		"  rig:foo  ",       // whitespace trimmed
	})
	want := []string{MRBaseLabel, "gt:auto-test-pr", "rig:foo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildMRLabels dedup/trim path = %v, want %v", got, want)
	}
	for _, l := range got {
		if l != strings.TrimSpace(l) {
			t.Errorf("label %q has surrounding whitespace; trim path failed", l)
		}
		if l == "" {
			t.Errorf("buildMRLabels emitted an empty label; should have been dropped")
		}
	}
}

// TestBuildMRLabels_ReturnNonNil ensures callers can range over the result
// without a nil-check. The MR-bead Create call uses Labels: buildMRLabels(...)
// directly and a nil slice would force callers to special-case the no-extras
// path.
func TestBuildMRLabels_ReturnNonNil(t *testing.T) {
	got := buildMRLabels(nil)
	if got == nil {
		t.Errorf("buildMRLabels(nil) returned nil; want non-nil slice with at least the base label")
	}
}

func labelSliceContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
