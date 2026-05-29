// Copyright (c) Steve Yegge. Licensed under the MIT License.

package daemon

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/witness"
)

func TestIsRestartPolecatMessage(t *testing.T) {
	tests := []struct {
		subject string
		want    bool
	}{
		{"RESTART_POLECAT: rig/cat", true},
		{"RESTART_POLECAT: rig/cat (zombie cleared)", true},
		{"RESTART_POLECAT: rig/cat (stalled-alive cleared)", true},
		{"  RESTART_POLECAT: rig/cat  ", true},
		{"RESTART_POLECAT:", false},                  // bare prefix — no payload
		{"RESTART_POLECAT: ", false},                 // bare prefix with trailing space
		{"NUKE_PENDING: rig/cat", false},             // different subject family
		{"Re: RESTART_POLECAT: rig/cat", false},      // forwarded mail — not a fresh request
		{"restart_polecat: rig/cat", false},          // case-sensitive
		{"", false},
	}
	for _, tc := range tests {
		got := IsRestartPolecatMessage(tc.subject)
		if got != tc.want {
			t.Errorf("IsRestartPolecatMessage(%q) = %v, want %v", tc.subject, got, tc.want)
		}
	}
}

func TestParseRestartPolecatSubject(t *testing.T) {
	tests := []struct {
		subject string
		rig     string
		polecat string
		ok      bool
	}{
		{"RESTART_POLECAT: gastown_upstream/nitro", "gastown_upstream", "nitro", true},
		{"RESTART_POLECAT: rig-a/dust (zombie cleared)", "rig-a", "dust", true},
		{"RESTART_POLECAT: rig-a/dust (stalled-alive cleared)", "rig-a", "dust", true},
		{"  RESTART_POLECAT:   rig/cat   ", "rig", "cat", true},
		{"RESTART_POLECAT: rig/", "", "", false},     // empty polecat
		{"RESTART_POLECAT: /cat", "", "", false},     // empty rig
		{"RESTART_POLECAT: badformat", "", "", false}, // no slash
		{"RESTART_POLECAT:", "", "", false},
		{"NUKE_PENDING: rig/cat", "", "", false},
	}
	for _, tc := range tests {
		rig, polecat, ok := ParseRestartPolecatSubject(tc.subject)
		if ok != tc.ok || rig != tc.rig || polecat != tc.polecat {
			t.Errorf("ParseRestartPolecatSubject(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.subject, rig, polecat, ok, tc.rig, tc.polecat, tc.ok)
		}
	}
}

// TestProcessRestartPolecatMessageList_Restarts verifies the happy path:
// a fresh RESTART_POLECAT mail triggers a restart and the message is
// claimed (deleted) before the restart runs. Core gu-nep2 fix.
func TestProcessRestartPolecatMessageList_Restarts(t *testing.T) {
	d := testHandlerDaemon(t, t.TempDir())

	messages := []BeadsMessage{
		{
			ID:        "msg-1",
			From:      "deacon/dogs/alpha",
			Subject:   "RESTART_POLECAT: rig-a/cat-1",
			Timestamp: time.Now().Format(time.RFC3339),
		},
	}

	var deleted []string
	deleter := func(id string) error {
		deleted = append(deleted, id)
		return nil
	}

	type call struct{ rig, polecat string }
	var calls []call
	restartFn := func(workDir, rig, polecat string) error {
		calls = append(calls, call{rig, polecat})
		return nil
	}

	processed := d.processRestartPolecatMessageList(messages, deleter, restartFn)

	if len(processed) != 1 || processed[0].Outcome != "restarted" {
		t.Fatalf("processed = %+v, want one restarted entry", processed)
	}
	if processed[0].Rig != "rig-a" || processed[0].Polecat != "cat-1" {
		t.Errorf("processed[0] rig/polecat = %s/%s, want rig-a/cat-1", processed[0].Rig, processed[0].Polecat)
	}
	if len(calls) != 1 || calls[0] != (call{"rig-a", "cat-1"}) {
		t.Errorf("restartFn calls = %+v, want one call with rig-a/cat-1", calls)
	}
	if len(deleted) != 1 || deleted[0] != "msg-1" {
		t.Errorf("deleted IDs = %v, want [msg-1]", deleted)
	}
}

// TestProcessRestartPolecatMessageList_StaleSkipped verifies that a
// stale RESTART_POLECAT mail is deleted but never triggers a restart. This
// matches the gu-nep2 hypothesis: stale requests should not cause
// double-start when the polecat has been restarted by another path.
func TestProcessRestartPolecatMessageList_StaleSkipped(t *testing.T) {
	d := testHandlerDaemon(t, t.TempDir())

	messages := []BeadsMessage{
		{
			ID:        "msg-stale",
			From:      "deacon/dogs/alpha",
			Subject:   "RESTART_POLECAT: rig-a/cat-1",
			Timestamp: time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
		},
	}

	var deleted []string
	deleter := func(id string) error { deleted = append(deleted, id); return nil }
	calls := 0
	restartFn := func(string, string, string) error { calls++; return nil }

	processed := d.processRestartPolecatMessageList(messages, deleter, restartFn)

	if len(processed) != 1 || processed[0].Outcome != "stale" {
		t.Errorf("processed = %+v, want one stale entry", processed)
	}
	if calls != 0 {
		t.Errorf("restartFn called %d times, want 0", calls)
	}
	if len(deleted) != 1 {
		t.Errorf("deleted = %v, want stale message to be deleted", deleted)
	}
}

// TestProcessRestartPolecatMessageList_UnparseableDeleted verifies that
// a malformed subject is deleted (so it doesn't pin the inbox) but never
// triggers a restart against an invalid target.
func TestProcessRestartPolecatMessageList_UnparseableDeleted(t *testing.T) {
	d := testHandlerDaemon(t, t.TempDir())

	messages := []BeadsMessage{
		{
			ID:        "msg-bad",
			From:      "deacon/dogs/alpha",
			Subject:   "RESTART_POLECAT: badformat",
			Timestamp: time.Now().Format(time.RFC3339),
		},
	}

	var deleted []string
	deleter := func(id string) error { deleted = append(deleted, id); return nil }
	calls := 0
	restartFn := func(string, string, string) error { calls++; return nil }

	processed := d.processRestartPolecatMessageList(messages, deleter, restartFn)

	if len(processed) != 1 || processed[0].Outcome != "unparseable" {
		t.Errorf("processed = %+v, want unparseable entry", processed)
	}
	if calls != 0 {
		t.Errorf("restartFn called %d times, want 0", calls)
	}
	if len(deleted) != 1 || deleted[0] != "msg-bad" {
		t.Errorf("deleted = %v, want [msg-bad]", deleted)
	}
}

// TestProcessRestartPolecatMessageList_IgnoresUnrelated verifies that
// non-RESTART_POLECAT mail (LIFECYCLE, DOG_DONE, NUKE_PENDING, etc.) is
// passed over without deletion or restart calls.
func TestProcessRestartPolecatMessageList_IgnoresUnrelated(t *testing.T) {
	d := testHandlerDaemon(t, t.TempDir())

	now := time.Now().Format(time.RFC3339)
	messages := []BeadsMessage{
		{ID: "m1", Subject: "LIFECYCLE: cycle", From: "mayor/", Timestamp: now},
		{ID: "m2", Subject: "DOG_DONE alpha", From: "deacon/dogs/alpha", Timestamp: now},
		{ID: "m3", Subject: "NUKE_PENDING: rig-a/cat-1", From: "deacon/dogs/alpha", Timestamp: now},
	}

	var deleted []string
	deleter := func(id string) error { deleted = append(deleted, id); return nil }
	calls := 0
	restartFn := func(string, string, string) error { calls++; return nil }

	processed := d.processRestartPolecatMessageList(messages, deleter, restartFn)

	if len(processed) != 0 {
		t.Errorf("processed = %+v, want empty (no RESTART_POLECAT messages)", processed)
	}
	if calls != 0 {
		t.Errorf("restartFn called %d times, want 0", calls)
	}
	if len(deleted) != 0 {
		t.Errorf("deleted = %v, want []", deleted)
	}
}

// TestProcessRestartPolecatMessageList_SkipsRead verifies idempotency:
// already-read messages are skipped so reprocessing the inbox doesn't
// double-restart polecats.
func TestProcessRestartPolecatMessageList_SkipsRead(t *testing.T) {
	d := testHandlerDaemon(t, t.TempDir())

	messages := []BeadsMessage{
		{
			ID:        "msg-read",
			From:      "deacon/dogs/alpha",
			Subject:   "RESTART_POLECAT: rig-a/cat-1",
			Timestamp: time.Now().Format(time.RFC3339),
			Read:      true,
		},
	}

	calls := 0
	restartFn := func(string, string, string) error { calls++; return nil }

	processed := d.processRestartPolecatMessageList(messages, func(string) error { return nil }, restartFn)

	if len(processed) != 0 {
		t.Errorf("processed = %+v, want empty (read message)", processed)
	}
	if calls != 0 {
		t.Errorf("restartFn called %d times, want 0", calls)
	}
}

// TestProcessRestartPolecatMessageList_BackoffSkipNotFailure verifies
// that an ErrPolecatInStartupBackoff return from the restart primitive
// is accounted as a deliberate skip, not a failure. This is the gs-549
// crash-loop guard surface — under sustained polecat startup failure,
// the witness backoff layer refuses to hammer the slot, and we must
// not flag that as an error.
func TestProcessRestartPolecatMessageList_BackoffSkipNotFailure(t *testing.T) {
	d := testHandlerDaemon(t, t.TempDir())

	messages := []BeadsMessage{
		{
			ID:        "msg-backoff",
			From:      "deacon/dogs/alpha",
			Subject:   "RESTART_POLECAT: rig-a/cat-1",
			Timestamp: time.Now().Format(time.RFC3339),
		},
	}

	deleter := func(string) error { return nil }
	restartFn := func(string, string, string) error {
		return fmt.Errorf("%w: synthetic", witness.ErrPolecatInStartupBackoff)
	}

	processed := d.processRestartPolecatMessageList(messages, deleter, restartFn)

	if len(processed) != 1 || processed[0].Outcome != "skipped-backoff" {
		t.Fatalf("processed = %+v, want skipped-backoff", processed)
	}
	if !errors.Is(processed[0].Err, witness.ErrPolecatInStartupBackoff) {
		t.Errorf("processed[0].Err = %v, want wrapping ErrPolecatInStartupBackoff", processed[0].Err)
	}
}

// TestProcessRestartPolecatMessageList_RestartFailureRecorded verifies
// that a restart primitive that returns an arbitrary error is recorded
// as restart-failed (not skipped) so operators can see real problems in
// the audit trail.
func TestProcessRestartPolecatMessageList_RestartFailureRecorded(t *testing.T) {
	d := testHandlerDaemon(t, t.TempDir())

	messages := []BeadsMessage{
		{
			ID:        "msg-fail",
			From:      "deacon/dogs/alpha",
			Subject:   "RESTART_POLECAT: rig-a/cat-1",
			Timestamp: time.Now().Format(time.RFC3339),
		},
	}

	deleter := func(string) error { return nil }
	restartFn := func(string, string, string) error {
		return errors.New("tmux wedged")
	}

	processed := d.processRestartPolecatMessageList(messages, deleter, restartFn)

	if len(processed) != 1 || processed[0].Outcome != "restart-failed" {
		t.Fatalf("processed = %+v, want restart-failed", processed)
	}
}

// TestProcessRestartPolecatMessageList_MultipleRequestsAllActioned is the
// regression test for the gu-nep2 incident: dogs filed 3 RESTART_POLECAT
// requests in a single dispatch cycle for nitro and the deacon reported
// "0 new" on each. Every RESTART_POLECAT mail in the inbox must be
// individually picked up and actioned, not deduplicated to nothing.
func TestProcessRestartPolecatMessageList_MultipleRequestsAllActioned(t *testing.T) {
	d := testHandlerDaemon(t, t.TempDir())

	now := time.Now().Format(time.RFC3339)
	messages := []BeadsMessage{
		{ID: "n1", From: "deacon/dogs/alpha", Subject: "RESTART_POLECAT: gastown_upstream/nitro", Timestamp: now},
		{ID: "n2", From: "deacon/dogs/alpha", Subject: "RESTART_POLECAT: gastown_upstream/nitro", Timestamp: now},
		{ID: "n3", From: "deacon/dogs/bravo", Subject: "RESTART_POLECAT: gastown_upstream/nitro", Timestamp: now},
	}

	var deleted []string
	deleter := func(id string) error { deleted = append(deleted, id); return nil }
	calls := 0
	restartFn := func(string, string, string) error { calls++; return nil }

	processed := d.processRestartPolecatMessageList(messages, deleter, restartFn)

	if len(processed) != 3 {
		t.Errorf("processed count = %d, want 3", len(processed))
	}
	if calls != 3 {
		t.Errorf("restartFn calls = %d, want 3 (every request must be picked up)", calls)
	}
	if len(deleted) != 3 {
		t.Errorf("deleted = %v, want all 3 messages claimed", deleted)
	}
}
