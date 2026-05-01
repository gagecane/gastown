package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/dog"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/tmux"
)

// fakeConsumer captures dispatch calls for assertions.
type fakeConsumer struct {
	mu    sync.Mutex
	calls []fakeCall

	// err is returned by DispatchAutoDispatchForRig if set.
	err error
}

type fakeCall struct {
	rig            string
	trigger        string
	triggerSession string
	triggerAgent   string
	at             time.Time
}

func (f *fakeConsumer) DispatchAutoDispatchForRig(rig, trigger, triggerSession, triggerAgent string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCall{
		rig:            rig,
		trigger:        trigger,
		triggerSession: triggerSession,
		triggerAgent:   triggerAgent,
		at:             time.Now(),
	})
	return f.err
}

func (f *fakeConsumer) Calls() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeConsumer) CallsForRig(rig string) []fakeCall {
	var out []fakeCall
	for _, c := range f.Calls() {
		if c.rig == rig {
			out = append(out, c)
		}
	}
	return out
}

// newTestWatcher builds a watcher with a fake consumer. Caller is responsible
// for feeding it lines via handleLine (no file I/O).
func newTestWatcher(t *testing.T) (*AutoDispatchWatcher, *fakeConsumer) {
	t.Helper()
	fc := &fakeConsumer{}
	w := NewAutoDispatchWatcher(t.TempDir(), log.New(os.Stderr, "test ", 0), fc)
	// Very short rate-limit window for tests that don't care about it.
	w.SetRateLimit(50 * time.Millisecond)
	return w, fc
}

// mkSessionDeathLine produces a JSONL line as handleLine expects.
func mkSessionDeathLine(actor string, payload map[string]interface{}) string {
	ev := map[string]interface{}{
		"ts":      time.Now().UTC().Format(time.RFC3339),
		"source":  "gt",
		"type":    events.TypeSessionDeath,
		"actor":   actor,
		"payload": payload,
	}
	b, err := json.Marshal(ev)
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

// TestProcessSessionDeath_CompletedPolecat_TriggersDispatch verifies the
// happy path: a polecat completed via `gt done`, the watcher sees the
// session_death event, and triggers dispatch for the polecat's rig.
func TestProcessSessionDeath_CompletedPolecat_TriggersDispatch(t *testing.T) {
	w, fc := newTestWatcher(t)

	line := mkSessionDeathLine("myrig/polecats/alpha", map[string]interface{}{
		"session": "my-alpha",
		"agent":   "myrig/polecats/alpha",
		"reason":  "self-clean: done means idle",
		"caller":  "gt done",
	})

	w.handleLine(line)

	calls := fc.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 dispatch call, got %d", len(calls))
	}
	if calls[0].rig != "myrig" {
		t.Errorf("expected rig=myrig, got %q", calls[0].rig)
	}
	if calls[0].trigger != "gt done" {
		t.Errorf("expected trigger=gt done, got %q", calls[0].trigger)
	}
	if calls[0].triggerSession != "my-alpha" {
		t.Errorf("expected triggerSession=my-alpha, got %q", calls[0].triggerSession)
	}
	if calls[0].triggerAgent != "myrig/polecats/alpha" {
		t.Errorf("expected triggerAgent=myrig/polecats/alpha, got %q", calls[0].triggerAgent)
	}
}

// TestProcessSessionDeath_CrashedPolecat_NoDispatch verifies that
// daemon-reported crashes do NOT trigger event-driven dispatch. The bead
// acceptance criteria explicitly require this so crashes aren't masked by
// immediate replacement.
func TestProcessSessionDeath_CrashedPolecat_NoDispatch(t *testing.T) {
	w, fc := newTestWatcher(t)

	line := mkSessionDeathLine("myrig/polecats/alpha", map[string]interface{}{
		"session": "my-alpha",
		"agent":   "myrig/polecats/alpha",
		"reason":  "crash detected by daemon health check",
		"caller":  "daemon",
	})

	w.handleLine(line)

	if calls := fc.Calls(); len(calls) != 0 {
		t.Fatalf("expected 0 dispatch calls for crash, got %d: %+v", len(calls), calls)
	}
}

