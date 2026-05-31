package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// fakeMRLister is a deterministic stub for verifyRefineryQueueScan tests.
type fakeMRLister struct {
	issues []*beads.Issue
	err    error
	calls  int
}

func (f *fakeMRLister) ListMergeRequests(opts beads.ListOptions) ([]*beads.Issue, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.issues, nil
}

// mrIssue builds a merge-request *beads.Issue with the rig field embedded
// in the description so beads.ParseMRFields() can recover it. The format
// matches what `gt mq submit` writes — keeping the fixture aligned with
// production guarantees ParseMRFields actually exercises the path under
// test.
func mrIssue(id, rig, branch, status string) *beads.Issue {
	return &beads.Issue{
		ID:     id,
		Status: status,
		Description: "branch: " + branch + "\n" +
			"rig: " + rig + "\n" +
			"source_issue: " + id + "-src\n",
	}
}

func TestVerifyRefineryQueueScan_NoQueueScanInSteps_Skips(t *testing.T) {
	lister := &fakeMRLister{
		issues: []*beads.Issue{mrIssue("gu-mr-1", "myrig", "polecat/foo", "open")},
	}
	// queue-scan absent — verifier has nothing to assert against.
	err := verifyRefineryQueueScan("myrig", "inbox-check:OK,merge-push:SKIP", lister)
	if err != nil {
		t.Fatalf("expected nil (no queue-scan in audit), got %v", err)
	}
	if lister.calls != 0 {
		t.Errorf("expected 0 list calls when queue-scan not reported, got %d", lister.calls)
	}
}

func TestVerifyRefineryQueueScan_QueueScanOK_Skips(t *testing.T) {
	lister := &fakeMRLister{
		issues: []*beads.Issue{mrIssue("gu-mr-1", "myrig", "polecat/foo", "open")},
	}
	// Agent claims it scanned the queue — we trust OK and don't second-guess.
	err := verifyRefineryQueueScan("myrig", "queue-scan:OK", lister)
	if err != nil {
		t.Fatalf("expected nil (queue-scan OK is trusted), got %v", err)
	}
	if lister.calls != 0 {
		t.Errorf("expected 0 list calls when queue-scan:OK, got %d", lister.calls)
	}
}

func TestVerifyRefineryQueueScan_EmptyAndQueueEmpty_Pass(t *testing.T) {
	lister := &fakeMRLister{issues: nil}
	err := verifyRefineryQueueScan("myrig", "queue-scan:EMPTY", lister)
	if err != nil {
		t.Fatalf("expected nil when queue truly empty, got %v", err)
	}
	if lister.calls != 1 {
		t.Errorf("expected 1 list call to verify empty claim, got %d", lister.calls)
	}
}

func TestVerifyRefineryQueueScan_EmptyButMRsPending_Fails(t *testing.T) {
	// Reproduces gu-6hzv: agent reports EMPTY, queue actually has work.
	lister := &fakeMRLister{
		issues: []*beads.Issue{
			mrIssue("gu-mr-1", "myrig", "polecat/a", "open"),
			mrIssue("gu-mr-2", "myrig", "polecat/b", "open"),
		},
	}
	err := verifyRefineryQueueScan("myrig", "queue-scan:EMPTY", lister)
	if err == nil {
		t.Fatal("expected error when queue-scan:EMPTY but MRs exist")
	}
	if !strings.Contains(err.Error(), queueScanVerifyMessage) {
		t.Errorf("error should carry the canonical prefix %q; got %q",
			queueScanVerifyMessage, err.Error())
	}
	// The missed MR IDs MUST surface in the error. Without them the
	// agent has no way to recover and would re-report EMPTY next cycle
	// (the 14h stall in gu-weki was diagnosed by listing MR IDs).
	if !strings.Contains(err.Error(), "gu-mr-1") || !strings.Contains(err.Error(), "gu-mr-2") {
		t.Errorf("error should name missed MR IDs, got %q", err.Error())
	}
	// The error should remind the agent of the right command. Otherwise
	// the failure tells them WHAT broke but not HOW to fix it.
	if !strings.Contains(err.Error(), "gt mq list myrig") {
		t.Errorf("error should mention the canonical `gt mq list <rig>` command, got %q", err.Error())
	}
}

func TestVerifyRefineryQueueScan_EmptyButOnlyOtherRigMRs_Pass(t *testing.T) {
	// Wisps are shared across the Dolt server. A refinery for rigA must
	// not be blocked because rigB has pending MRs.
	lister := &fakeMRLister{
		issues: []*beads.Issue{
			mrIssue("gu-mr-1", "rigB", "polecat/a", "open"),
			mrIssue("gu-mr-2", "rigB", "polecat/b", "open"),
		},
	}
	err := verifyRefineryQueueScan("rigA", "queue-scan:EMPTY", lister)
	if err != nil {
		t.Fatalf("rigA should not be blocked by rigB MRs, got %v", err)
	}
}

