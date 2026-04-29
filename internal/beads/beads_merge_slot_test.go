package beads

import (
	"context"
	"encoding/json"
	"testing"

	beadsdk "github.com/steveyegge/beads"
)

// --- parseMergeSlotData ---

func TestParseMergeSlotData(t *testing.T) {
	tests := []struct {
		name    string
		desc    string
		wantH   string
		wantW   []string
	}{
		{
			name:  "empty description",
			desc:  "",
			wantH: "",
		},
		{
			name:  "empty holder, no waiters",
			desc:  `{"holder":""}`,
			wantH: "",
		},
		{
			name:  "holder only",
			desc:  `{"holder":"alice"}`,
			wantH: "alice",
		},
		{
			name:  "holder with waiters",
			desc:  `{"holder":"alice","waiters":["bob","carol"]}`,
			wantH: "alice",
			wantW: []string{"bob", "carol"},
		},
		{
			name:  "malformed JSON yields zero value",
			desc:  `{invalid`,
			wantH: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &Issue{Description: tt.desc}
			got := parseMergeSlotData(issue)
			if got.Holder != tt.wantH {
				t.Errorf("Holder = %q, want %q", got.Holder, tt.wantH)
			}
			if len(got.Waiters) != len(tt.wantW) {
				t.Fatalf("Waiters len = %d, want %d", len(got.Waiters), len(tt.wantW))
			}
			for i := range tt.wantW {
				if got.Waiters[i] != tt.wantW[i] {
					t.Errorf("Waiters[%d] = %q, want %q", i, got.Waiters[i], tt.wantW[i])
				}
			}
		})
	}
}

// --- mergeSlotStatusFromIssue ---

func TestMergeSlotStatusFromIssue(t *testing.T) {
	issue := &Issue{
		ID:          "slot-1",
		Description: `{"holder":"alice","waiters":["bob"]}`,
	}
	got := mergeSlotStatusFromIssue(issue)
	if got.ID != "slot-1" {
		t.Errorf("ID = %q, want slot-1", got.ID)
	}
	if got.Available {
		t.Error("Available should be false when holder is set")
	}
	if got.Holder != "alice" {
		t.Errorf("Holder = %q, want alice", got.Holder)
	}
	if len(got.Waiters) != 1 || got.Waiters[0] != "bob" {
		t.Errorf("Waiters = %v, want [bob]", got.Waiters)
	}
}

func TestMergeSlotStatusFromIssue_Available(t *testing.T) {
	issue := &Issue{
		ID:          "slot-1",
		Description: `{"holder":""}`,
	}
	got := mergeSlotStatusFromIssue(issue)
	if !got.Available {
		t.Error("Available should be true when holder is empty")
	}
}

// --- MergeSlotCheck ---

// TestMergeSlotCheck_NotFound returns "not found" when no slot bead exists.
func TestMergeSlotCheck_NotFound(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	status, err := b.MergeSlotCheck()
	if err != nil {
		t.Fatalf("MergeSlotCheck: %v", err)
	}
	if status.Error != "not found" {
		t.Errorf("Error = %q, want 'not found'", status.Error)
	}
}

// TestMergeSlotCheck_Available returns available=true when slot is open.
func TestMergeSlotCheck_Available(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	slotID := createMergeSlotBead(t, store, mergeSlotData{})

	status, err := b.MergeSlotCheck()
	if err != nil {
		t.Fatalf("MergeSlotCheck: %v", err)
	}
	if status.ID != slotID {
		t.Errorf("ID = %q, want %q", status.ID, slotID)
	}
	if !status.Available {
		t.Error("expected Available=true")
	}
	if status.Holder != "" {
		t.Errorf("Holder = %q, want empty", status.Holder)
	}
}

// TestMergeSlotCheck_Held returns the current holder when slot is held.
func TestMergeSlotCheck_Held(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	createMergeSlotBead(t, store, mergeSlotData{Holder: "alice", Waiters: []string{"bob"}})

	status, err := b.MergeSlotCheck()
	if err != nil {
		t.Fatalf("MergeSlotCheck: %v", err)
	}
	if status.Available {
		t.Error("expected Available=false")
	}
	if status.Holder != "alice" {
		t.Errorf("Holder = %q, want alice", status.Holder)
	}
	if len(status.Waiters) != 1 || status.Waiters[0] != "bob" {
		t.Errorf("Waiters = %v, want [bob]", status.Waiters)
	}
}

