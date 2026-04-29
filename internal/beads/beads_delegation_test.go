package beads

import (
	"context"
	"encoding/json"
	"testing"

	beadsdk "github.com/steveyegge/beads"
)

// --- parseDelegationFromMetadata ---

func TestParseDelegationFromMetadata(t *testing.T) {
	tests := []struct {
		name    string
		meta    string
		want    *Delegation
		wantErr bool
	}{
		{
			name: "empty metadata",
			meta: "",
			want: nil,
		},
		{
			name: "metadata with no delegated_from key",
			meta: `{"other":"stuff"}`,
			want: nil,
		},
		{
			name: "explicit null delegated_from",
			meta: `{"delegated_from":null}`,
			want: nil,
		},
		{
			name: "valid delegation",
			meta: `{"delegated_from":{"parent":"p-1","child":"c-1","delegated_by":"alice","delegated_to":"bob"}}`,
			want: &Delegation{
				Parent:      "p-1",
				Child:       "c-1",
				DelegatedBy: "alice",
				DelegatedTo: "bob",
			},
		},
		{
			name: "delegation with terms",
			meta: `{"delegated_from":{"parent":"p-1","child":"c-1","delegated_by":"a","delegated_to":"b","terms":{"portion":"half","credit_share":50}}}`,
			want: &Delegation{
				Parent:      "p-1",
				Child:       "c-1",
				DelegatedBy: "a",
				DelegatedTo: "b",
				Terms:       &DelegationTerms{Portion: "half", CreditShare: 50},
			},
		},
		{
			name: "malformed metadata returns nil, no error",
			meta: `not json at all`,
			want: nil,
		},
		{
			name:    "malformed delegation value returns error",
			meta:    `{"delegated_from":"not-an-object"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDelegationFromMetadata(json.RawMessage(tt.meta))
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseDelegationFromMetadata err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !delegationsEqual(got, tt.want) {
				t.Errorf("parseDelegationFromMetadata = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// delegationsEqual compares two delegation pointers for equality, handling
// nils and the Terms pointer.
func delegationsEqual(a, b *Delegation) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if a == nil {
		return true
	}
	if a.Parent != b.Parent || a.Child != b.Child ||
		a.DelegatedBy != b.DelegatedBy || a.DelegatedTo != b.DelegatedTo ||
		a.CreatedAt != b.CreatedAt {
		return false
	}
	if (a.Terms == nil) != (b.Terms == nil) {
		return false
	}
	if a.Terms == nil {
		return true
	}
	return a.Terms.Portion == b.Terms.Portion &&
		a.Terms.Deadline == b.Terms.Deadline &&
		a.Terms.AcceptanceCriteria == b.Terms.AcceptanceCriteria &&
		a.Terms.CreditShare == b.Terms.CreditShare
}

// --- AddDelegation validation ---

func TestAddDelegation_Validation(t *testing.T) {
	b := newTestBeads(newMockStorage())

	tests := []struct {
		name string
		d    *Delegation
	}{
		{"missing parent", &Delegation{Child: "c", DelegatedBy: "a", DelegatedTo: "b"}},
		{"missing child", &Delegation{Parent: "p", DelegatedBy: "a", DelegatedTo: "b"}},
		{"missing delegated_by", &Delegation{Parent: "p", Child: "c", DelegatedTo: "b"}},
		{"missing delegated_to", &Delegation{Parent: "p", Child: "c", DelegatedBy: "a"}},
		{"all empty", &Delegation{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := b.AddDelegation(tt.d); err == nil {
				t.Errorf("AddDelegation(%+v) = nil, want error", tt.d)
			}
		})
	}
}

// TestAddDelegation_Success adds delegation metadata and a blocking dep.
func TestAddDelegation_Success(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)
	ctx := context.Background()

	// Create parent and child issues.
	parent := &beadsdk.Issue{Title: "parent"}
	if err := store.CreateIssue(ctx, parent, ""); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child := &beadsdk.Issue{Title: "child"}
	if err := store.CreateIssue(ctx, child, ""); err != nil {
		t.Fatalf("create child: %v", err)
	}

	d := &Delegation{
		Parent:      parent.ID,
		Child:       child.ID,
		DelegatedBy: "alice",
		DelegatedTo: "bob",
		Terms:       &DelegationTerms{Portion: "ui", CreditShare: 40},
	}
	if err := b.AddDelegation(d); err != nil {
		t.Fatalf("AddDelegation: %v", err)
	}

	// Verify delegation metadata stored on child.
	got, err := b.GetDelegation(child.ID)
	if err != nil {
		t.Fatalf("GetDelegation: %v", err)
	}
	if got == nil {
		t.Fatal("GetDelegation returned nil")
	}
	if got.Parent != parent.ID || got.Child != child.ID ||
		got.DelegatedBy != "alice" || got.DelegatedTo != "bob" {
		t.Errorf("delegation mismatch: %+v", got)
	}
	if got.Terms == nil || got.Terms.Portion != "ui" || got.Terms.CreditShare != 40 {
		t.Errorf("terms mismatch: %+v", got.Terms)
	}

	// Verify blocking dependency added: parent depends on child.
	deps := store.deps[parent.ID]
	found := false
	for _, dep := range deps {
		if dep == child.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected blocking dep parent->child, got %v", deps)
	}
}

// TestGetDelegation_NotExist returns nil when no delegation recorded.
func TestGetDelegation_NotExist(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)
	ctx := context.Background()

	child := &beadsdk.Issue{Title: "child"}
	if err := store.CreateIssue(ctx, child, ""); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := b.GetDelegation(child.ID)
	if err != nil {
		t.Fatalf("GetDelegation: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

// TestGetDelegation_ShowError propagates show errors.
func TestGetDelegation_ShowError(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	_, err := b.GetDelegation("nonexistent-id")
	if err == nil {
		t.Error("expected error for missing issue, got nil")
	}
}

// TestRemoveDelegation clears metadata and removes dep.
func TestRemoveDelegation(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)
	ctx := context.Background()

	parent := &beadsdk.Issue{Title: "parent"}
	_ = store.CreateIssue(ctx, parent, "")
	child := &beadsdk.Issue{Title: "child"}
	_ = store.CreateIssue(ctx, child, "")

	d := &Delegation{
		Parent:      parent.ID,
		Child:       child.ID,
		DelegatedBy: "alice",
		DelegatedTo: "bob",
	}
	if err := b.AddDelegation(d); err != nil {
		t.Fatalf("AddDelegation: %v", err)
	}

	if err := b.RemoveDelegation(parent.ID, child.ID); err != nil {
		t.Fatalf("RemoveDelegation: %v", err)
	}

	// Delegation metadata should be gone.
	got, err := b.GetDelegation(child.ID)
	if err != nil {
		t.Fatalf("GetDelegation after remove: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after remove, got %+v", got)
	}

	// Blocking dep should be gone.
	for _, dep := range store.deps[parent.ID] {
		if dep == child.ID {
			t.Error("blocking dep was not removed")
		}
	}
}

// TestListDelegationsFrom enumerates delegations whose parent matches.
func TestListDelegationsFrom(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)
	ctx := context.Background()

	parent := &beadsdk.Issue{Title: "parent"}
	_ = store.CreateIssue(ctx, parent, "")
	other := &beadsdk.Issue{Title: "other-parent"}
	_ = store.CreateIssue(ctx, other, "")

	// Two children delegated from parent
	c1 := &beadsdk.Issue{Title: "c1"}
	_ = store.CreateIssue(ctx, c1, "")
	c2 := &beadsdk.Issue{Title: "c2"}
	_ = store.CreateIssue(ctx, c2, "")
	// One child delegated from other
	c3 := &beadsdk.Issue{Title: "c3"}
	_ = store.CreateIssue(ctx, c3, "")

	for _, pair := range []struct{ p, c string }{
		{parent.ID, c1.ID},
		{parent.ID, c2.ID},
		{other.ID, c3.ID},
	} {
		if err := b.AddDelegation(&Delegation{
			Parent:      pair.p,
			Child:       pair.c,
			DelegatedBy: "alice",
			DelegatedTo: "bob",
		}); err != nil {
			t.Fatalf("AddDelegation: %v", err)
		}
	}

	delegations, err := b.ListDelegationsFrom(parent.ID)
	if err != nil {
		t.Fatalf("ListDelegationsFrom: %v", err)
	}
	if len(delegations) != 2 {
		t.Fatalf("expected 2 delegations from parent, got %d", len(delegations))
	}
	children := map[string]bool{}
	for _, d := range delegations {
		if d.Parent != parent.ID {
			t.Errorf("wrong parent in result: %+v", d)
		}
		children[d.Child] = true
	}
	if !children[c1.ID] || !children[c2.ID] {
		t.Errorf("missing expected children: %v", children)
	}
}

// TestListDelegationsFrom_Empty returns empty slice when no delegations.
func TestListDelegationsFrom_Empty(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	delegations, err := b.ListDelegationsFrom("nonexistent")
	if err != nil {
		t.Fatalf("ListDelegationsFrom: %v", err)
	}
	if len(delegations) != 0 {
		t.Errorf("expected 0 delegations, got %d", len(delegations))
	}
}
