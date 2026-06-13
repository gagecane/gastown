package curio

import "strings"

// Outcome values for a reconciled curio_ledger row. These are the resolution
// classes the P3 precision lane reasons over; only OutcomeFalsePositive
// decrements a rule's measured precision. The string forms match the design's
// precision query (design-doc §"Outcome values" / §"Precision computation"),
// so they MUST stay underscore-form (`false_positive`, not `false-positive`).
const (
	// OutcomeFixed — the finding led to a real fix (bead closed with a merge
	// commit / explicit fixed/landed/resolved close).
	OutcomeFixed = "fixed"
	// OutcomeFalsePositive — the finding was wrong. The ONLY outcome that
	// decrements precision.
	OutcomeFalsePositive = "false_positive"
	// OutcomeDuplicate — the finding duplicated a known issue.
	OutcomeDuplicate = "duplicate"
	// OutcomeDeferred — the finding was real but not actionable now.
	OutcomeDeferred = "deferred"
	// OutcomeUnknown — the close reason was ambiguous. Per review Must-Fix #2 an
	// ambiguous close maps here, NEVER to OutcomeFixed, and the precision
	// formula EXCLUDES unknown (a young/unjudgeable filing must not inflate or
	// deflate a rule's measured precision).
	OutcomeUnknown = "unknown"
)

// outcomeLabelPrefix is the structured close-label the reconciler trusts first:
// a closer stamps `curio-outcome:<fixed|fp|dup|deferred>` to record the
// resolution explicitly, sidestepping the lossy free-text heuristic. Short
// codes (fp/dup) keep the label terse; classifyOutcomeLabel expands them.
const outcomeLabelPrefix = "curio-outcome:"

// ClassifyOutcome maps a closed curio bead's signals to a ledger outcome.
//
// Resolution order (review Must-Fix #2):
//  1. PREFERRED — a structured `curio-outcome:<code>` label. High confidence:
//     the closer named the outcome, so it wins over any free text.
//  2. FALLBACK — a low-confidence free-text heuristic over the close reason.
//     Only clear signals map to a concrete outcome; anything ambiguous (the
//     empty reason, a generic "done"/"closed", arbitrary text) maps to
//     OutcomeUnknown — never silently to OutcomeFixed.
func ClassifyOutcome(closeReason string, labels []string) string {
	if o := classifyOutcomeLabel(labels); o != "" {
		return o
	}
	return classifyCloseReason(closeReason)
}

// classifyOutcomeLabel returns the outcome named by a curio-outcome:<code>
// label, or "" if no such label is present. An unrecognized code is treated as
// no label (falls through to the free-text heuristic) rather than guessed.
func classifyOutcomeLabel(labels []string) string {
	for _, l := range labels {
		code, ok := strings.CutPrefix(strings.TrimSpace(l), outcomeLabelPrefix)
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(code)) {
		case "fixed":
			return OutcomeFixed
		case "fp", "false_positive", "false-positive":
			return OutcomeFalsePositive
		case "dup", "duplicate":
			return OutcomeDuplicate
		case "deferred", "defer":
			return OutcomeDeferred
		}
	}
	return ""
}

// classifyCloseReason is the low-confidence free-text fallback. It scans the
// close reason for unambiguous markers and defaults to OutcomeUnknown for
// everything else — generic closes ("done", "closed", "") are intentionally
// NOT classified as fixed (Must-Fix #2). False-positive is checked before
// fixed/duplicate/deferred so a reason like "false alarm, not a real spike"
// classifies as FP rather than matching an incidental keyword.
func classifyCloseReason(closeReason string) string {
	r := strings.ToLower(strings.TrimSpace(closeReason))
	switch {
	case r == "":
		return OutcomeUnknown
	case strings.Contains(r, "false") || strings.Contains(r, "not a real") ||
		strings.Contains(r, "not real") || strings.Contains(r, "invalid") ||
		strings.Contains(r, "no-changes") || strings.Contains(r, "no changes"):
		return OutcomeFalsePositive
	case strings.Contains(r, "duplicate") || strings.Contains(r, "dup of") ||
		strings.Contains(r, "superseded"):
		return OutcomeDuplicate
	case strings.Contains(r, "defer") || strings.Contains(r, "wontfix") ||
		strings.Contains(r, "won't fix") || strings.Contains(r, "not actionable"):
		return OutcomeDeferred
	case strings.Contains(r, "merged") || strings.Contains(r, "merge commit") ||
		strings.Contains(r, "fixed") || strings.Contains(r, "landed") ||
		strings.Contains(r, "resolved"):
		return OutcomeFixed
	default:
		return OutcomeUnknown
	}
}
