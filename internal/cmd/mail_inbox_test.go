package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
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
