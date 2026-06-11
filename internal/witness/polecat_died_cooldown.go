package witness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// polecatDiedNotifyState is the file-backed dedup record for POLECAT_DIED
// escalations. The witness patrol runs as a fresh `gt patrol scan` process each
// cycle, so in-memory dedup (MessageDeduplicator) does not survive between
// cycles — the same dead polecat in the same stuck state was re-escalated to
// mayor on every patrol, flooding the inbox (4 POLECAT_DIED mails in 17min for
// one stuck polecat observed on gu-b4b39).
//
// This record persists the last escalation per (rig, polecat) under
// .runtime/polecat_died/ so the next cycle's process can decide whether the
// condition is "the same death already reported" (suppress, bump counter)
// versus "materially different" (re-escalate).
type polecatDiedNotifyState struct {
	// LastNotifiedAt is when we last sent a POLECAT_DIED mail for this polecat.
	LastNotifiedAt time.Time `json:"last_notified_at"`
	// LastSignature is the stuck-state signature at last notification:
	// classification + hook bead. A change means the polecat moved to a
	// materially different failure (different work or different death class)
	// and is worth re-notifying even inside the cooldown window.
	LastSignature string `json:"last_signature"`
	// SuppressedSince is when we first started suppressing the current run of
	// identical alarms (i.e. the cycle after LastNotifiedAt's mail). Used to
	// report the duration of the suppressed window in the next mail.
	SuppressedSince time.Time `json:"suppressed_since,omitempty"`
	// SuppressedCount counts patrol cycles suppressed since LastNotifiedAt for
	// the same signature. Rolled into the next escalation ("fired N more times
	// since ...") so the operator sees the alarm has been persistent without
	// the inbox flood.
	SuppressedCount int `json:"suppressed_count,omitempty"`
	// RestartLoopReported records whether the one-shot
	// "restart-loop-not-clearing" diagnostic has already fired for the current
	// crash-loop episode, so it surfaces once rather than every cycle.
	RestartLoopReported bool `json:"restart_loop_reported,omitempty"`
}

// polecatDiedSignature builds the stuck-state signature for a zombie: the
// classification plus the hooked bead. Two cycles with the same signature are
// "the same death"; a change (e.g. the polecat is restarted onto a new bead, or
// its death reclassifies) is a material change worth a fresh escalation.
func polecatDiedSignature(z ZombieResult) string {
	return string(z.Classification) + "|" + z.HookBead
}

func polecatDiedStateDir(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "polecat_died")
}

func polecatDiedStateFile(townRoot, rigName, polecatName string) string {
	// rig + polecat uniquely identify the watched worker. Sanitize separators so
	// the key is a safe single filename component.
	safe := strings.ReplaceAll(rigName+"__"+polecatName, "/", "_")
	return filepath.Join(polecatDiedStateDir(townRoot), safe+".json")
}

// readPolecatDiedState returns the dedup record for a polecat, or nil if none
// exists or it cannot be parsed. A malformed record is treated as absent so a
// corrupt file can never wedge escalation off permanently (fails open toward
// visibility — the safe direction for an alarm).
func readPolecatDiedState(townRoot, rigName, polecatName string) *polecatDiedNotifyState {
	data, err := os.ReadFile(polecatDiedStateFile(townRoot, rigName, polecatName))
	if err != nil {
		return nil
	}
	var s polecatDiedNotifyState
	if json.Unmarshal(data, &s) != nil {
		return nil
	}
	return &s
}

