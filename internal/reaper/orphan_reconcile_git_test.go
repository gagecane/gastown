package reaper

import (
	"errors"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// fakeGitOrphanBeads implements OrphanGitReconcileBeads + GitMergeProof for
// tests. It models the durable-artifact world: a set of labeled source issues,
// a per-issue "is it merged on the target branch" answer, and a per-issue
// "could git even verify" answer (to exercise the fail-closed path).
type fakeGitOrphanBeads struct {
	labeled         []*beads.Issue
	issues          map[string]*beads.Issue
	listErr         error
	showMultipleErr error
	closeErr        map[string]error
	closed          map[string]string
	removed         map[string][]string

	// proof controls ProveMerged per issue ID.
	proven   map[string]bool
	verified map[string]bool // defaults to true when absent
}

func newFakeGitOrphanBeads() *fakeGitOrphanBeads {
	return &fakeGitOrphanBeads{
		issues:   map[string]*beads.Issue{},
		closeErr: map[string]error{},
		closed:   map[string]string{},
		removed:  map[string][]string{},
		proven:   map[string]bool{},
		verified: map[string]bool{},
	}
}

func (f *fakeGitOrphanBeads) addLabeled(issue *beads.Issue, proven, verified bool) {
	f.labeled = append(f.labeled, issue)
	f.issues[issue.ID] = issue
	f.proven[issue.ID] = proven
	f.verified[issue.ID] = verified
}

func (f *fakeGitOrphanBeads) ListIssuesWithLabel(label string) ([]*beads.Issue, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.labeled, nil
}

func (f *fakeGitOrphanBeads) Show(id string) (*beads.Issue, error) {
	if issue, ok := f.issues[id]; ok {
		return issue, nil
	}
	return nil, beads.ErrNotFound
}

// showMultipleErr, when set, forces ShowMultiple to fail (batch-read failure).
func (f *fakeGitOrphanBeads) ShowMultiple(ids []string) (map[string]*beads.Issue, error) {
	if f.showMultipleErr != nil {
		return nil, f.showMultipleErr
	}
	out := make(map[string]*beads.Issue, len(ids))
	for _, id := range ids {
		if issue, ok := f.issues[id]; ok {
			out[id] = issue // missing IDs omitted, mirroring beads.ShowMultiple
		}
	}
	return out, nil
}

func (f *fakeGitOrphanBeads) ForceCloseWithReason(reason string, ids ...string) error {
	for _, id := range ids {
		if err := f.closeErr[id]; err != nil {
			return err
		}
		f.closed[id] = reason
		if issue, ok := f.issues[id]; ok && issue != nil {
			issue.Status = "closed"
		}
	}
	return nil
}

func (f *fakeGitOrphanBeads) Update(id string, opts beads.UpdateOptions) error {
	f.removed[id] = append(f.removed[id], opts.RemoveLabels...)
	return nil
}

func (f *fakeGitOrphanBeads) ProveMerged(issue *beads.Issue) (bool, bool) {
	if issue == nil {
		return false, false
	}
	verified, ok := f.verified[issue.ID]
	if !ok {
		verified = true
	}
	return f.proven[issue.ID], verified
}

func labeledSource(id, status string) *beads.Issue {
	return &beads.Issue{ID: id, Status: status, Labels: []string{awaitingRefineryMergeLabel}}
}

func TestGitReconcile_ClosesProvenMergedOrphan(t *testing.T) {
	f := newFakeGitOrphanBeads()
	// The gu-hrweu signature: MR wisp purged + active_mr scrubbed, but the
	// source issue still carries awaiting_refinery_merge and the work is on the
	// target branch (a commit cites the bead ID).
	f.addLabeled(labeledSource("src-merged", "hooked"), true, true)

	result, err := ReconcileMergedOrphansByGitEvidence(f, f, false)
	if err != nil {
		t.Fatalf("ReconcileMergedOrphansByGitEvidence: %v", err)
	}
	if result.Reconciled != 1 {
		t.Fatalf("Reconciled = %d, want 1 (anomalies=%v)", result.Reconciled, result.Anomalies)
	}
	if _, ok := f.closed["src-merged"]; !ok {
		t.Fatalf("expected src-merged force-closed; closed=%v", f.closed)
	}
	if got := f.removed["src-merged"]; len(got) != 1 || got[0] != awaitingRefineryMergeLabel {
		t.Fatalf("expected awaiting_refinery_merge cleared, got %v", got)
	}
}

func TestGitReconcile_SkipsUnmergedWork(t *testing.T) {
	f := newFakeGitOrphanBeads()
	// Git ran but found NO citing commit — the work has not landed. The label
	// is legitimately still set (refinery just hasn't merged yet). Must not close.
	f.addLabeled(labeledSource("src-pending", "hooked"), false, true)

	result, err := ReconcileMergedOrphansByGitEvidence(f, f, false)
	if err != nil {
		t.Fatalf("ReconcileMergedOrphansByGitEvidence: %v", err)
	}
	if result.Reconciled != 0 {
		t.Fatalf("Reconciled = %d, want 0 (work not merged)", result.Reconciled)
	}
	if result.NotYetMerged != 1 {
		t.Fatalf("NotYetMerged = %d, want 1", result.NotYetMerged)
	}
	if len(f.closed) != 0 {
		t.Fatalf("unmerged work triggered a close: %v", f.closed)
	}
}

func TestGitReconcile_FailsClosedWhenUnverifiable(t *testing.T) {
	f := newFakeGitOrphanBeads()
	// Git could not verify (no worktree / git error). proven=true is meaningless
	// when verified=false: the pass MUST fail closed and leave the bead open.
	f.addLabeled(labeledSource("src-unverifiable", "hooked"), true, false)

	result, err := ReconcileMergedOrphansByGitEvidence(f, f, false)
	if err != nil {
		t.Fatalf("ReconcileMergedOrphansByGitEvidence: %v", err)
	}
	if result.Reconciled != 0 {
		t.Fatalf("Reconciled = %d, want 0 (must fail closed when unverifiable)", result.Reconciled)
	}
	if result.Unverified != 1 {
		t.Fatalf("Unverified = %d, want 1", result.Unverified)
	}
	if len(f.closed) != 0 {
		t.Fatalf("unverifiable bead was closed (fail-closed violation): %v", f.closed)
	}
}

func TestGitReconcile_SkipsAlreadyTerminalSource(t *testing.T) {
	f := newFakeGitOrphanBeads()
	// The label leaked onto an already-closed bead (e.g. the agent-bead
	// reconcile or refinery closed it first). Idempotent — nothing to do, and
	// the git prover must not even be consulted for a terminal bead.
	f.addLabeled(labeledSource("src-closed", "closed"), true, true)

	result, err := ReconcileMergedOrphansByGitEvidence(f, f, false)
	if err != nil {
		t.Fatalf("ReconcileMergedOrphansByGitEvidence: %v", err)
	}
	if result.Reconciled != 0 {
		t.Fatalf("Reconciled = %d, want 0 (source already terminal)", result.Reconciled)
	}
	if result.AlreadyTerminal != 1 {
		t.Fatalf("AlreadyTerminal = %d, want 1", result.AlreadyTerminal)
	}
}

func TestGitReconcile_SkipsTombstoneSource(t *testing.T) {
	f := newFakeGitOrphanBeads()
	f.addLabeled(labeledSource("src-tombstone", "tombstone"), true, true)

	result, err := ReconcileMergedOrphansByGitEvidence(f, f, false)
	if err != nil {
		t.Fatalf("ReconcileMergedOrphansByGitEvidence: %v", err)
	}
	if result.AlreadyTerminal != 1 || result.Reconciled != 0 {
		t.Fatalf("AlreadyTerminal=%d Reconciled=%d, want 1/0", result.AlreadyTerminal, result.Reconciled)
	}
}

func TestGitReconcile_DryRunDoesNotMutate(t *testing.T) {
	f := newFakeGitOrphanBeads()
	f.addLabeled(labeledSource("src-merged", "hooked"), true, true)

	result, err := ReconcileMergedOrphansByGitEvidence(f, f, true)
	if err != nil {
		t.Fatalf("ReconcileMergedOrphansByGitEvidence: %v", err)
	}
	if !result.DryRun {
		t.Fatal("DryRun flag not propagated")
	}
	if result.Reconciled != 1 {
		t.Fatalf("Reconciled = %d, want 1 (dry run still counts)", result.Reconciled)
	}
	if len(f.closed) != 0 || len(f.removed) != 0 {
		t.Fatalf("dry run mutated state: closed=%v removed=%v", f.closed, f.removed)
	}
}

func TestGitReconcile_TolerantOfCloseFailures(t *testing.T) {
	f := newFakeGitOrphanBeads()
	f.addLabeled(labeledSource("src-bad", "hooked"), true, true)
	f.addLabeled(labeledSource("src-good", "hooked"), true, true)
	f.closeErr["src-bad"] = errors.New("dolt explosion")

	result, err := ReconcileMergedOrphansByGitEvidence(f, f, false)
	if err != nil {
		t.Fatalf("ReconcileMergedOrphansByGitEvidence: %v", err)
	}
	if result.Reconciled != 1 {
		t.Fatalf("Reconciled = %d, want 1 (one succeeded)", result.Reconciled)
	}
	if len(result.Anomalies) != 1 || result.Anomalies[0].Type != "orphan_git_source_close_failed" {
		t.Fatalf("Anomalies = %v, want 1 orphan_git_source_close_failed", result.Anomalies)
	}
}

func TestGitReconcile_BatchShowFailureRecordsAnomaly(t *testing.T) {
	f := newFakeGitOrphanBeads()
	f.addLabeled(labeledSource("src-merged", "hooked"), true, true)
	f.showMultipleErr = errors.New("dolt batch read failed")

	result, err := ReconcileMergedOrphansByGitEvidence(f, f, false)
	if err != nil {
		t.Fatalf("ReconcileMergedOrphansByGitEvidence: %v", err)
	}
	if result.Reconciled != 0 {
		t.Fatalf("Reconciled = %d, want 0 (batch read failed)", result.Reconciled)
	}
	if len(result.Anomalies) != 1 || result.Anomalies[0].Type != "orphan_git_source_show_failed" {
		t.Fatalf("Anomalies = %v, want 1 orphan_git_source_show_failed", result.Anomalies)
	}
	if len(f.closed) != 0 {
		t.Fatalf("batch failure triggered a close: %v", f.closed)
	}
}

func TestGitReconcile_ListErrorPropagates(t *testing.T) {
	f := newFakeGitOrphanBeads()
	f.listErr = errors.New("dolt down")
	if _, err := ReconcileMergedOrphansByGitEvidence(f, f, false); err == nil {
		t.Fatal("expected error when ListIssuesWithLabel fails")
	}
}

func TestGitReconcile_NilClientAndProver(t *testing.T) {
	f := newFakeGitOrphanBeads()
	if _, err := ReconcileMergedOrphansByGitEvidence(nil, f, false); err == nil {
		t.Fatal("expected error for nil beads client")
	}
	if _, err := ReconcileMergedOrphansByGitEvidence(f, nil, false); err == nil {
		t.Fatal("expected error for nil git prover")
	}
}

func TestGitReconcile_ScansAllCandidates(t *testing.T) {
	f := newFakeGitOrphanBeads()
	f.addLabeled(labeledSource("src-merged", "hooked"), true, true)
	f.addLabeled(labeledSource("src-pending", "in_progress"), false, true)
	f.addLabeled(labeledSource("src-unverifiable", "hooked"), false, false)

	result, err := ReconcileMergedOrphansByGitEvidence(f, f, false)
	if err != nil {
		t.Fatalf("ReconcileMergedOrphansByGitEvidence: %v", err)
	}
	if result.Scanned != 3 {
		t.Fatalf("Scanned = %d, want 3", result.Scanned)
	}
	if result.Reconciled != 1 || result.NotYetMerged != 1 || result.Unverified != 1 {
		t.Fatalf("Reconciled=%d NotYetMerged=%d Unverified=%d, want 1/1/1",
			result.Reconciled, result.NotYetMerged, result.Unverified)
	}
}
