package witness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/tmux"
)

// pushFailedTestSetup builds a workspace-shaped temp dir and configures the
// nudge log so any mayor PUSH_FAILED nudges are captured rather than sent to
// a live tmux session. Returns the (townRoot, workDir) pair.
//
// Mirrors push_failed_nudge_isolation_test.go's setup; centralised so the
// per-outcome cases below stay focused on the assertion.
func pushFailedTestSetup(t *testing.T) (townRoot, workDir string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "nudge.log")
	t.Setenv(tmux.EnvTestNudgeLog, logPath)

	townRoot = t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	workDir = filepath.Join(townRoot, "gastown", "witness")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return townRoot, workDir
}

// withStubbedRecovery installs a stub for recoverPushFailed that returns the
// requested outcome and restores the previous binding when the test ends.
// Recovery budget is reset before and after so cap state from earlier tests
// can't leak in (or out).
func withStubbedRecovery(t *testing.T, outcome PushRecoveryOutcome) {
	t.Helper()
	resetPushRecoveryBudget()
	prev := recoverPushFailed
	recoverPushFailed = func(_ string, _, _, _ string) PushRecoveryOutcome {
		return outcome
	}
	t.Cleanup(func() {
		recoverPushFailed = prev
		resetPushRecoveryBudget()
	})
}

// pushFailedFields builds the canonical PushFailed=true agent fields used by
// the per-outcome cases below.
func pushFailedFields() *beads.AgentFields {
	return &beads.AgentFields{
		ExitType:        "FAILED",
		Branch:          "polecat/deathclaw/lost",
		HookBead:        "gt-test-issue",
		LastSourceIssue: "gt-test-issue",
		PushFailed:      true,
		CompletionTime:  "2026-05-31T05:00:00Z",
	}
}

// TestHandlePolecatDoneFromBead_PushFailed_Diverged_Escalates verifies that
// when the recovery handler reports incompatible histories, the witness
// preserves the existing escalate-to-mayor behavior with an action label
// that describes the divergence.
func TestHandlePolecatDoneFromBead_PushFailed_Diverged_Escalates(t *testing.T) {
	withStubbedRecovery(t, PushRecoveryDiverged)
	_, workDir := pushFailedTestSetup(t)

	result := HandlePolecatDoneFromBead(DefaultBdCli(), workDir, "gastown", "deathclaw", pushFailedFields(), nil)

	if !result.Handled {
		t.Fatalf("expected Handled=true on diverged outcome, got result=%+v", result)
	}
	if !strings.Contains(result.Action, "push-failed-recovery-diverged") {
		t.Errorf("expected diverged action label, got %q", result.Action)
	}
}

// TestHandlePolecatDoneFromBead_PushFailed_Backoff_Escalates verifies that
// when the per-branch retry budget is exhausted, the witness still routes
// through the escalate path (mayor needs to know we've stopped trying).
func TestHandlePolecatDoneFromBead_PushFailed_Backoff_Escalates(t *testing.T) {
	withStubbedRecovery(t, PushRecoveryBackoff)
	_, workDir := pushFailedTestSetup(t)

	result := HandlePolecatDoneFromBead(DefaultBdCli(), workDir, "gastown", "deathclaw", pushFailedFields(), nil)

	if !result.Handled {
		t.Fatalf("expected Handled=true on backoff outcome, got result=%+v", result)
	}
	if !strings.Contains(result.Action, "push-failed-recovery-backoff") {
		t.Errorf("expected backoff action label, got %q", result.Action)
	}
}

// TestHandlePolecatDoneFromBead_PushFailed_Unknown_Escalates verifies that
// when recovery couldn't be attempted (worktree missing, ls-remote failed,
// etc) the witness preserves the original escalate-to-mayor behavior.
func TestHandlePolecatDoneFromBead_PushFailed_Unknown_Escalates(t *testing.T) {
	withStubbedRecovery(t, PushRecoveryUnknown)
	_, workDir := pushFailedTestSetup(t)

	result := HandlePolecatDoneFromBead(DefaultBdCli(), workDir, "gastown", "deathclaw", pushFailedFields(), nil)

	if !result.Handled {
		t.Fatalf("expected Handled=true on unknown outcome, got result=%+v", result)
	}
	if !strings.Contains(result.Action, "push-failed-recovery-unknown") {
		t.Errorf("expected unknown action label, got %q", result.Action)
	}
}

// TestHandlePolecatDoneFromBead_PushFailed_Pushed_FallsThrough verifies the
// success path: when recovery successfully pushes the branch the action
// label records the recovery and the handler routes through normal
// completion (Handled is set by the downstream completion routing, not by
// the recovery branch returning early).
func TestHandlePolecatDoneFromBead_PushFailed_Pushed_FallsThrough(t *testing.T) {
	withStubbedRecovery(t, PushRecoveryPushed)
	_, workDir := pushFailedTestSetup(t)

	fields := pushFailedFields()
	fields.ExitType = "COMPLETED" // exercise the no-MR completion path

	result := HandlePolecatDoneFromBead(DefaultBdCli(), workDir, "gastown", "deathclaw", fields, nil)

	// The recovery branch DOES NOT return early on Pushed/AlreadyOnOrigin —
	// it falls through to the existing routing, which handles the COMPLETED
	// payload. We assert the action label was set by recovery and that the
	// handler did NOT short-circuit with the divergence-style escalate.
	if !strings.Contains(result.Action, "push-failed-recovery-pushed") {
		t.Errorf("expected pushed action label to survive fallthrough, got %q", result.Action)
	}
	if strings.Contains(result.Action, "diverged") || strings.Contains(result.Action, "backoff") {
		t.Errorf("pushed outcome should not produce a diverge/backoff action, got %q", result.Action)
	}
}

