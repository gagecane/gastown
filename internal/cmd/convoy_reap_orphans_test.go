package cmd

import (
	"testing"
)

// TestWorkflowDriversByRoot verifies the driver extraction for the gu-guwpn
// orphan reaper: roots that record a driver_issue are mapped; legacy roots with
// no field are omitted (they cannot be safely reaped); driver IDs are
// deduplicated for the batched status lookup.
func TestWorkflowDriversByRoot(t *testing.T) {
	roots := []convoyListIssue{
		{ID: "hq-wf-aaa", Description: "driver_issue: ticd-5te\nWorkflow: mol-review-leg"},
		{ID: "hq-wf-bbb", Description: "driver_issue: ticd-ofp\nWorkflow: mol-review-leg"},
		{ID: "hq-wf-ccc", Description: "driver_issue: ticd-5te\nWorkflow: mol-review-leg"}, // dup driver
		{ID: "hq-wf-ddd", Description: "Workflow: mol-review-leg\nRig: talon_cdk"},         // legacy, no field
	}

	byRoot, driverIDs := workflowDriversByRoot(roots)

	if byRoot["hq-wf-aaa"] != "ticd-5te" || byRoot["hq-wf-bbb"] != "ticd-ofp" || byRoot["hq-wf-ccc"] != "ticd-5te" {
		t.Fatalf("driver mapping wrong: %v", byRoot)
	}
	if _, ok := byRoot["hq-wf-ddd"]; ok {
		t.Fatalf("legacy root with no driver_issue must be omitted, got %v", byRoot)
	}
	// driverIDs deduplicated: ticd-5te appears once though two roots reference it.
	if len(driverIDs) != 2 {
		t.Fatalf("expected 2 deduplicated driver IDs, got %d: %v", len(driverIDs), driverIDs)
	}
	seen := map[string]bool{}
	for _, id := range driverIDs {
		seen[id] = true
	}
	if !seen["ticd-5te"] || !seen["ticd-ofp"] {
		t.Fatalf("missing expected driver IDs: %v", driverIDs)
	}
}

// TestShouldReapWorkflow pins the conservative reap decision (gu-guwpn): reap
// ONLY when the driver's status is known AND terminal. Unknown status (driver
// unresolvable or not yet visible) and non-terminal status both leave the chain
// untouched — the reaper never acts on ambiguity, so it cannot close a chain
// whose driver is still live or merely unreachable.
func TestShouldReapWorkflow(t *testing.T) {
	tests := []struct {
		name   string
		driver string
		status map[string]string
		want   bool
	}{
		{
			name:   "driver closed -> reap",
			driver: "ticd-5te",
			status: map[string]string{"ticd-5te": "closed"},
			want:   true,
		},
		{
			name:   "driver tombstone -> reap",
			driver: "ticd-5te",
			status: map[string]string{"ticd-5te": "tombstone"},
			want:   true,
		},
		{
			name:   "driver open -> keep",
			driver: "ticd-5te",
			status: map[string]string{"ticd-5te": "open"},
			want:   false,
		},
		{
			name:   "driver in_progress -> keep",
			driver: "ticd-5te",
			status: map[string]string{"ticd-5te": "in_progress"},
			want:   false,
		},
		{
			name:   "driver hooked -> keep",
			driver: "ticd-5te",
			status: map[string]string{"ticd-5te": "hooked"},
			want:   false,
		},
		{
			name:   "driver status unknown (unresolvable) -> keep",
			driver: "ticd-5te",
			status: map[string]string{},
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldReapWorkflow(tt.driver, tt.status); got != tt.want {
				t.Errorf("shouldReapWorkflow(%q, %v) = %v, want %v", tt.driver, tt.status, got, tt.want)
			}
		})
	}
}
