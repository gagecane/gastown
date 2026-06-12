package daemon

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	beadsdk "github.com/steveyegge/beads"
)

// writeFakeTmuxWithSession creates a fake tmux binary that reports the Deacon
// session as existing (has-session returns 0). Used for deacon idle guard tests
// where the session must be present so checkDeaconHeartbeat reaches the nudge path.
func writeFakeTmuxWithSession(t *testing.T, dir string) {
	t.Helper()
	script := `#!/usr/bin/env bash
set -euo pipefail

cmd=""
skip_next=0
for arg in "$@"; do
  if [[ "$skip_next" -eq 1 ]]; then
    skip_next=0
    continue
  fi
  if [[ "$arg" == "-u" ]]; then
    continue
  fi
  if [[ "$arg" == "-L" ]]; then
    skip_next=1
    continue
  fi
  cmd="$arg"
  break
done

if [[ -n "${TMUX_LOG:-}" ]]; then
  printf "%s %s\n" "$cmd" "$*" >> "$TMUX_LOG"
fi

if [[ "${1:-}" == "-V" ]]; then
  echo "tmux 3.3a"
  exit 0
fi

# Session exists: has-session returns 0 so the nudge path is reachable.
if [[ "$cmd" == "has-session" ]]; then
  exit 0
fi

# list-sessions: when TMUX_SESSION_CREATED is set, emit it as the session_created
# Unix timestamp. Used by gs-3ee tests that exercise the checkDeaconAge fallback
# via GetSessionCreatedTime. Empty by default so other tests see "no session".
if [[ "$cmd" == "list-sessions" ]]; then
  if [[ -n "${TMUX_SESSION_CREATED:-}" ]]; then
    echo "${TMUX_SESSION_CREATED}"
  fi
  exit 0
fi

# capture-pane: emit TMUX_PANE_CONTENT when set so IsIdle can be exercised
# (gu-8izpk parked-prompt discriminator). Empty by default → IsIdle=false,
# preserving the original idle-guard suppression behavior.
if [[ "$cmd" == "capture-pane" ]]; then
  if [[ -n "${TMUX_PANE_CONTENT:-}" ]]; then
    printf "%b\n" "${TMUX_PANE_CONTENT}"
  fi
  exit 0
fi

exit 0
`
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
}

// TestCheckDeaconHeartbeat_IdleGuard verifies that the nudge is suppressed when
// the Deacon heartbeat is stale but no active work is in flight (idle guard).
func TestCheckDeaconHeartbeat_IdleGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}

	tests := []struct {
		name             string
		heartbeatAge     time.Duration
		stores           map[string]beadsdk.Storage
		wantNudgeLog     bool
		wantIdleGuardLog bool
		desc             string
	}{
		{
			name:         "idle: stale heartbeat, no work — nudge suppressed",
			heartbeatAge: 20 * time.Minute,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{}},
			},
			wantNudgeLog:     false,
			wantIdleGuardLog: true,
			desc:             "Idle guard must suppress nudge when no work is in flight",
		},
		{
			name:         "active work: stale heartbeat, in_progress bead — nudge sent",
			heartbeatAge: 20 * time.Minute,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{
					"in_progress": {{ID: "sc-abc"}},
				}},
			},
			wantNudgeLog:     true,
			wantIdleGuardLog: false,
			desc:             "Nudge must fire when in_progress work exists",
		},
		{
			name:         "hooked only: stale heartbeat, patrol wisp — nudge suppressed",
			heartbeatAge: 20 * time.Minute,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{
					"hooked": {{ID: "hq-wisp-34zi"}},
				}},
			},
			wantNudgeLog:     false,
			wantIdleGuardLog: true,
			desc:             "Patrol wisps in hooked state do not count as active work; nudge must be suppressed",
		},
		{
			name:         "store error: stale heartbeat, store fails — nudge sent conservatively",
			heartbeatAge: 20 * time.Minute,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{err: fmt.Errorf("db offline")},
			},
			wantNudgeLog:     true,
			wantIdleGuardLog: false,
			desc:             "Nudge must fire conservatively when work state is unknown",
		},
		{
			name:         "very stale: heartbeat >= 30 min — escalation path, no nudge",
			heartbeatAge: 31 * time.Minute,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{}},
			},
			wantNudgeLog:     false,
			wantIdleGuardLog: false,
			desc:             "Very stale heartbeat takes escalation path, not nudge path; idle guard not reached",
		},
		{
			name:         "fresh idle: heartbeat 10m old (within backoff-max=15m) — no nudge, no log",
			heartbeatAge: 10 * time.Minute,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{
					"in_progress": {{ID: "sc-active"}},
				}},
			},
			wantNudgeLog:     false,
			wantIdleGuardLog: false,
			desc:             "10m heartbeat is fresh (<16m); the early-return on IsFresh fires before any stuck/idle path. Regression guard for gu-70rg.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			townRoot := t.TempDir()
			fakeBinDir := t.TempDir()
			tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
			if err := os.WriteFile(tmuxLog, []byte{}, 0o644); err != nil {
				t.Fatalf("create tmux log: %v", err)
			}

			writeFakeTmuxWithSession(t, fakeBinDir)
			t.Setenv("PATH", fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TMUX_LOG", tmuxLog)

			writeDeaconHeartbeat(t, townRoot, tc.heartbeatAge)

			d := newTestDaemonWithStores(t, townRoot, tc.stores)

			logBuf := &strings.Builder{}
			d.logger = log.New(logBuf, "", 0)

			d.checkDeaconHeartbeat()

			logOutput := logBuf.String()

			hasIdleGuardLog := strings.Contains(logOutput, "nudge skipped")
			if hasIdleGuardLog != tc.wantIdleGuardLog {
				t.Errorf("%s\nidle guard log present=%v, want=%v\nlog:\n%s",
					tc.desc, hasIdleGuardLog, tc.wantIdleGuardLog, logOutput)
			}

			hasNudgeLog := strings.Contains(logOutput, "nudging session")
			if hasNudgeLog != tc.wantNudgeLog {
				t.Errorf("%s\nnudge log present=%v, want=%v\nlog:\n%s",
					tc.desc, hasNudgeLog, tc.wantNudgeLog, logOutput)
			}
		})
	}
}

