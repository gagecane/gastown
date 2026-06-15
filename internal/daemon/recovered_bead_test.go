// Copyright (c) Steve Yegge. Licensed under the MIT License.

package daemon

import (
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/deacon"
)

func TestParseRecoveredBeadSubject(t *testing.T) {
	tests := []struct {
		subject string
		bead    string
		ok      bool
	}{
		{"RECOVERED_BEAD gu-abc123", "gu-abc123", true},
		{"  RECOVERED_BEAD   gu-abc123  ", "gu-abc123", true},
		{"SPAWN_STORM RECOVERED_BEAD gu-abc123 (respawned 3x)", "gu-abc123", true},
		{"RECOVERED_BEAD gu-abc123 (respawned 3x)", "gu-abc123", true},
		{"RECOVERED_BEAD", "", false},  // bare token, no bead
		{"RECOVERED_BEAD ", "", false}, // bare token with trailing space
		{"RESTART_POLECAT: rig/cat", "", false},
		{"MERGE_READY: gu-abc123", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		bead, ok := ParseRecoveredBeadSubject(tc.subject)
		if ok != tc.ok || bead != tc.bead {
			t.Errorf("ParseRecoveredBeadSubject(%q) = (%q, %v), want (%q, %v)",
				tc.subject, bead, ok, tc.bead, tc.ok)
		}
	}
}

// recoveredBeadCall records one invocation of the stub redispatch func.
type recoveredBeadCall struct {
	beadID    string
	sourceRig string
}

// TestProcessRecoveredBeadMessageList_Redispatches verifies the core gu-jbcag
// fix: a past-grace RECOVERED_BEAD mail triggers a re-dispatch and the mail is
// deleted (claimed) on the terminal "redispatched" outcome.
func TestProcessRecoveredBeadMessageList_Redispatches(t *testing.T) {
	d := testHandlerDaemon(t, t.TempDir())

	messages := []BeadsMessage{
		{
			ID:        "msg-1",
			From:      "rig-a/witness",
			Subject:   "RECOVERED_BEAD gu-abc123",
			Body:      "Recovered abandoned bead from dead polecat.\n\nBead: gu-abc123\nPolecat: rig-a/cat-1\n",
			Timestamp: time.Now().Add(-30 * time.Minute).Format(time.RFC3339),
		},
	}

	var calls []recoveredBeadCall
	redispatchFn := func(beadID, sourceRig string) *deacon.RedispatchResult {
		calls = append(calls, recoveredBeadCall{beadID, sourceRig})
		return &deacon.RedispatchResult{BeadID: beadID, Action: "redispatched", TargetRig: "rig-a"}
	}
	var deleted []string
	deleter := func(id string) error { deleted = append(deleted, id); return nil }

	processed := d.processRecoveredBeadMessageList(messages, 15*time.Minute, deleter, redispatchFn)

	if len(processed) != 1 || processed[0].Outcome != "redispatched" {
		t.Fatalf("processed = %+v, want one redispatched entry", processed)
	}
	if processed[0].BeadID != "gu-abc123" || !processed[0].Deleted {
		t.Errorf("processed[0] = %+v, want bead gu-abc123 deleted", processed[0])
	}
	if len(calls) != 1 || calls[0] != (recoveredBeadCall{"gu-abc123", "rig-a"}) {
		t.Errorf("redispatch calls = %+v, want one call gu-abc123/rig-a", calls)
	}
	if len(deleted) != 1 || deleted[0] != "msg-1" {
		t.Errorf("deleted = %v, want [msg-1]", deleted)
	}
}

// TestProcessRecoveredBeadMessageList_WithinGraceSkipped verifies the Deacon
// keeps first crack: a fresh RECOVERED_BEAD inside the grace window is left
// untouched (no redispatch, no delete).
func TestProcessRecoveredBeadMessageList_WithinGraceSkipped(t *testing.T) {
	d := testHandlerDaemon(t, t.TempDir())

	messages := []BeadsMessage{
		{
			ID:        "msg-fresh",
			From:      "rig-a/witness",
			Subject:   "RECOVERED_BEAD gu-abc123",
			Timestamp: time.Now().Add(-1 * time.Minute).Format(time.RFC3339),
		},
	}

	calls := 0
	redispatchFn := func(string, string) *deacon.RedispatchResult { calls++; return &deacon.RedispatchResult{} }
	var deleted []string
	deleter := func(id string) error { deleted = append(deleted, id); return nil }

	processed := d.processRecoveredBeadMessageList(messages, 15*time.Minute, deleter, redispatchFn)

	if len(processed) != 1 || processed[0].Outcome != "within-grace" {
		t.Errorf("processed = %+v, want one within-grace entry", processed)
	}
	if calls != 0 {
		t.Errorf("redispatchFn called %d times, want 0", calls)
	}
	if len(deleted) != 0 {
		t.Errorf("deleted = %v, want [] (mail left for Deacon)", deleted)
	}
}

// TestProcessRecoveredBeadMessageList_EscalatedDeleted verifies that an
// escalate outcome is terminal: mail is deleted so it doesn't re-fire.
func TestProcessRecoveredBeadMessageList_EscalatedDeleted(t *testing.T) {
	d := testHandlerDaemon(t, t.TempDir())

	messages := []BeadsMessage{
		{
			ID:        "msg-esc",
			From:      "rig-a/witness",
			Subject:   "SPAWN_STORM RECOVERED_BEAD gu-xyz (respawned 4x)",
			Timestamp: time.Now().Add(-30 * time.Minute).Format(time.RFC3339),
		},
	}

	redispatchFn := func(beadID, _ string) *deacon.RedispatchResult {
		return &deacon.RedispatchResult{BeadID: beadID, Action: "escalated"}
	}
	var deleted []string
	deleter := func(id string) error { deleted = append(deleted, id); return nil }

	processed := d.processRecoveredBeadMessageList(messages, 15*time.Minute, deleter, redispatchFn)

	if len(processed) != 1 || processed[0].Outcome != "escalated" {
		t.Fatalf("processed = %+v, want one escalated entry", processed)
	}
	if processed[0].BeadID != "gu-xyz" || !processed[0].Deleted {
		t.Errorf("processed[0] = %+v, want bead gu-xyz deleted", processed[0])
	}
	if len(deleted) != 1 || deleted[0] != "msg-esc" {
		t.Errorf("deleted = %v, want [msg-esc]", deleted)
	}
}

// TestProcessRecoveredBeadMessageList_TransientLeftPinned verifies that a
// transient cooldown/error outcome leaves the mail pinned so a later cycle
// retries — the daemon must never silently drop a recovered bead.
func TestProcessRecoveredBeadMessageList_TransientLeftPinned(t *testing.T) {
	for _, action := range []string{"cooldown", "error"} {
		t.Run(action, func(t *testing.T) {
			d := testHandlerDaemon(t, t.TempDir())

			messages := []BeadsMessage{
				{
					ID:        "msg-t",
					From:      "rig-a/witness",
					Subject:   "RECOVERED_BEAD gu-abc123",
					Timestamp: time.Now().Add(-30 * time.Minute).Format(time.RFC3339),
				},
			}

			redispatchFn := func(beadID, _ string) *deacon.RedispatchResult {
				r := &deacon.RedispatchResult{BeadID: beadID, Action: action}
				if action == "error" {
					r.Error = fmt.Errorf("transient")
				}
				return r
			}
			var deleted []string
			deleter := func(id string) error { deleted = append(deleted, id); return nil }

			processed := d.processRecoveredBeadMessageList(messages, 15*time.Minute, deleter, redispatchFn)

			if len(processed) != 1 || processed[0].Outcome != action {
				t.Fatalf("processed = %+v, want one %s entry", processed, action)
			}
			if processed[0].Deleted {
				t.Errorf("processed[0].Deleted = true, want false (mail must stay pinned on %s)", action)
			}
			if len(deleted) != 0 {
				t.Errorf("deleted = %v, want [] (mail must stay pinned on %s)", deleted, action)
			}
		})
	}
}

// TestProcessRecoveredBeadMessageList_SkipsReadAndUnrelated verifies that
// already-read messages and non-RECOVERED_BEAD mail are passed over without
// redispatch or deletion.
func TestProcessRecoveredBeadMessageList_SkipsReadAndUnrelated(t *testing.T) {
	d := testHandlerDaemon(t, t.TempDir())

	now := time.Now().Add(-30 * time.Minute).Format(time.RFC3339)
	messages := []BeadsMessage{
		{ID: "m-read", Subject: "RECOVERED_BEAD gu-abc123", From: "rig-a/witness", Timestamp: now, Read: true},
		{ID: "m-other", Subject: "RESTART_POLECAT: rig-a/cat-1", From: "deacon/dogs/x", Timestamp: now},
		{ID: "m-lifecycle", Subject: "LIFECYCLE: cycle", From: "mayor/", Timestamp: now},
	}

	calls := 0
	redispatchFn := func(string, string) *deacon.RedispatchResult { calls++; return &deacon.RedispatchResult{} }
	var deleted []string
	deleter := func(id string) error { deleted = append(deleted, id); return nil }

	processed := d.processRecoveredBeadMessageList(messages, 15*time.Minute, deleter, redispatchFn)

	if len(processed) != 0 {
		t.Errorf("processed = %+v, want empty", processed)
	}
	if calls != 0 {
		t.Errorf("redispatchFn called %d times, want 0", calls)
	}
	if len(deleted) != 0 {
		t.Errorf("deleted = %v, want []", deleted)
	}
}

// TestProcessRecoveredBeadMessageList_UnparseableTimestampActs verifies that a
// recognized subject with a bad timestamp is acted on (treated as past-grace)
// rather than stranded.
func TestProcessRecoveredBeadMessageList_UnparseableTimestampActs(t *testing.T) {
	d := testHandlerDaemon(t, t.TempDir())

	messages := []BeadsMessage{
		{
			ID:        "msg-badts",
			From:      "rig-a/witness",
			Subject:   "RECOVERED_BEAD gu-abc123",
			Timestamp: "not-a-timestamp",
		},
	}

	calls := 0
	redispatchFn := func(beadID, _ string) *deacon.RedispatchResult {
		calls++
		return &deacon.RedispatchResult{BeadID: beadID, Action: "redispatched"}
	}
	deleter := func(string) error { return nil }

	processed := d.processRecoveredBeadMessageList(messages, 15*time.Minute, deleter, redispatchFn)

	if len(processed) != 1 || processed[0].Outcome != "redispatched" {
		t.Fatalf("processed = %+v, want one redispatched entry", processed)
	}
	if calls != 1 {
		t.Errorf("redispatchFn called %d times, want 1", calls)
	}
}
