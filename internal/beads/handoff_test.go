package beads

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	beadsdk "github.com/steveyegge/beads"
)

func TestHandoffBeadTitle(t *testing.T) {
	tests := []struct {
		role string
		want string
	}{
		{"mayor", "mayor Handoff"},
		{"deacon", "deacon Handoff"},
		{"gastown/witness", "gastown/witness Handoff"},
		{"gastown/crew/joe", "gastown/crew/joe Handoff"},
		{"", " Handoff"},
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			got := HandoffBeadTitle(tt.role)
			if got != tt.want {
				t.Errorf("HandoffBeadTitle(%q) = %q, want %q", tt.role, got, tt.want)
			}
		})
	}
}

func TestStatusConstants(t *testing.T) {
	// Verify the status constants haven't changed (these are used in protocol)
	if StatusPinned != "pinned" {
		t.Errorf("StatusPinned = %q, want %q", StatusPinned, "pinned")
	}
	if StatusHooked != "hooked" {
		t.Errorf("StatusHooked = %q, want %q", StatusHooked, "hooked")
	}
}

func TestCurrentTimestamp(t *testing.T) {
	ts := currentTimestamp()
	if ts == "" {
		t.Fatal("currentTimestamp() returned empty string")
	}
	// Should be RFC3339 format
	if len(ts) < 20 {
		t.Errorf("timestamp too short: %q (expected RFC3339)", ts)
	}
	// Should contain T separator and Z suffix (UTC)
	found := false
	for _, c := range ts {
		if c == 'T' {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("timestamp missing T separator: %q", ts)
	}
}

// TestWithBeadLockSerializes is a regression test for gu-tucci (lost-update RMW
// races). The escalation/channel/queue mutators do Show -> mutate -> Update,
// and Update rewrites the entire description, so two concurrent critical
// sections on the same bead lose one another's writes. withBeadLock is the
// shared primitive that must serialize them. This verifies the primitive itself
// provides mutual exclusion: with real locking the max observed concurrency
// inside the critical section is 1; the pre-fix unlocked code would observe >1.
func TestWithBeadLockSerializes(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	// beadsDir set directly so lock files resolve to a known temp location and
	// routing (forIssueID) is a no-op (no routes.jsonl present).
	b := &Beads{workDir: tmpDir, beadsDir: beadsDir, isolated: true}

	const id = "gt-lock-test"
	var inside int32    // current goroutines inside the critical section
	var maxInside int32 // high-water mark
	var completed int32 // critical sections that ran

	const workers = 16
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := b.withBeadLock(id, func() error {
				n := atomic.AddInt32(&inside, 1)
				for {
					m := atomic.LoadInt32(&maxInside)
					if n <= m || atomic.CompareAndSwapInt32(&maxInside, m, n) {
						break
					}
				}
				// Busy-spin briefly to widen the race window without sleeping.
				for j := 0; j < 100000; j++ {
					_ = j
				}
				atomic.AddInt32(&inside, -1)
				atomic.AddInt32(&completed, 1)
				return nil
			})
			if err != nil {
				t.Errorf("withBeadLock: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&completed); got != workers {
		t.Fatalf("completed = %d, want %d", got, workers)
	}
	if got := atomic.LoadInt32(&maxInside); got != 1 {
		t.Fatalf("max concurrent critical sections = %d, want 1 (lock not serializing)", got)
	}
}

func TestClearMailResultZeroValues(t *testing.T) {
	// Verify zero-value struct is safe to use
	result := &ClearMailResult{}
	if result.Closed != 0 || result.Cleared != 0 {
		t.Errorf("expected zero values, got Closed=%d Cleared=%d", result.Closed, result.Cleared)
	}
}

func TestCloseStaleHookedMailBeads(t *testing.T) {
	hookedMailBead := func(store *mockStorage, assignee string) string {
		id := "test-hm-1"
		store.issues[id] = &beadsdk.Issue{
			ID:       id,
			Title:    "🤝 HANDOFF: prev session",
			Status:   beadsdk.Status(StatusHooked),
			Assignee: assignee,
			Labels:   []string{"gt:message"},
		}
		store.labels[id] = []string{"gt:message"}
		return id
	}

	t.Run("closes hooked gt:message beads for agent", func(t *testing.T) {
		store := newMockStorage()
		b := newTestBeads(store)
		id := hookedMailBead(store, "gastown/mayor")

		n, err := b.CloseStaleHookedMailBeads("gastown/mayor")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 1 {
			t.Errorf("want 1 closed, got %d", n)
		}
		if !store.closed[id] {
			t.Errorf("bead %s was not closed", id)
		}
	})

	t.Run("does not close hooked gt:task beads", func(t *testing.T) {
		store := newMockStorage()
		b := newTestBeads(store)
		taskID := "test-task-1"
		store.issues[taskID] = &beadsdk.Issue{
			ID:       taskID,
			Status:   beadsdk.Status(StatusHooked),
			Assignee: "gastown/mayor",
			Labels:   []string{"gt:task"},
		}
		store.labels[taskID] = []string{"gt:task"}

		n, err := b.CloseStaleHookedMailBeads("gastown/mayor")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 0 {
			t.Errorf("want 0 (gt:task bead should be untouched), got %d", n)
		}
		if store.closed[taskID] {
			t.Errorf("gt:task bead %s was incorrectly closed", taskID)
		}
	})

	t.Run("returns 0 when no hooked mail beads exist", func(t *testing.T) {
		b := newTestBeads(newMockStorage())

		n, err := b.CloseStaleHookedMailBeads("gastown/mayor")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 0 {
			t.Errorf("want 0, got %d", n)
		}
	})

	t.Run("does not close mail beads belonging to other agents", func(t *testing.T) {
		store := newMockStorage()
		b := newTestBeads(store)
		id := hookedMailBead(store, "gastown/witness")

		n, err := b.CloseStaleHookedMailBeads("gastown/mayor")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 0 {
			t.Errorf("want 0 (other agent's bead should be untouched), got %d", n)
		}
		if store.closed[id] {
			t.Errorf("other agent's bead %s was incorrectly closed", id)
		}
	})
}