func TestVerifyRefineryQueueScan_EmptyButOnlyClosedMRs_Pass(t *testing.T) {
	// Closed MRs are processed work; they should not trip the gate.
	lister := &fakeMRLister{
		issues: []*beads.Issue{
			mrIssue("gu-mr-1", "myrig", "polecat/a", "closed"),
		},
	}
	err := verifyRefineryQueueScan("myrig", "queue-scan:EMPTY", lister)
	if err != nil {
		t.Fatalf("closed MRs should not trigger the gate, got %v", err)
	}
}

func TestVerifyRefineryQueueScan_EmptyButOnlyBlockedMRs_Pass(t *testing.T) {
	// Blocked MRs aren't processable this cycle. Reporting EMPTY when only
	// blocked MRs exist is correct (queue-scan output excludes blocked
	// items from "ready" anyway), so the gate must not fire.
	issue := mrIssue("gu-mr-1", "myrig", "polecat/a", "open")
	issue.BlockedByCount = 1
	lister := &fakeMRLister{issues: []*beads.Issue{issue}}
	err := verifyRefineryQueueScan("myrig", "queue-scan:EMPTY", lister)
	if err != nil {
		t.Fatalf("blocked MRs should not trigger the gate, got %v", err)
	}
}

func TestVerifyRefineryQueueScan_ListErr_FailsOpen(t *testing.T) {
	// Transient bd failures must NOT block the patrol from closing —
	// otherwise a flaky Dolt connection would wedge the refinery worse
	// than the original bug. Re-listing happens next cycle anyway.
	lister := &fakeMRLister{err: errors.New("dolt connection refused")}
	err := verifyRefineryQueueScan("myrig", "queue-scan:EMPTY", lister)
	if err != nil {
		t.Fatalf("verifier must fail open on list error, got %v", err)
	}
}

func TestVerifyRefineryQueueScan_NoLister_NoOp(t *testing.T) {
	// Defensive: if wiring drops the lister (e.g. rig lookup fails), the
	// gate must degrade silently rather than panic.
	if err := verifyRefineryQueueScan("myrig", "queue-scan:EMPTY", nil); err != nil {
		t.Fatalf("nil lister must be a no-op, got %v", err)
	}
}

func TestVerifyRefineryQueueScan_EmptyRigName_NoOp(t *testing.T) {
	// Refinery without a rig (shouldn't happen in practice) must not
	// trigger a cross-rig false positive.
	lister := &fakeMRLister{
		issues: []*beads.Issue{mrIssue("gu-mr-1", "myrig", "polecat/a", "open")},
	}
	if err := verifyRefineryQueueScan("", "queue-scan:EMPTY", lister); err != nil {
		t.Fatalf("empty rig must be a no-op, got %v", err)
	}
}

func TestVerifyRefineryQueueScan_RigCaseInsensitive(t *testing.T) {
	// MRFields.Rig comparison is case-insensitive in mq_list.go; the
	// gate must match that contract or report-time and list-time would
	// disagree.
	lister := &fakeMRLister{
		issues: []*beads.Issue{mrIssue("gu-mr-1", "MyRig", "polecat/a", "open")},
	}
	err := verifyRefineryQueueScan("myrig", "queue-scan:EMPTY", lister)
	if err == nil {
		t.Fatal("expected error: rig comparison should be case-insensitive to match `gt mq list`")
	}
}

func TestVerifyRefineryQueueScan_UnscopedMR_CountedAsHit(t *testing.T) {
	// An MR with no rig metadata could belong to anyone. Surface it as
	// a hit so the wrong-label bug is caught even when older MRs lack
	// the rig field. Better a noisy false positive than a silent miss.
	issue := &beads.Issue{
		ID:          "gu-mr-legacy",
		Status:      "open",
		Description: "branch: polecat/foo\nsource_issue: gu-mr-legacy-src\n",
	}
	lister := &fakeMRLister{issues: []*beads.Issue{issue}}
	err := verifyRefineryQueueScan("myrig", "queue-scan:EMPTY", lister)
	if err == nil {
		t.Fatal("expected error: unscoped MR should be counted as a potential hit")
	}
	if !strings.Contains(err.Error(), "gu-mr-legacy") {
		t.Errorf("unscoped MR ID should be in error, got %q", err.Error())
	}
}
