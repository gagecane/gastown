package witness

import (
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/session"
)

// TestShouldNotifyStaleAgent_DisabledCooldown verifies cooldown<=0 always
// notifies (pre-gu-z8qzq behavior / operator opt-out), regardless of prior
// state.
func TestShouldNotifyStaleAgent_DisabledCooldown(t *testing.T) {
	now := time.Now().UTC()
	prev := &staleAgentNotifyState{LastNotifiedAt: now, LastBand: 1}
	if !shouldNotifyStaleAgent(prev, now, 0, 1, false) {
		t.Errorf("cooldown=0 must always notify")
	}
}

// TestShouldNotifyStaleAgent_FirstObservation verifies the absence of a prior
// record always notifies — the first time we see a stale agent we must alarm.
func TestShouldNotifyStaleAgent_FirstObservation(t *testing.T) {
	now := time.Now().UTC()
	if !shouldNotifyStaleAgent(nil, now, 30*time.Minute, 1, false) {
		t.Errorf("first observation (prev=nil) must notify")
	}
}

// TestShouldNotifyStaleAgent_SuppressWithinCooldown verifies the core fix: the
// same unchanged condition reported inside the cooldown window is suppressed.
func TestShouldNotifyStaleAgent_SuppressWithinCooldown(t *testing.T) {
	start := time.Now().UTC()
	prev := &staleAgentNotifyState{LastNotifiedAt: start, LastBand: 1, LastMissing: false}
	// 10m later, same band, same kind — within the 30m cooldown.
	if shouldNotifyStaleAgent(prev, start.Add(10*time.Minute), 30*time.Minute, 1, false) {
		t.Errorf("unchanged condition within cooldown must be suppressed")
	}
}

// TestShouldNotifyStaleAgent_ReNotifyAfterCooldown verifies the alarm re-fires
// once the cooldown window elapses, so a genuinely stuck agent is not forgotten.
func TestShouldNotifyStaleAgent_ReNotifyAfterCooldown(t *testing.T) {
	start := time.Now().UTC()
	prev := &staleAgentNotifyState{LastNotifiedAt: start, LastBand: 1, LastMissing: false}
	if !shouldNotifyStaleAgent(prev, start.Add(31*time.Minute), 30*time.Minute, 1, false) {
		t.Errorf("must re-notify after cooldown elapses")
	}
}

// TestShouldNotifyStaleAgent_ReNotifyOnBandIncrease verifies that a materially
// worse condition (higher staleness band) re-fires even inside the cooldown.
func TestShouldNotifyStaleAgent_ReNotifyOnBandIncrease(t *testing.T) {
	start := time.Now().UTC()
	prev := &staleAgentNotifyState{LastNotifiedAt: start, LastBand: 1, LastMissing: false}
	// Still within cooldown, but band climbed 1 -> 2 (age crossed 2x threshold).
	if !shouldNotifyStaleAgent(prev, start.Add(5*time.Minute), 30*time.Minute, 2, false) {
		t.Errorf("band increase must re-notify even within cooldown")
	}
}

// TestShouldNotifyStaleAgent_NoReNotifyOnBandDecrease verifies that a lower
// band (shouldn't normally happen for a worsening agent, but defend against a
// flapping heartbeat) does NOT re-fire within cooldown.
func TestShouldNotifyStaleAgent_NoReNotifyOnBandDecrease(t *testing.T) {
	start := time.Now().UTC()
	prev := &staleAgentNotifyState{LastNotifiedAt: start, LastBand: 3, LastMissing: false}
	if shouldNotifyStaleAgent(prev, start.Add(5*time.Minute), 30*time.Minute, 2, false) {
		t.Errorf("band decrease within cooldown must not re-notify")
	}
}

// TestShouldNotifyStaleAgent_ReNotifyOnMissingTransition verifies that a
// transition between missing and present heartbeat is a material change.
func TestShouldNotifyStaleAgent_ReNotifyOnMissingTransition(t *testing.T) {
	start := time.Now().UTC()
	prev := &staleAgentNotifyState{LastNotifiedAt: start, LastBand: 1, LastMissing: false}
	// Heartbeat file vanished entirely — different failure kind.
	if !shouldNotifyStaleAgent(prev, start.Add(5*time.Minute), 30*time.Minute, 1, true) {
		t.Errorf("present->missing transition must re-notify even within cooldown")
	}
}

