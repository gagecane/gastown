package cmd

import "testing"

func TestDoneContaminationBaseRef(t *testing.T) {
	tests := []struct {
		name           string
		defaultBranch  string
		explicitTarget string
		want           string
	}{
		{
			name:           "defaults to rig branch",
			defaultBranch:  "main",
			explicitTarget: "",
			want:           "origin/main",
		},
		{
			name:           "uses explicit target branch",
			defaultBranch:  "main",
			explicitTarget: "upstream-rebuild-main",
			want:           "origin/upstream-rebuild-main",
		},
		{
			name:           "avoids double origin prefix",
			defaultBranch:  "main",
			explicitTarget: "origin/upstream-rebuild-main",
			want:           "origin/upstream-rebuild-main",
		},
		{
			// gs-xbo: a relay leg's base (resolved via effectiveBaseBranch) must
			// drive the rebase/contamination base, NOT the rig default. Here the
			// rig default is gagecane/gt but the relay base is proto/v3-build —
			// the auto-rebase must target origin/proto/v3-build or it aborts and
			// strands the polecat in stuck-in-done limbo.
			name:           "relay base overrides non-main rig default",
			defaultBranch:  "gagecane/gt",
			explicitTarget: "proto/v3-build",
			want:           "origin/proto/v3-build",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := doneContaminationBaseRef(tt.defaultBranch, tt.explicitTarget)
			if got != tt.want {
				t.Fatalf("doneContaminationBaseRef(%q, %q) = %q, want %q", tt.defaultBranch, tt.explicitTarget, got, tt.want)
			}
		})
	}
}
