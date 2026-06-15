package prwatcher

import (
	"regexp"
	"strings"
)

// Class is the triage verdict for a review comment.
type Class int

const (
	// ClassMechanical means the comment clearly requests a mechanical change
	// (typo, formatting, rename, gofmt-style) with no judgment signal. These
	// are auto-dispatched: bead created + slung to a fresh polecat.
	ClassMechanical Class = iota

	// ClassJudgment means the comment requires human judgment to action — it
	// asks "why", proposes a refactor, raises a design/security/correctness
	// concern, or simply doesn't match any mechanical signal. These are gated:
	// bead created with needs-human-triage, mayor mailed, NOT auto-slung.
	ClassJudgment
)

// String renders the class for logs and the bead label suffix.
func (c Class) String() string {
	switch c {
	case ClassMechanical:
		return "mechanical"
	case ClassJudgment:
		return "judgment"
	default:
		return "unknown"
	}
}

// mechanicalSignals are word/phrase patterns that, in the ABSENCE of any
// judgment signal, mark a comment as clearly mechanical. Matched
// case-insensitively against the comment body. Kept deliberately narrow: a
// false "mechanical" verdict auto-dispatches a change without human review, so
// the bar is "a junior could apply this blindly and be right". Anything broader
// belongs in the judgment class where a human confirms first.
var mechanicalSignals = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\btypo\b`),
	regexp.MustCompile(`(?i)\bmisspell`),
	regexp.MustCompile(`(?i)\bspelling\b`),
	regexp.MustCompile(`(?i)\bgofmt\b`),
	regexp.MustCompile(`(?i)\bformat(ting)?\b`),
	regexp.MustCompile(`(?i)\bindent(ation)?\b`),
	regexp.MustCompile(`(?i)\bwhitespace\b`),
	regexp.MustCompile(`(?i)\btrailing (whitespace|space|comma|newline)\b`),
	regexp.MustCompile(`(?i)\brename\b`),
	regexp.MustCompile(`(?i)\bunused (import|variable|var)\b`),
	regexp.MustCompile(`(?i)\bmissing (newline|semicolon|period)\b`),
	regexp.MustCompile(`(?i)\blint(er|ing)?\b`),
}

// judgmentSignals are patterns that VETO a mechanical verdict. If any of these
// appear, the comment requires human judgment regardless of any mechanical
// signal also present — a comment like "fix the typo, but also why is this
// locked here?" must gate, not auto-dispatch. This is the gate-by-default
// safety net: when a comment mixes signals, judgment wins.
var judgmentSignals = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bwhy\b`),
	regexp.MustCompile(`(?i)\bconsider\b`),
	regexp.MustCompile(`(?i)\brefactor`),
	regexp.MustCompile(`(?i)\bredesign\b`),
	regexp.MustCompile(`(?i)\barchitect`),
	regexp.MustCompile(`(?i)\bsecurity\b`),
	regexp.MustCompile(`(?i)\bvulnerab`),
	regexp.MustCompile(`(?i)\brace\b`),
	regexp.MustCompile(`(?i)\bdeadlock\b`),
	regexp.MustCompile(`(?i)\bperformance\b`),
	regexp.MustCompile(`(?i)\bcorrectness\b`),
	regexp.MustCompile(`(?i)\bedge case\b`),
	regexp.MustCompile(`(?i)\bbug\b`),
	regexp.MustCompile(`(?i)\bbreak(s|ing)?\b`),
	regexp.MustCompile(`(?i)\bregress`),
	regexp.MustCompile(`(?i)\bshould (we|this|it|n't)\b`),
	regexp.MustCompile(`(?i)\bnot sure\b`),
	regexp.MustCompile(`(?i)\bthoughts\?`),
	regexp.MustCompile(`(?i)\bdiscuss\b`),
	regexp.MustCompile(`(?i)\?\s*$`), // a trailing question mark = the reviewer is asking, not directing
}

// Triage classifies a review comment. The rule is gate-by-default:
//
//   - If the body matches ANY judgment signal → ClassJudgment (judgment wins
//     over any mechanical signal also present).
//   - Else if the body matches ANY mechanical signal → ClassMechanical.
//   - Else (no signal at all) → ClassJudgment. An unrecognized comment is
//     treated as judgment so we never auto-dispatch something we don't
//     understand.
//
// The classifier is intentionally a pure function of the comment body with no
// LLM call: deterministic, fast, and unit-testable. It is isolated behind this
// single entry point so an LLM-backed classifier could replace it later without
// touching the watcher.
func Triage(c ReviewComment) Class {
	body := strings.TrimSpace(c.Body)
	if body == "" {
		return ClassJudgment
	}
	for _, re := range judgmentSignals {
		if re.MatchString(body) {
			return ClassJudgment
		}
	}
	for _, re := range mechanicalSignals {
		if re.MatchString(body) {
			return ClassMechanical
		}
	}
	return ClassJudgment
}
