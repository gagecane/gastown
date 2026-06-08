package cmd

import (
	"github.com/steveyegge/gastown/internal/mail"
)

// mailboxQuiescer is the minimal mailbox surface needed to mark stale inbox
// mail read at polecat-reuse time. *mail.Mailbox satisfies it; tests use a stub.
type mailboxQuiescer interface {
	ListUnread() ([]*mail.Message, error)
	MarkReadOnly(id string) error
}

// quiesceReusedPolecatMail marks every currently-unread message in a reused
// idle polecat's inbox as read, so a stale message about a PRIOR assignment is
// not re-surfaced by `gt mail check --inject` on the next prime. (gs-lgof)
//
// When an idle polecat is reused, it is being freshly dispatched a new hook
// bead. Any mail already sitting unread in its inbox predates this dispatch and
// therefore concerns a prior (closed/escalated) assignment — new mail for the
// new assignment has not arrived yet. Left unread, prime injects that stale mail
// into the fresh session's context, where the agent can conflate it with the new
// hook bead, conclude "already done / nothing to ship", and silently no-code-
// close the freshly-hooked bead (the gs-lgof false-close class).
//
// Messages are marked read, NOT deleted: they remain visible via
// `gt mail inbox --all` for audit, they just stop being auto-injected.
func quiesceReusedPolecatMail(address string) (int, error) {
	mailbox, err := getMailbox(address)
	if err != nil {
		return 0, err
	}
	return quiesceMailbox(mailbox)
}

// quiesceMailbox marks all unread messages read and returns the count marked.
// Best-effort: a per-message failure is recorded but does not abort the sweep.
func quiesceMailbox(mailbox mailboxQuiescer) (int, error) {
	unread, err := mailbox.ListUnread()
	if err != nil {
		return 0, err
	}
	marked := 0
	var firstErr error
	for _, msg := range unread {
		if err := mailbox.MarkReadOnly(msg.ID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		marked++
	}
	return marked, firstErr
}
