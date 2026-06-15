package cmd

import (
	"testing"
	"time"
)

// TestShouldReapStagedConvoy pins the conservative reap decision for the
// interrupted-mountain-launch reaper (gu-eqv21): complete the launch ONLY for a
// mountain convoy (title prefix "Mountain: ") that is in a staged_* status AND
// older than the staleness window. A non-staged status, a non-mountain title
// (e.g. a deliberately-staged `gt convoy stage` convoy), or a too-young convoy
// (mid-launch) is left untouched — the reaper never auto-launches a convoy a
// human meant to launch manually, and never disturbs a launch in flight.
func TestShouldReapStagedConvoy(t *testing.T) {
	stale := stagedConvoyStaleness + time.Minute
	fresh := stagedConvoyStaleness - time.Minute

	tests := []struct {
		name   string
		status string
		title  string
		age    time.Duration
		want   bool
	}{
		{
			name:   "stale staged_ready mountain -> reap",
			status: convoyStatusStagedReady,
			title:  mountainTitlePrefix + "Grind the epic",
			age:    stale,
			want:   true,
		},
		{
			name:   "stale staged_warnings mountain -> reap",
			status: convoyStatusStagedWarnings,
			title:  mountainTitlePrefix + "Grind the epic",
			age:    stale,
			want:   true,
		},
		{
			name:   "exactly at threshold -> reap",
			status: convoyStatusStagedReady,
			title:  mountainTitlePrefix + "Grind the epic",
			age:    stagedConvoyStaleness,
			want:   true,
		},
		{
			name:   "fresh mountain (mid-launch) -> keep",
			status: convoyStatusStagedReady,
			title:  mountainTitlePrefix + "Grind the epic",
			age:    fresh,
			want:   false,
		},
		{
			name:   "stale but deliberately staged (gt convoy stage) -> keep",
			status: convoyStatusStagedReady,
			title:  "Convoy: manual batch",
			age:    stale,
			want:   false,
		},
		{
			name:   "stale Staged: title -> keep",
			status: convoyStatusStagedReady,
			title:  "Staged: 3 beads across 2 rigs",
			age:    stale,
			want:   false,
		},
		{
			name:   "already open mountain -> keep",
			status: convoyStatusOpen,
			title:  mountainTitlePrefix + "Grind the epic",
			age:    stale,
			want:   false,
		},
		{
			name:   "closed mountain -> keep",
			status: convoyStatusClosed,
			title:  mountainTitlePrefix + "Grind the epic",
			age:    stale,
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldReapStagedConvoy(tt.status, tt.title, tt.age); got != tt.want {
				t.Errorf("shouldReapStagedConvoy(%q, %q, %v) = %v, want %v",
					tt.status, tt.title, tt.age, got, tt.want)
			}
		})
	}
}
