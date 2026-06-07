package witness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/lock"
)

// Cross-rig STALE_RIG_AGENT correlation (gu-nejgh, follow-up #4 to gu-z8qzq)
// ==========================================================================
//
// The gu-z8qzq cooldown dedupes STALE_RIG_AGENT escalations per-(rig,session):
// one wedged agent no longer re-mails mayor every patrol cycle. But during a
// town-wide incident (the canonical case: Dolt saturation wedging every rig's
// refinery and witness at once) each rig's witness is a SEPARATE `gt patrol
// scan` process with its own per-(rig,session) cooldown state. So M rigs × 2
// agents still produce up to 2M independent HIGH escalations to mayor for ONE
// underlying root cause — exactly the Mayor-interrupting flood gu-z8qzq set
// out to stop, just shifted from "same agent re-firing" to "many agents
// firing once each."
//
// This file folds that burst into a single escalation thread. The heuristic is
// the one the gu-z8qzq bead itself proposes: "all STALE_RIG_AGENT in a short
// window" are treated as the same root-cause class. The first agent to escalate
// within a correlation window becomes the LEAD and sends the mail; every other
// (rig,session) that would escalate inside the same window FOLDS into the lead's
// thread silently (no mail), recording itself as a member for observability.
//
// Cross-process coordination: the correlation record is a SINGLE town-level
// file shared by every rig witness, so concurrent writers are real (unlike the
// per-(rig,session) cooldown files, which are partitioned by filename). All
// read-modify-write access is serialized with an advisory flock on a sibling
// .flock file — the same pattern spawn_count.go and polecat_startup_backoff.go
// use.
//
// State layout:
//   <townRoot>/.runtime/stale_rig_agent_correlation.json
//   <townRoot>/.runtime/stale_rig_agent_correlation.json.flock (advisory)
//
// window<=0 disables correlation entirely: every agent is its own lead and
// always sends, reproducing the pre-gu-nejgh behavior. This is the operator
// opt-out and the default in tests that exercise only single-rig logic.

// staleAgentCorrelationMu serializes in-process access. Cross-process safety is
// the flock on the sibling .flock file.
var staleAgentCorrelationMu sync.Mutex

// staleAgentCorrelationState is the town-level record of the currently-active
// STALE_RIG_AGENT correlation window. A window is "active" while
// now-WindowStartedAt < window; once it elapses the next escalation opens a
// fresh window with itself as the new lead.
type staleAgentCorrelationState struct {
	// WindowStartedAt is when the current window's lead first escalated.
	WindowStartedAt time.Time `json:"window_started_at"`
	// LeadKey is the "rig/session" of the agent that sent the escalation mail
	// for this window — the canonical thread that members fold into.
	LeadKey string `json:"lead_key"`
	// Members is the distinct set of "rig/session" keys that have escalated or
	// folded within this window, including the lead. Order is insertion order;
	// duplicates are not appended.
	Members []string `json:"members"`
	// LastUpdatedAt is the most recent fold/lead time, for diagnostics.
	LastUpdatedAt time.Time `json:"last_updated_at"`
}

func staleAgentCorrelationFile(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "stale_rig_agent_correlation.json")
}

// staleAgentCorrelationKey is the town-unique identifier for a watched agent.
// rig + session together are unique across the town (two rigs may share a role
// but not a session prefix).
func staleAgentCorrelationKey(rigName, sessionName string) string {
	return rigName + "/" + sessionName
}

// readStaleAgentCorrelation returns the current correlation record, or nil if
// none exists or it cannot be parsed. A malformed record is treated as absent
// so a corrupt file can never wedge escalation off permanently — the next
// escalation simply opens a fresh window. Caller must hold the flock.
func readStaleAgentCorrelation(townRoot string) *staleAgentCorrelationState {
	data, err := os.ReadFile(staleAgentCorrelationFile(townRoot)) //nolint:gosec // G304: path from trusted townRoot
	if err != nil {
		return nil
	}
	var s staleAgentCorrelationState
	if json.Unmarshal(data, &s) != nil {
		return nil
	}
	return &s
}

