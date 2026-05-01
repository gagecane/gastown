package daemon

import (
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/dog"
)

func TestIsDogDoneMessage(t *testing.T) {
	tests := []struct {
		subject string
		want    bool
	}{
		{"DOG_DONE alpha", true},
		{"DOG_DONE: backup", true},
		{"DOG_DONE\talpha", true},
		{"DOG_DONE", true}, // bare subject is also accepted — from field identifies dog
		{"  DOG_DONE alpha  ", true},
		{"DOG_DONEISH", false}, // no delimiter — not a DOG_DONE message
		{"dog_done alpha", false}, // case-sensitive on purpose (keeps signal explicit)
		{"Re: DOG_DONE alpha", false},
		{"", false},
		{"Plugin: orphan-scan", false},
		{"LIFECYCLE: cycle", false},
	}

	for _, tc := range tests {
		got := isDogDoneMessage(tc.subject)
		if got != tc.want {
			t.Errorf("isDogDoneMessage(%q) = %v, want %v", tc.subject, got, tc.want)
		}
	}
}

func TestExtractDogNameFromSender(t *testing.T) {
	tests := []struct {
		from string
		want string
	}{
		{"deacon/dogs/alpha", "alpha"},
		{"deacon/dogs/my-dog", "my-dog"},
		{"deacon/dogs/alpha/", "alpha"},  // trailing slash ignored
		{"deacon/dogs/alpha/sub", "alpha"}, // sub-path stripped defensively
		{"deacon/dogs/", ""},               // no name — reject
		{"deacon", ""},                      // not a dog sender
		{"mayor/", ""},
		{"gastown_upstream/polecats/dust", ""},
		{"", ""},
	}

	for _, tc := range tests {
		got := extractDogNameFromSender(tc.from)
		if got != tc.want {
			t.Errorf("extractDogNameFromSender(%q) = %q, want %q", tc.from, got, tc.want)
		}
	}
}

// TestProcessDogDoneMessageList_ClearsWorkingDog verifies that a DOG_DONE
// mail from a working dog transitions the dog back to idle. Core gu-7537 fix.
func TestProcessDogDoneMessageList_ClearsWorkingDog(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)

	// Create a dog that's currently working (the state that must be cleared).
	testSetupWorkingDogState(t, townRoot, "alpha", constants.MolDogReaper, time.Now())

	// Simulate a DOG_DONE mail from alpha.
	messages := []BeadsMessage{
		{
			ID:        "msg-1",
			From:      "deacon/dogs/alpha",
			To:        "deacon/",
			Subject:   "DOG_DONE alpha",
			Body:      "Task: mol-dog-reaper\nStatus: COMPLETE",
			Timestamp: time.Now().Format(time.RFC3339),
		},
	}

	var deletedIDs []string
	deleter := func(id string) error {
		deletedIDs = append(deletedIDs, id)
		return nil
	}

	d.processDogDoneMessageList(mgr, messages, deleter)

	// Verify dog is now idle with no work.
	dg, err := mgr.Get("alpha")
	if err != nil {
		t.Fatalf("Get(alpha) error: %v", err)
	}
	if dg.State != dog.StateIdle {
		t.Errorf("alpha state = %q, want idle", dg.State)
	}
	if dg.Work != "" {
		t.Errorf("alpha work = %q, want empty", dg.Work)
	}

	// Verify the message was deleted.
	if len(deletedIDs) != 1 || deletedIDs[0] != "msg-1" {
		t.Errorf("deleted IDs = %v, want [msg-1]", deletedIDs)
	}
}

// TestProcessDogDoneMessageList_ColonVariant verifies the "DOG_DONE: <task>"
// subject format (used by mol-dog-backup, mol-dog-doctor, etc.) also works.
func TestProcessDogDoneMessageList_ColonVariant(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)

	testSetupWorkingDogState(t, townRoot, "bravo", "mol-dog-backup", time.Now())

	messages := []BeadsMessage{
		{
			ID:      "msg-2",
			From:    "deacon/dogs/bravo",
			Subject: "DOG_DONE: backup",
			Body:    "Task: dolt-backup\nSynced: 3/3",
		},
	}

	d.processDogDoneMessageList(mgr, messages, func(string) error { return nil })

	dg, err := mgr.Get("bravo")
	if err != nil {
		t.Fatalf("Get(bravo) error: %v", err)
	}
	if dg.State != dog.StateIdle {
		t.Errorf("bravo state = %q, want idle", dg.State)
	}
}

