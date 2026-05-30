package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNudgeSession_TestModeShortCircuit verifies that, when the package's own
// real-tmux opt-in (GT_TEST_TMUX_REAL_NUDGE) is cleared, NudgeSession no-ops
// instead of reaching into a live tmux session. Filed under gu-l8zp:
//
// > internal/witness/handlers.go HandlePolecatDoneFromAgent unconditionally
// > nudges the live mayor session when payload.PushFailed=true. Tests built
// > a synthetic PolecatDonePayload, the handler ran in-process, and the
// > developer's mayor inbox received a real PUSH_FAILED URGENT nudge with
// > test fixture branch names (`polecat/deathclaw/lost`, `gt-test-issue`).
//
// This test exercises the lowest-level boundary that the fix lives at. If
// somebody ever removes the test-mode guard from NudgeSessionWithOpts, this
// test will fail because the bare NudgeSession call against a session that
// does NOT exist on the test socket would error — instead of returning nil.
func TestNudgeSession_TestModeShortCircuit(t *testing.T) {
	// testmain_test.go sets GT_TEST_TMUX_REAL_NUDGE=1 for this package. Clear
	// it for the duration of this test only, simulating how every OTHER test
	// package sees the guard by default.
	t.Setenv(EnvTmuxRealNudge, "")

	logPath := filepath.Join(t.TempDir(), "nudge.log")
	t.Setenv(EnvTestNudgeLog, logPath)

	tm := NewTmux()

	// "hq-mayor-not-real" deliberately does not exist on the test socket.
	// Without the guard this would either error out or, on a developer's
	// machine where they happen to have a session by that name, leak a
	// real nudge.
	const fakeSession = "hq-mayor-not-real"
	const msg = "PUSH_FAILED: polecat=fake branch=polecat/fake/lost issue=gt-test-issue"

	if err := tm.NudgeSession(fakeSession, msg); err != nil {
		t.Fatalf("NudgeSession() under test mode = %v, want nil (guard should short-circuit before any tmux call)", err)
	}

	// The capture log should contain the suppressed nudge so tests still have
	// observability. Format mirrors internal/cmd/sling_helpers.go.
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading nudge log: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, fakeSession) || !strings.Contains(got, msg) {
		t.Errorf("nudge log %q does not contain expected session %q and message %q", got, fakeSession, msg)
	}
}

// TestNudgeSession_TestModeWithoutLogPath verifies that the guard still
// short-circuits when GT_TEST_NUDGE_LOG is unset — i.e. tests that don't
// care about capturing nudges should not need to set it just to avoid
// leaking into production.
func TestNudgeSession_TestModeWithoutLogPath(t *testing.T) {
	t.Setenv(EnvTmuxRealNudge, "")
	t.Setenv(EnvTestNudgeLog, "")

	tm := NewTmux()
	if err := tm.NudgeSession("hq-mayor-not-real", "should be discarded"); err != nil {
		t.Fatalf("NudgeSession() with no log path = %v, want nil", err)
	}
}

// TestNudgeSession_RealOptInBypassesGuard verifies that callers who set
// GT_TEST_TMUX_REAL_NUDGE=1 still go through the real send-keys path. This
// guards against an over-eager future change that short-circuits unconditionally
// and breaks the tmux package's own integration tests.
func TestNudgeSession_RealOptInBypassesGuard(t *testing.T) {
	// testmain_test.go has already set this; assert the guard respects it.
	if v := os.Getenv(EnvTmuxRealNudge); v == "" {
		t.Fatalf("expected %s to be set by TestMain for this package", EnvTmuxRealNudge)
	}

	skip, _ := testNudgeShortCircuit()
	if skip {
		t.Errorf("testNudgeShortCircuit() = true under GT_TEST_TMUX_REAL_NUDGE=1, want false (real path must be reachable)")
	}
}
