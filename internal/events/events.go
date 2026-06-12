// Package events provides event logging for the gt activity feed.
//
// Events are written to ~/gt/.events.jsonl (raw audit log) and later
// curated by the feed daemon into ~/.feed.jsonl (user-facing).
package events

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Event represents an activity event in Gas Town.
type Event struct {
	Timestamp  string                 `json:"ts"`
	Source     string                 `json:"source"`
	Type       string                 `json:"type"`
	Actor      string                 `json:"actor"`
	Payload    map[string]interface{} `json:"payload,omitempty"`
	Visibility string                 `json:"visibility"`
}

// Visibility levels for events.
const (
	VisibilityAudit = "audit" // Only in raw events log
	VisibilityFeed  = "feed"  // Appears in curated feed
	VisibilityBoth  = "both"  // Both audit and feed
)

// Common event types for gt commands.
const (
	TypeSling   = "sling"
	TypeHook    = "hook"
	TypeUnhook  = "unhook"
	TypeHandoff = "handoff"
	TypeDone    = "done"
	TypeMail    = "mail"
	TypeSpawn   = "spawn"
	TypeNudge   = "nudge"
	TypeBoot    = "boot"
	TypeHalt    = "halt"

	// Session events (for seance discovery)
	TypeSessionStart = "session_start"
	TypeSessionEnd   = "session_end"

	// Session death events (for crash investigation)
	TypeSessionDeath = "session_death" // Feed-visible session termination
	TypeMassDeath    = "mass_death"    // Multiple sessions died in short window

	// Witness patrol events
	TypePatrolStarted    = "patrol_started"
	TypePolecatChecked   = "polecat_checked"
	TypePolecatNudged    = "polecat_nudged"
	TypeEscalationSent   = "escalation_sent"
	TypeEscalationAcked  = "escalation_acked"
	TypeEscalationClosed = "escalation_closed"
	TypePatrolComplete   = "patrol_complete"

	// Merge queue events (emitted by refinery)
	TypeMergeStarted = "merge_started"
	TypeMerged       = "merged"
	TypeMergeFailed  = "merge_failed"
	TypeMergeSkipped = "merge_skipped"

	// TypeRefineryPaused is emitted when the refinery defers an MR awaiting
	// human direction (PR needs approving review, auto-test-pr awaits
	// approved-by:<user> label, etc.). The pause is correct uncertainty
	// handling, but without a structured signal the queue piles up silently
	// — see gu-t3why / hq:gc-o66p90. The witness's DetectRefineryPaused scan
	// reads these events to surface the deferred MR(s) separately from
	// STALE_RIG_AGENT (which only fires on heartbeat lag).
	TypeRefineryPaused = "refinery_paused"

	// Scheduler events
	TypeSchedulerEnqueue        = "scheduler_enqueue"         // Bead scheduled for deferred dispatch
	TypeSchedulerDispatch       = "scheduler_dispatch"        // Bead dispatched from scheduler
	TypeSchedulerDispatchFailed = "scheduler_dispatch_failed" // Bead dispatch failed (requeued)
	TypeSchedulerCloseRetry     = "scheduler_close_retry"     // Context close needed last-resort attempt
	TypeSchedulerCloseFailed    = "scheduler_close_failed"    // Last-resort context close failed — double-dispatch risk
	TypeSchedulerDeferReleased  = "scheduler_defer_released"  // Bead auto-released from defer (defer_until <= now)

	// Auto-dispatch events (event-driven refill observability)
	TypeAutoDispatchEventTriggered = "auto_dispatch_event_triggered" // Event-driven auto-dispatch fired

	// Daemon plugin dispatch events (transport-split foundation — gu-zwui / gt-to45a).
	// Emitted additively alongside existing mail dispatch so future consumers can
	// migrate off the mail transport one at a time. See docs/design/plugin-dispatch-transport.md.
	TypeDaemonPluginDispatch = "daemon.plugin.dispatch" // Daemon dispatched a plugin to an agent

	// RESTART_POLECAT processing (gu-nep2). Emitted by the daemon's
	// processRestartPolecatRequests handler for each mail it actions so
	// operators can tell what was picked up vs skipped without scraping
	// logs. Audit-only by design — this is mechanical self-healing, not
	// feed-visible activity.
	TypeRestartPolecatHandled = "restart_polecat_handled"
)

