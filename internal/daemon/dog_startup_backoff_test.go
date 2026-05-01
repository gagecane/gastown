package daemon

import (
	"log"
	"os"
	"testing"
	"time"
)

// testDaemonWithTracker returns a minimal Daemon wired with a real
// RestartTracker rooted at a temporary directory and a tight backoff
// schedule so unit tests can observe state transitions without sleeping.
func testDaemonWithTracker(t *testing.T) (*Daemon, *RestartTracker) {
	t.Helper()

	tmp := t.TempDir()
	if err := os.MkdirAll(tmp+"/daemon", 0o755); err != nil {
		t.Fatalf("mkdir daemon dir: %v", err)
	}

	tracker := NewRestartTracker(tmp, RestartTrackerConfig{
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        1 * time.Second,
		BackoffMultiplier: 2.0,
		CrashLoopWindow:   1 * time.Second,
		CrashLoopCount:    3,
		StabilityPeriod:   200 * time.Millisecond,
	})

	d := &Daemon{
		config:         &Config{TownRoot: tmp},
		logger:         log.New(os.Stderr, "backoff-test: ", log.LstdFlags),
		restartTracker: tracker,
	}
	return d, tracker
}

// TestDogBackoff_FirstAttemptNotThrottled documents the baseline: a dog that
// has never failed to start is never in backoff. If this ever changes, the
// first dispatch of every newly-created dog would be delayed — which would
// be a regression.
func TestDogBackoff_FirstAttemptNotThrottled(t *testing.T) {
	d, _ := testDaemonWithTracker(t)

	skip, reason := d.isDogInStartupBackoff("alpha")
	if skip {
		t.Fatalf("expected new dog to bypass backoff, got skip=true reason=%q", reason)
	}
}

// TestDogBackoff_AfterFailureSkipsUntilExpires is the core behaviour gu-ro75
// requests: once a dog's startup fails, the next heartbeat sees it as
// "in backoff" and the dispatcher must skip it rather than hot-looping.
func TestDogBackoff_AfterFailureSkipsUntilExpires(t *testing.T) {
	d, tracker := testDaemonWithTracker(t)

	d.recordDogStartFailure("bravo")

	skip, reason := d.isDogInStartupBackoff("bravo")
	if !skip {
		t.Fatalf("expected bravo to be in backoff after failure, got skip=false")
	}
	if reason == "" {
		t.Fatal("expected a non-empty backoff reason")
	}
	if got := tracker.GetBackoffRemaining(dogBackoffAgentID("bravo")); got <= 0 {
		t.Fatalf("expected positive backoff remaining, got %v", got)
	}

	// After the backoff window elapses the gate reopens.
	// InitialBackoff in the test config is 100ms; give a small safety margin
	// to avoid flake on slow CI.
	time.Sleep(250 * time.Millisecond)
	skip, _ = d.isDogInStartupBackoff("bravo")
	if skip {
		t.Fatalf("expected bravo backoff to expire, still skip=true")
	}
}

// TestDogBackoff_ExponentialGrowth verifies the key symptom fix: repeated
// startup failures produce progressively longer backoff windows, not the
// same rapid-fire retry interval gu-ro75 observed (~30s on every heartbeat).
func TestDogBackoff_ExponentialGrowth(t *testing.T) {
	d, tracker := testDaemonWithTracker(t)
	id := dogBackoffAgentID("charlie")

	d.recordDogStartFailure("charlie")
	first := tracker.GetBackoffRemaining(id)
	if first <= 0 {
		t.Fatalf("expected positive backoff after 1st failure, got %v", first)
	}

	d.recordDogStartFailure("charlie")
	second := tracker.GetBackoffRemaining(id)
	if second <= first {
		t.Fatalf("expected backoff to grow exponentially, got %v → %v", first, second)
	}
}

// TestDogBackoff_CrashLoopMutesDog verifies the "after N consecutive failures,
// emit an escalation or mute the dog until manually cleared" acceptance
// criterion from gu-ro75. Here the test config uses CrashLoopCount=3 so the
// third failure within the window flips the dog into crash-loop state.
func TestDogBackoff_CrashLoopMutesDog(t *testing.T) {
	d, _ := testDaemonWithTracker(t)

	for i := 0; i < 3; i++ {
		d.recordDogStartFailure("delta")
	}

	skip, reason := d.isDogInStartupBackoff("delta")
	if !skip {
		t.Fatalf("expected delta to be muted after crash-loop threshold, got skip=false")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason for crash-looped dog")
	}
	if wantSub := "clear-backoff dog:delta"; !containsSubstring(reason, wantSub) {
		t.Errorf("expected reason to instruct operator to run `gt daemon %s`, got %q", wantSub, reason)
	}
}

// TestDogBackoff_SuccessResetsAfterStability verifies that a dog which runs
// stably for StabilityPeriod clears its backoff counter. Without this a
// formerly-flaky dog would accumulate state forever and eventually hit the
// crash-loop cap on perfectly healthy restarts.
func TestDogBackoff_SuccessResetsAfterStability(t *testing.T) {
	d, tracker := testDaemonWithTracker(t)
	id := dogBackoffAgentID("echo")

	d.recordDogStartFailure("echo")
	if tracker.GetBackoffRemaining(id) <= 0 {
		t.Fatal("expected initial backoff after failure")
	}

	// Stability period in the test config is 200ms. Sleep past it so
	// RecordSuccess clears the tracker's counters.
	time.Sleep(250 * time.Millisecond)
	d.recordDogStartSuccess("echo")

	if got := tracker.GetBackoffRemaining(id); got > 0 {
		t.Errorf("expected backoff cleared after stable success, got %v remaining", got)
	}
	if tracker.IsInCrashLoop(id) {
		t.Error("expected crash-loop state cleared after stable success")
	}
}

// TestDogBackoff_NoTrackerIsSafe ensures that a Daemon without a restart
// tracker (older test fixtures, or during early init) does not panic and
// silently allows dispatch. Backoff is a safety net, not a hard requirement.
func TestDogBackoff_NoTrackerIsSafe(t *testing.T) {
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(os.Stderr, "nt: ", log.LstdFlags),
	}

	skip, reason := d.isDogInStartupBackoff("foxtrot")
	if skip || reason != "" {
		t.Errorf("expected nil-tracker daemon to skip=false reason=\"\", got skip=%v reason=%q", skip, reason)
	}

	// Recording should be a noop, not panic.
	d.recordDogStartFailure("foxtrot")
	d.recordDogStartSuccess("foxtrot")
}

// TestDogBackoff_AgentIDNamespacing ensures the dog's backoff state is keyed
// under a namespace that cannot collide with other tracked agents (deacon,
// witness, etc.). If dogs started sharing an ID with the deacon, one
// flapping deacon would mute an unrelated dog.
func TestDogBackoff_AgentIDNamespacing(t *testing.T) {
	if got := dogBackoffAgentID("alpha"); got != "dog:alpha" {
		t.Errorf("dogBackoffAgentID = %q, want dog:alpha", got)
	}
	if dogBackoffAgentID("deacon") == "deacon" {
		t.Error("dog namespacing must not collide with the bare deacon agent ID")
	}
}

func containsSubstring(s, sub string) bool {
	if sub == "" {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
