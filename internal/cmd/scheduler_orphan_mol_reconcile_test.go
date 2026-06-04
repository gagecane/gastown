package cmd

import (
	"sync"
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

// TestReconcileOrphanMolecules_DedupsDuplicateCandidates verifies that when
// listOrphanWispCandidates surfaces the same wisp ID more than once (the
// overlapping route/redirect views beadsSearchDirs can return), the wisp is
// fetched and burned at most once — not once per duplicate (gu-adbef: a wisp
// was reaped twice in a single pass, doubling the per-wisp bd-show + burn work).
func TestReconcileOrphanMolecules_DedupsDuplicateCandidates(t *testing.T) {
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	old := now.Add(-1 * time.Hour).Format(time.RFC3339)

	prevNow := timeNowForOrphanReconcile
	timeNowForOrphanReconcile = func() time.Time { return now }
	t.Cleanup(func() { timeNowForOrphanReconcile = prevNow })

	// Same zombie wisp returned three times (duplicate dir views).
	prevList := listOrphanWispCandidates
	listOrphanWispCandidates = func(townRoot string, n time.Time) []*beads.Issue {
		return []*beads.Issue{
			{ID: "gu-wisp-dup", Type: "molecule", Status: "open", CreatedAt: old},
			{ID: "gu-wisp-dup", Type: "molecule", Status: "open", CreatedAt: old},
			{ID: "gu-wisp-dup", Type: "molecule", Status: "open", CreatedAt: old},
		}
	}
	t.Cleanup(func() { listOrphanWispCandidates = prevList })

	var fetchCount int
	var fetchMu sync.Mutex
	prevFetch := fetchWispInfoForReconcile
	fetchWispInfoForReconcile = func(townRoot, wispID string) *beads.Issue {
		fetchMu.Lock()
		fetchCount++
		fetchMu.Unlock()
		return &beads.Issue{ID: wispID, Assignee: "", Dependents: []beads.IssueDep{{ID: "gu-done", Status: "closed"}}}
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

	if got != 1 {
		t.Errorf("reconciled count = %d, want 1 (deduped)", got)
	}
	if fetchCount != 1 {
		t.Errorf("fetch calls = %d, want 1 (each unique wisp fetched once)", fetchCount)
	}
	if burnCount != 1 {
		t.Errorf("burn calls = %d, want 1 (each unique wisp burned once)", burnCount)
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
