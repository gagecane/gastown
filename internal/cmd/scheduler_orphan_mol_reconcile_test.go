package cmd

import (
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestIsOrphanMoleculeWispListEntry covers the list-side candidate filter:
// a wisp from `bd mol wisp list --json` is a candidate only when it is a
// molecule (type=molecule or "-wisp-" ID), status=open, and older than the
// reconcile TTL. assignee/dependents are checked later against a full show.
func TestIsOrphanMoleculeWispListEntry(t *testing.T) {
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	old := now.Add(-1 * time.Hour).Format(time.RFC3339)     // well past TTL
	fresh := now.Add(-1 * time.Minute).Format(time.RFC3339) // inside TTL
	ttl := orphanMolReconcileMinAge

	tests := []struct {
		name string
		w    *beads.Issue
		want bool
	}{
		{
			name: "nil",
			w:    nil,
			want: false,
		},
		{
			name: "molecule + open + old → candidate",
			w:    &beads.Issue{ID: "gu-wisp-aaa", Type: "molecule", Status: "open", CreatedAt: old},
			want: true,
		},
		{
			name: "wisp ID fallback when type omitted",
			w:    &beads.Issue{ID: "gu-wisp-bbb", Type: "", Status: "open", CreatedAt: old},
			want: true,
		},
		{
			name: "not a molecule / not a wisp ID",
			w:    &beads.Issue{ID: "gu-abc123", Type: "task", Status: "open", CreatedAt: old},
			want: false,
		},
		{
			// gu-bdzbd: a task-type wisp (dog/patrol formula step, plugin-run)
			// has a "-wisp-" ID but must NOT be treated as a molecule. The old
			// `type != molecule && !contains("-wisp-")` check let it through.
			name: "task-type wisp ID → skip (not a molecule)",
			w:    &beads.Issue{ID: "gc-wisp-task", Type: "task", Status: "open", CreatedAt: old},
			want: false,
		},
		{
			name: "chore-type wisp ID → skip (formula step, not a molecule)",
			w:    &beads.Issue{ID: "gc-wisp-chore", Type: "chore", Status: "open", CreatedAt: old},
			want: false,
		},
		{
			// gu-bdzbd: mail is sent as a wisp by default (gt:message label).
			// Even with type omitted (so the ID fallback would otherwise match),
			// mail must never be a reconcile candidate.
			name: "mail wisp (gt:message) → skip even when type omitted",
			w:    &beads.Issue{ID: "gc-wisp-mail", Type: "", Status: "open", CreatedAt: old, Labels: []string{"gt:message", "msg-type:notification"}},
			want: false,
		},
		{
			name: "mail wisp typed task → skip",
			w:    &beads.Issue{ID: "gc-wisp-mail2", Type: "task", Status: "open", CreatedAt: old, Labels: []string{"gt:message"}},
			want: false,
		},
		{
			name: "closed wisp → skip",
			w:    &beads.Issue{ID: "gu-wisp-ccc", Type: "molecule", Status: "closed", CreatedAt: old},
			want: false,
		},
		{
			name: "hooked wisp → skip (status != open)",
			w:    &beads.Issue{ID: "gu-wisp-ddd", Type: "molecule", Status: "hooked", CreatedAt: old},
			want: false,
		},
		{
			name: "too fresh → skip (racing dispatch)",
			w:    &beads.Issue{ID: "gu-wisp-eee", Type: "molecule", Status: "open", CreatedAt: fresh},
			want: false,
		},
		{
			name: "missing created_at → skip (can't prove staleness)",
			w:    &beads.Issue{ID: "gu-wisp-fff", Type: "molecule", Status: "open", CreatedAt: ""},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isOrphanMoleculeWispListEntry(tt.w, now, ttl)
			if got != tt.want {
				t.Errorf("isOrphanMoleculeWispListEntry() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIsReconcilableMoleculeWisp covers the authoritative show-side identity
// gate (gu-bdzbd). Unlike the list view, `bd show` carries issue_type and
// labels, so this is where mail / non-molecule wisps are excluded for real.
func TestIsReconcilableMoleculeWisp(t *testing.T) {
	tests := []struct {
		name string
		info *beads.Issue
		want bool
	}{
		{
			name: "nil → false",
			info: nil,
			want: false,
		},
		{
			name: "molecule type → reconcilable",
			info: &beads.Issue{ID: "gc-wisp-mol", Type: "molecule"},
			want: true,
		},
		{
			name: "type omitted but -wisp- ID → reconcilable (fallback)",
			info: &beads.Issue{ID: "gc-wisp-x", Type: ""},
			want: true,
		},
		{
			name: "type omitted, non-wisp ID → not reconcilable",
			info: &beads.Issue{ID: "gc-abc", Type: ""},
			want: false,
		},
		{
			name: "task wisp (dog/patrol step) → NOT reconcilable",
			info: &beads.Issue{ID: "gc-wisp-task", Type: "task"},
			want: false,
		},
		{
			name: "chore wisp (formula step) → NOT reconcilable",
			info: &beads.Issue{ID: "gc-wisp-chore", Type: "chore"},
			want: false,
		},
		{
			name: "mail (gt:message, task) → NOT reconcilable",
			info: &beads.Issue{ID: "gc-wisp-mail", Type: "task", Labels: []string{"gt:message", "msg-type:notification"}},
			want: false,
		},
		{
			name: "mail with molecule type still excluded by label guard",
			info: &beads.Issue{ID: "gc-wisp-mail2", Type: "molecule", Labels: []string{"gt:message"}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isReconcilableMoleculeWisp(tt.info); got != tt.want {
				t.Errorf("isReconcilableMoleculeWisp() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestReconcileOrphanMolecules_SkipsNonMoleculeAndMail verifies the production
// path (gu-bdzbd): the list view can't see issue_type/labels, so a task-type
// dog-step wisp and a mail wisp both reach the bd-show fetch as "-wisp-" IDs —
// and must be skipped there, never burned, while a real molecule zombie beside
// them is still reaped.
func TestReconcileOrphanMolecules_SkipsNonMoleculeAndMail(t *testing.T) {
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	old := now.Add(-1 * time.Hour).Format(time.RFC3339)

	prevNow := timeNowForOrphanReconcile
	timeNowForOrphanReconcile = func() time.Time { return now }
	t.Cleanup(func() { timeNowForOrphanReconcile = prevNow })

	// List view: emits NO issue_type and NO labels (as real bd mol wisp list
	// does), so every entry looks identical here — just a "-wisp-" ID.
	prevList := listOrphanWispCandidates
	listOrphanWispCandidates = func(townRoot string, n time.Time) []*beads.Issue {
		return []*beads.Issue{
			{ID: "gc-wisp-mol", Status: "open", CreatedAt: old},
			{ID: "gc-wisp-dogstep", Status: "open", CreatedAt: old},
			{ID: "gc-wisp-mail", Status: "open", CreatedAt: old},
		}
	}
	t.Cleanup(func() { listOrphanWispCandidates = prevList })

	// Show: carries the real issue_type + labels.
	prevFetch := fetchWispInfoForReconcile
	fetchWispInfoForReconcile = func(townRoot, wispID string) *beads.Issue {
		switch wispID {
		case "gc-wisp-mol": // real molecule zombie → burn
			return &beads.Issue{ID: wispID, Type: "molecule", Assignee: "", Dependents: nil}
		case "gc-wisp-dogstep": // operational dog/patrol step → skip
			return &beads.Issue{ID: wispID, Type: "chore", Assignee: "", Dependents: nil}
		case "gc-wisp-mail": // mail → skip
			return &beads.Issue{ID: wispID, Type: "task", Assignee: "", Labels: []string{"gt:message"}, Dependents: nil}
		}
		return nil
	}
	t.Cleanup(func() { fetchWispInfoForReconcile = prevFetch })

	var burned []string
	prevBurn := burnExistingMoleculesForRecovery
	burnExistingMoleculesForRecovery = func(molecules []string, beadID, townRoot string) error {
		burned = append(burned, molecules...)
		return nil
	}
	t.Cleanup(func() { burnExistingMoleculesForRecovery = prevBurn })

	got := reconcileOrphanMolecules("/fake/town")
	if got != 1 {
		t.Errorf("reconciled count = %d, want 1 (only the real molecule)", got)
	}
	if len(burned) != 1 || burned[0] != "gc-wisp-mol" {
		t.Errorf("burned = %v, want [gc-wisp-mol] (dog-step and mail must never be reaped)", burned)
	}
}

// TestReconcileDecision covers the per-wisp action verdict given the work
// beads bonded to the orphan wisp (its dependents).
func TestReconcileDecision(t *testing.T) {
	tests := []struct {
		name       string
		dependents []beads.IssueDep
		want       orphanWispAction
	}{
		{
			name:       "no dependents → burn zombie",
			dependents: nil,
			want:       orphanWispActionBurn,
		},
		{
			name:       "single closed work bead → burn zombie (the observed case)",
			dependents: []beads.IssueDep{{ID: "gu-work1", Status: "closed"}},
			want:       orphanWispActionBurn,
		},
		{
			name:       "tombstone work bead → burn zombie",
			dependents: []beads.IssueDep{{ID: "gu-work1", Status: "tombstone"}},
			want:       orphanWispActionBurn,
		},
		{
			name:       "open work bead → re-enqueue (unblock for re-dispatch)",
			dependents: []beads.IssueDep{{ID: "gu-work1", Status: "open"}},
			want:       orphanWispActionReenqueue,
		},
		{
			name:       "hooked work bead → skip (live owner)",
			dependents: []beads.IssueDep{{ID: "gu-work1", Status: "hooked"}},
			want:       orphanWispActionSkip,
		},
		{
			name:       "in_progress work bead → skip (live owner)",
			dependents: []beads.IssueDep{{ID: "gu-work1", Status: "in_progress"}},
			want:       orphanWispActionSkip,
		},
		{
			name: "skip beats re-enqueue when both present",
			dependents: []beads.IssueDep{
				{ID: "gu-work-open", Status: "open"},
				{ID: "gu-work-live", Status: "in_progress"},
			},
			want: orphanWispActionSkip,
		},
		{
			name: "wisp-to-wisp dep ignored, falls through to burn",
			dependents: []beads.IssueDep{
				{ID: "gu-wisp-other", Status: "open"},
			},
			want: orphanWispActionBurn,
		},
		{
			name: "open work bead beside closed one → re-enqueue",
			dependents: []beads.IssueDep{
				{ID: "gu-work-closed", Status: "closed"},
				{ID: "gu-work-open", Status: "open"},
			},
			want: orphanWispActionReenqueue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reconcileDecision(tt.dependents)
			if got != tt.want {
				t.Errorf("reconcileDecision() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestBurnBaseBeadForWisp verifies the burn-base selection: prefer a non-closed
// work-bead dependent, else fall back to the wisp's own ID.
func TestBurnBaseBeadForWisp(t *testing.T) {
	tests := []struct {
		name       string
		wispID     string
		dependents []beads.IssueDep
		want       string
	}{
		{
			name:       "no dependents → wisp itself",
			wispID:     "gu-wisp-x",
			dependents: nil,
			want:       "gu-wisp-x",
		},
		{
			name:       "all closed → wisp itself",
			wispID:     "gu-wisp-x",
			dependents: []beads.IssueDep{{ID: "gu-work", Status: "closed"}},
			want:       "gu-wisp-x",
		},
		{
			name:       "open work bead → that bead",
			wispID:     "gu-wisp-x",
			dependents: []beads.IssueDep{{ID: "gu-work", Status: "open"}},
			want:       "gu-work",
		},
		{
			name:   "skips closed, picks live work bead",
			wispID: "gu-wisp-x",
			dependents: []beads.IssueDep{
				{ID: "gu-work-closed", Status: "closed"},
				{ID: "gu-work-live", Status: "hooked"},
			},
			want: "gu-work-live",
		},
		{
			name:   "ignores wisp dep, falls back to wisp ID",
			wispID: "gu-wisp-x",
			dependents: []beads.IssueDep{
				{ID: "gu-wisp-other", Status: "open"},
			},
			want: "gu-wisp-x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := burnBaseBeadForWisp(tt.wispID, tt.dependents)
			if got != tt.want {
				t.Errorf("burnBaseBeadForWisp() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestReconcileOrphanMolecules_SkipsMergeRequestWisp verifies that an open
// merge-request wisp is NEVER reaped by the orphan pass (gs-bpq) — it is a
// pending merge that intentionally outlives its self-terminated submitter — while
// a plain molecule zombie alongside it is still burned.
func TestReconcileOrphanMolecules_SkipsMergeRequestWisp(t *testing.T) {
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	old := now.Add(-1 * time.Hour).Format(time.RFC3339)

	prevNow := timeNowForOrphanReconcile
	timeNowForOrphanReconcile = func() time.Time { return now }
	t.Cleanup(func() { timeNowForOrphanReconcile = prevNow })

	prevList := listOrphanWispCandidates
	listOrphanWispCandidates = func(townRoot string, n time.Time) []*beads.Issue {
		return []*beads.Issue{
			{ID: "gu-wisp-mr", Type: "molecule", Status: "open", CreatedAt: old},
			{ID: "gu-wisp-plain", Type: "molecule", Status: "open", CreatedAt: old},
		}
	}
	t.Cleanup(func() { listOrphanWispCandidates = prevList })

	prevFetch := fetchWispInfoForReconcile
	fetchWispInfoForReconcile = func(townRoot, wispID string) *beads.Issue {
		switch wispID {
		case "gu-wisp-mr": // MR wisp (no live work bead) → must be skipped, not burned
			return &beads.Issue{
				ID:         wispID,
				Assignee:   "",
				Labels:     []string{mergeRequestWispLabel},
				Dependents: nil,
			}
		case "gu-wisp-plain": // plain molecule zombie → burned
			return &beads.Issue{ID: wispID, Assignee: "", Dependents: nil}
		}
		return nil
	}
	t.Cleanup(func() { fetchWispInfoForReconcile = prevFetch })

	var burned []string
	prevBurn := burnExistingMoleculesForRecovery
	burnExistingMoleculesForRecovery = func(molecules []string, beadID, townRoot string) error {
		burned = append(burned, molecules...)
		return nil
	}
	t.Cleanup(func() { burnExistingMoleculesForRecovery = prevBurn })

	got := reconcileOrphanMolecules("/fake/town")
	if got != 1 {
		t.Errorf("reconciled count = %d, want 1 (only the plain zombie)", got)
	}
	if len(burned) != 1 || burned[0] != "gu-wisp-plain" {
		t.Errorf("burned = %v, want [gu-wisp-plain] (the MR wisp must never be reaped)", burned)
	}
}

// TestReconcileOrphanMolecules_Orchestration drives the full pass with the
// listing, show, and burn seams stubbed so no real bd is needed. It asserts
// which wisps get burned and with which base bead.
func TestReconcileOrphanMolecules_Orchestration(t *testing.T) {
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	old := now.Add(-1 * time.Hour).Format(time.RFC3339)

	// Stub the clock.
	prevNow := timeNowForOrphanReconcile
	timeNowForOrphanReconcile = func() time.Time { return now }
	t.Cleanup(func() { timeNowForOrphanReconcile = prevNow })

	// Stub the list: three candidate molecule wisps.
	prevList := listOrphanWispCandidates
	listOrphanWispCandidates = func(townRoot string, n time.Time) []*beads.Issue {
		return []*beads.Issue{
			{ID: "gu-wisp-zombie", Type: "molecule", Status: "open", CreatedAt: old},
			{ID: "gu-wisp-reenq", Type: "molecule", Status: "open", CreatedAt: old},
			{ID: "gu-wisp-live", Type: "molecule", Status: "open", CreatedAt: old},
			{ID: "gu-wisp-assigned", Type: "molecule", Status: "open", CreatedAt: old},
		}
	}
	t.Cleanup(func() { listOrphanWispCandidates = prevList })

	// Stub the per-wisp show.
	prevFetch := fetchWispInfoForReconcile
	fetchWispInfoForReconcile = func(townRoot, wispID string) *beads.Issue {
		switch wispID {
		case "gu-wisp-zombie": // closed work bead → burn
			return &beads.Issue{ID: wispID, Assignee: "", Dependents: []beads.IssueDep{{ID: "gu-done", Status: "closed"}}}
		case "gu-wisp-reenq": // open work bead → re-enqueue (burn to unblock)
			return &beads.Issue{ID: wispID, Assignee: "", Dependents: []beads.IssueDep{{ID: "gu-open", Status: "open"}}}
		case "gu-wisp-live": // hooked work bead → skip
			return &beads.Issue{ID: wispID, Assignee: "", Dependents: []beads.IssueDep{{ID: "gu-busy", Status: "in_progress"}}}
		case "gu-wisp-assigned": // assignee set → skip
			return &beads.Issue{ID: wispID, Assignee: "rig/polecats/alive", Dependents: nil}
		}
		return nil
	}
	t.Cleanup(func() { fetchWispInfoForReconcile = prevFetch })

	// Capture burns.
	type burnCall struct {
		molecules []string
		baseBead  string
	}
	var burns []burnCall
	prevBurn := burnExistingMoleculesForRecovery
	burnExistingMoleculesForRecovery = func(molecules []string, beadID, townRoot string) error {
		burns = append(burns, burnCall{molecules: molecules, baseBead: beadID})
		return nil
	}
	t.Cleanup(func() { burnExistingMoleculesForRecovery = prevBurn })

	got := reconcileOrphanMolecules("/fake/town")

	if got != 2 {
		t.Errorf("reconciled count = %d, want 2 (zombie + reenq)", got)
	}
	if len(burns) != 2 {
		t.Fatalf("burn calls = %d, want 2: %+v", len(burns), burns)
	}

	// Zombie: burned with the wisp's own ID (its only dependent is closed).
	if burns[0].baseBead != "gu-wisp-zombie" || len(burns[0].molecules) != 1 || burns[0].molecules[0] != "gu-wisp-zombie" {
		t.Errorf("zombie burn = %+v, want molecules=[gu-wisp-zombie] base=gu-wisp-zombie", burns[0])
	}
	// Re-enqueue: burned with the open work bead as the base.
	if burns[1].baseBead != "gu-open" || len(burns[1].molecules) != 1 || burns[1].molecules[0] != "gu-wisp-reenq" {
		t.Errorf("reenq burn = %+v, want molecules=[gu-wisp-reenq] base=gu-open", burns[1])
	}
}

// TestReconcileOrphanMolecules_SkipsAssignedAndLive verifies that an assigned
// wisp and a wisp with a live (hooked/in_progress) work bead are never burned.
func TestReconcileOrphanMolecules_SkipsAssignedAndLive(t *testing.T) {
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	old := now.Add(-1 * time.Hour).Format(time.RFC3339)

	prevNow := timeNowForOrphanReconcile
	timeNowForOrphanReconcile = func() time.Time { return now }
	t.Cleanup(func() { timeNowForOrphanReconcile = prevNow })

	prevList := listOrphanWispCandidates
	listOrphanWispCandidates = func(townRoot string, n time.Time) []*beads.Issue {
		return []*beads.Issue{
			{ID: "gu-wisp-assigned", Type: "molecule", Status: "open", CreatedAt: old},
			{ID: "gu-wisp-live", Type: "molecule", Status: "open", CreatedAt: old},
			{ID: "gu-wisp-nil", Type: "molecule", Status: "open", CreatedAt: old},
		}
	}
	t.Cleanup(func() { listOrphanWispCandidates = prevList })

	prevFetch := fetchWispInfoForReconcile
	fetchWispInfoForReconcile = func(townRoot, wispID string) *beads.Issue {
		switch wispID {
		case "gu-wisp-assigned":
			return &beads.Issue{ID: wispID, Assignee: "rig/crew/bob", Dependents: nil}
		case "gu-wisp-live":
			return &beads.Issue{ID: wispID, Assignee: "", Dependents: []beads.IssueDep{{ID: "gu-busy", Status: "hooked"}}}
		case "gu-wisp-nil":
			return nil // show failed → skip
		}
		return nil
	}
	t.Cleanup(func() { fetchWispInfoForReconcile = prevFetch })

	var burnCount int
	prevBurn := burnExistingMoleculesForRecovery
	burnExistingMoleculesForRecovery = func(molecules []string, beadID, townRoot string) error {
		burnCount++
		return nil
	}
	t.Cleanup(func() { burnExistingMoleculesForRecovery = prevBurn })

	got := reconcileOrphanMolecules("/fake/town")

	if got != 0 {
		t.Errorf("reconciled count = %d, want 0", got)
	}
	if burnCount != 0 {
		t.Errorf("burn calls = %d, want 0 (nothing should be touched)", burnCount)
	}
}
