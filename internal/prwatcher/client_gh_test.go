package prwatcher

import "testing"

func TestParseGitHubOwnerRepo(t *testing.T) {
	type want struct{ owner, repo string }
	cases := map[string]want{
		"https://github.com/gagecane/gastown":     {"gagecane", "gastown"},
		"https://github.com/gagecane/gastown.git": {"gagecane", "gastown"},
		"https://github.com/gagecane/gastown/":    {"gagecane", "gastown"},
		"git@github.com:gagecane/gastown.git":     {"gagecane", "gastown"},
		"git@github.com:gagecane/gastown":         {"gagecane", "gastown"},
		"https://github.com/owner/repo/extra":     {"owner", "repo"},
		"https://bitbucket.org/ws/repo.git":       {"", ""},
		"https://github.com/onlyowner":            {"", ""},
		"":                                        {"", ""},
	}
	for in, w := range cases {
		o, r := parseGitHubOwnerRepo(in)
		if o != w.owner || r != w.repo {
			t.Errorf("parseGitHubOwnerRepo(%q) = (%q,%q), want (%q,%q)", in, o, r, w.owner, w.repo)
		}
	}
}

func TestIsNotFoundErr(t *testing.T) {
	cases := map[string]bool{
		"HTTP 404: Not Found": true,
		"Could not resolve to a Repository with the name 'x/y'": true,
		"HTTP 403: Forbidden": false,
		"some other error":    false,
		"":                    false,
	}
	for in, want := range cases {
		if got := isNotFoundErr(in); got != want {
			t.Errorf("isNotFoundErr(%q) = %v, want %v", in, got, want)
		}
	}
}
