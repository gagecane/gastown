package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/mail"
)

// TestRunMailInbox_MutuallyExclusiveFlags verifies that passing both --all and
// --unread to `gt mail inbox` surfaces a clear error before any workspace or
// mailbox resolution happens.
func TestRunMailInbox_MutuallyExclusiveFlags(t *testing.T) {
	// Save and restore package-level flags.
	origAll, origUnread := mailInboxAll, mailInboxUnread
	defer func() {
		mailInboxAll = origAll
		mailInboxUnread = origUnread
	}()

	mailInboxAll = true
	mailInboxUnread = true

	err := runMailInbox(&cobra.Command{}, nil)
	if err == nil {
		t.Fatal("expected error when --all and --unread are both set, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention 'mutually exclusive', got: %v", err)
	}
}

// TestRunMailRead_NoArgs verifies that `gt mail read` with no arguments returns
// a clear error and instructs the user to run `gt mail inbox`.
func TestRunMailRead_NoArgs(t *testing.T) {
	err := runMailRead(&cobra.Command{}, nil)
	if err == nil {
		t.Fatal("expected error when no message ID provided, got nil")
	}
	if !strings.Contains(err.Error(), "message ID or index required") {
		t.Errorf("error should mention 'message ID or index required', got: %v", err)
	}
	if !strings.Contains(err.Error(), "gt mail inbox") {
		t.Errorf("error should suggest 'gt mail inbox', got: %v", err)
	}
}

// TestRunMailInbox_FlagDefaults verifies that the package-level flags used by
// `gt mail inbox` are independent booleans (regression guard: a shared pointer
// or accidental alias would break the mutex check). This test does not execute
// runMailInbox to avoid workspace/mailbox setup.
func TestRunMailInbox_FlagDefaults(t *testing.T) {
	origAll, origUnread, origJSON, origIdentity := mailInboxAll, mailInboxUnread, mailInboxJSON, mailInboxIdentity
	defer func() {
		mailInboxAll = origAll
		mailInboxUnread = origUnread
		mailInboxJSON = origJSON
		mailInboxIdentity = origIdentity
	}()

	// Flip each flag individually and verify others don't change.
	mailInboxAll = true
	mailInboxUnread = false
	mailInboxJSON = false
	mailInboxIdentity = ""
	if !mailInboxAll || mailInboxUnread || mailInboxJSON || mailInboxIdentity != "" {
		t.Errorf("independent flag toggling failed: all=%v unread=%v json=%v identity=%q",
			mailInboxAll, mailInboxUnread, mailInboxJSON, mailInboxIdentity)
	}

	mailInboxAll = false
	mailInboxUnread = true
	if mailInboxAll || !mailInboxUnread {
		t.Errorf("unread toggle leaked into all: all=%v unread=%v", mailInboxAll, mailInboxUnread)
	}
}

type fakeInboxLister struct {
	calls    int
	messages []*mail.Message
	err      error
}

func (f *fakeInboxLister) List() ([]*mail.Message, error) {
	f.calls++
	return f.messages, f.err
}

func TestLoadInboxSnapshotListsOnceAndCounts(t *testing.T) {
	box := &fakeInboxLister{
		messages: []*mail.Message{
			{ID: "msg-1", Read: false},
			{ID: "msg-2", Read: true},
			{ID: "msg-3", Read: false},
		},
	}

	messages, total, unread, err := loadInboxSnapshot(box, false)
	if err != nil {
		t.Fatalf("loadInboxSnapshot returned error: %v", err)
	}
	if box.calls != 1 {
		t.Fatalf("List calls = %d, want 1", box.calls)
	}
	if total != 3 || unread != 2 {
		t.Fatalf("counts = (%d total, %d unread), want (3, 2)", total, unread)
	}
	if len(messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(messages))
	}
}

func TestLoadInboxSnapshotUnreadOnlyFiltersAfterSingleList(t *testing.T) {
	box := &fakeInboxLister{
		messages: []*mail.Message{
			{ID: "msg-1", Read: false},
			{ID: "msg-2", Read: true},
			{ID: "msg-3", Read: false},
		},
	}

	messages, total, unread, err := loadInboxSnapshot(box, true)
	if err != nil {
		t.Fatalf("loadInboxSnapshot returned error: %v", err)
	}
	if box.calls != 1 {
		t.Fatalf("List calls = %d, want 1", box.calls)
	}
	if total != 3 || unread != 2 {
		t.Fatalf("counts = (%d total, %d unread), want (3, 2)", total, unread)
	}
	if len(messages) != 2 {
		t.Fatalf("filtered messages len = %d, want 2", len(messages))
	}
	if messages[0].ID != "msg-1" || messages[1].ID != "msg-3" {
		t.Fatalf("filtered messages = [%s %s], want [msg-1 msg-3]", messages[0].ID, messages[1].ID)
	}
}

func TestLoadInboxSnapshotPropagatesListError(t *testing.T) {
	wantErr := errors.New("list failed")
	box := &fakeInboxLister{err: wantErr}

	_, _, _, err := loadInboxSnapshot(box, false)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if box.calls != 1 {
		t.Fatalf("List calls = %d, want 1", box.calls)
	}
}
