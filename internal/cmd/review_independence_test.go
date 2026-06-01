package cmd

import (
	"strings"
	"testing"
)

// TestReviewGateReviewedBead verifies the structural review-gate detection
// (gs-aoz): a bead is a review gate only with BOTH the gt:review-gate label AND
// a reviews: target; otherwise the reviewed bead is "" (not a gate).
func TestReviewGateReviewedBead(t *testing.T) {
	cases := []struct {
		name        string
		labels      []string
		description string
		want        string
	}{
		{
			name:        "label + reviews field",
			labels:      []string{labelReviewGate},
			description: "owner: mayor/\nreviews: lb-yuhl\nseverity: high",
			want:        "lb-yuhl",
		},
		{
			name:        "label but no reviews target is not actionable",
			labels:      []string{labelReviewGate},
			description: "owner: mayor/\n",
			want:        "",
		},
		{
			name:        "reviews field but no label is not a gate",
			labels:      []string{"priority-high"},
			description: "reviews: lb-yuhl\n",
			want:        "",
		},
		{
			name:        "no label no field",
			labels:      nil,
			description: "just a normal bead",
			want:        "",
		},
		{
			name:        "reviews key is case-insensitive and trimmed",
			labels:      []string{"x", labelReviewGate},
			description: "Reviews:   lb-abc  ",
			want:        "lb-abc",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reviewGateReviewedBead(tc.labels, tc.description); got != tc.want {
				t.Errorf("reviewGateReviewedBead = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestViolatesBuilderIndependence verifies the builder==reviewer decision,
// including identity normalization and the fail-open on empty identifiers.
func TestViolatesBuilderIndependence(t *testing.T) {
	cases := []struct {
		name     string
		builder  string
		acquirer string
		want     bool
	}{
		{"same agent violates", "gastown/polecats/capable", "gastown/polecats/capable", true},
		{"trailing slash + case normalized", "gastown/polecats/Capable/", "gastown/polecats/capable", true},
		{"different agent is fine", "gastown/polecats/capable", "gastown/polecats/rictus", false},
		{"empty builder fails open", "", "gastown/polecats/capable", false},
		{"empty acquirer fails open", "gastown/polecats/capable", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := violatesBuilderIndependence(tc.builder, tc.acquirer); got != tc.want {
				t.Errorf("violatesBuilderIndependence(%q,%q) = %v, want %v",
					tc.builder, tc.acquirer, got, tc.want)
			}
		})
	}
}

// TestAssertReviewerIndependence_FailsOpen verifies the guard never blocks when
// the bead is not a review gate (no label / no target), independent of agent —
// so it can only ever block a genuine builder-reviews-own-work case.
func TestAssertReviewerIndependence_FailsOpen(t *testing.T) {
	// Not a review gate → nil even though the agent string is non-empty.
	if err := assertReviewerIndependence("", "lb-x", []string{"priority-high"}, "reviews: lb-y", "gastown/polecats/capable"); err != nil {
		t.Errorf("non-gate bead must pass: %v", err)
	}
	// Review-gate label but no reviews target → nil (nothing to enforce).
	if err := assertReviewerIndependence("", "lb-x", []string{labelReviewGate}, "owner: mayor/", "gastown/polecats/capable"); err != nil {
		t.Errorf("gate without reviewed target must pass: %v", err)
	}
}

// TestWithReviewsField verifies the producer writes a reviews: line that the
// consumer (parseReviewsField) reads back — appending when absent, replacing
// when present, and preserving surrounding content (gs-bo1).
func TestWithReviewsField(t *testing.T) {
	cases := []struct {
		name  string
		desc  string
		build string
	}{
		{"empty description", "", "gs-yuhl"},
		{"append to existing content", "owner: mayor/\nseverity: high", "gs-yuhl"},
		{"replace existing reviews line", "owner: mayor/\nreviews: gs-old\nseverity: high", "gs-new"},
		{"replace case-insensitive key", "Reviews:   gs-old  ", "gs-new"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := withReviewsField(tc.desc, tc.build)
			// Round-trip: the consumer must read back exactly what we wrote.
			if rt := parseReviewsField(got); rt != tc.build {
				t.Errorf("round-trip parseReviewsField = %q, want %q\nresult:\n%s", rt, tc.build, got)
			}
			// Exactly one reviews: line — no duplicates left behind.
			n := 0
			for _, line := range strings.Split(got, "\n") {
				key, _, ok := strings.Cut(strings.TrimSpace(line), ":")
				if ok && strings.ToLower(strings.TrimSpace(key)) == reviewsFieldKey {
					n++
				}
			}
			if n != 1 {
				t.Errorf("expected exactly 1 reviews line, got %d:\n%s", n, got)
			}
		})
	}

	// Non-reviews content is preserved verbatim.
	got := withReviewsField("owner: mayor/\nseverity: high", "gs-yuhl")
	if !strings.Contains(got, "owner: mayor/") || !strings.Contains(got, "severity: high") {
		t.Errorf("surrounding content not preserved:\n%s", got)
	}
}