// TestProcessDogDoneMessageList_IgnoresNonDogDone verifies that non-DOG_DONE
// mail is left untouched. The deacon inbox also receives LIFECYCLE and
// Plugin mail — we must not affect dog state on those.
func TestProcessDogDoneMessageList_IgnoresNonDogDone(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)

	testSetupWorkingDogState(t, townRoot, "charlie", "mol-session-gc", time.Now())

	messages := []BeadsMessage{
		{ID: "msg-3", From: "deacon/dogs/charlie", Subject: "Plugin: rebuild-gt"},
		{ID: "msg-4", From: "mayor/", Subject: "LIFECYCLE: cycle"},
		{ID: "msg-5", From: "deacon/dogs/charlie", Subject: "Re: DOG_DONE charlie"},
	}

	var deletedIDs []string
	d.processDogDoneMessageList(mgr, messages, func(id string) error {
		deletedIDs = append(deletedIDs, id)
		return nil
	})

	// Charlie should still be working.
	dg, err := mgr.Get("charlie")
	if err != nil {
		t.Fatalf("Get(charlie) error: %v", err)
	}
	if dg.State != dog.StateWorking {
		t.Errorf("charlie state = %q, want working", dg.State)
	}

	// No messages should have been deleted.
	if len(deletedIDs) != 0 {
		t.Errorf("deletedIDs = %v, want []", deletedIDs)
	}
}

// TestProcessDogDoneMessageList_SkipsReadMessages verifies that messages
// already marked as read are skipped, so reprocessing is idempotent.
func TestProcessDogDoneMessageList_SkipsReadMessages(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)

	testSetupWorkingDogState(t, townRoot, "delta", "mol-orphan-scan", time.Now())

	messages := []BeadsMessage{
		{
			ID:      "msg-6",
			From:    "deacon/dogs/delta",
			Subject: "DOG_DONE delta",
			Read:    true, // Already processed — should be skipped.
		},
	}

	var deletedIDs []string
	d.processDogDoneMessageList(mgr, messages, func(id string) error {
		deletedIDs = append(deletedIDs, id)
		return nil
	})

	// Delta should remain working — read messages are skipped.
	dg, err := mgr.Get("delta")
	if err != nil {
		t.Fatalf("Get(delta) error: %v", err)
	}
	if dg.State != dog.StateWorking {
		t.Errorf("delta state = %q, want working (read messages must not transition state)", dg.State)
	}
	if len(deletedIDs) != 0 {
		t.Errorf("deletedIDs = %v, want [] (read messages must not be re-deleted)", deletedIDs)
	}
}

// TestProcessDogDoneMessageList_NonDogSender verifies that DOG_DONE subject
// from a non-dog sender is ignored (defense against misconfigured senders).
func TestProcessDogDoneMessageList_NonDogSender(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)

	testSetupWorkingDogState(t, townRoot, "echo", "mol-dog-reaper", time.Now())

	messages := []BeadsMessage{
		{
			ID:      "msg-7",
			From:    "gastown_upstream/polecats/dust", // polecat, not a dog
			Subject: "DOG_DONE echo",
		},
	}

	var deletedIDs []string
	d.processDogDoneMessageList(mgr, messages, func(id string) error {
		deletedIDs = append(deletedIDs, id)
		return nil
	})

	// Echo must remain working — we only clear on mail from the dog itself.
	dg, err := mgr.Get("echo")
	if err != nil {
		t.Fatalf("Get(echo) error: %v", err)
	}
	if dg.State != dog.StateWorking {
		t.Errorf("echo state = %q, want working", dg.State)
	}
	if len(deletedIDs) != 0 {
		t.Errorf("deletedIDs = %v, want [] (must not delete non-dog DOG_DONE)", deletedIDs)
	}
}