// TestCheckDeaconHeartbeat_ParkedPromptResume verifies the gu-8izpk fix: a
// Deacon parked at its idle prompt (interrupted bash heartbeat-write dropped it
// back to ❯) with a stale heartbeat and NO active work must be nudged to
// auto-resume — it is not in await-signal and will never self-wake. The
// pre-fix idle guard would suppress the nudge and strand it until the 30m
// very-stale kill+restart.
func TestCheckDeaconHeartbeat_ParkedPromptResume(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}

	tests := []struct {
		name             string
		paneContent      string
		wantNudgeLog     bool
		wantIdleGuardLog bool
		wantResumeLog    bool
		desc             string
	}{
		{
			name:             "parked at idle prompt — resume nudge fires",
			paneContent:      "❯ ",
			wantNudgeLog:     false, // parked path logs "nudging to auto-resume", not "nudging session"
			wantIdleGuardLog: false,
			wantResumeLog:    true,
			desc:             "Parked Deacon (idle prompt visible) must be nudged to auto-resume despite no active work",
		},
		{
			name:             "busy in await-signal — suppression preserved",
			paneContent:      "⏵⏵ Running await-signal... esc to interrupt",
			wantNudgeLog:     false,
			wantIdleGuardLog: true,
			wantResumeLog:    false,
			desc:             "A Deacon blocked in await-signal shows the busy indicator; the idle guard must still suppress",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			townRoot := t.TempDir()
			fakeBinDir := t.TempDir()
			tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
			if err := os.WriteFile(tmuxLog, []byte{}, 0o644); err != nil {
				t.Fatalf("create tmux log: %v", err)
			}

			writeFakeTmuxWithSession(t, fakeBinDir)
			t.Setenv("PATH", fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TMUX_LOG", tmuxLog)
			t.Setenv("TMUX_PANE_CONTENT", tc.paneContent)

			// Stale (not very stale) heartbeat reaches the idle-guard branch.
			writeDeaconHeartbeat(t, townRoot, 20*time.Minute)

			// No active work in flight — without the parked-prompt exception
			// the idle guard suppresses the nudge unconditionally.
			stores := map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{}},
			}
			d := newTestDaemonWithStores(t, townRoot, stores)

			logBuf := &strings.Builder{}
			d.logger = log.New(logBuf, "", 0)

			d.checkDeaconHeartbeat()

			logOutput := logBuf.String()

			hasResumeLog := strings.Contains(logOutput, "parked at idle prompt")
			if hasResumeLog != tc.wantResumeLog {
				t.Errorf("%s\nresume log present=%v, want=%v\nlog:\n%s",
					tc.desc, hasResumeLog, tc.wantResumeLog, logOutput)
			}

			hasIdleGuardLog := strings.Contains(logOutput, "nudge skipped")
			if hasIdleGuardLog != tc.wantIdleGuardLog {
				t.Errorf("%s\nidle guard log present=%v, want=%v\nlog:\n%s",
					tc.desc, hasIdleGuardLog, tc.wantIdleGuardLog, logOutput)
			}

			hasNudgeLog := strings.Contains(logOutput, "nudging session")
			if hasNudgeLog != tc.wantNudgeLog {
				t.Errorf("%s\nnudge log present=%v, want=%v\nlog:\n%s",
					tc.desc, hasNudgeLog, tc.wantNudgeLog, logOutput)
			}
		})
	}
}
