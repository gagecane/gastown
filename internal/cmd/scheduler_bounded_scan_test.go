package cmd

import (
	"testing"
	"time"
)

// TestRunBoundedScanCompletes verifies that a scan finishing within the timeout
// returns its result with complete=true.
func TestRunBoundedScanCompletes(t *testing.T) {
	want := []scheduledBeadInfo{{ID: "gu-1", Title: "ready"}}
	got, complete := runBoundedScan(time.Second, func() []scheduledBeadInfo {
		return want
	})
	if !complete {
		t.Fatalf("expected complete=true for fast scan")
	}
	if len(got) != 1 || got[0].ID != "gu-1" {
		t.Fatalf("unexpected scan result: %+v", got)
	}
}

// TestRunBoundedScanTimesOut verifies that a scan exceeding the timeout returns
// (nil, false) instead of blocking — the behavior that prevents
// `gt scheduler status` from hanging to exit 124 (gu-nhgev).
func TestRunBoundedScanTimesOut(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	got, complete := runBoundedScan(10*time.Millisecond, func() []scheduledBeadInfo {
		<-release // block well past the timeout
		return []scheduledBeadInfo{{ID: "gu-slow"}}
	})
	if complete {
		t.Fatalf("expected complete=false for scan exceeding timeout")
	}
	if got != nil {
		t.Fatalf("expected nil beads on timeout, got %+v", got)
	}
}
