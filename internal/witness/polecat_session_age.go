package witness

import (
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

// Polecat min-time-since-spawn guard (gs-549 fix #2)
// ==================================================
//
// On 2026-05-19, lia_bac lost 14 polecats over ~50 minutes in coordinated
// death batches under sustained load (~25 avg on 12 CPUs). The cascade was:
//
//   sustained load → polecat tool call stalls
//   → stuck-agent-dog flags stalled (STUCK_STALLED_THRESHOLD=600s)
//   → witness RESTART_POLECAT kills + respawns the session
//   → new session inherits the same load + still-stalling tool call
//   → loop repeats every patrol cycle
//
// The existing startup backoff (polecat_startup_backoff.go) only kicks in
// AFTER a recorded restart failure. A polecat that the witness "successfully"
// restarts but the dog then re-flags as stalled within 10 minutes does not
// trip backoff — it just dies again, and again, and again.
//
// This file adds the narrowest correctness fix called for by gs-549: refuse
// to kill a tmux session that is younger than polecatMinSessionAge. A polecat
// killed mid-startup never gets the chance to write its first heartbeat,
// which the dog then re-flags as stalled — feeding the restart cascade.
//
// Dead sessions are not gated: when GetSessionCreatedUnix reports no session,
// there is nothing to kill, so the slot can be respawned normally. The guard
// applies only to LIVE sessions that recently started.

// polecatMinSessionAge is the minimum age a tmux session must reach before
// the witness will honor a restart request. Sessions younger than this
// are presumed to still be in normal startup (kiro-cli initialization,
// initial bd prime, first heartbeat write) and are spared.
//
// 5 minutes is chosen to comfortably exceed normal cold-start time on a
// large repo (sling can take several minutes — see SpawnGracePeriod in
// handlers.go) while still being short enough that a genuinely broken
// polecat eventually becomes restartable.
//
// Package-level var so tests can shorten without faking time.
var polecatMinSessionAge = 5 * time.Minute

// ErrPolecatSessionTooYoung is the sentinel returned by
// restartPolecatWithBackoff when the polecat's live tmux session is
// younger than polecatMinSessionAge. Callers unwrap it via
// isPolecatRestartSkip to distinguish "we chose not to restart yet"
// from "restart was attempted and failed."
var ErrPolecatSessionTooYoung = errors.New("polecat session too young")

// polecatSessionAge returns the age of a polecat's live tmux session and
// whether the session exists. Package-level var so tests can substitute
// without standing up a real tmux server.
var polecatSessionAge = func(rigName, polecatName string) (age time.Duration, exists bool) {
	t := tmux.NewTmux()
	sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)
	createdUnix, err := t.GetSessionCreatedUnix(sessionName)
	if err != nil || createdUnix <= 0 {
		return 0, false
	}
	return time.Since(time.Unix(createdUnix, 0)), true
}

// IsPolecatSessionTooYoung reports whether a polecat's tmux session is
// younger than polecatMinSessionAge. If the session does not exist, the
// guard returns (false, 0) — there is nothing to kill so the restart
// should proceed (creating a fresh session).
//
// Returns (skip, age) where skip indicates the restart should be deferred
// and age is the current session age for diagnostic logging.
func IsPolecatSessionTooYoung(rigName, polecatName string) (bool, time.Duration) {
	age, exists := polecatSessionAge(rigName, polecatName)
	if !exists {
		return false, 0
	}
	return age < polecatMinSessionAge, age
}

// formatSessionTooYoungReason returns a human-readable reason suitable
// for ZombieResult.Action / log lines, including the remaining wait
// before the session becomes restartable.
func formatSessionTooYoungReason(rigName, polecatName string, age time.Duration) string {
	remaining := (polecatMinSessionAge - age).Round(time.Second)
	return fmt.Sprintf("polecat %s/%s session too young (age=%s, %s before restartable)",
		rigName, polecatName, age.Round(time.Second), remaining)
}