// EventsFile is the name of the raw events log.
const EventsFile = ".events.jsonl"

// suppressWrites disables all event file writes when true. Used by tests
// to prevent phantom events leaking into the live town feed. (gu-wmf2r)
var suppressWrites bool

// SuppressWrites prevents event writes for the duration of a test. Returns
// a restore function that re-enables writes.
func SuppressWrites() func() {
	suppressWrites = true
	return func() { suppressWrites = false }
}

// Log writes an event to the events log.
// The event is appended to ~/gt/.events.jsonl.
// Returns nil if logging fails (events are best-effort).
func Log(eventType, actor string, payload map[string]interface{}, visibility string) error {
	event := Event{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Source:     "gt",
		Type:       eventType,
		Actor:      actor,
		Payload:    payload,
		Visibility: visibility,
	}
	return write(event)
}

// LogFeed is a convenience wrapper for feed-visible events.
func LogFeed(eventType, actor string, payload map[string]interface{}) error {
	return Log(eventType, actor, payload, VisibilityFeed)
}

// LogAudit is a convenience wrapper for audit-only events.
func LogAudit(eventType, actor string, payload map[string]interface{}) error {
	return Log(eventType, actor, payload, VisibilityAudit)
}

// write appends an event to the events file.
// Uses flock for cross-process synchronization — sync.Mutex only protects
// intra-process goroutines, but multiple gt processes write concurrently.
func write(event Event) error {
	if suppressWrites {
		return nil
	}

	// Find town root
	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		// Silently ignore - we're not in a Gas Town workspace
		return nil
	}

	eventsPath := filepath.Join(townRoot, EventsFile)

	// Marshal event to JSON
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshaling event: %w", err)
	}
	data = append(data, '\n')

	// Acquire cross-process file lock
	fl := flock.New(eventsPath + ".lock")
	if err := fl.Lock(); err != nil {
		return fmt.Errorf("acquiring events file lock: %w", err)
	}
	defer fl.Unlock() //nolint:errcheck // best-effort unlock

	f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644) //nolint:gosec // G302: events file is non-sensitive operational data
	if err != nil {
		return fmt.Errorf("opening events file: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing event: %w", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("closing events file: %w", err)
	}

	return nil
}

// Payload helpers for common event structures.

// SlingPayload creates a payload for sling events.
func SlingPayload(beadID, target string) map[string]interface{} {
	return map[string]interface{}{
		"bead":   beadID,
		"target": target,
	}
}

// HookPayload creates a payload for hook events.
func HookPayload(beadID string) map[string]interface{} {
	return map[string]interface{}{
		"bead": beadID,
	}
}

// HandoffPayload creates a payload for handoff events.
func HandoffPayload(subject string, toSession bool) map[string]interface{} {
	p := map[string]interface{}{
		"to_session": toSession,
	}
	if subject != "" {
		p["subject"] = subject
	}
	return p
}

// DonePayload creates a payload for done events.
func DonePayload(beadID, branch string) map[string]interface{} {
	return map[string]interface{}{
		"bead":   beadID,
		"branch": branch,
	}
}

// MailPayload creates a payload for mail events.
func MailPayload(to, subject string) map[string]interface{} {
	return map[string]interface{}{
		"to":      to,
		"subject": subject,
	}
}

// SpawnPayload creates a payload for spawn events.
func SpawnPayload(rig, polecat string) map[string]interface{} {
	return map[string]interface{}{
		"rig":     rig,
		"polecat": polecat,
	}
}

// BootPayload creates a payload for rig boot events.
func BootPayload(rig string, agents []string) map[string]interface{} {
	return map[string]interface{}{
		"rig":    rig,
		"agents": agents,
	}
}

// MergePayload creates a payload for merge queue events.
// mrID: merge request ID
// worker: polecat name that submitted the work
// branch: source branch being merged
// reason: failure reason (for merge_failed/merge_skipped events)
func MergePayload(mrID, worker, branch, reason string) map[string]interface{} {
	p := map[string]interface{}{
		"mr":     mrID,
		"worker": worker,
		"branch": branch,
	}
	if reason != "" {
		p["reason"] = reason
	}
	return p
}

