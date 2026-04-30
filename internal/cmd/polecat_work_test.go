package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/mail"
)

// TestFilterHookableMessages verifies that we only classify messages as
// "hookable" when their body carries one of the molecule references
// recognized by gt mol attach-from-mail (attached_molecule:, molecule_id:,
// molecule:, mol:).
func TestFilterHookableMessages(t *testing.T) {
	msgs := []*mail.Message{
		{ID: "mail-1", Subject: "plain info", Body: "just a note, no molecule here"},
		{ID: "mail-2", Subject: "task with molecule",
			Body: "Please handle this.\nattached_molecule: gu-wisp-abcd\n"},
		{ID: "mail-3", Subject: "molecule via alt key",
			Body: "molecule_id: gu-wisp-efgh"},
		{ID: "mail-4", Subject: "short form",
			Body: "mol: gu-wisp-ijkl"},
		nil, // defensive: helper must skip nils
		{ID: "mail-5", Subject: "another plain", Body: "nothing here"},
	}

	got := filterHookableMessages(msgs)
	if len(got) != 3 {
		t.Fatalf("expected 3 hookable messages, got %d", len(got))
	}
	wantIDs := map[string]bool{"mail-2": true, "mail-3": true, "mail-4": true}
	for _, m := range got {
		if !wantIDs[m.ID] {
			t.Errorf("unexpected hookable message in result: %s", m.ID)
		}
	}
}

// TestFilterHookableMessagesEmpty ensures empty/nil input yields empty
// result (and, importantly, does not panic).
func TestFilterHookableMessagesEmpty(t *testing.T) {
	if got := filterHookableMessages(nil); len(got) != 0 {
		t.Errorf("expected 0 from nil input, got %d", len(got))
	}
	if got := filterHookableMessages([]*mail.Message{}); len(got) != 0 {
		t.Errorf("expected 0 from empty input, got %d", len(got))
	}
}

// TestFilterHookableMessagesNoneMatch makes sure messages lacking any
// molecule-key reference are filtered out entirely. The extractor requires
// a literal "<keyword>:" prefix, so bodies that merely mention "molecule"
// as part of another word are not matches.
func TestFilterHookableMessagesNoneMatch(t *testing.T) {
	msgs := []*mail.Message{
		{ID: "a", Body: "hello"},
		{ID: "b", Body: "world"},
		// "molecule" appears but as part of an identifier, not followed
		// directly by ":" at a recognized keyword position.
		{ID: "c", Body: "no_molecule_field_here: gu-wisp-x"},
	}
	got := filterHookableMessages(msgs)
	if len(got) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(got))
	}
}

// TestPolecatWorkCmdRegistered is a sanity check that the subcommand is
// wired up on the polecat command with the right shape. This guards against
// future refactors that might disconnect it from the command tree.
func TestPolecatWorkCmdRegistered(t *testing.T) {
	var found bool
	for _, c := range polecatCmd.Commands() {
		if c.Name() == "work" {
			found = true
			if c.Annotations[AnnotationPolecatSafe] != "true" {
				t.Errorf("gt polecat work: expected AnnotationPolecatSafe=true, got %q",
					c.Annotations[AnnotationPolecatSafe])
			}
			break
		}
	}
	if !found {
		t.Fatalf("gt polecat work: expected 'work' subcommand on polecat command; not found")
	}
}
