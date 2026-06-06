package cmd

import (
	"errors"
	"testing"
)

// TestCorrectPhantomMainTarget covers the gu-aucji safety net: an MR whose
// resolved target is the literal "main" must be retargeted to the rig default
// on a rig where origin/main does not exist, and must be left untouched in every
// other case.
func TestCorrectPhantomMainTarget(t *testing.T) {
	tests := []struct {
		name          string
		currentTarget string
		defaultBranch string
		mainExists    bool
		lookupErr     error
		want          string
	}{
		{
			// The talontriage footgun: target leaked to "main" on a mainline-only
			// rig. origin/main is absent → rewrite to the rig default.
			name:          "phantom main on mainline-only rig is corrected",
			currentTarget: "main",
			defaultBranch: "mainline",
			mainExists:    false,
			want:          "mainline",
		},
		{
			// origin/main genuinely exists → "main" may be deliberate; never
			// second-guess it.
			name:          "main retained when origin/main exists",
			currentTarget: "main",
			defaultBranch: "mainline",
			mainExists:    true,
			want:          "main",
		},
		{
			// Main-default rig: "main" is correct, nothing to do (and we skip the
			// remote query entirely).
			name:          "main-default rig is a no-op",
			currentTarget: "main",
			defaultBranch: "main",
			mainExists:    false,
			want:          "main",
		},
		{
			// Non-main target is never touched, even on a mainline rig.
			name:          "non-main target untouched",
			currentTarget: "feat/some-branch",
			defaultBranch: "mainline",
			mainExists:    false,
			want:          "feat/some-branch",
		},
		{
			// A transient remote error must not silently retarget the MR.
			name:          "remote lookup error leaves target unchanged",
			currentTarget: "main",
			defaultBranch: "mainline",
			lookupErr:     errors.New("ls-remote: connection reset"),
			want:          "main",
		},
		{
			// Empty default branch (unresolvable rig) → no correction.
			name:          "empty default branch is a no-op",
			currentTarget: "main",
			defaultBranch: "",
			mainExists:    false,
			want:          "main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			branchExists := func(remote, branch string) (bool, error) {
				called = true
				if remote != "origin" || branch != "main" {
					t.Fatalf("unexpected remote query: remote=%q branch=%q (want origin/main)", remote, branch)
				}
				return tt.mainExists, tt.lookupErr
			}

			got := correctPhantomMainTarget(tt.currentTarget, tt.defaultBranch, branchExists)
			if got != tt.want {
				t.Errorf("correctPhantomMainTarget(%q, %q) = %q, want %q",
					tt.currentTarget, tt.defaultBranch, got, tt.want)
			}

			// The remote should only be queried when target=="main" AND the rig
			// default is a non-empty non-main branch — i.e. the only case worth
			// the network round-trip.
			wantCalled := tt.currentTarget == "main" && tt.defaultBranch != "" && tt.defaultBranch != "main"
			if called != wantCalled {
				t.Errorf("branchExists called=%v, want %v", called, wantCalled)
			}
		})
	}
}
