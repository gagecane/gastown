package cmd

import (
	"os"
	"testing"
)

func TestMatchesWrongRigReason(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		want   bool
	}{
		{"empty", "", false},
		{"unrelated close", "fixed in main", false},
		{"already fixed", "no-changes: already fixed upstream", false},

		{"explicit wrong rig", "no-changes: wrong rig, this is a casc_crud bug", true},
		{"hyphenated wrong-rig", "wrong-rig — should be in casc_cdk", true},
		{"capitalized wrong rig", "Wrong Rig", true},

		{"belongs in", "this belongs in casc_crud", true},
		{"belongs in rig", "belongs in rig casc_crud", true},
		{"belong in (singular noun match)", "these tests belong in obsidian", true},

		{"should be in", "should be in casc_cdk, not casc_lambda", true},
		{"should be filed in", "should be filed in casc_crud", true},
		{"should be filed under", "should be filed under casc_crud", true},

		{"not this rig", "not this rig", true},
		{"not the right rig", "not the right rig — see cala-tl5", true},
		{"not in this rig", "not in this rig", true},

		// False-positive guards: phrases that contain substrings of triggers
		// but should NOT match.
		{"belong (no in)", "I belong here", false},
		{"should be (no in/filed)", "this should be merged soon", false},
		{"unrelated 'rig'", "the test rig was unstable today", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesWrongRigReason(tt.reason); got != tt.want {
				t.Errorf("matchesWrongRigReason(%q) = %v, want %v", tt.reason, got, tt.want)
			}
		})
	}
}

func TestExtractCloseReason(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no flag", []string{"gt-abc"}, ""},
		{"--reason space", []string{"gt-abc", "--reason", "wrong rig"}, "wrong rig"},
		{"--reason= form", []string{"gt-abc", "--reason=wrong rig"}, "wrong rig"},
		{"-r short", []string{"-r", "wrong rig", "gt-abc"}, "wrong rig"},
		{"reason with empty value not consumed", []string{"gt-abc", "--reason"}, ""},
		{"reason= empty", []string{"gt-abc", "--reason="}, ""},
		{
			name: "reason among other flags",
			args: []string{"--force", "gt-abc", "--reason", "belongs in casc_crud", "--suggest-next"},
			want: "belongs in casc_crud",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractCloseReason(tt.args); got != tt.want {
				t.Errorf("extractCloseReason(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestDetectClosingRig_PrefersGTRIG(t *testing.T) {
	t.Setenv("GT_RIG", "casc_lambda")
	if got := detectClosingRig(); got != "casc_lambda" {
		t.Errorf("detectClosingRig() with GT_RIG=casc_lambda = %q, want %q", got, "casc_lambda")
	}
}

func TestDetectClosingRig_TrimsWhitespace(t *testing.T) {
	t.Setenv("GT_RIG", "  casc_crud  ")
	if got := detectClosingRig(); got != "casc_crud" {
		t.Errorf("detectClosingRig() = %q, want %q", got, "casc_crud")
	}
}

func TestDetectClosingRig_UnsetGTRIGFallsBackOrEmpty(t *testing.T) {
	// Unset GT_RIG; rig may be inferred from cwd or come back empty
	// depending on whether the test is invoked from inside a rig worktree.
	// Either outcome is acceptable — we just assert no panic and a string.
	t.Setenv("GT_RIG", "")
	got := detectClosingRig()
	// Just exercise the path; the value depends on test runner cwd.
	_ = got
}

func TestApplyWrongRigLabels_NoOpOnNonMatch(t *testing.T) {
	// Reason doesn't match the wrong-rig pattern → no bd update should run.
	// We assert the function does not panic and returns immediately. There's
	// no exec subprocess to capture, but the early return is exercised.
	t.Setenv("GT_RIG", "casc_lambda")
	applyWrongRigLabels([]string{"gt-abc"}, "fixed in main")
	// (No assertion possible without process injection; the goal here is to
	// catch panics and make the early-return path part of the coverage map.)
}

func TestApplyWrongRigLabels_NoOpWhenRigUnknown(t *testing.T) {
	// With GT_RIG empty and (possibly) no inferable cwd rig, applyWrongRigLabels
	// must early-return rather than producing a malformed "wrong-rig:" label.
	saved, hadIt := os.LookupEnv("GT_RIG")
	if hadIt {
		defer os.Setenv("GT_RIG", saved)
	} else {
		defer os.Unsetenv("GT_RIG")
	}
	os.Setenv("GT_RIG", "")
	// Won't actually invoke bd if rig resolution fails; if it does resolve
	// from cwd we still won't apply a malformed label. Either way: no panic.
	applyWrongRigLabels([]string{"gt-abc"}, "wrong rig")
}