// TestProcessSessionDeath_IdleReap_NoDispatch verifies that idle-reap events
// (daemon-initiated cleanup of idle polecat sessions) do NOT trigger
// event-driven dispatch. The existing cooldown fallback will pick them up.
func TestProcessSessionDeath_IdleReap_NoDispatch(t *testing.T) {
	w, fc := newTestWatcher(t)

	line := mkSessionDeathLine("myrig/alpha", map[string]interface{}{
		"session": "my-alpha",
		"agent":   "myrig/polecats/alpha",
		"reason":  "idle-reap: exiting, idle 16m21s (threshold 15m0s)",
		"caller":  "daemon",
	})

	w.handleLine(line)

	if calls := fc.Calls(); len(calls) != 0 {
		t.Fatalf("expected 0 dispatch calls for idle-reap, got %d: %+v", len(calls), calls)
	}
}

// TestProcessSessionDeath_GtDoctor_NoDispatch verifies that gt doctor zombie
// / orphan cleanups do NOT trigger event-driven dispatch.
func TestProcessSessionDeath_GtDoctor_NoDispatch(t *testing.T) {
	w, fc := newTestWatcher(t)

	for _, reason := range []string{"zombie cleanup", "orphan cleanup"} {
		line := mkSessionDeathLine("myrig/alpha", map[string]interface{}{
			"session": "my-alpha",
			"agent":   "myrig/polecats/alpha",
			"reason":  reason,
			"caller":  "gt doctor",
		})
		w.handleLine(line)
	}

	if calls := fc.Calls(); len(calls) != 0 {
		t.Fatalf("expected 0 dispatch calls for gt doctor, got %d: %+v", len(calls), calls)
	}
}

// TestProcessSessionDeath_GtDown_NoDispatch verifies that town shutdown does
// NOT trigger event-driven dispatch (we're literally shutting down).
func TestProcessSessionDeath_GtDown_NoDispatch(t *testing.T) {
	w, fc := newTestWatcher(t)

	line := mkSessionDeathLine("myrig/alpha", map[string]interface{}{
		"session": "my-alpha",
		"agent":   "myrig/polecats/alpha",
		"reason":  "gt down",
		"caller":  "gt down",
	})

	w.handleLine(line)

	if calls := fc.Calls(); len(calls) != 0 {
		t.Fatalf("expected 0 dispatch calls for gt down, got %d: %+v", len(calls), calls)
	}
}

// TestProcessSessionDeath_NonPolecat_NoDispatch verifies that non-polecat
// session deaths (deacon, witness, refinery, mayor) do not trigger dispatch,
// since killing those doesn't free a polecat slot.
func TestProcessSessionDeath_NonPolecat_NoDispatch(t *testing.T) {
	w, fc := newTestWatcher(t)

	for _, agent := range []string{
		"deacon",
		"myrig/witness",
		"myrig/refinery",
		"mayor",
		"", // empty
	} {
		line := mkSessionDeathLine("daemon", map[string]interface{}{
			"session": "deacon-1",
			"agent":   agent,
			"reason":  "self-clean: done means idle",
			"caller":  "gt done",
		})
		w.handleLine(line)
	}

	if calls := fc.Calls(); len(calls) != 0 {
		t.Fatalf("expected 0 dispatch calls for non-polecat agents, got %d: %+v", len(calls), calls)
	}
}

// TestProcessSessionDeath_RateLimit_OneDispatchPerRig verifies the per-rig
// rate-limit: two completions in the same rig within the window should only
// produce one dispatch.
func TestProcessSessionDeath_RateLimit_OneDispatchPerRig(t *testing.T) {
	w, fc := newTestWatcher(t)
	w.SetRateLimit(500 * time.Millisecond)

	for i := 0; i < 5; i++ {
		line := mkSessionDeathLine(fmt.Sprintf("myrig/polecats/p%d", i), map[string]interface{}{
			"session": fmt.Sprintf("my-p%d", i),
			"agent":   fmt.Sprintf("myrig/polecats/p%d", i),
			"reason":  "self-clean: done means idle",
			"caller":  "gt done",
		})
		w.handleLine(line)
	}

	// Only the first should have made it through; the other four are rate-limited.
	if calls := fc.Calls(); len(calls) != 1 {
		t.Fatalf("expected 1 dispatch call under rate-limit, got %d: %+v", len(calls), calls)
	}
}