// writeStaleAgentCorrelation persists the correlation record. Best-effort: a
// write failure fails open toward visibility (the next escalation may lead and
// send rather than fold), which is the safe direction for an alarm. Caller must
// hold the flock.
func writeStaleAgentCorrelation(townRoot string, s *staleAgentCorrelationState) {
	dir := filepath.Dir(staleAgentCorrelationFile(townRoot))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	_ = os.WriteFile(staleAgentCorrelationFile(townRoot), data, 0o600)
}

// correlationDecision is the outcome of joinOrLeadStaleAgentCorrelation.
type correlationDecision struct {
	// IsLead is true when this agent should send the escalation mail (it opened
	// the window or it IS the window's lead re-firing after its own cooldown).
	IsLead bool
	// FoldedInto is the lead's "rig/session" key when IsLead is false, so the
	// caller can surface "folded into X" in patrol output / town.log.
	FoldedInto string
	// MemberCount is the number of distinct agents in the current window after
	// this decision, including the lead.
	MemberCount int
}

// joinOrLeadStaleAgentCorrelation decides whether the agent identified by
// (rigName, sessionName) should LEAD the current STALE_RIG_AGENT correlation
// window (send the mail) or FOLD into an existing lead's thread (suppress the
// mail), given the town-level correlation record.
//
// Rules:
//   - window<=0 disables correlation: always lead (send), never fold.
//   - no active window (no record, or the prior window elapsed): open a fresh
//     window with this agent as lead → IsLead=true.
//   - active window, this agent is the lead: it is re-firing after its own
//     per-(rig,session) cooldown elapsed → still lead (the canonical thread).
//   - active window, a different agent leads: fold in as a member → IsLead=false.
//
// All access is serialized with the in-process mutex + cross-process flock so
// concurrent rig-witness processes cannot both claim lead for the same window.
func joinOrLeadStaleAgentCorrelation(townRoot, rigName, sessionName string, now time.Time, window time.Duration) correlationDecision {
	key := staleAgentCorrelationKey(rigName, sessionName)

	if window <= 0 {
		// Correlation disabled — every agent is its own lead.
		return correlationDecision{IsLead: true, MemberCount: 1}
	}

	staleAgentCorrelationMu.Lock()
	defer staleAgentCorrelationMu.Unlock()

	// Ensure the directory exists before flock so the sibling .flock file can
	// be created even on a brand-new town.
	if err := os.MkdirAll(filepath.Dir(staleAgentCorrelationFile(townRoot)), 0o755); err == nil {
		if unlock, flockErr := lock.FlockAcquire(staleAgentCorrelationFile(townRoot) + ".flock"); flockErr == nil {
			defer unlock()
		}
	}

	prev := readStaleAgentCorrelation(townRoot)

	// Open a fresh window when there is no record or the prior one elapsed.
	if prev == nil || now.Sub(prev.WindowStartedAt) >= window {
		fresh := &staleAgentCorrelationState{
			WindowStartedAt: now,
			LeadKey:         key,
			Members:         []string{key},
			LastUpdatedAt:   now,
		}
		writeStaleAgentCorrelation(townRoot, fresh)
		return correlationDecision{IsLead: true, MemberCount: 1}
	}

	// Active window: record this agent as a member if not already present.
	if !containsStaleAgentMember(prev.Members, key) {
		prev.Members = append(prev.Members, key)
	}
	prev.LastUpdatedAt = now
	writeStaleAgentCorrelation(townRoot, prev)

	// The lead re-firing within its own window stays the lead (its per-rig
	// cooldown gate already governs how often that happens); everyone else folds.
	if key == prev.LeadKey {
		return correlationDecision{IsLead: true, MemberCount: len(prev.Members)}
	}
	return correlationDecision{IsLead: false, FoldedInto: prev.LeadKey, MemberCount: len(prev.Members)}
}

func containsStaleAgentMember(members []string, key string) bool {
	for _, m := range members {
		if m == key {
			return true
		}
	}
	return false
}
