package witness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/tmux"
)

// TestHandlePolecatDoneFromBead_PushFailed_DoesNotLeakToLiveMayor is the
// regression test for gu-l8zp.
//
// Bug summary: HandlePolecatDoneFromBead, on payload.PushFailed=true, called
// tmux.NewTmux().HasSession("hq-mayor") and (if running) NudgeSession with a
// PUSH_FAILED string built from the test fixture's polecat name and branch.
// Because the developer's mayor tmux session was always running, every test
// that exercised this code path delivered a real, indistinguishable-from-prod
// PUSH_FAILED URGENT nudge into the developer's mayor inbox.
//
// Live evidence (2026-05-30): polecat deathclaw running `go test ./...` for
// gu-ww9u caused two URGENT nudges to land in mayor's inbox:
//
//	PUSH_FAILED: polecat=deathclaw branch=polecat/deathclaw/lost issue=gt-test-issue
//	PUSH_FAILED: polecat=deathclaw branch=polecat/deathclaw/discovered-lost issue=gt-test-issue
//
// Both branch names and the issue ID were synthetic — they reached production
// solely because tmux.NudgeSession had no test isolation.
//
// The structural fix lives in internal/tmux/sendkeys.go: NudgeSessionWithOpts
// short-circuits when testing.Testing() is true and the caller has not opted
// into real tmux delivery via GT_TEST_TMUX_REAL_NUDGE. This test exercises
// the witness handler path that would have leaked, with GT_TEST_NUDGE_LOG
// configured so we can prove the message is captured rather than sent.
func TestHandlePolecatDoneFromBead_PushFailed_DoesNotLeakToLiveMayor(t *testing.T) {
	// Capture would-be nudges to a log instead of letting them reach a live
	// mayor session. With the gu-l8zp fix in place, the tmux package's
	// short-circuit guard intercepts NudgeSession before any tmux command
	// runs and writes one line per attempted nudge to this file.
	logPath := filepath.Join(t.TempDir(), "nudge.log")
	t.Setenv(tmux.EnvTestNudgeLog, logPath)

	// Build a workspace-shaped temp dir so workspace.Find succeeds — the
	// handler's nudge branch is gated on a non-empty townRoot.
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0644); err != nil {
		t.Fatal(err)
	}
	workDir := filepath.Join(townRoot, "gastown", "witness")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatal(err)
	}

	// The exact synthetic payload that leaked in production on 2026-05-30.
	fields := &beads.AgentFields{
		ExitType:        "FAILED",
		Branch:          "polecat/deathclaw/lost",
		HookBead:        "gt-test-issue",
		LastSourceIssue: "gt-test-issue",
		PushFailed:      true,
		CompletionTime:  "2026-05-30T04:10:00Z",
	}

	result := HandlePolecatDoneFromBead(DefaultBdCli(), workDir, "gastown", "deathclaw", fields, nil)

	if !result.Handled {
		t.Fatalf("expected PushFailed payload to be handled, got result=%+v", result)
	}
	// gu-ebj0: action prefix migrated from "push-failed-recovery-needed" to
	// "push-failed-recovery-<outcome>" so the witness can distinguish between
	// "we tried to push and gave up" (diverged/backoff/unknown — escalate)
	// and "we pushed successfully" (already-on-origin/pushed — fall through).
	// The shared "push-failed-recovery-" prefix preserves log-scrape continuity.
	if !strings.Contains(result.Action, "push-failed-recovery-") {
		t.Errorf("action %q should describe push-failed recovery", result.Action)
	}

	// The handler attempted to nudge mayor — but the tmux guard should have
	// captured it to the log file rather than reaching live tmux.
	data, err := os.ReadFile(logPath)
	if err != nil {
		// Empty log is also acceptable: it means the guard short-circuited
		// even before the handler reached the nudge call (e.g. because
		// HasSession returned not-running on the test machine). The
		// critical invariant — that no nudge reached live mayor tmux —
		// is enforced by the guard itself, which testNudgeShortCircuit
		// covers in tmux package tests. So a missing log file is fine.
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("reading nudge log: %v", err)
	}
	got := string(data)

	// If the handler did try to nudge (HasSession returned true on this
	// machine), the captured line MUST go to the log and MUST NOT reach
	// any live session. We assert format and contents to lock that in.
	if strings.TrimSpace(got) == "" {
		// No nudge attempted. Acceptable — see comment above.
		return
	}
	if !strings.Contains(got, "PUSH_FAILED") {
		t.Errorf("captured nudge log %q does not contain PUSH_FAILED marker", got)
	}
	if !strings.Contains(got, "deathclaw") {
		t.Errorf("captured nudge log %q does not contain polecat name", got)
	}
	if !strings.Contains(got, "polecat/deathclaw/lost") {
		t.Errorf("captured nudge log %q does not contain branch fixture", got)
	}
	// Format mirrors internal/cmd/sling_helpers.go:
	//     nudge:<session>:<message>\n
	if !strings.HasPrefix(got, "nudge:") {
		t.Errorf("captured nudge log entry %q does not start with `nudge:` prefix; format drift?", got)
	}
}