// TestStaleAgentBand verifies band computation: floor(age/threshold).
func TestStaleAgentBand(t *testing.T) {
	thr := time.Hour
	cases := []struct {
		age  time.Duration
		want int
	}{
		{30 * time.Minute, 0}, // below threshold (caller won't escalate)
		{time.Hour, 1},
		{90 * time.Minute, 1},
		{2 * time.Hour, 2},
		{150 * time.Minute, 2},
		{3 * time.Hour, 3},
	}
	for _, c := range cases {
		if got := staleAgentBand(c.age, thr); got != c.want {
			t.Errorf("staleAgentBand(%s) = %d, want %d", c.age, got, c.want)
		}
	}
	// Defensive: threshold<=0 returns 1, never divides by zero.
	if got := staleAgentBand(time.Hour, 0); got != 1 {
		t.Errorf("staleAgentBand with threshold=0 = %d, want 1", got)
	}
}

// TestStaleAgentState_RoundTrip verifies the on-disk record persists and reads
// back, and that a missing file reads as nil (no prior notification).
func TestStaleAgentState_RoundTrip(t *testing.T) {
	townRoot := t.TempDir()
	rig := "testrig"
	sess := "gu-refinery"

	if got := readStaleAgentState(townRoot, rig, sess); got != nil {
		t.Errorf("expected nil for missing state, got %+v", got)
	}

	now := time.Now().UTC().Truncate(time.Second)
	writeStaleAgentState(townRoot, rig, sess, &staleAgentNotifyState{
		LastNotifiedAt: now, LastBand: 2, LastMissing: true,
	})

	got := readStaleAgentState(townRoot, rig, sess)
	if got == nil {
		t.Fatalf("expected state after write, got nil")
	}
	if got.LastBand != 2 || !got.LastMissing || !got.LastNotifiedAt.Equal(now) {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}

// TestDetectStaleRigAgentHeartbeats_CooldownSuppressesSecondCycle is the
// integration test for gu-z8qzq: a stale agent escalates on the first patrol
// cycle, then is suppressed (Action=skip-cooldown) on an immediate second
// cycle because the condition has not materially changed.
func TestDetectStaleRigAgentHeartbeats_CooldownSuppressesSecondCycle(t *testing.T) {
	installFakeTmuxNoServer(t)

	townRoot := t.TempDir()
	rigName := "testrig"
	prefix := session.PrefixFor(rigName)
	refSession := session.RefinerySessionName(prefix)
	writeRigAgentHeartbeat(t, townRoot, refSession, 2*time.Hour)
	writeRigAgentHeartbeat(t, townRoot, session.WitnessSessionName(prefix), 30*time.Second)

	// Cycle 1: cooldown enabled, first observation -> escalate.
	res1 := DetectStaleRigAgentHeartbeats(townRoot, rigName, nil, time.Hour, "", 30*time.Minute)
	ref1 := findStaleResult(res1, "refinery")
	if ref1 == nil || ref1.Action != "escalated" {
		t.Fatalf("cycle 1 refinery Action = %v, want escalated", ref1)
	}

	// Cycle 2: same condition (heartbeat still ~2h, same band) -> suppressed.
	res2 := DetectStaleRigAgentHeartbeats(townRoot, rigName, nil, time.Hour, "", 30*time.Minute)
	ref2 := findStaleResult(res2, "refinery")
	if ref2 == nil || ref2.Action != "skip-cooldown" {
		t.Fatalf("cycle 2 refinery Action = %v, want skip-cooldown", ref2)
	}
	if ref2.MailSent {
		t.Errorf("cycle 2 refinery MailSent = true, want false (suppressed)")
	}
}

// TestDetectStaleRigAgentHeartbeats_CooldownDisabledReNotifies verifies that
// with cooldown=0 the detector escalates every cycle (regression guard for the
// opt-out / legacy behavior).
func TestDetectStaleRigAgentHeartbeats_CooldownDisabledReNotifies(t *testing.T) {
	installFakeTmuxNoServer(t)

	townRoot := t.TempDir()
	rigName := "testrig"
	prefix := session.PrefixFor(rigName)
	writeRigAgentHeartbeat(t, townRoot, session.RefinerySessionName(prefix), 2*time.Hour)
	writeRigAgentHeartbeat(t, townRoot, session.WitnessSessionName(prefix), 30*time.Second)

	for cycle := 1; cycle <= 2; cycle++ {
		res := DetectStaleRigAgentHeartbeats(townRoot, rigName, nil, time.Hour, "", 0)
		ref := findStaleResult(res, "refinery")
		if ref == nil || ref.Action != "escalated" {
			t.Fatalf("cycle %d refinery Action = %v, want escalated (cooldown disabled)", cycle, ref)
		}
	}
}

func findStaleResult(res *DetectStaleRigAgentHeartbeatsResult, role string) *StaleRigAgentResult {
	for i := range res.Stale {
		if res.Stale[i].AgentRole == role {
			return &res.Stale[i]
		}
	}
	return nil
}
