package cmd

import (
	"errors"
	"strings"
	"testing"
)

// gs-nva3: a rebase-onto-base abort must PARK the polecat (deferred, slot
// released) ONLY when the base is an integration branch (base != rig default).
// An abort onto the rig default stays a hard error the polecat must resolve.
func TestIntegrationBranchRebaseParkReason(t *testing.T) {
	rebaseErr := errors.New("CONFLICT (content): merge conflict in analytics/mod.go")

	tests := []struct {
		name              string
		contaminationBase string
		defaultBranch     string
		wantPark          bool
	}{
		{
			name:              "mainline abort hard-fails (no park)",
			contaminationBase: "origin/main",
			defaultBranch:     "main",
			wantPark:          false,
		},
		{
			name:              "non-main rig default abort hard-fails (no park)",
			contaminationBase: "origin/gagecane/gt",
			defaultBranch:     "gagecane/gt",
			wantPark:          false,
		},
		{
			name:              "integration branch abort parks",
			contaminationBase: "origin/gagecane/gt",
			defaultBranch:     "main",
			wantPark:          true,
		},
		{
			name:              "relay base abort parks",
			contaminationBase: "origin/proto/v3-build",
			defaultBranch:     "gagecane/gt",
			wantPark:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := integrationBranchRebaseParkReason(tt.contaminationBase, tt.defaultBranch, rebaseErr)
			if tt.wantPark {
				if got == "" {
					t.Fatalf("expected a park reason for base=%q default=%q, got empty", tt.contaminationBase, tt.defaultBranch)
				}
				if !strings.Contains(got, tt.contaminationBase) {
					t.Errorf("park reason should name the integration base %q, got %q", tt.contaminationBase, got)
				}
			} else if got != "" {
				t.Fatalf("expected no park (hard-fail) for base=%q default=%q, got %q", tt.contaminationBase, tt.defaultBranch, got)
			}
		})
	}
}

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