// TestHandlePolecatDoneFromBead_PushFailed_AlreadyOnOrigin_FallsThrough is
// the symmetric case to Pushed: a race-safe re-check showed origin already
// has the branch, so we clear the sticky flag and continue routing.
func TestHandlePolecatDoneFromBead_PushFailed_AlreadyOnOrigin_FallsThrough(t *testing.T) {
	withStubbedRecovery(t, PushRecoveryAlreadyOnOrigin)
	_, workDir := pushFailedTestSetup(t)

	fields := pushFailedFields()
	fields.ExitType = "COMPLETED"

	result := HandlePolecatDoneFromBead(DefaultBdCli(), workDir, "gastown", "deathclaw", fields, nil)

	if !strings.Contains(result.Action, "push-failed-recovery-already-on-origin") {
		t.Errorf("expected already-on-origin action label, got %q", result.Action)
	}
}

// TestRecoverPushFailed_BudgetCap_TripsBackoff verifies that the in-process
// per-branch retry cap is enforced. After pushRecoveryMaxAttempts successive
// calls for the same (rig, polecat, branch), the next attempt returns
// PushRecoveryBackoff regardless of what the underlying git ops would do.
//
// We exercise the real _recoverPushFailed (not the stub) so this also serves
// as the budget-accounting regression test: a future refactor that moves
// the chargePushRecovery call could silently uncap the loop, and this test
// would catch it.
func TestRecoverPushFailed_BudgetCap_TripsBackoff(t *testing.T) {
	resetPushRecoveryBudget()
	t.Cleanup(resetPushRecoveryBudget)

	// townRoot points at a non-existent path so polecatWorktreePath will
	// stat-miss → PushRecoveryUnknown. That is sufficient: each call still
	// charges the budget (per the chargePushRecovery contract — see
	// push_failed_recovery.go), so after MaxAttempts further calls flip to
	// Backoff. We are testing the cap, not the git path.
	bogusTownRoot := filepath.Join(t.TempDir(), "no-such-town")

	for i := 0; i < pushRecoveryMaxAttempts; i++ {
		got := _recoverPushFailed(bogusTownRoot, "gastown", "deathclaw", "polecat/deathclaw/lost")
		if got != PushRecoveryUnknown {
			t.Fatalf("attempt %d: expected Unknown (worktree absent), got %s", i+1, got)
		}
	}
	// Cap exhausted — the next call MUST short-circuit to Backoff before
	// reaching the worktree stat (otherwise a stuck rig spins forever).
	got := _recoverPushFailed(bogusTownRoot, "gastown", "deathclaw", "polecat/deathclaw/lost")
	if got != PushRecoveryBackoff {
		t.Errorf("expected Backoff after %d attempts, got %s", pushRecoveryMaxAttempts, got)
	}
}

// TestRecoverPushFailed_BudgetIsPerBranch verifies that the cap is keyed by
// branch — exhausting one branch's budget does NOT block a different branch
// on the same polecat.
func TestRecoverPushFailed_BudgetIsPerBranch(t *testing.T) {
	resetPushRecoveryBudget()
	t.Cleanup(resetPushRecoveryBudget)

	bogusTownRoot := filepath.Join(t.TempDir(), "no-such-town")
	const polecatName = "deathclaw"
	const branchA = "polecat/deathclaw/branch-a"
	const branchB = "polecat/deathclaw/branch-b"

	// Exhaust branch A.
	for i := 0; i < pushRecoveryMaxAttempts; i++ {
		_ = _recoverPushFailed(bogusTownRoot, "gastown", polecatName, branchA)
	}
	if got := _recoverPushFailed(bogusTownRoot, "gastown", polecatName, branchA); got != PushRecoveryBackoff {
		t.Fatalf("branchA: expected Backoff, got %s", got)
	}
	// Branch B should still be allowed.
	if got := _recoverPushFailed(bogusTownRoot, "gastown", polecatName, branchB); got == PushRecoveryBackoff {
		t.Errorf("branchB: expected non-Backoff (independent budget), got Backoff")
	}
}

// TestPushRecoveryOutcome_String covers the action-string contract — these
// values are embedded in HandlerResult.Action and CompletionDiscovery.Action,
// which the daemon and operator log scrapers match on. A typo here would
// silently desync those surfaces from the witness.
func TestPushRecoveryOutcome_String(t *testing.T) {
	cases := []struct {
		outcome PushRecoveryOutcome
		want    string
	}{
		{PushRecoveryUnknown, "unknown"},
		{PushRecoveryAlreadyOnOrigin, "already-on-origin"},
		{PushRecoveryPushed, "pushed"},
		{PushRecoveryDiverged, "diverged"},
		{PushRecoveryBackoff, "backoff"},
	}
	for _, tc := range cases {
		if got := tc.outcome.String(); got != tc.want {
			t.Errorf("outcome %d: got %q, want %q", tc.outcome, got, tc.want)
		}
	}
}
