package doctor

import "testing"

func TestSuggestRigForBug(t *testing.T) {
	cases := []struct {
		name       string
		title      string
		labels     []string
		wantRig    string
		wantPrefix string
	}{
		{"explicit lia_bac", "lia_bac main_branch_test gate red", nil, "lia_bac", "lb-"},
		{"explicit lia_web", "lia_web build broken", nil, "lia_web", "lw-"},
		{"explicit lia_iac label", "infra flake", []string{"lia_iac"}, "lia_iac", "li-"},
		{"gastown daemon", "gt daemon: dog hook-delivery broken", nil, "gastown", "gs-"},
		{"gastown refinery", "Refinery received empty MERGE_READY signals", nil, "gastown", "gs-"},
		{"gastown scheduler label", "no-auto-dispatch bypassed", []string{"scheduler"}, "gastown", "gs-"},
		{"rig name wins over subsystem word", "lia_bac scheduler dispatch bug", nil, "lia_bac", "lb-"},
		{"no hint defaults to gastown", "something vague is broken", nil, "gastown", "gs-"},
		{"case-insensitive", "DAEMON crash", nil, "gastown", "gs-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig, prefix := suggestRigForBug(tc.title, tc.labels)
			if rig != tc.wantRig || prefix != tc.wantPrefix {
				t.Errorf("suggestRigForBug(%q,%v) = (%q,%q), want (%q,%q)",
					tc.title, tc.labels, rig, prefix, tc.wantRig, tc.wantPrefix)
			}
		})
	}
}

func TestFindMisfiledRigBugs(t *testing.T) {
	bugs := []townBug{
		{ID: "hq-aaa", Title: "daemon scheduler broken", IssueType: "bug"},
		{ID: "hq-bbb", Title: "lia_bac gate red", IssueType: "bug", Labels: []string{"lia_bac"}},
		{ID: "hq-ccc", Title: "convoy tracking task", IssueType: "task"},        // not a bug → ignored
		{ID: "gs-ddd", Title: "already a rig bug", IssueType: "bug"},            // not hq- → ignored
		{ID: "hq-cv-eee", Title: "convoy", IssueType: "convoy"},                 // not a bug → ignored
		{ID: "hq-fff", Title: "daemon bug", IssueType: "bug", Status: "closed"}, // closed → ignored
	}
	got := findMisfiledRigBugs(bugs)
	if len(got) != 2 {
		t.Fatalf("got %d misfiled, want 2: %+v", len(got), got)
	}
	// Sorted by ID: hq-aaa (gastown), hq-bbb (lia_bac).
	if got[0].ID != "hq-aaa" || got[0].Prefix != "gs-" {
		t.Errorf("first = %+v, want hq-aaa/gs-", got[0])
	}
	if got[1].ID != "hq-bbb" || got[1].Prefix != "lb-" {
		t.Errorf("second = %+v, want hq-bbb/lb-", got[1])
	}
}
