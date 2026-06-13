package curio

import "testing"

// TestClassifyOutcome_StructuredLabelPreferred asserts the structured
// curio-outcome:<code> label wins over the free-text heuristic — including when
// the free text would otherwise classify differently — and that every code
// (long and short forms) maps correctly.
func TestClassifyOutcome_StructuredLabelPreferred(t *testing.T) {
	cases := []struct {
		name        string
		closeReason string
		labels      []string
		want        string
	}{
		{"label fixed", "false alarm", []string{"curio-outcome:fixed"}, OutcomeFixed},
		{"label fp short", "merged and landed", []string{"curio-outcome:fp"}, OutcomeFalsePositive},
		{"label fp long", "done", []string{"curio-outcome:false_positive"}, OutcomeFalsePositive},
		{"label dup short", "merged", []string{"curio-outcome:dup"}, OutcomeDuplicate},
		{"label dup long", "", []string{"curio-outcome:duplicate"}, OutcomeDuplicate},
		{"label deferred", "merged", []string{"curio-outcome:deferred"}, OutcomeDeferred},
		{"label among others", "x", []string{"gt:task", "curio-outcome:fixed", "rule:r"}, OutcomeFixed},
		{"label whitespace tolerated", "x", []string{" curio-outcome: FP "}, OutcomeFalsePositive},
		{"unknown code falls through to heuristic", "merged", []string{"curio-outcome:bogus"}, OutcomeFixed},
		{"unknown code, ambiguous reason -> unknown", "done", []string{"curio-outcome:bogus"}, OutcomeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyOutcome(tc.closeReason, tc.labels); got != tc.want {
				t.Errorf("ClassifyOutcome(%q, %v) = %q, want %q", tc.closeReason, tc.labels, got, tc.want)
			}
		})
	}
}

// TestClassifyOutcome_AmbiguousMapsToUnknown is the review Must-Fix #2 guard:
// an ambiguous / generic / empty close reason MUST map to 'unknown', never to
// 'fixed'. A misclassification here would silently inflate a rule's measured
// precision (unknown is excluded from precision; fixed counts as a true
// positive), so this case is asserted explicitly.
func TestClassifyOutcome_AmbiguousMapsToUnknown(t *testing.T) {
	ambiguous := []string{
		"",
		"done",
		"closed",
		"closing this out",
		"per discussion",
		"handled",
		"obsolete",
	}
	for _, r := range ambiguous {
		t.Run("reason="+r, func(t *testing.T) {
			got := ClassifyOutcome(r, nil)
			if got == OutcomeFixed {
				t.Errorf("ClassifyOutcome(%q) = %q; ambiguous reason must NOT map to fixed", r, got)
			}
			if got != OutcomeUnknown {
				t.Errorf("ClassifyOutcome(%q) = %q, want %q", r, got, OutcomeUnknown)
			}
		})
	}
}

// TestClassifyOutcome_FreeTextHeuristic exercises the low-confidence fallback
// for unambiguous free-text reasons (no structured label present).
func TestClassifyOutcome_FreeTextHeuristic(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{"merged in abc123", OutcomeFixed},
		{"fixed by the patch", OutcomeFixed},
		{"landed on main", OutcomeFixed},
		{"resolved", OutcomeFixed},
		{"false positive — not a real spike", OutcomeFalsePositive},
		{"not a real issue", OutcomeFalsePositive},
		{"invalid finding", OutcomeFalsePositive},
		{"no-changes: cannot reproduce", OutcomeFalsePositive},
		{"duplicate of gu-xyz", OutcomeDuplicate},
		{"dup of gu-abc", OutcomeDuplicate},
		{"superseded by newer finding", OutcomeDuplicate},
		{"deferred to next quarter", OutcomeDeferred},
		{"wontfix", OutcomeDeferred},
		{"not actionable right now", OutcomeDeferred},
		// FP precedence: a reason mentioning both "false" and "merged" is FP.
		{"false alarm, merged the revert", OutcomeFalsePositive},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			if got := ClassifyOutcome(tc.reason, nil); got != tc.want {
				t.Errorf("ClassifyOutcome(%q) = %q, want %q", tc.reason, got, tc.want)
			}
		})
	}
}