// RefineryPausedPayload creates a payload for refinery pause events.
//
// rig: the rig whose refinery paused (e.g. "gastown_upstream")
// mrID: the merge-request bead ID being held in queue
// branch: the source branch of the held MR
// sourceIssue: the issue ID being merged (may be empty for ad-hoc MRs)
// reason: short machine-readable reason tag, e.g. "pr_needs_approval",
//
//	"auto_test_pr_needs_approved_by_label"
//
// details: free-form human-readable diagnostic text suitable for a witness
//
//	escalation body (e.g. "PR #123 requires approving review before merge")
//
// suspectedConvention: optional best-guess of the convention/policy the
//
//	pause is waiting on (e.g. "github_pr_review",
//	"approved-by:<user> label"); empty when unknown
func RefineryPausedPayload(rig, mrID, branch, sourceIssue, reason, details, suspectedConvention string) map[string]interface{} {
	p := map[string]interface{}{
		"rig":    rig,
		"reason": reason,
	}
	if mrID != "" {
		p["mr"] = mrID
	}
	if branch != "" {
		p["branch"] = branch
	}
	if sourceIssue != "" {
		p["source_issue"] = sourceIssue
	}
	if details != "" {
		p["details"] = details
	}
	if suspectedConvention != "" {
		p["suspected_convention"] = suspectedConvention
	}
	return p
}

// PatrolPayload creates a payload for patrol start/complete events.
func PatrolPayload(rig string, polecatCount int, message string) map[string]interface{} {
	p := map[string]interface{}{
		"rig":           rig,
		"polecat_count": polecatCount,
	}
	if message != "" {
		p["message"] = message
	}
	return p
}

// PolecatCheckPayload creates a payload for polecat check events.
func PolecatCheckPayload(rig, polecat, status, issue string) map[string]interface{} {
	p := map[string]interface{}{
		"rig":     rig,
		"polecat": polecat,
		"status":  status,
	}
	if issue != "" {
		p["issue"] = issue
	}
	return p
}

// NudgePayload creates a payload for nudge events.
func NudgePayload(rig, target, reason string) map[string]interface{} {
	return map[string]interface{}{
		"rig":    rig,
		"target": target,
		"reason": reason,
	}
}

// EscalationPayload creates a payload for escalation events.
func EscalationPayload(rig, target, to, reason string) map[string]interface{} {
	return map[string]interface{}{
		"rig":    rig,
		"target": target,
		"to":     to,
		"reason": reason,
	}
}

// UnhookPayload creates a payload for unhook events.
func UnhookPayload(beadID string) map[string]interface{} {
	return map[string]interface{}{
		"bead": beadID,
	}
}

// HaltPayload creates a payload for halt events.
func HaltPayload(services []string) map[string]interface{} {
	return map[string]interface{}{
		"services": services,
	}
}

// SessionDeathPayload creates a payload for session death events.
// session: tmux session name that died
// agent: Gas Town agent identity (e.g., "gastown/polecats/Toast")
// reason: why the session was killed (e.g., "zombie cleanup", "user request", "doctor fix")
// caller: what initiated the kill (e.g., "daemon", "doctor", "gt down")
func SessionDeathPayload(session, agent, reason, caller string) map[string]interface{} {
	return map[string]interface{}{
		"session": session,
		"agent":   agent,
		"reason":  reason,
		"caller":  caller,
	}
}

// SessionEndPayload creates a payload for clean session-end events.
//
// TypeSessionEnd is the clean-exit complement to TypeSessionDeath: it is
// emitted when a session terminates intentionally (gt done, gt handoff) rather
// than crashing or being killed. Pairing the two gives the crash-rate KPI a
// clean denominator — rate(session_death) / rate(session_end) per rig — instead
// of overloading session_death to mean both crashes and clean exits (audit
// gu-nid89.14, fix D3). Mirrors SessionDeathPayload's keys so the two events
// can be queried side by side.
//
// session: tmux session name that ended (GT_SESSION); empty when unknown
// agent: Gas Town agent identity (e.g., "gastown_upstream/polecats/chrome")
// rig: rig name for per-rig aggregation; omitted when empty (town-level roles)
// reason: how the session ended (e.g., "gt done (exit=COMPLETED)", "gt handoff")
// caller: what initiated the clean exit (e.g., "gt done", "gt handoff")
func SessionEndPayload(session, agent, rig, reason, caller string) map[string]interface{} {
	p := map[string]interface{}{
		"session": session,
		"agent":   agent,
		"reason":  reason,
		"caller":  caller,
	}
	if rig != "" {
		p["rig"] = rig
	}
	return p
}

