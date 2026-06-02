package reaper

import (
	"errors"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// fakeFKScrubBeads implements DanglingFKScrubBeads for tests. It tracks
// UpdateAgentDescriptionFields calls so we can assert which FK fields the
// scrubber cleared (or left alone) on each agent bead.
type fakeFKScrubBeads struct {
	agents     map[string]*beads.Issue
	issues     map[string]*beads.Issue
	listErr    error
	updateErrs map[string]error
	updates    map[string]beads.AgentFieldUpdates // id -> updates applied
}

func newFakeFKScrubBeads() *fakeFKScrubBeads {
	return &fakeFKScrubBeads{
		agents:     map[string]*beads.Issue{},
		issues:     map[string]*beads.Issue{},
		updateErrs: map[string]error{},
		updates:    map[string]beads.AgentFieldUpdates{},
	}
}

func (f *fakeFKScrubBeads) ListAgentBeads() (map[string]*beads.Issue, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.agents, nil
}

func (f *fakeFKScrubBeads) Show(id string) (*beads.Issue, error) {
	if issue, ok := f.issues[id]; ok {
		return issue, nil
	}
	if issue, ok := f.agents[id]; ok {
		return issue, nil
	}
	return nil, beads.ErrNotFound
}

func (f *fakeFKScrubBeads) UpdateAgentDescriptionFields(id string, updates beads.AgentFieldUpdates) error {
	if err := f.updateErrs[id]; err != nil {
		return err
	}
	f.updates[id] = updates
	// Reflect the change back into agents so subsequent reads see it.
	if issue, ok := f.agents[id]; ok && issue != nil {
		fields := beads.ParseAgentFields(issue.Description)
		if fields == nil {
			fields = &beads.AgentFields{}
		}
		if updates.MRID != nil {
			fields.MRID = *updates.MRID
		}
		if updates.HookBead != nil {
			fields.HookBead = *updates.HookBead
		}
		issue.Description = beads.FormatAgentDescription(issue.Title, fields)
	}
	return nil
}

// makeFKAgentBead builds an agent bead Issue with the given mr_id, hook_bead,
// and cleanup_status fields populated.
func makeFKAgentBead(id, mrID, hookBead, cleanup string) *beads.Issue {
	fields := &beads.AgentFields{
		RoleType:      "polecat",
		AgentState:    "idle",
		MRID:          mrID,
		HookBead:      hookBead,
		CleanupStatus: cleanup,
	}
	return &beads.Issue{
		ID:          id,
		Title:       "agent " + id,
		Type:        "agent",
		Status:      "open",
		Description: beads.FormatAgentDescription("agent "+id, fields),
	}
}

func TestScrubDanglingFKRefs_ClearsMissingReferents(t *testing.T) {
	f := newFakeFKScrubBeads()
	// Both mr_id and hook_bead point at wisps that have been reaped (absent →
	// ErrNotFound). Both should be cleared.
	f.agents["polecat-1"] = makeFKAgentBead("polecat-1", "wisp-reaped-mr", "wisp-reaped-hook", "clean")

	result, err := ScrubDanglingFKRefs(f, false)
	if err != nil {
		t.Fatalf("ScrubDanglingFKRefs returned error: %v", err)
	}
	if result.ClearedMRID != 1 {
		t.Fatalf("ClearedMRID = %d, want 1", result.ClearedMRID)
	}
	if result.ClearedHookBead != 1 {
		t.Fatalf("ClearedHookBead = %d, want 1", result.ClearedHookBead)
	}
	upd := f.updates["polecat-1"]
	if upd.MRID == nil || *upd.MRID != "" {
		t.Fatalf("mr_id not cleared: %+v", upd.MRID)
	}
	if upd.HookBead == nil || *upd.HookBead != "" {
		t.Fatalf("hook_bead not cleared: %+v", upd.HookBead)
	}
}

func TestScrubDanglingFKRefs_PreservesPresentReferents(t *testing.T) {
	f := newFakeFKScrubBeads()
	// Referents still exist (any status) → leave untouched. A present referent
	// may still matter to a live agent.
	f.agents["polecat-live"] = makeFKAgentBead("polecat-live", "wisp-open-mr", "src-open-hook", "clean")
	f.issues["wisp-open-mr"] = &beads.Issue{ID: "wisp-open-mr", Status: "open"}
	f.issues["src-open-hook"] = &beads.Issue{ID: "src-open-hook", Status: "in_progress"}

	result, err := ScrubDanglingFKRefs(f, false)
	if err != nil {
		t.Fatalf("ScrubDanglingFKRefs returned error: %v", err)
	}
	if result.ClearedMRID != 0 || result.ClearedHookBead != 0 {
		t.Fatalf("cleared present referents: mr=%d hook=%d", result.ClearedMRID, result.ClearedHookBead)
	}
	if _, updated := f.updates["polecat-live"]; updated {
		t.Fatal("UpdateAgentDescriptionFields called for a bead with present referents")
	}
}

func TestScrubDanglingFKRefs_PreservesClosedButPresentReferent(t *testing.T) {
	f := newFakeFKScrubBeads()
	// Referent is closed but still present (not yet purged). Existence-only
	// semantics: a present referent is left untouched regardless of status.
	f.agents["polecat-closed"] = makeFKAgentBead("polecat-closed", "wisp-closed", "", "clean")
	f.issues["wisp-closed"] = &beads.Issue{ID: "wisp-closed", Status: "closed"}

	result, err := ScrubDanglingFKRefs(f, false)
	if err != nil {
		t.Fatalf("ScrubDanglingFKRefs returned error: %v", err)
	}
	if result.ClearedMRID != 0 {
		t.Fatalf("ClearedMRID = %d, want 0 (present closed referent must be left alone)", result.ClearedMRID)
	}
}

func TestScrubDanglingFKRefs_PreservesHumanWIP(t *testing.T) {
	cases := []string{"has_uncommitted", "has_stash", "has_unpushed"}
	for _, cleanup := range cases {
		t.Run(cleanup, func(t *testing.T) {
			f := newFakeFKScrubBeads()
			// mr_id + hook_bead both dangling, but cleanup_status marks human
			// WIP → leave both refs intact as audit trail (gc-eysed).
			f.agents["wip-polecat"] = makeFKAgentBead("wip-polecat", "wisp-gone-mr", "wisp-gone-hook", cleanup)

			result, err := ScrubDanglingFKRefs(f, false)
			if err != nil {
				t.Fatalf("ScrubDanglingFKRefs returned error: %v", err)
			}
			if result.ClearedMRID != 0 || result.ClearedHookBead != 0 {
				t.Fatalf("WIP polecat scrubbed: mr=%d hook=%d", result.ClearedMRID, result.ClearedHookBead)
			}
			if result.PreservedWIP != 1 {
				t.Fatalf("PreservedWIP = %d, want 1", result.PreservedWIP)
			}
			if _, updated := f.updates["wip-polecat"]; updated {
				t.Fatal("UpdateAgentDescriptionFields called on a WIP polecat (gc-eysed violation)")
			}
		})
	}
}

func TestScrubDanglingFKRefs_ClearsOnlyDanglingField(t *testing.T) {
	f := newFakeFKScrubBeads()
	// mr_id dangling, hook_bead still present → clear only mr_id.
	f.agents["polecat-mixed"] = makeFKAgentBead("polecat-mixed", "wisp-gone", "src-present", "clean")
	f.issues["src-present"] = &beads.Issue{ID: "src-present", Status: "open"}

	result, err := ScrubDanglingFKRefs(f, false)
	if err != nil {
		t.Fatalf("ScrubDanglingFKRefs returned error: %v", err)
	}
	if result.ClearedMRID != 1 {
		t.Fatalf("ClearedMRID = %d, want 1", result.ClearedMRID)
	}
	if result.ClearedHookBead != 0 {
		t.Fatalf("ClearedHookBead = %d, want 0 (present hook must stay)", result.ClearedHookBead)
	}
	upd := f.updates["polecat-mixed"]
	if upd.MRID == nil || *upd.MRID != "" {
		t.Fatal("mr_id not cleared")
	}
	if upd.HookBead != nil {
		t.Fatal("hook_bead should not have been touched")
	}
}

func TestScrubDanglingFKRefs_FailClosedOnLookupError(t *testing.T) {
	// A lookup error other than ErrNotFound must NOT trigger a clear. We model
	// it by having Show return a generic error for the referent. fakeFKScrubBeads
	// only returns ErrNotFound, so use a custom reader that errors.
	erroring := &erroringFKBeads{
		fakeFKScrubBeads: newFakeFKScrubBeads(),
		showErr:          errors.New("dolt connection reset"),
	}
	erroring.agents["polecat-flaky"] = makeFKAgentBead("polecat-flaky", "wisp-uncertain", "", "clean")

	result, err := ScrubDanglingFKRefs(erroring, false)
	if err != nil {
		t.Fatalf("ScrubDanglingFKRefs returned error: %v", err)
	}
	if result.ClearedMRID != 0 {
		t.Fatalf("ClearedMRID = %d, want 0 (lookup error must fail closed)", result.ClearedMRID)
	}
}

// erroringFKBeads overrides Show to return a non-NotFound error, modeling a
// transient Dolt failure.
type erroringFKBeads struct {
	*fakeFKScrubBeads
	showErr error
}

func (e *erroringFKBeads) Show(id string) (*beads.Issue, error) {
	return nil, e.showErr
}

func TestScrubDanglingFKRefs_DryRunDoesNotMutate(t *testing.T) {
	f := newFakeFKScrubBeads()
	f.agents["polecat-dry"] = makeFKAgentBead("polecat-dry", "wisp-gone", "wisp-gone-2", "clean")

	result, err := ScrubDanglingFKRefs(f, true)
	if err != nil {
		t.Fatalf("ScrubDanglingFKRefs returned error: %v", err)
	}
	if !result.DryRun {
		t.Fatal("DryRun flag not propagated")
	}
	if result.ClearedMRID != 1 || result.ClearedHookBead != 1 {
		t.Fatalf("dry run counts wrong: mr=%d hook=%d", result.ClearedMRID, result.ClearedHookBead)
	}
	if len(f.updates) != 0 {
		t.Fatalf("dry run wrote updates: %v", f.updates)
	}
}

func TestScrubDanglingFKRefs_TolerantOfPerBeadFailures(t *testing.T) {
	f := newFakeFKScrubBeads()
	f.agents["bead-bad"] = makeFKAgentBead("bead-bad", "wisp-gone-1", "", "clean")
	f.agents["bead-good"] = makeFKAgentBead("bead-good", "wisp-gone-2", "", "clean")
	f.updateErrs["bead-bad"] = errors.New("dolt explosion")

	result, err := ScrubDanglingFKRefs(f, false)
	if err != nil {
		t.Fatalf("ScrubDanglingFKRefs returned error: %v", err)
	}
	if result.ClearedMRID != 1 {
		t.Fatalf("ClearedMRID = %d, want 1 (one succeeded)", result.ClearedMRID)
	}
	if len(result.Anomalies) != 1 {
		t.Fatalf("Anomalies = %d, want 1; got %v", len(result.Anomalies), result.Anomalies)
	}
	if result.Anomalies[0].Type != "dangling_fk_clear_failed" {
		t.Fatalf("Anomaly type = %q, want dangling_fk_clear_failed", result.Anomalies[0].Type)
	}
}

func TestScrubDanglingFKRefs_ListErrorPropagates(t *testing.T) {
	f := newFakeFKScrubBeads()
	f.listErr = errors.New("dolt down")
	if _, err := ScrubDanglingFKRefs(f, false); err == nil {
		t.Fatal("expected error when ListAgentBeads fails")
	}
}

func TestScrubDanglingFKRefs_NilClient(t *testing.T) {
	if _, err := ScrubDanglingFKRefs(nil, false); err == nil {
		t.Fatal("expected error for nil client")
	}
}

func TestScrubDanglingFKRefs_SkipsBeadsWithoutFKFields(t *testing.T) {
	f := newFakeFKScrubBeads()
	f.agents["no-fk"] = makeFKAgentBead("no-fk", "", "", "clean")
	f.agents["mr-set"] = makeFKAgentBead("mr-set", "wisp-gone", "", "clean")

	result, err := ScrubDanglingFKRefs(f, false)
	if err != nil {
		t.Fatalf("ScrubDanglingFKRefs: %v", err)
	}
	if result.Scanned != 2 {
		t.Fatalf("Scanned = %d, want 2", result.Scanned)
	}
	if result.HadMRID != 1 {
		t.Fatalf("HadMRID = %d, want 1", result.HadMRID)
	}
	if result.ClearedMRID != 1 {
		t.Fatalf("ClearedMRID = %d, want 1", result.ClearedMRID)
	}
}

func TestScrubDanglingFKRefs_ClearedEntriesHaveContext(t *testing.T) {
	f := newFakeFKScrubBeads()
	f.agents["polecat-x"] = makeFKAgentBead("polecat-x", "wisp-mr-gone", "wisp-hook-gone", "clean")

	result, err := ScrubDanglingFKRefs(f, false)
	if err != nil {
		t.Fatalf("ScrubDanglingFKRefs: %v", err)
	}
	if len(result.ClearedEntries) != 2 {
		t.Fatalf("ClearedEntries len = %d, want 2", len(result.ClearedEntries))
	}
	byField := map[string]ScrubDanglingFKEntry{}
	for _, e := range result.ClearedEntries {
		byField[e.Field] = e
	}
	if got := byField["mr_id"]; got.AgentBeadID != "polecat-x" || got.Referent != "wisp-mr-gone" {
		t.Fatalf("mr_id entry wrong: %+v", got)
	}
	if got := byField["hook_bead"]; got.AgentBeadID != "polecat-x" || got.Referent != "wisp-hook-gone" {
		t.Fatalf("hook_bead entry wrong: %+v", got)
	}
}
