package cmd

import (
	"reflect"
	"testing"

	"github.com/steveyegge/gastown/internal/rig"
)

func TestRepoBasenameFromGitURL(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"https with .git", "https://github.com/owner/CodegenAgentSchedulerConstructs.git", "CodegenAgentSchedulerConstructs"},
		{"https without .git", "https://github.com/owner/foo", "foo"},
		{"scp style", "git@github.com:owner/foo.git", "foo"},
		{"trailing slash dropped by Base", "https://github.com/owner/foo/", "foo"},
		{"whitespace", "  https://github.com/owner/foo.git  ", "foo"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := repoBasenameFromGitURL(tc.in)
			if got != tc.want {
				t.Errorf("repoBasenameFromGitURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDetectRigMismatchFromTitle(t *testing.T) {
	rigs := []*rig.Rig{
		{Name: "casc_cdk", GitURL: "https://github.com/o/CodegenAgentSchedulerCDK.git"},
		{Name: "casc_constructs", GitURL: "https://github.com/o/CodegenAgentSchedulerConstructs.git"},
		{Name: "casc_lambda", GitURL: "https://github.com/o/CodegenAgentSchedulerLambda.git"},
		{Name: "gastown", GitURL: "https://github.com/o/gastown.git"},
	}

	tests := []struct {
		name      string
		title     string
		targetRig string
		want      []string
	}{
		{
			name:      "concrete gu-an4y case: cadk bead titled for constructs",
			title:     "Bootstrap docs for CodegenAgentSchedulerConstructs",
			targetRig: "casc_cdk",
			want:      []string{"casc_constructs"},
		},
		{
			name:      "rig name token in title",
			title:     "Update casc_lambda integration",
			targetRig: "casc_cdk",
			want:      []string{"casc_lambda"},
		},
		{
			name:      "title mentions target rig only — no warning",
			title:     "Refactor CodegenAgentSchedulerCDK plumbing",
			targetRig: "casc_cdk",
			want:      nil,
		},
		{
			name:      "title mentions target rig name only — no warning",
			title:     "Ship something in casc_cdk",
			targetRig: "casc_cdk",
			want:      nil,
		},
		{
			name:      "no rig mentions",
			title:     "Fix flaky test in scheduler",
			targetRig: "casc_cdk",
			want:      nil,
		},
		{
			name:      "case-insensitive",
			title:     "fix CASC_LAMBDA build",
			targetRig: "casc_cdk",
			want:      []string{"casc_lambda"},
		},
		{
			name:      "multiple foreign rigs sorted",
			title:     "Touch casc_lambda and CodegenAgentSchedulerConstructs",
			targetRig: "casc_cdk",
			want:      []string{"casc_constructs", "casc_lambda"},
		},
		{
			name:      "substring should NOT match (whole-word boundary)",
			title:     "Update casc_lambdas test", // 'casc_lambdas' != 'casc_lambda'
			targetRig: "casc_cdk",
			want:      nil,
		},
		{
			name:      "empty title",
			title:     "",
			targetRig: "casc_cdk",
			want:      nil,
		},
		{
			name:      "empty target rig — still detects all foreign mentions",
			title:     "casc_lambda thing",
			targetRig: "",
			want:      []string{"casc_lambda"},
		},
		{
			name:      "punctuation around token still matches",
			title:     "[bootstrap] casc_constructs: docs",
			targetRig: "casc_cdk",
			want:      []string{"casc_constructs"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := detectRigMismatchFromTitle(tc.title, tc.targetRig, rigs)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("detectRigMismatchFromTitle(%q, %q) = %v, want %v",
					tc.title, tc.targetRig, got, tc.want)
			}
		})
	}
}

func TestDetectRigMismatchFromTitle_EmptyRigs(t *testing.T) {
	got := detectRigMismatchFromTitle("anything goes here", "x", nil)
	if got != nil {
		t.Errorf("nil rigs should return nil, got %v", got)
	}
	got = detectRigMismatchFromTitle("anything", "x", []*rig.Rig{})
	if got != nil {
		t.Errorf("empty rigs should return nil, got %v", got)
	}
}

func TestDetectRigMismatchFromTitle_MultiTokenRepoBaseSkipped(t *testing.T) {
	// A repo basename containing characters outside [A-Za-z0-9_] tokenizes to
	// multiple tokens. We deliberately skip those rather than substring-match.
	rigs := []*rig.Rig{
		{Name: "weird", GitURL: "https://github.com/o/foo-bar-baz.git"},
	}
	// "foo" by itself is too generic; we don't substring-match the basename.
	got := detectRigMismatchFromTitle("the foo refactor", "other", rigs)
	if got != nil {
		t.Errorf("multi-token repo basename should not partial-match, got %v", got)
	}
	// But the rig name itself still matches.
	got = detectRigMismatchFromTitle("the weird thing", "other", rigs)
	if !reflect.DeepEqual(got, []string{"weird"}) {
		t.Errorf("rig name should match even when basename is skipped, got %v", got)
	}
}
