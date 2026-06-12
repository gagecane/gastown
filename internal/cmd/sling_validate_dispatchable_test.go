package cmd

import (
	"strings"
	"testing"
)

// TestValidateBeadDispatchable covers the pre-dispatch guard wall extracted from
// runSling (gu-nid89.12.2). Each case asserts a specific guard fires (or that a
// clean bead passes), exercising the validation in isolation from the dispatch
// path.
func TestValidateBeadDispatchable(t *testing.T) {
	// Stub the only external dependency (open-children CLI query) so the
	// validator is hermetic: report no open children unless a case overrides it.
	origHasOpen := hasOpenChildrenFn
	hasOpenChildrenFn = func(string) (bool, error) { return false, nil }
	t.Cleanup(func() { hasOpenChildrenFn = origHasOpen })

	cases := []struct {
		name    string
		beadID  string
		info    *beadInfo
		wantErr string // substring expected in the error; "" means expect nil
	}{
		{
			name:    "clean task passes",
			beadID:  "gu-clean1",
			info:    &beadInfo{Title: "Refactor runSling", Status: "open", IssueType: "task"},
			wantErr: "",
		},
		{
			name:    "closed bead rejected",
			beadID:  "gu-closed1",
			info:    &beadInfo{Title: "Done task", Status: "closed", IssueType: "task"},
			wantErr: "work already completed",
		},
		{
			name:    "tombstone bead rejected",
			beadID:  "gu-tomb1",
			info:    &beadInfo{Title: "Gone task", Status: "tombstone", IssueType: "task"},
			wantErr: "work already completed",
		},
		{
			name:    "container (epic type) rejected",
			beadID:  "gu-epic1",
			info:    &beadInfo{Title: "Some epic", Status: "open", IssueType: "epic"},
			wantErr: "non-work container",
		},
		{
			name:    "refinery workflow-step id rejected",
			beadID:  "gu-wfs-abc",
			info:    &beadInfo{Title: "Workflow step", Status: "open", IssueType: "task"},
			wantErr: "workflow step",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBeadDispatchable(tc.beadID, tc.info)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected nil error, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}