// --- MergeSlotCreate ---

// TestMergeSlotCreate creates a fresh slot bead.
func TestMergeSlotCreate(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	id, err := b.MergeSlotCreate()
	if err != nil {
		t.Fatalf("MergeSlotCreate: %v", err)
	}
	if id == "" {
		t.Error("got empty slot ID")
	}

	// Verify the created bead has the gt:merge-slot label and zero-value data.
	issue, ok := store.issues[id]
	if !ok {
		t.Fatalf("bead %q not in store", id)
	}
	hasLabel := false
	for _, l := range issue.Labels {
		if l == "gt:merge-slot" {
			hasLabel = true
			break
		}
	}
	if !hasLabel {
		t.Errorf("missing gt:merge-slot label: %v", issue.Labels)
	}

	var data mergeSlotData
	if err := json.Unmarshal([]byte(issue.Description), &data); err != nil {
		t.Fatalf("unmarshal description: %v", err)
	}
	if data.Holder != "" {
		t.Errorf("new slot Holder = %q, want empty", data.Holder)
	}
}

// --- MergeSlotAcquire ---

// TestMergeSlotAcquire_Available acquires an open slot.
func TestMergeSlotAcquire_Available(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)
	createMergeSlotBead(t, store, mergeSlotData{})

	status, err := b.MergeSlotAcquire("alice", false)
	if err != nil {
		t.Fatalf("MergeSlotAcquire: %v", err)
	}
	if status.Holder != "alice" {
		t.Errorf("Holder = %q, want alice", status.Holder)
	}
}

// TestMergeSlotAcquire_HeldBySomeoneElse does not change holder; adds to
// waiters when addWaiter=true.
func TestMergeSlotAcquire_HeldBySomeoneElse(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)
	slotID := createMergeSlotBead(t, store, mergeSlotData{Holder: "alice"})

	status, err := b.MergeSlotAcquire("bob", true)
	if err != nil {
		t.Fatalf("MergeSlotAcquire: %v", err)
	}
	if status.Holder != "alice" {
		t.Errorf("Holder should remain alice, got %q", status.Holder)
	}
	// bob should be on waiters.
	issue := store.issues[slotID]
	var data mergeSlotData
	_ = json.Unmarshal([]byte(issue.Description), &data)
	foundBob := false
	for _, w := range data.Waiters {
		if w == "bob" {
			foundBob = true
		}
	}
	if !foundBob {
		t.Errorf("bob not added to waiters: %v", data.Waiters)
	}
}

// TestMergeSlotAcquire_ReacquireByHolder allows the current holder to
// reacquire without conflict.
func TestMergeSlotAcquire_ReacquireByHolder(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)
	createMergeSlotBead(t, store, mergeSlotData{Holder: "alice"})

	status, err := b.MergeSlotAcquire("alice", false)
	if err != nil {
		t.Fatalf("MergeSlotAcquire: %v", err)
	}
	if status.Holder != "alice" {
		t.Errorf("Holder = %q, want alice", status.Holder)
	}
	if status.Available {
		t.Error("Available should be false after acquire")
	}
}

// TestMergeSlotAcquire_NoDuplicateWaiters ensures the same waiter isn't added
// twice.
func TestMergeSlotAcquire_NoDuplicateWaiters(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)
	slotID := createMergeSlotBead(t, store, mergeSlotData{Holder: "alice", Waiters: []string{"bob"}})

	if _, err := b.MergeSlotAcquire("bob", true); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := b.MergeSlotAcquire("bob", true); err != nil {
		t.Fatalf("second acquire: %v", err)
	}

	issue := store.issues[slotID]
	var data mergeSlotData
	_ = json.Unmarshal([]byte(issue.Description), &data)
	count := 0
	for _, w := range data.Waiters {
		if w == "bob" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("bob appears %d times in waiters, want 1: %v", count, data.Waiters)
	}
}

// --- MergeSlotRelease ---