// TestProcessSessionDeath_RateLimit_MultipleRigs_Independent verifies that
// the rate-limit is per-rig: multiple different rigs completing at the same
// time all trigger independently.
func TestProcessSessionDeath_RateLimit_MultipleRigs_Independent(t *testing.T) {
	w, fc := newTestWatcher(t)
	w.SetRateLimit(1 * time.Hour) // effectively disabled

	for _, rig := range []string{"alpha", "beta", "gamma"} {
		line := mkSessionDeathLine(rig+"/polecats/p1", map[string]interface{}{
			"session": rig + "-p1",
			"agent":   rig + "/polecats/p1",
			"reason":  "self-clean: done means idle",
			"caller":  "gt done",
		})
		w.handleLine(line)
	}

	if calls := fc.Calls(); len(calls) != 3 {
		t.Fatalf("expected 3 dispatch calls (one per rig), got %d: %+v", len(calls), calls)
	}

	// Verify each rig appears exactly once.
	seen := map[string]int{}
	for _, c := range fc.Calls() {
		seen[c.rig]++
	}
	for _, rig := range []string{"alpha", "beta", "gamma"} {
		if seen[rig] != 1 {
			t.Errorf("rig %s dispatched %d times, want 1", rig, seen[rig])
		}
	}
}

// TestProcessSessionDeath_RateLimit_ExpiresAfterWindow verifies that the
// rate-limit window releases: after the window elapses, the same rig can
// dispatch again.
func TestProcessSessionDeath_RateLimit_ExpiresAfterWindow(t *testing.T) {
	w, fc := newTestWatcher(t)
	w.SetRateLimit(20 * time.Millisecond)

	line := mkSessionDeathLine("myrig/polecats/alpha", map[string]interface{}{
		"session": "my-alpha",
		"agent":   "myrig/polecats/alpha",
		"reason":  "self-clean: done means idle",
		"caller":  "gt done",
	})

	w.handleLine(line)
	time.Sleep(40 * time.Millisecond) // exceeds 20ms window
	w.handleLine(line)

	if calls := fc.Calls(); len(calls) != 2 {
		t.Fatalf("expected 2 dispatches after window expiry, got %d", len(calls))
	}
}

// TestHandleLine_MalformedJSON_NoCrash ensures malformed lines are skipped
// silently without crashing the watcher.
func TestHandleLine_MalformedJSON_NoCrash(t *testing.T) {
	w, fc := newTestWatcher(t)

	badLines := []string{
		"",
		"\n",
		"not json",
		`{"type":"session_death","payload": }`,
		`{"no-type-field": true}`,
	}

	for _, line := range badLines {
		w.handleLine(line)
	}

	if calls := fc.Calls(); len(calls) != 0 {
		t.Fatalf("expected 0 dispatches for malformed input, got %d", len(calls))
	}
}

// TestHandleLine_NonSessionDeathEvent_Skipped verifies that other event
// types are ignored (not just skipped but cheaply so).
func TestHandleLine_NonSessionDeathEvent_Skipped(t *testing.T) {
	w, fc := newTestWatcher(t)

	for _, eventType := range []string{"nudge", "hook", "done", "sling", "mass_death", "merge_started"} {
		ev := map[string]interface{}{
			"ts":      time.Now().UTC().Format(time.RFC3339),
			"source":  "gt",
			"type":    eventType,
			"actor":   "myrig/polecats/alpha",
			"payload": map[string]interface{}{"caller": "gt done", "agent": "myrig/polecats/alpha"},
		}
		b, _ := json.Marshal(ev)
		w.handleLine(string(b) + "\n")
	}

	if calls := fc.Calls(); len(calls) != 0 {
		t.Fatalf("expected 0 dispatches for non-session_death events, got %d", len(calls))
	}
}

