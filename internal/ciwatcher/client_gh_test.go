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

func TestParseGitHubRepo(t *testing.T) {
	cases := map[string]string{
		"https://github.com/gagecane/gastown":      "gagecane/gastown",
		"https://github.com/gagecane/gastown.git":  "gagecane/gastown",
		"https://github.com/gagecane/gastown/":     "gagecane/gastown",
		"git@github.com:gagecane/gastown.git":      "gagecane/gastown",
		"git@github.com:gagecane/gastown":          "gagecane/gastown",
		"https://github.com/owner/repo/extra/path": "owner/repo",
		// Non-GitHub or malformed → empty (fall back to gh inference).
		"https://bitbucket.org/ws/repo.git": "",
		"https://github.com/onlyowner":      "",
		"":                                  "",
	}
	for in, want := range cases {
		if got := parseGitHubRepo(in); got != want {
			t.Errorf("parseGitHubRepo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsRunsNotFoundErr(t *testing.T) {
	cases := map[string]bool{
		"failed to get runs: HTTP 404: Not Found (https://api.github.com/repos/owner/repo/actions/runs?...)": true,
		"HTTP 404": true,
		"failed to get runs: HTTP 403: Forbidden": false,
		"some other error":                        false,
		"":                                        false,
	}
	for in, want := range cases {
		if got := isRunsNotFoundErr(in); got != want {
			t.Errorf("isRunsNotFoundErr(%q) = %v, want %v", in, got, want)
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