// writePolecatDiedState persists the dedup record. Best-effort: a write failure
// only means the next cycle may re-notify (fails open toward visibility).
func writePolecatDiedState(townRoot, rigName, polecatName string, s *polecatDiedNotifyState) {
	dir := polecatDiedStateDir(townRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	_ = os.WriteFile(polecatDiedStateFile(townRoot, rigName, polecatName), data, 0o600)
}

// clearPolecatDiedState removes the dedup record for a polecat. Called when the
// polecat is observed healthy again so a later death starts a fresh alarm
// (first observation re-notifies immediately) rather than being suppressed by a
// stale cooldown window. Best-effort.
func clearPolecatDiedState(townRoot, rigName, polecatName string) {
	_ = os.Remove(polecatDiedStateFile(townRoot, rigName, polecatName))
}

// shouldNotifyPolecatDied decides whether to send a fresh POLECAT_DIED mail for
// a dead polecat this cycle, given the prior dedup record and the current
// signature. It returns true when:
//   - cooldown is disabled (<=0): always notify (pre-gu-b4b39 behavior), OR
//   - there is no prior record (first observation), OR
//   - the signature changed (different classification or hook bead — a
//     materially different death), OR
//   - the cooldown window has elapsed since the last notification.
//
// Otherwise it returns false: the same death was already reported recently and
// has not materially changed, so re-notifying would just be the duplicate noise
// documented on gu-b4b39.
func shouldNotifyPolecatDied(prev *polecatDiedNotifyState, now time.Time, cooldown time.Duration, signature string) bool {
	if cooldown <= 0 {
		return true
	}
	if prev == nil {
		return true
	}
	if prev.LastSignature != signature {
		return true
	}
	return now.Sub(prev.LastNotifiedAt) >= cooldown
}

// polecatDiedSuppressionNote returns a human-readable rollup of how long and how
// many cycles a polecat's death has been suppressed since its last escalation,
// suitable for appending to the next POLECAT_DIED mail body. Returns "" when
// there is nothing meaningful to report (no prior record or no suppression).
func polecatDiedSuppressionNote(prev *polecatDiedNotifyState, now time.Time) string {
	if prev == nil || prev.SuppressedCount <= 0 {
		return ""
	}
	since := prev.SuppressedSince
	if since.IsZero() {
		since = prev.LastNotifiedAt
	}
	return fmt.Sprintf(
		"(Suppressed %d duplicate patrol cycle(s) over the prior %s — same stuck state. "+
			"This re-escalation fires because the dedup cooldown elapsed.)",
		prev.SuppressedCount, now.Sub(since).Round(time.Minute),
	)
}

// PolecatDeathNotice is a single dead-polecat escalation that survived dedup
// this cycle, enriched with the suppression rollup and restart-loop diagnostics
// the patrol mail should carry. Returned by FilterFreshPolecatDeaths.
type PolecatDeathNotice struct {
	// Zombie is the underlying detection result for the dead polecat.
	Zombie ZombieResult
	// SuppressionNote summarizes how many prior cycles were suppressed for this
	// same stuck state, or "" if this is a first/fresh escalation.
	SuppressionNote string
	// RestartLoopNotClearing is true when the polecat has been restarted
	// repeatedly without recovering (the witness keeps observing it dead despite
	// restarts). The detection fires the diagnostic exactly once per crash-loop
	// episode (gu-b4b39 acceptance: "fires once with diagnostics").
	RestartLoopNotClearing bool
	// RestartLoopDiagnostics is a human-readable explanation of the restart loop
	// (restart count, how to clear), set only when RestartLoopNotClearing is true.
	RestartLoopDiagnostics string
}

// FilterFreshPolecatDeaths applies per-polecat POLECAT_DIED dedup to a zombie
// detection result and returns the active-work deaths that should escalate this
// cycle. Each returned notice carries the suppression rollup and (at most once
// per crash-loop episode) the restart-loop-not-clearing diagnostic.
//
// For every active-work zombie:
//   - Build its stuck-state signature (classification + hook bead).
//   - If shouldNotifyPolecatDied says suppress (same signature, inside the
//     cooldown), bump the suppressed counter and emit nothing for it.
//   - Otherwise emit a notice, reset the suppression window, and (if the
//     polecat is in a not-clearing restart loop and we haven't already reported
//     it this episode) attach the one-shot diagnostic.
//
// Active-work zombies that are NOT escalating this cycle still have their state
// reconciled (counters bumped); zombies that are no longer present have stale
// state cleared so a future death re-notifies immediately.
//
// cooldown<=0 disables suppression entirely (every active death escalates every
// cycle — the pre-gu-b4b39 behavior, used as the operator opt-out).
func FilterFreshPolecatDeaths(workDir, townRoot, rigName string, result *DetectZombiePolecatsResult, cooldown time.Duration, now time.Time) []PolecatDeathNotice {
	if result == nil {
		return nil
	}

	var notices []PolecatDeathNotice
	for _, z := range result.Zombies {
		if !z.WasActive {
			// Not an active-work death — never escalated as POLECAT_DIED, so any
			// prior cooldown record is moot. Clear it so a later active death for
			// this polecat starts fresh.
			clearPolecatDiedState(townRoot, rigName, z.PolecatName)
			continue
		}

		signature := polecatDiedSignature(z)
		prev := readPolecatDiedState(townRoot, rigName, z.PolecatName)

		if !shouldNotifyPolecatDied(prev, now, cooldown, signature) {
			// Same death, already reported recently — suppress the duplicate mail
			// and just record that another cycle observed it.
			next := &polecatDiedNotifyState{
				LastNotifiedAt:      prev.LastNotifiedAt,
				LastSignature:       prev.LastSignature,
				SuppressedSince:     prev.SuppressedSince,
				SuppressedCount:     prev.SuppressedCount + 1,
				RestartLoopReported: prev.RestartLoopReported,
			}
			if next.SuppressedSince.IsZero() {
				next.SuppressedSince = now
			}
			writePolecatDiedState(townRoot, rigName, z.PolecatName, next)
			continue
		}

		notice := PolecatDeathNotice{
			Zombie:          z,
			SuppressionNote: polecatDiedSuppressionNote(prev, now),
		}

		// Restart-loop-not-clearing: the polecat keeps dying despite restarts.
		// Surface the diagnostic exactly once per crash-loop episode so the
		// operator sees it without the alarm re-firing every escalation.
		alreadyReported := prev != nil && prev.RestartLoopReported
		restartLoopReported := alreadyReported
		if IsPolecatInCrashLoop(workDir, rigName, z.PolecatName) {
			if !alreadyReported {
				notice.RestartLoopNotClearing = true
				notice.RestartLoopDiagnostics = fmt.Sprintf(
					"Clear with: gt witness clear-polecat-backoff %s/%s",
					rigName, z.PolecatName)
				restartLoopReported = true
			}
		} else {
			// Out of the crash loop — re-arm the one-shot for any future episode.
			restartLoopReported = false
		}

		writePolecatDiedState(townRoot, rigName, z.PolecatName, &polecatDiedNotifyState{
			LastNotifiedAt:      now,
			LastSignature:       signature,
			RestartLoopReported: restartLoopReported,
		})
		notices = append(notices, notice)
	}

	return notices
}