// MassDeathPayload creates a payload for mass death events.
// count: number of sessions that died
// window: time window in which deaths occurred (e.g., "5s")
// sessions: list of session names that died
// possibleCause: suspected cause if known
func MassDeathPayload(count int, window string, sessions []string, possibleCause string) map[string]interface{} {
	p := map[string]interface{}{
		"count":    count,
		"window":   window,
		"sessions": sessions,
	}
	if possibleCause != "" {
		p["possible_cause"] = possibleCause
	}
	return p
}

// SessionPayload creates a payload for session start/end events.
// sessionID: Claude Code session UUID
// role: Gas Town role (e.g., "gastown/crew/joe", "deacon")
// topic: What the session is working on
// cwd: Working directory
func SessionPayload(sessionID, role, topic, cwd string) map[string]interface{} {
	p := map[string]interface{}{
		"session_id": sessionID,
		"role":       role,
		"actor_pid":  fmt.Sprintf("%s-%d", role, os.Getpid()),
	}
	if topic != "" {
		p["topic"] = topic
	}
	if cwd != "" {
		p["cwd"] = cwd
	}
	return p
}

// SchedulerEnqueuePayload creates a payload for scheduler enqueue events.
func SchedulerEnqueuePayload(beadID, rig string) map[string]interface{} {
	return map[string]interface{}{
		"bead": beadID,
		"rig":  rig,
	}
}

// SchedulerDispatchPayload creates a payload for scheduler dispatch events.
func SchedulerDispatchPayload(beadID, rig, polecat string) map[string]interface{} {
	return map[string]interface{}{
		"bead":    beadID,
		"rig":     rig,
		"polecat": polecat,
	}
}

// SchedulerDispatchFailedPayload creates a payload for scheduler dispatch failure events.
func SchedulerDispatchFailedPayload(beadID, rig, errMsg string) map[string]interface{} {
	return map[string]interface{}{
		"bead":  beadID,
		"rig":   rig,
		"error": errMsg,
	}
}

// SchedulerDeferReleasedPayload creates a payload for the auto-release pass
// (gu-0i09): records the bead that was flipped back from deferred → open and
// the defer_until timestamp it had at release time.
func SchedulerDeferReleasedPayload(beadID, deferUntil string) map[string]interface{} {
	return map[string]interface{}{
		"bead":        beadID,
		"defer_until": deferUntil,
	}
}

// AutoDispatchEventTriggeredPayload creates a payload for event-driven
// auto-dispatch observability events.
// rig: the rig whose auto-dispatch was triggered
// trigger: the session_death reason that caused the trigger (e.g., "gt done")
// triggerSession: the tmux session name that ended
// triggerAgent: the agent identity (e.g., "myrig/polecats/alpha") that ended
func AutoDispatchEventTriggeredPayload(rig, trigger, triggerSession, triggerAgent string) map[string]interface{} {
	p := map[string]interface{}{
		"rig":     rig,
		"trigger": trigger,
	}
	if triggerSession != "" {
		p["trigger_session"] = triggerSession
	}
	if triggerAgent != "" {
		p["trigger_agent"] = triggerAgent
	}
	return p
}

// DaemonPluginDispatchPayload creates a payload for daemon plugin-dispatch events.
// Emitted as audit-only observability alongside the existing mail dispatch so future
// consumers can migrate off mail one at a time. See docs/design/plugin-dispatch-transport.md
// (gu-zwui / gt-to45a).
//
// plugin: the plugin name being dispatched (e.g., "dolt-backup", "code-scout")
// rig: the rig the plugin is scoped to, if any; empty string for town-level plugins
// target: the agent recipient address (e.g., "deacon/dogs/alpha")
// trigger: what caused the dispatch ("cooldown", "event-driven", "manual"); empty allowed
func DaemonPluginDispatchPayload(plugin, rig, target, trigger string) map[string]interface{} {
	p := map[string]interface{}{
		"plugin": plugin,
		"target": target,
	}
	if rig != "" {
		p["rig"] = rig
	}
	if trigger != "" {
		p["trigger"] = trigger
	}
	return p
}