// TestMergeSlotRelease_NoSlot returns nil when there is no slot bead.
func TestMergeSlotRelease_NoSlot(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	if err := b.MergeSlotRelease(""); err != nil {
		t.Errorf("MergeSlotRelease with no slot = %v, want nil", err)
	}
}

// TestMergeSlotRelease_Success clears the holder.
func TestMergeSlotRelease_Success(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)
	slotID := createMergeSlotBead(t, store, mergeSlotData{Holder: "alice"})

	if err := b.MergeSlotRelease("alice"); err != nil {
		t.Fatalf("MergeSlotRelease: %v", err)
	}

	issue := store.issues[slotID]
	var data mergeSlotData
	_ = json.Unmarshal([]byte(issue.Description), &data)
	if data.Holder != "" {
		t.Errorf("Holder = %q, want empty", data.Holder)
	}
}

// TestMergeSlotRelease_PromotesWaiter promotes the first waiter after release.
func TestMergeSlotRelease_PromotesWaiter(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)
	slotID := createMergeSlotBead(t, store, mergeSlotData{
		Holder:  "alice",
		Waiters: []string{"bob", "carol"},
	})

	if err := b.MergeSlotRelease("alice"); err != nil {
		t.Fatalf("MergeSlotRelease: %v", err)
	}

	issue := store.issues[slotID]
	var data mergeSlotData
	_ = json.Unmarshal([]byte(issue.Description), &data)
	if data.Holder != "bob" {
		t.Errorf("expected bob promoted to holder, got %q", data.Holder)
	}
	if len(data.Waiters) != 1 || data.Waiters[0] != "carol" {
		t.Errorf("waiters = %v, want [carol]", data.Waiters)
	}
}

// TestMergeSlotRelease_WrongHolder errors when release actor does not match.
func TestMergeSlotRelease_WrongHolder(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)
	createMergeSlotBead(t, store, mergeSlotData{Holder: "alice"})

	if err := b.MergeSlotRelease("bob"); err == nil {
		t.Error("expected error when bob releases alice's slot, got nil")
	}
}

// TestMergeSlotRelease_AlreadyFree returns nil when slot is already free.
func TestMergeSlotRelease_AlreadyFree(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)
	createMergeSlotBead(t, store, mergeSlotData{})

	if err := b.MergeSlotRelease(""); err != nil {
		t.Errorf("MergeSlotRelease on free slot = %v, want nil", err)
	}
}

// --- MergeSlotEnsureExists ---

// TestMergeSlotEnsureExists_Creates creates the slot when none exists.
func TestMergeSlotEnsureExists_Creates(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	id, err := b.MergeSlotEnsureExists()
	if err != nil {
		t.Fatalf("MergeSlotEnsureExists: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty ID")
	}
	if _, ok := store.issues[id]; !ok {
		t.Errorf("slot %q not created in store", id)
	}
}

// TestMergeSlotEnsureExists_Idempotent returns existing ID when slot present.
func TestMergeSlotEnsureExists_Idempotent(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)
	existing := createMergeSlotBead(t, store, mergeSlotData{})

	id, err := b.MergeSlotEnsureExists()
	if err != nil {
		t.Fatalf("MergeSlotEnsureExists: %v", err)
	}
	if id != existing {
		t.Errorf("expected existing ID %q, got %q", existing, id)
	}
	// Still only one bead with the label.
	count := 0
	for _, issue := range store.issues {
		for _, l := range issue.Labels {
			if l == "gt:merge-slot" {
				count++
			}
		}
	}
	if count != 1 {
		t.Errorf("expected 1 merge slot bead, got %d", count)
	}
}

// createMergeSlotBead creates a merge-slot bead in the mock store with the
// given data and returns its ID.
func createMergeSlotBead(t *testing.T, store *mockStorage, data mergeSlotData) string {
	t.Helper()
	desc, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	issue := &beadsdk.Issue{
		Title:       "merge-slot",
		Description: string(desc),
		Labels:      []string{"gt:merge-slot"},
	}
	if err := store.CreateIssue(context.Background(), issue, "test"); err != nil {
		t.Fatalf("create slot bead: %v", err)
	}
	return issue.ID
}
