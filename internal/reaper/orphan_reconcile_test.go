package reaper

import (
	"errors"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// fakeOrphanBeads implements OrphanReconcileBeads for tests. It tracks
// ForceCloseWithReason and Update calls so we can assert the reconcile closed
// (or did not close) each orphaned source issue and cleared its label.
type fakeOrphanBeads struct {
	agents    map[string]*beads.Issue
	issues    map[string]*beads.Issue
	listErr   error
	closeErrs map[string]error    // source-issue ID -> error
	closed    map[string]string   // source-issue ID -> close reason
	removed   map[string][]string // source-issue ID -> removed labels
}

func newFakeOrphanBeads() *fakeOrphanBeads {
	return &fakeOrphanBeads{
		agents:    map[string]*beads.Issue{},
		issues:    map[string]*beads.Issue{},
		closeErrs: map[string]error{},
		closed:    map[string]string{},
		removed:   map[string][]string{},
	}
}

func (f *fakeOrphanBeads) ListAgentBeads() (map[string]*beads.Issue, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.agents, nil
}

func (f *fakeOrphanBeads) Show(id string) (*beads.Issue, error) {
	if issue, ok := f.issues[id]; ok {
		return issue, nil
	}
	if issue, ok := f.agents[id]; ok {
		return issue, nil
	}
	return nil, beads.ErrNotFound
}

func (f *fakeOrphanBeads) ForceCloseWithReason(reason string, ids ...string) error {
	for _, id := range ids {
		if err := f.closeErrs[id]; err != nil {
			return err
		}
		f.closed[id] = reason
		// Reflect the close so subsequent reads see a terminal status.
		if issue, ok := f.issues[id]; ok && issue != nil {
			issue.Status = "closed"
		}
	}
	return nil
}

func (f *fakeOrphanBeads) Update(id string, opts beads.UpdateOptions) error {
	f.removed[id] = append(f.removed[id], opts.RemoveLabels...)
	return nil
}

// makeMergedMR builds an MR bead Issue that is closed with close_reason=merged
// and a source_issue field, mirroring what the refinery writes.
func makeMergedMR(id, sourceIssue string) *beads.Issue {
	return &beads.Issue{
		ID:     id,
		Title:  "MR " + id,
		Type:   "merge-request",
		Status: "closed",
		Description: "branch: polecat/test/" + sourceIssue + "\n" +
			"source_issue: " + sourceIssue + "\n" +
			"close_reason: merged\n" +
			"merge_commit: abc123\n",
	}
}

// makeRejectedMR builds a closed MR bead with close_reason=rejected (no merge).
func makeRejectedMR(id, sourceIssue string) *beads.Issue {
	return &beads.Issue{
		ID:     id,
		Title:  "MR " + id,
		Type:   "merge-request",
		Status: "closed",
		Description: "branch: polecat/test/" + sourceIssue + "\n" +
			"source_issue: " + sourceIssue + "\n" +
			"close_reason: rejected\n",
	}
}

func TestReconcileMergedOrphans_ClosesOrphanedSource(t *testing.T) {
	f := newFakeOrphanBeads()
	// The gu-7igu8 signature: merged MR, but source issue still HOOKED on the
	// (now dead) polecat because the refinery's reconcile was interrupted.
	f.agents["polecat-1"] = makeAgentBead("polecat-1", "mr-merged", "src-hooked", "clean")
	f.issues["mr-merged"] = makeMergedMR("mr-merged", "src-hooked")
	f.issues["src-hooked"] = &beads.Issue{ID: "src-hooked", Status: "hooked"}

	result, err := ReconcileMergedOrphans(f, false)
	if err != nil {
		t.Fatalf("ReconcileMergedOrphans: %v", err)
	}
	if result.Reconciled != 1 {
		t.Fatalf("Reconciled = %d, want 1 (entries=%v anomalies=%v)", result.Reconciled, result.ReconciledEntries, result.Anomalies)
	}
	if _, ok := f.closed["src-hooked"]; !ok {
		t.Fatalf("expected src-hooked to be force-closed; closed=%v", f.closed)
	}
	if got := f.removed["src-hooked"]; len(got) != 1 || got[0] != awaitingRefineryMergeLabel {
		t.Fatalf("expected awaiting_refinery_merge label cleared on src-hooked, got %v", got)
	}
}

func TestReconcileMergedOrphans_SkipsRejectedMR(t *testing.T) {
	f := newFakeOrphanBeads()
	// MR was rejected — the work did NOT land. Closing the source issue would
	// lose the work, so the reconcile must skip it.
	f.agents["polecat-1"] = makeAgentBead("polecat-1", "mr-rejected", "src-open", "clean")
	f.issues["mr-rejected"] = makeRejectedMR("mr-rejected", "src-open")
	f.issues["src-open"] = &beads.Issue{ID: "src-open", Status: "open"}

	result, err := ReconcileMergedOrphans(f, false)
	if err != nil {
		t.Fatalf("ReconcileMergedOrphans: %v", err)
	}
	if result.Reconciled != 0 {
		t.Fatalf("Reconciled = %d, want 0 (rejected MR must not close source)", result.Reconciled)
	}
	if len(f.closed) != 0 {
		t.Fatalf("rejected MR triggered a source close: %v", f.closed)
	}
}

func TestReconcileMergedOrphans_SkipsMissingMR(t *testing.T) {
	f := newFakeOrphanBeads()
	// MR bead is missing (reaped). Absence is not proof the work landed, so
	// the reconcile must NOT close the source issue.
	f.agents["polecat-1"] = makeAgentBead("polecat-1", "mr-gone", "src-open", "clean")
	// mr-gone intentionally absent → ErrNotFound
	f.issues["src-open"] = &beads.Issue{ID: "src-open", Status: "open"}

	result, err := ReconcileMergedOrphans(f, false)
	if err != nil {
		t.Fatalf("ReconcileMergedOrphans: %v", err)
	}
	if result.Reconciled != 0 {
		t.Fatalf("Reconciled = %d, want 0 (missing MR is not merge proof)", result.Reconciled)
	}
	if len(f.closed) != 0 {
		t.Fatalf("missing MR triggered a source close: %v", f.closed)
	}
}

func TestReconcileMergedOrphans_SkipsAlreadyClosedSource(t *testing.T) {
	f := newFakeOrphanBeads()
	// Happy path already completed: merged MR AND closed source. Idempotent —
	// nothing to reconcile.
	f.agents["polecat-1"] = makeAgentBead("polecat-1", "mr-merged", "src-closed", "clean")
	f.issues["mr-merged"] = makeMergedMR("mr-merged", "src-closed")
	f.issues["src-closed"] = &beads.Issue{ID: "src-closed", Status: "closed"}

	result, err := ReconcileMergedOrphans(f, false)
	if err != nil {
		t.Fatalf("ReconcileMergedOrphans: %v", err)
	}
	if result.Reconciled != 0 {
		t.Fatalf("Reconciled = %d, want 0 (source already terminal)", result.Reconciled)
	}
}

func TestReconcileMergedOrphans_PreservesHumanWIP(t *testing.T) {
	for _, cleanup := range []string{"has_uncommitted", "has_stash", "has_unpushed"} {
		t.Run(cleanup, func(t *testing.T) {
			f := newFakeOrphanBeads()
			f.agents["wip-polecat"] = makeAgentBead("wip-polecat", "mr-merged", "src-hooked", cleanup)
			f.issues["mr-merged"] = makeMergedMR("mr-merged", "src-hooked")
			f.issues["src-hooked"] = &beads.Issue{ID: "src-hooked", Status: "hooked"}

			result, err := ReconcileMergedOrphans(f, false)
			if err != nil {
				t.Fatalf("ReconcileMergedOrphans: %v", err)
			}
			if result.Reconciled != 0 {
				t.Fatalf("Reconciled = %d, want 0 (WIP polecat preserved)", result.Reconciled)
			}
			if result.PreservedWIP != 1 {
				t.Fatalf("PreservedWIP = %d, want 1", result.PreservedWIP)
			}
			if len(f.closed) != 0 {
				t.Fatalf("WIP polecat triggered a source close (gc-eysed violation): %v", f.closed)
			}
		})
	}
}

func TestReconcileMergedOrphans_DryRunDoesNotMutate(t *testing.T) {
	f := newFakeOrphanBeads()
	f.agents["polecat-1"] = makeAgentBead("polecat-1", "mr-merged", "src-hooked", "clean")
	f.issues["mr-merged"] = makeMergedMR("mr-merged", "src-hooked")
	f.issues["src-hooked"] = &beads.Issue{ID: "src-hooked", Status: "hooked"}

	result, err := ReconcileMergedOrphans(f, true)
	if err != nil {
		t.Fatalf("ReconcileMergedOrphans: %v", err)
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

func TestReconcileMergedOrphans_TolerantOfCloseFailures(t *testing.T) {
	f := newFakeOrphanBeads()
	f.agents["bead-bad"] = makeAgentBead("bead-bad", "mr-1", "src-bad", "clean")
	f.agents["bead-good"] = makeAgentBead("bead-good", "mr-2", "src-good", "clean")
	f.issues["mr-1"] = makeMergedMR("mr-1", "src-bad")
	f.issues["mr-2"] = makeMergedMR("mr-2", "src-good")
	f.issues["src-bad"] = &beads.Issue{ID: "src-bad", Status: "hooked"}
	f.issues["src-good"] = &beads.Issue{ID: "src-good", Status: "hooked"}
	f.closeErrs["src-bad"] = errors.New("dolt explosion")

	result, err := ReconcileMergedOrphans(f, false)
	if err != nil {
		t.Fatalf("ReconcileMergedOrphans: %v", err)
	}
	if result.Reconciled != 1 {
		t.Fatalf("Reconciled = %d, want 1 (one succeeded)", result.Reconciled)
	}
	if len(result.Anomalies) != 1 || result.Anomalies[0].Type != "orphan_source_close_failed" {
		t.Fatalf("Anomalies = %v, want 1 orphan_source_close_failed", result.Anomalies)
	}
}

func TestReconcileMergedOrphans_ListErrorPropagates(t *testing.T) {
	f := newFakeOrphanBeads()
	f.listErr = errors.New("dolt down")
	if _, err := ReconcileMergedOrphans(f, false); err == nil {
		t.Fatal("expected error when ListAgentBeads fails")
	}
}

func TestReconcileMergedOrphans_NilClient(t *testing.T) {
	if _, err := ReconcileMergedOrphans(nil, false); err == nil {
		t.Fatal("expected error for nil client")
	}
}

func TestReconcileMergedOrphans_SkipsBeadsWithoutActiveMR(t *testing.T) {
	f := newFakeOrphanBeads()
	f.agents["no-mr"] = makeAgentBead("no-mr", "", "", "clean")
	f.agents["mr-set"] = makeAgentBead("mr-set", "mr-merged", "src-hooked", "clean")
	f.issues["mr-merged"] = makeMergedMR("mr-merged", "src-hooked")
	f.issues["src-hooked"] = &beads.Issue{ID: "src-hooked", Status: "hooked"}

	result, err := ReconcileMergedOrphans(f, false)
	if err != nil {
		t.Fatalf("ReconcileMergedOrphans: %v", err)
	}
	if result.Scanned != 2 {
		t.Fatalf("Scanned = %d, want 2", result.Scanned)
	}
	if result.HadActiveMR != 1 {
		t.Fatalf("HadActiveMR = %d, want 1", result.HadActiveMR)
	}
	if result.Reconciled != 1 {
		t.Fatalf("Reconciled = %d, want 1", result.Reconciled)
	}
}
