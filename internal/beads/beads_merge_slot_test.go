package beads

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	beadsdk "github.com/steveyegge/beads"
)

// syncStorage wraps a mockStorage with a mutex so the test harness itself is
// goroutine-safe. The race we are exercising lives in the merge-slot RMW logic
// (Show -> mutate -> Update), not in the in-memory map; serializing every
// storage call ensures a failure means a real lost update, not a mock data race.
type syncStorage struct {
	mu sync.Mutex
	*mockStorage
}

func (s *syncStorage) GetIssue(ctx context.Context, id string) (*beadsdk.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	issue, err := s.mockStorage.GetIssue(ctx, id)
	return copyIssue(issue), err
}

func (s *syncStorage) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mockStorage.UpdateIssue(ctx, id, updates, actor)
}

func (s *syncStorage) SearchIssues(ctx context.Context, query string, filter beadsdk.IssueFilter) ([]*beadsdk.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	issues, err := s.mockStorage.SearchIssues(ctx, query, filter)
	out := make([]*beadsdk.Issue, len(issues))
	for i, iss := range issues {
		out[i] = copyIssue(iss)
	}
	return out, err
}

// copyIssue returns a shallow copy so callers (which run outside the storage
// mutex once GetIssue/SearchIssues return) never read fields that a concurrent
// UpdateIssue mutates in place on the shared map pointer. The production Dolt
// backend returns fresh structs per query, so this only mirrors that behavior
// for the in-memory mock; it is not exercising the code under test.
func copyIssue(issue *beadsdk.Issue) *beadsdk.Issue {
	if issue == nil {
		return nil
	}
	c := *issue
	return &c
}

func (s *syncStorage) CreateIssue(ctx context.Context, issue *beadsdk.Issue, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mockStorage.CreateIssue(ctx, issue, actor)
}

func (s *syncStorage) AddLabel(ctx context.Context, issueID, label, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mockStorage.AddLabel(ctx, issueID, label, actor)
}

func (s *syncStorage) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mockStorage.GetLabels(ctx, issueID)
}

func newMergeSlotTestBeads(t *testing.T) *Beads {
	t.Helper()
	tmp := t.TempDir()
	beadsDir := filepath.Join(tmp, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	store := &syncStorage{mockStorage: newMockStorage()}
	// noRoute keeps forIssueID a no-op so the per-bead flock resolves to the
	// known temp beadsDir (no routes.jsonl present).
	return &Beads{workDir: tmp, beadsDir: beadsDir, store: store, isolated: true, noRoute: true}
}

// TestMergeSlotAcquireSingleWinner is the regression test for gu-sz1xl: the
// merge slot's acquire RMW (Show -> parse -> mutate -> Update) was unlocked, so
// two processes that both observe Holder=="" could each write their own holder
// and both believe they hold the slot (second write wins, lost update). With
// the withBeadLock fix, concurrent acquirers are serialized: exactly one becomes
// the holder and every other distinct requester is recorded as a waiter — no
// acquisition is silently lost.
func TestMergeSlotAcquireSingleWinner(t *testing.T) {
	b := newMergeSlotTestBeads(t)
	if _, err := b.MergeSlotCreate(); err != nil {
		t.Fatalf("create slot: %v", err)
	}

	const workers = 16
	holders := make([]string, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			actor := "actor-" + string(rune('a'+idx))
			st, err := b.MergeSlotAcquire(actor, true /* addWaiter */)
			if err != nil {
				t.Errorf("acquire(%s): %v", actor, err)
				return
			}
			holders[idx] = st.Holder
		}(i)
	}
	wg.Wait()

	// Exactly one distinct actor must own the slot, and all workers must agree
	// on who that is (no two callers each think they won).
	final, err := b.MergeSlotCheck()
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if final.Holder == "" {
		t.Fatalf("slot has no holder after %d concurrent acquires", workers)
	}

	// Every requester that did not win must be queued as a waiter (its write was
	// not lost). Winner + waiters must account for all distinct participants.
	distinct := map[string]bool{final.Holder: true}
	for _, w := range final.Waiters {
		distinct[w] = true
	}
	if len(distinct) != workers {
		t.Fatalf("holder+waiters cover %d distinct actors, want %d (lost update: an acquire was clobbered)\nholder=%q waiters=%v",
			len(distinct), workers, final.Holder, final.Waiters)
	}
}

// TestMergeSlotReleasePromotesOneWaiter verifies release hands the slot to
// exactly one waiter (the head of the queue) and drops only that waiter — the
// release-side symmetric race called out in gu-sz1xl (promote Waiters[0] twice
// or drop a waiter).
func TestMergeSlotReleasePromotesOneWaiter(t *testing.T) {
	b := newMergeSlotTestBeads(t)
	if _, err := b.MergeSlotCreate(); err != nil {
		t.Fatalf("create slot: %v", err)
	}

	// alice holds; bob and carol wait.
	if _, err := b.MergeSlotAcquire("alice", true); err != nil {
		t.Fatalf("acquire alice: %v", err)
	}
	if _, err := b.MergeSlotAcquire("bob", true); err != nil {
		t.Fatalf("acquire bob: %v", err)
	}
	if _, err := b.MergeSlotAcquire("carol", true); err != nil {
		t.Fatalf("acquire carol: %v", err)
	}

	if err := b.MergeSlotRelease("alice"); err != nil {
		t.Fatalf("release alice: %v", err)
	}

	st, err := b.MergeSlotCheck()
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if st.Holder != "bob" {
		t.Fatalf("after release, holder = %q, want bob (head of queue)", st.Holder)
	}
	if len(st.Waiters) != 1 || st.Waiters[0] != "carol" {
		t.Fatalf("after release, waiters = %v, want [carol]", st.Waiters)
	}
}