// TestRigFromAgent validates the agent-to-rig parser across inputs that can
// appear in real-world events.
func TestRigFromAgent(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"myrig/polecats/alpha", "myrig"},
		{"gastown_upstream/polecats/guzzle", "gastown_upstream"},
		{"rig_with_underscores/polecats/x", "rig_with_underscores"},
		{"", ""},
		{"deacon", ""},
		{"mayor", ""},
		{"myrig/witness", ""},
		{"myrig/refinery", ""},
		{"myrig/crew/joe", ""},
		{"/polecats/alpha", ""}, // empty rig
		{"myrig/polecats", ""},  // missing name, len=2
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := rigFromAgent(tt.in)
			if got != tt.want {
				t.Errorf("rigFromAgent(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestIsPlannedPolecatExit enumerates the caller/reason → eligibility rules.
func TestIsPlannedPolecatExit(t *testing.T) {
	tests := []struct {
		name   string
		caller string
		reason string
		want   bool
	}{
		// Planned polecat exits — trigger dispatch.
		{"gt_done_completed", "gt done", "self-clean: done means idle", true},
		{"gt_done_deferred", "gt done", "self-clean: done means idle", true}, // same reason string today

		// Daemon-originated — always skip.
		{"daemon_crash", "daemon", "crash detected by daemon health check", false},
		{"daemon_idle_reap", "daemon", "idle-reap: working-no-hook, idle 20m0s", false},
		{"daemon_exiting_reap", "daemon", "idle-reap: exiting, idle 16m21s", false},

		// Operator-initiated — always skip.
		{"gt_doctor_zombie", "gt doctor", "zombie cleanup", false},
		{"gt_doctor_orphan", "gt doctor", "orphan cleanup", false},
		{"gt_down", "gt down", "gt down", false},

		// Unknown caller — skip (conservative default).
		{"unknown_caller", "witness", "arbitrary", false},
		{"empty_caller", "", "anything", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPlannedPolecatExit(tt.caller, tt.reason)
			if got != tt.want {
				t.Errorf("isPlannedPolecatExit(%q, %q) = %v, want %v", tt.caller, tt.reason, got, tt.want)
			}
		})
	}
}

// TestWithExtra verifies payload cloning (no mutation of input).
func TestWithExtra(t *testing.T) {
	original := map[string]interface{}{"a": 1, "b": "two"}
	got := withExtra(original, "c", "three")
	if got["a"] != 1 || got["b"] != "two" || got["c"] != "three" {
		t.Errorf("withExtra result missing keys: %+v", got)
	}
	if _, hasC := original["c"]; hasC {
		t.Errorf("withExtra mutated the input map")
	}
}

// TestDrainReader_ProcessesMultipleLines verifies the tail loop consumes all
// available lines in a single drain pass.
func TestDrainReader_ProcessesMultipleLines(t *testing.T) {
	w, fc := newTestWatcher(t)
	w.SetRateLimit(1 * time.Hour) // disable rate-limit so every line fires

	var buf strings.Builder
	for _, rig := range []string{"alpha", "beta", "gamma"} {
		buf.WriteString(mkSessionDeathLine(rig+"/polecats/p1", map[string]interface{}{
			"session": rig + "-p1",
			"agent":   rig + "/polecats/p1",
			"reason":  "self-clean: done means idle",
			"caller":  "gt done",
		}))
	}

	reader := bufio.NewReader(strings.NewReader(buf.String()))
	w.drainReader(reader)

	if calls := fc.Calls(); len(calls) != 3 {
		t.Fatalf("expected 3 dispatches, got %d", len(calls))
	}
}

// TestWatcher_EndToEnd_TailsEventsFile exercises the full start→tail→stop
// flow by writing to a real .events.jsonl file and verifying dispatch.
func TestWatcher_EndToEnd_TailsEventsFile(t *testing.T) {
	townRoot := t.TempDir()
	eventsPath := filepath.Join(townRoot, events.EventsFile)

	// Seed the events file so the watcher can open it and seek to end.
	if err := os.WriteFile(eventsPath, []byte{}, 0644); err != nil {
		t.Fatalf("creating events file: %v", err)
	}

	fc := &fakeConsumer{}
	w := NewAutoDispatchWatcher(townRoot, log.New(os.Stderr, "test ", 0), fc)
	w.SetPollInterval(10 * time.Millisecond)
	w.SetRateLimit(1 * time.Hour)

	if err := w.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer w.Stop()

	// Give the watcher a moment to open/seek.
	time.Sleep(30 * time.Millisecond)

	// Append a session_death event.
	f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("opening events file for append: %v", err)
	}
	line := mkSessionDeathLine("alpha/polecats/p1", map[string]interface{}{
		"session": "alpha-p1",
		"agent":   "alpha/polecats/p1",
		"reason":  "self-clean: done means idle",
		"caller":  "gt done",
	})
	if _, err := f.WriteString(line); err != nil {
		t.Fatalf("writing event: %v", err)
	}
	f.Close()

	// Wait for dispatch to be observed.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(fc.Calls()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	calls := fc.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 dispatch from tail, got %d", len(calls))
	}
	if calls[0].rig != "alpha" {
		t.Errorf("expected rig=alpha, got %q", calls[0].rig)
	}
}

// TestWatcher_Start_Idempotent verifies a double Start() doesn't spawn
// duplicate goroutines.
func TestWatcher_Start_Idempotent(t *testing.T) {
	w, _ := newTestWatcher(t)
	if err := w.Start(); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer w.Stop()

	// Second start should no-op.
	if err := w.Start(); err != nil {
		t.Fatalf("second Start: %v", err)
	}
}

