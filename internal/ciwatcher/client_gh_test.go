package ciwatcher

import "testing"

func TestMapGHConclusion(t *testing.T) {
	cases := map[string]Conclusion{
		"success":         ConclusionSuccess,
		"failure":         ConclusionFailure,
		"cancelled":       ConclusionCancelled,
		"timed_out":       ConclusionTimedOut,
		"startup_failure": ConclusionStartupFailure,
		"":                ConclusionUnknown,
		"action_required": ConclusionUnknown,
		"neutral":         ConclusionUnknown,
	}
	for in, want := range cases {
		if got := mapGHConclusion(in); got != want {
			t.Errorf("mapGHConclusion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConclusionIsFailureLike(t *testing.T) {
	failureLike := []Conclusion{ConclusionFailure, ConclusionTimedOut, ConclusionStartupFailure}
	for _, c := range failureLike {
		if !c.IsFailureLike() {
			t.Errorf("%s should be failure-like", c)
		}
	}
	notFailureLike := []Conclusion{ConclusionSuccess, ConclusionCancelled, ConclusionUnknown}
	for _, c := range notFailureLike {
		if c.IsFailureLike() {
			t.Errorf("%s should NOT be failure-like", c)
		}
	}
}
