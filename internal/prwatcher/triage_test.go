package prwatcher

import "testing"

func TestTriageMechanical(t *testing.T) {
	mechanical := []string{
		"typo: 'recieve' should be 'receive'",
		"please run gofmt on this file",
		"nit: fix the indentation here",
		"rename this variable to something clearer",
		"trailing whitespace",
		"unused import",
		"missing newline at end of file",
		"the linter is complaining about this",
		"Formatting is off here",
	}
	for _, body := range mechanical {
		if got := Triage(ReviewComment{Body: body}); got != ClassMechanical {
			t.Errorf("Triage(%q) = %s, want mechanical", body, got)
		}
	}
}

func TestTriageJudgment(t *testing.T) {
	judgment := []string{
		"why is this locked here?",
		"consider extracting this into a helper",
		"this should be refactored",
		"potential security issue with this input",
		"there's a race condition here",
		"this could deadlock under load",
		"performance concern: this is O(n^2)",
		"is this correct? what about the edge case where n=0",
		"should we handle the nil case?",
		"thoughts?",
		"let's discuss this approach",
		"this breaks the existing contract",
		"not sure this is right",
		"",                                     // empty body gates
		"please update the documentation here", // no mechanical signal → gates
	}
	for _, body := range judgment {
		if got := Triage(ReviewComment{Body: body}); got != ClassJudgment {
			t.Errorf("Triage(%q) = %s, want judgment", body, got)
		}
	}
}

func TestTriageJudgmentWinsOverMechanical(t *testing.T) {
	// A comment that mixes a mechanical ask with a judgment signal must gate —
	// judgment wins. This is the gate-by-default safety net.
	mixed := []string{
		"fix the typo, but also why is this locked here?",
		"rename this — though consider whether the whole struct should move",
		"gofmt this. should we also add a test?",
	}
	for _, body := range mixed {
		if got := Triage(ReviewComment{Body: body}); got != ClassJudgment {
			t.Errorf("Triage(%q) = %s, want judgment (judgment must win over mechanical)", body, got)
		}
	}
}

func TestTriageTrailingQuestionGates(t *testing.T) {
	// A reviewer asking a question (trailing ?) is requesting discussion, not
	// directing a mechanical change.
	if got := Triage(ReviewComment{Body: "rename this to fooBar?"}); got != ClassJudgment {
		t.Errorf("trailing question should gate, got %s", got)
	}
}

func TestClassString(t *testing.T) {
	if ClassMechanical.String() != "mechanical" {
		t.Errorf("ClassMechanical.String() = %q", ClassMechanical.String())
	}
	if ClassJudgment.String() != "judgment" {
		t.Errorf("ClassJudgment.String() = %q", ClassJudgment.String())
	}
}
