package cmd

import (
	"errors"
	"testing"

	"github.com/steveyegge/gastown/internal/mail"
)

// fakeMailbox is an in-memory mailboxQuiescer for testing quiesceMailbox.
type fakeMailbox struct {
	msgs      []*mail.Message
	listErr   error
	markErrID string // ID for which MarkReadOnly returns an error
}

func (f *fakeMailbox) ListUnread() ([]*mail.Message, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var unread []*mail.Message
	for _, m := range f.msgs {
		if !m.Read {
			unread = append(unread, m)
		}
	}
	return unread, nil
}

func (f *fakeMailbox) MarkReadOnly(id string) error {
	if id == f.markErrID {
		return errors.New("simulated mark-read failure")
	}
	for _, m := range f.msgs {
		if m.ID == id {
			m.Read = true
			return nil
		}
	}
	return mail.ErrMessageNotFound
}

func unreadCount(msgs []*mail.Message) int {
	n := 0
	for _, m := range msgs {
		if !m.Read {
			n++
		}
	}
	return n
}

// TestQuiesceMailbox_StaleMailMarkedRead is the gs-lgof regression: a reused
// polecat's inbox carrying stale mail about a PRIOR (closed) assignment must be
// quiesced so the next prime does not inject it. After quiesce, no unread mail
// remains, so `gt mail check --inject` (which surfaces only unread) shows nothing
// and the agent works the freshly-hooked bead per its formula.
func TestQuiesceMailbox_StaleMailMarkedRead(t *testing.T) {
	fb := &fakeMailbox{
		msgs: []*mail.Message{
			{ID: "hq-wisp-5ffs", Subject: "wisp: prior assignment li-wfs-ezrmc", Read: false},
			{ID: "hq-old-2", Subject: "POLECAT_DONE prior", Read: false},
			{ID: "hq-already-read", Subject: "seen", Read: true},
		},
	}

	marked, err := quiesceMailbox(fb)
	if err != nil {
		t.Fatalf("quiesceMailbox returned error: %v", err)
	}
	if marked != 2 {
		t.Fatalf("marked = %d, want 2", marked)
	}
	if got := unreadCount(fb.msgs); got != 0 {
		t.Fatalf("unread after quiesce = %d, want 0 (stale mail must not survive to next prime)", got)
	}
}

// TestQuiesceMailbox_EmptyInbox is a no-op on an empty/all-read inbox.
func TestQuiesceMailbox_EmptyInbox(t *testing.T) {
	fb := &fakeMailbox{
		msgs: []*mail.Message{
			{ID: "hq-read", Subject: "seen", Read: true},
		},
	}
	marked, err := quiesceMailbox(fb)
	if err != nil {
		t.Fatalf("quiesceMailbox returned error: %v", err)
	}
	if marked != 0 {
		t.Fatalf("marked = %d, want 0", marked)
	}
}

// TestQuiesceMailbox_ListError surfaces a list failure to the caller.
func TestQuiesceMailbox_ListError(t *testing.T) {
	fb := &fakeMailbox{listErr: errors.New("dolt unavailable")}
	if _, err := quiesceMailbox(fb); err == nil {
		t.Fatal("expected error from ListUnread failure, got nil")
	}
}

// TestQuiesceMailbox_PartialFailure keeps sweeping past a per-message failure
// and reports both the successes and the first error (best-effort).
func TestQuiesceMailbox_PartialFailure(t *testing.T) {
	fb := &fakeMailbox{
		markErrID: "hq-bad",
		msgs: []*mail.Message{
			{ID: "hq-good-1", Read: false},
			{ID: "hq-bad", Read: false},
			{ID: "hq-good-2", Read: false},
		},
	}
	marked, err := quiesceMailbox(fb)
	if err == nil {
		t.Fatal("expected first-error to be reported, got nil")
	}
	if marked != 2 {
		t.Fatalf("marked = %d, want 2 (sweep continues past failure)", marked)
	}
}
