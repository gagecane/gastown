package witness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// staleAgentNotifyState is the file-backed dedup record for STALE_RIG_AGENT
// escalations. The witness patrol runs as a fresh `gt patrol scan` process each
// cycle, so in-memory dedup (MessageDeduplicator) does not survive between
// cycles — the same stale agent was re-escalated to mayor on every patrol,
// interrupting the Mayor mid-task on nearly every tool call (gu-z8qzq).
//
// This record persists the last escalation per (rig, session) under
// .runtime/stale_rig_agent/ so the next cycle's process can decide whether the
// condition is "the same alarm already reported" (suppress) versus "materially
// worse / newly different" (re-escalate).
type staleAgentNotifyState struct {
	// LastNotifiedAt is when we last sent a STALE_RIG_AGENT mail for this agent.
	LastNotifiedAt time.Time `json:"last_notified_at"`
	// LastBand is the staleness band at last notification. A band is
	// floor(age/threshold): band 1 = [1x,2x) threshold, band 2 = [2x,3x), etc.
	// Crossing into a higher band means the condition materially worsened and
	// is worth re-notifying even inside the cooldown window.
	LastBand int `json:"last_band"`
	// LastMissing records whether the last notification was for a fully-missing
	// heartbeat (vs a present-but-stale one). A transition between the two kinds
	// is a material change worth re-notifying.
	LastMissing bool `json:"last_missing"`
}

func staleAgentStateDir(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "stale_rig_agent")
}

func staleAgentStateFile(townRoot, rigName, sessionName string) string {
	// rig + session uniquely identify the watched agent. Sanitize separators so
	// the key is a safe single filename component.
	safe := strings.ReplaceAll(rigName+"__"+sessionName, "/", "_")
	return filepath.Join(staleAgentStateDir(townRoot), safe+".json")
}

// staleAgentBand returns the staleness band for a present heartbeat: how many
// whole multiples of the threshold the age has reached. Callers only invoke
// this once age >= threshold, so the result is always >= 1. threshold<=0 is
// guarded by the caller (cooldown disabled), but defend anyway.
func staleAgentBand(age, threshold time.Duration) int {
	if threshold <= 0 {
		return 1
	}
	return int(age / threshold)
}

// readStaleAgentState returns the dedup record for an agent, or nil if none
// exists or it cannot be parsed. A malformed record is treated as absent so a
// corrupt file can never wedge escalation off permanently.
func readStaleAgentState(townRoot, rigName, sessionName string) *staleAgentNotifyState {
	data, err := os.ReadFile(staleAgentStateFile(townRoot, rigName, sessionName))
	if err != nil {
		return nil
	}
	var s staleAgentNotifyState
	if json.Unmarshal(data, &s) != nil {
		return nil
	}
	return &s
}

// writeStaleAgentState persists the dedup record. Best-effort: a write failure
// only means the next cycle may re-notify (fails open toward visibility), which
// is the safe direction for an alarm.
func writeStaleAgentState(townRoot, rigName, sessionName string, s *staleAgentNotifyState) {
	dir := staleAgentStateDir(townRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	_ = os.WriteFile(staleAgentStateFile(townRoot, rigName, sessionName), data, 0o600)
}

// shouldNotifyStaleAgent decides whether to send a fresh STALE_RIG_AGENT mail
// for an agent this cycle, given the prior dedup record. It returns true (and
// the caller then records the new state) when:
//   - there is no prior record (first observation), OR
//   - the condition KIND changed (missing<->present heartbeat), OR
//   - the staleness band increased (condition materially worsened), OR
//   - the cooldown window has elapsed since the last notification.
//
// Otherwise it returns false: the same alarm was already reported recently and
// the condition has not materially changed, so re-notifying would just be the
// duplicate noise documented on gu-z8qzq.
//
// cooldown<=0 disables suppression entirely (always notify) — the pre-gu-z8qzq
// behavior, used as the operator opt-out and the default in tests that don't
// exercise dedup.
func shouldNotifyStaleAgent(prev *staleAgentNotifyState, now time.Time, cooldown time.Duration, band int, missing bool) bool {
	if cooldown <= 0 {
		return true
	}
	if prev == nil {
		return true
	}
	if prev.LastMissing != missing {
		return true
	}
	if band > prev.LastBand {
		return true
	}
	return now.Sub(prev.LastNotifiedAt) >= cooldown
}