// TestProcessDogDoneMessageList_UnknownDog verifies that DOG_DONE from a dog
// that no longer exists in the kennel is handled gracefully — the message
// is deleted but no crash.
func TestProcessDogDoneMessageList_UnknownDog(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)

	// No dog "ghost" in the kennel.

	messages := []BeadsMessage{
		{
			ID:      "msg-8",
			From:    "deacon/dogs/ghost",
			Subject: "DOG_DONE ghost",
		},
	}

	var deletedIDs []string
	d.processDogDoneMessageList(mgr, messages, func(id string) error {
		deletedIDs = append(deletedIDs, id)
		return nil
	})

	// The mail should still be deleted — stale messages must not accumulate.
	if len(deletedIDs) != 1 || deletedIDs[0] != "msg-8" {
		t.Errorf("deletedIDs = %v, want [msg-8]", deletedIDs)
	}
}

// TestProcessDogDoneMessageList_AlreadyIdle verifies that DOG_DONE from a dog
// that's already idle (maybe a duplicate mail or racy state) is a no-op.
func TestProcessDogDoneMessageList_AlreadyIdle(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)

	// Dog exists and is already idle (previous DOG_DONE processed, or dog
	// cleared via gt dog done / gt dog clear).
	testSetupDogState(t, townRoot, "foxtrot", dog.StateIdle, time.Now())

	messages := []BeadsMessage{
		{
			ID:      "msg-9",
			From:    "deacon/dogs/foxtrot",
			Subject: "DOG_DONE foxtrot",
		},
	}

	var deletedIDs []string
	d.processDogDoneMessageList(mgr, messages, func(id string) error {
		deletedIDs = append(deletedIDs, id)
		return nil
	})

	// Dog should still be idle.
	dg, err := mgr.Get("foxtrot")
	if err != nil {
		t.Fatalf("Get(foxtrot) error: %v", err)
	}
	if dg.State != dog.StateIdle {
		t.Errorf("foxtrot state = %q, want idle", dg.State)
	}
	// Message should be deleted — otherwise it re-triggers every heartbeat.
	if len(deletedIDs) != 1 || deletedIDs[0] != "msg-9" {
		t.Errorf("deletedIDs = %v, want [msg-9]", deletedIDs)
	}
}

// TestProcessDogDoneMessageList_DeleteFailureDoesNotBlockClear verifies that
// if mail deletion fails, the handler still clears the dog's state. This
// matches the ProcessLifecycleRequests "claim then execute" pattern: we'd
// rather have a stuck mail than a stuck dog.
func TestProcessDogDoneMessageList_DeleteFailureDoesNotBlockClear(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)

	testSetupWorkingDogState(t, townRoot, "golf", "mol-dog-reaper", time.Now())

	messages := []BeadsMessage{
		{ID: "msg-10", From: "deacon/dogs/golf", Subject: "DOG_DONE golf"},
	}

	failingDeleter := func(id string) error {
		return fmt.Errorf("simulated delete failure")
	}

	d.processDogDoneMessageList(mgr, messages, failingDeleter)

	// State transition must still happen even if delete failed.
	dg, err := mgr.Get("golf")
	if err != nil {
		t.Fatalf("Get(golf) error: %v", err)
	}
	if dg.State != dog.StateIdle {
		t.Errorf("golf state = %q, want idle (delete failure must not block clear)", dg.State)
	}
}

// TestProcessDogDoneMessageList_MultipleDogs verifies handling a batch of
// DOG_DONE messages from multiple dogs in a single heartbeat.
func TestProcessDogDoneMessageList_MultipleDogs(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)

	testSetupWorkingDogState(t, townRoot, "hotel", "mol-orphan-scan", time.Now())
	testSetupWorkingDogState(t, townRoot, "india", "mol-session-gc", time.Now())
	testSetupWorkingDogState(t, townRoot, "juliet", "mol-dog-backup", time.Now())

	messages := []BeadsMessage{
		{ID: "m1", From: "deacon/dogs/hotel", Subject: "DOG_DONE hotel"},
		{ID: "m2", From: "deacon/dogs/india", Subject: "DOG_DONE india"},
		{ID: "m3", From: "deacon/dogs/juliet", Subject: "DOG_DONE: backup"},
	}

	d.processDogDoneMessageList(mgr, messages, func(string) error { return nil })

	for _, name := range []string{"hotel", "india", "juliet"} {
		dg, err := mgr.Get(name)
		if err != nil {
			t.Fatalf("Get(%s) error: %v", name, err)
		}
		if dg.State != dog.StateIdle {
			t.Errorf("%s state = %q, want idle", name, dg.State)
		}
	}
}
