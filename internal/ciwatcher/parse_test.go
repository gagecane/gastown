package ciwatcher

import "testing"

func TestExtractBeadID(t *testing.T) {
	cases := []struct {
		name    string
		subject string
		want    string
	}{
		{"trailing parens, simple", "fix(done): close attached wisp on no-MR close path (gu-irou)", "gu-irou"},
		{"trailing parens, leg ID", "docs(design): add UX analysis (gu-leg-xtwu2)", "gu-leg-xtwu2"},
		{"trailing parens, subtask ID", "feat(refinery): handle slot timeout (gu-aei.1)", "gu-aei.1"},
		{"hq prefix", "chore(hq): bump version (hq-12ab)", "hq-12ab"},
		{"multiple — last wins", "fix(done): cite (gu-c13x) and address (gu-rh0g)", "gu-rh0g"},
		{"no parens", "WIP work in progress on something", ""},
		{"non-bead parens", "fix(refinery): handle (n+1) edge case", ""},
		{"github issue ref ignored", "fix: address (#1234)", ""},
		{"empty string", "", ""},
		{"bead embedded mid-line, no trailer", "feat(refinery): wire (gu-aei) into engine but no trailer", "gu-aei"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := ExtractBeadID(c.subject)
			if got != c.want {
				t.Errorf("ExtractBeadID(%q) = %q, want %q", c.subject, got, c.want)
			}
		})
	}
}
