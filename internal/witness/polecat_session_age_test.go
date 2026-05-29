package witness

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// withFakePolecatSessionAge swaps the package-level session-age probe
// with a deterministic fake so tests don't depend on a real tmux server.
// The fake returns (age, exists) verbatim from the table for every
// (rig, name) lookup.
func withFakePolecatSessionAge(t *testing.T, age time.Duration, exists bool) func() {
	t.Helper()
	prev := polecatSessionAge
	polecatSessionAge = func(string, string) (time.Duration, bool) {
		return age, exists
	}
	return func() { polecatSessionAge = prev }
}

// withTightPolecatMinSessionAge shortens the gs-549 spawn guard so tests
// can express "too young" vs "old enough" in milliseconds.
func withTightPolecatMinSessionAge(t *testing.T, d time.Duration) func() {
	t.Helper()
	prev := polecatMinSessionAge
	polecatMinSessionAge = d
	return func() { polecatMinSessionAge = prev }
}

// TestSessionTooYoung_NoSession documents the dead-session contract: when
// the tmux session does not exist there is nothing to kill and the guard
// must allow the restart to proceed. If this regresses the witness would
// be unable to respawn a crashed polecat — exactly the opposite of what
// gs-549 fix #2 is trying to achieve.
func TestSessionTooYoung_NoSession(t *testing.T) {
	defer withFakePolecatSessionAge(t, 0, false)()
	defer withTightPolecatMinSessionAge(t, 5*time.Minute)()

	skip, age := IsPolecatSessionTooYoung("rig-a", "alpha")
	if skip {
		t.Errorf("expected restart allowed when no session exists, got skip=true age=%v", age)
	}
	if age != 0 {
		t.Errorf("expected age=0 when no session exists, got %v", age)
	}
}

// TestSessionTooYoung_LiveYoungSession is the core gs-549 fix #2 case: a
// live tmux session younger than the guard must be left alone so the
// agent can finish starting up and write its first heartbeat.
func TestSessionTooYoung_LiveYoungSession(t *testing.T) {
	defer withTightPolecatMinSessionAge(t, 5*time.Minute)()
	defer withFakePolecatSessionAge(t, 30*time.Second, true)()

	skip, age := IsPolecatSessionTooYoung("rig-a", "bravo")
	if !skip {
		t.Errorf("expected restart deferred for 30s-old session under 5m guard, got skip=false")
	}
	if age != 30*time.Second {
		t.Errorf("expected reported age=30s, got %v", age)
	}
}

// TestSessionTooYoung_LiveOldSession verifies the guard opens once the
// session has aged past the threshold — otherwise a permanently stuck
// polecat could never be restarted.
func TestSessionTooYoung_LiveOldSession(t *testing.T) {
	defer withTightPolecatMinSessionAge(t, 5*time.Minute)()
	defer withFakePolecatSessionAge(t, 10*time.Minute, true)()

	skip, _ := IsPolecatSessionTooYoung("rig-a", "charlie")
	if skip {
		t.Errorf("expected restart allowed for 10m-old session under 5m guard, got skip=true")
	}
}

// TestRestartWithBackoff_SkipsYoungSession verifies the wrapper integration:
// RestartPolecatWithBackoff must surface ErrPolecatSessionTooYoung as a
// recognizable skip rather than attempting RestartPolecatSession. The
// test passes a workDir tmpdir to keep the backoff state isolated from
// other tests.
func TestRestartWithBackoff_SkipsYoungSession(t *testing.T) {
	defer withTightPolecatMinSessionAge(t, 5*time.Minute)()
	defer withFakePolecatSessionAge(t, 1*time.Minute, true)()

	tmp := newPolecatBackoffTempDir(t)

	err := RestartPolecatWithBackoff(tmp, "rig-a", "delta")
	if err == nil {
		t.Fatal("expected RestartPolecatWithBackoff to skip a too-young session, got nil error")
	}
	if !errors.Is(err, ErrPolecatSessionTooYoung) {
		t.Errorf("expected ErrPolecatSessionTooYoung in error chain, got %v", err)
	}
	if !isPolecatRestartSkip(err) {
		t.Errorf("expected isPolecatRestartSkip to recognize too-young error, got false (err=%v)", err)
	}
	if !strings.Contains(err.Error(), "before restartable") {
		t.Errorf("expected human-readable remaining-time hint, got %q", err.Error())
	}
}

// TestRestartWithBackoff_BackoffStillRecognizedAsSkip is a regression
// guard for the rename from isPolecatBackoffSkip to isPolecatRestartSkip:
// pre-existing backoff skips must still be classified as deliberate skips,
// not failures.
func TestRestartWithBackoff_BackoffStillRecognizedAsSkip(t *testing.T) {
	defer withTightPolecatBackoff(t)()
	defer withTightPolecatMinSessionAge(t, 5*time.Minute)()
	defer withFakePolecatSessionAge(t, 0, false)() // no session, would otherwise allow restart

	tmp := newPolecatBackoffTempDir(t)
	RecordPolecatStartFailure(tmp, "rig-a", "echo")

	err := RestartPolecatWithBackoff(tmp, "rig-a", "echo")
	if err == nil {
		t.Fatal("expected RestartPolecatWithBackoff to skip during backoff, got nil error")
	}
	if !errors.Is(err, ErrPolecatInStartupBackoff) {
		t.Errorf("expected ErrPolecatInStartupBackoff in error chain, got %v", err)
	}
	if !isPolecatRestartSkip(err) {
		t.Errorf("expected isPolecatRestartSkip to recognize backoff error, got false (err=%v)", err)
	}
}