// TestClassifyNoDispatchable_NoIdleDog verifies that when no dog is idle
// (all working, or pack empty) the classifier reports "no_idle_dog".
// This preserves the original outcome label emitted by dispatchAutoDispatchForRig
// before gu-yq9w unified the idle/session-live/backoff checks via
// findDispatchableDog.
func TestClassifyNoDispatchable_NoIdleDog(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	// All dogs are working.
	testSetupWorkingDogState(t, townRoot, "alpha", "plugin:x", time.Now())
	testSetupWorkingDogState(t, townRoot, "bravo", "plugin:y", time.Now())

	mgr := dog.NewManager(townRoot, nil)
	sm := dog.NewSessionManager(tmux.NewTmux(), townRoot, mgr)

	got := classifyNoDispatchable(d, mgr, sm)
	if got != "no_idle_dog" {
		t.Errorf("classifyNoDispatchable = %q, want no_idle_dog", got)
	}
}

// TestClassifyNoDispatchable_EmptyKennelReportsNoIdle verifies that an
// empty pack classifies as "no_idle_dog" rather than leaking "unknown".
func TestClassifyNoDispatchable_EmptyKennelReportsNoIdle(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	mgr := dog.NewManager(townRoot, nil)
	sm := dog.NewSessionManager(tmux.NewTmux(), townRoot, mgr)

	got := classifyNoDispatchable(d, mgr, sm)
	if got != "no_idle_dog" {
		t.Errorf("classifyNoDispatchable = %q, want no_idle_dog", got)
	}
}

// TestClassifyNoDispatchable_DogInBackoffReportsBackoff verifies that when
// the only idle dog is muted by the startup-failure backoff, the outcome is
// "dog_in_backoff" — the label operators look for when the event-driven
// path is deferring to the cooldown fallback (gu-ro75).
func TestClassifyNoDispatchable_DogInBackoffReportsBackoff(t *testing.T) {
	townRoot := t.TempDir()
	d, tracker := testDaemonWithTracker(t)
	// Rewire the daemon at the same townRoot so state files land where we
	// created the dog directory.
	d.config = &Config{TownRoot: townRoot}
	_ = tracker // keeping the reference clear that backoff persists in the daemon

	testSetupDogState(t, townRoot, "alpha", dog.StateIdle, time.Now())
	d.recordDogStartFailure("alpha")

	mgr := dog.NewManager(townRoot, nil)
	sm := dog.NewSessionManager(tmux.NewTmux(), townRoot, mgr)

	got := classifyNoDispatchable(d, mgr, sm)
	if got != "dog_in_backoff" {
		t.Errorf("classifyNoDispatchable = %q, want dog_in_backoff", got)
	}
}

// TestClassifyNoDispatchable_MixedStatesReportsRunningFirst verifies ordering
// of the classifier: when the pack contains both a session-live idle dog
// and a backed-off idle dog, we report "idle_session_live" because that
// condition is usually the more immediate and self-healing case (the tmux
// session finishes tearing down within a tick).
//
// Note: we can't fake sm.IsRunning returning true without a real tmux session,
// so this test uses an alternate path — it verifies that the classifier
// falls through to "dog_in_backoff" when backoff is the only applicable
// condition, leaving the session-live case covered at the caller level
// (the dispatch test below) where tmux can actually be observed.
func TestClassifyNoDispatchable_MixedNoRunningFallsThroughToBackoff(t *testing.T) {
	townRoot := t.TempDir()
	d, _ := testDaemonWithTracker(t)
	d.config = &Config{TownRoot: townRoot}

	// Two idle dogs; both in backoff — should still classify as dog_in_backoff.
	testSetupDogState(t, townRoot, "alpha", dog.StateIdle, time.Now())
	testSetupDogState(t, townRoot, "bravo", dog.StateIdle, time.Now())
	d.recordDogStartFailure("alpha")
	d.recordDogStartFailure("bravo")

	mgr := dog.NewManager(townRoot, nil)
	sm := dog.NewSessionManager(tmux.NewTmux(), townRoot, mgr)

	got := classifyNoDispatchable(d, mgr, sm)
	if got != "dog_in_backoff" {
		t.Errorf("classifyNoDispatchable = %q, want dog_in_backoff", got)
	}
}
