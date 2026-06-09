package witness

import (
	"fmt"
	"os"
	"time"

	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// StaleRigAgentResult is a single rig-agent staleness observation produced by
// DetectStaleRigAgentHeartbeats. The agent is identified by SessionName so
// callers (and tests) don't need to construct sessionPrefix-aware names
// themselves. (gu-0nmw)
type StaleRigAgentResult struct {
	// AgentRole is the simple role name: "refinery" or "witness".
	AgentRole string
	// SessionName is the tmux session name that should carry the heartbeat
	// (e.g. "gu-refinery").
	SessionName string
	// HeartbeatAge is the age of the heartbeat file. Zero when no heartbeat
	// file exists at all (HeartbeatMissing == true).
	HeartbeatAge time.Duration
	// HeartbeatMissing is true when there is no heartbeat file. The witness
	// treats a missing heartbeat for an agent that should be running as
	// equivalently stale to a very old one — both indicate "no liveness signal."
	HeartbeatMissing bool
	// SessionAlive reports whether the agent's tmux session exists. The
	// detector still fires when the session is alive but the heartbeat is
	// stale (the gu-rh0g failure mode: process running, agent stuck).
	SessionAlive bool
	// Action describes what the detector did: "escalated" when mail was sent,
	// "skip-fresh" when the heartbeat was within threshold,
	// "skip-cooldown" when the condition was already reported recently and has
	// not materially changed (gu-z8qzq dedup), "skip-correlated" when the alarm
	// folded into another rig's concurrent STALE_RIG_AGENT escalation for the
	// same town-wide root cause (gu-nejgh), "skip-idle-empty-mq" when a
	// refinery's heartbeat is stale but its session is alive and its merge
	// queue is empty — a harmlessly-idle refinery, not a wedged one (gs-ecdg),
	// etc.
	Action string
	// CorrelatedInto is the lead agent's "rig/session" key when Action is
	// "skip-correlated" — the escalation thread this alarm folded into. Empty
	// otherwise. (gu-nejgh)
	CorrelatedInto string
	// MailSent is true when the escalation mail was successfully delivered
	// to mayor (or via nudge fallback).
	MailSent bool
	// Error captures non-fatal errors encountered processing this agent so
	// the caller can surface them without aborting the rest of the scan.
	Error error
}

// DetectStaleRigAgentHeartbeatsResult aggregates the per-agent results plus
// scan-wide errors.
type DetectStaleRigAgentHeartbeatsResult struct {
	Checked int
	Stale   []StaleRigAgentResult
	Errors  []error
}

// MergeQueueProber reports the number of actionable (open, unblocked) merge
// requests in a rig's merge queue. The refinery staleness check (gs-ecdg) uses
// it to distinguish a harmlessly-idle refinery (empty queue — nothing to merge,
// so it legitimately stops touching its heartbeat) from a genuinely wedged one
// (queue non-empty but not draining — the gu-rh0g signature). A nil prober
// disables the check: every stale refinery escalates, the pre-gs-ecdg behavior.
type MergeQueueProber interface {
	PendingMergeRequestCount(rigName string) (int, error)
}

// DetectStaleRigAgentHeartbeats scans the rig's refinery and witness heartbeat
// files. When a heartbeat is older than staleThreshold (or missing entirely
// while the session exists), it mails mayor a STALE_RIG_AGENT escalation.
//
// Why this exists (gu-0nmw): the gastown_upstream refinery sat with a 28h-stale
// heartbeat without any agent surfacing the staleness. The witness's existing
// scans (DetectZombiePolecats, DetectStaleInProgressBeads) only cover polecats;
// nothing watches the refinery and witness themselves. A wedged refinery that
// silently stops merging requires an operator to notice the queue depth grow
// — a slow, unreliable signal. This detector closes the gap by reading the
// per-rig heartbeat files and escalating directly to mayor.
//
// Behavior:
//   - Threshold <= 0 disables the scan (returns empty result). Operators can
//     opt out via operational.witness.stale_rig_agent_heartbeat="0".
//   - For each agent (refinery, witness): read the heartbeat. If missing AND
//     the session is alive, that's stale (process up, never wrote a heartbeat
//     — likely a pre-gu-0nmw build that didn't touch heartbeats). If missing
//     AND session dead, skip silently (the agent is intentionally not running).
//     If present, compare age against the threshold.
//   - On stale: send a HIGH-priority mail to mayor with the role, session,
//     age, and recovery hint — UNLESS the same condition was already reported
//     within the cooldown window and has not materially worsened (gu-z8qzq).
//
// Dedup / cooldown (gu-z8qzq): the witness patrol runs as a fresh `gt patrol
// scan` process each cycle, so this detector previously re-sent an identical
// STALE_RIG_AGENT mail to mayor on EVERY cycle for the same wedged agent —
// interrupting the Mayor mid-task on nearly every tool call during the
// 2026-06-06 Dolt-saturation incident. A file-backed per-(rig,session) record
// under .runtime/stale_rig_agent/ now suppresses re-notification while the
// condition is unchanged, and re-notifies only when it materially changes:
// the staleness band crosses a new threshold multiple, the heartbeat
// transitions missing<->present, or the cooldown window elapses. cooldown<=0
// disables suppression (pre-gu-z8qzq behavior / operator opt-out).
//
// The detector intentionally does NOT restart the agent itself — that
// responsibility belongs to the daemon supervisor (which already runs
// `ensureRefineryRunning` every cycle) or to the operator.
//
// selfSession is the session name of the agent running this scan (typically
// $GT_SESSION). The detector NEVER escalates its own session: the scanning
// agent is provably alive (it is executing this code), so flagging its own
// heartbeat as stale is always a false positive. This breaks the
// self-amplifying flood documented on gu-vqmmp — a witness whose own idle
// heartbeat aged out would otherwise escalate itself every patrol cycle,
// and cross-nudged peers re-running `gt patrol scan` would escalate theirs
// in turn. Pass "" to disable the self-skip (e.g. in tests).
//
// Cross-rig correlation (gu-nejgh): during a town-wide incident every rig's
// witness independently detects its own wedged refinery/witness and — even
// with the per-(rig,session) cooldown above — would each send one HIGH
// escalation, flooding mayor with M near-simultaneous mails for ONE root
// cause. correlationWindow folds that burst into a single thread: the first
// agent to escalate within the window leads (sends), and every other
// (rig,session) that escalates inside the window folds into the lead's thread
// with Action="skip-correlated" and no mail. correlationWindow<=0 disables
// correlation (every agent sends), the operator opt-out.
//
// Idle refinery suppression (gs-ecdg): an idle refinery whose merge queue is
// persistently empty stops refreshing its heartbeat — its patrol loop only
// wakes on MQ activity, so after hours of an empty queue the heartbeat ages
// past threshold even though the refinery is healthy (just idle-quiet). That
// produced recurring FALSE STALE_RIG_AGENT escalations to mayor. When mqProber
// is non-nil and reports the rig's queue empty for an ALIVE refinery, the stale
// heartbeat is suppressed (Action="skip-idle-empty-mq"). The detector still
// escalates a stale refinery whose session is DEAD (supervisor missed a
// restart) or whose queue is NON-empty (real wedge: work waiting, not
// draining). A nil prober disables suppression. The witness candidate is never
// suppressed — it has no merge queue and a wedged witness is always actionable.
func DetectStaleRigAgentHeartbeats(workDir, rigName string, router *mail.Router, staleThreshold time.Duration, selfSession string, notifyCooldown, correlationWindow time.Duration, mqProber MergeQueueProber) *DetectStaleRigAgentHeartbeatsResult {
	result := &DetectStaleRigAgentHeartbeatsResult{}

	if staleThreshold <= 0 {
		// Explicit opt-out — operator disabled the scan.
		return result
	}

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}
	initRegistryFromTownRoot(townRoot)

	prefix := session.PrefixFor(rigName)
	t := tmux.NewTmux()
	now := time.Now().UTC()

	// Order matters only for deterministic test output; both checks are
	// independent.
	candidates := []struct {
		role        string
		sessionName string
	}{
		{"refinery", session.RefinerySessionName(prefix)},
		{"witness", session.WitnessSessionName(prefix)},
	}

	for _, c := range candidates {
		result.Checked++
		item := StaleRigAgentResult{
			AgentRole:   c.role,
			SessionName: c.sessionName,
		}

		// Self-skip (gu-vqmmp): never escalate the scanning agent's own
		// session. The scanner is executing right now, so it is alive by
		// definition — an idle agent whose session heartbeat aged out (it
		// blocks in `gt mol step await-signal` between cycles without
		// touching the session heartbeat) would otherwise escalate itself
		// every patrol cycle, and cross-nudged peers would amplify it. This
		// guard stops the feedback loop regardless of the threshold.
		if selfSession != "" && c.sessionName == selfSession {
			item.Action = "skip-self"
			result.Stale = append(result.Stale, item)
			continue
		}

		alive, sessErr := t.HasSession(c.sessionName)
		if sessErr != nil {
			item.Error = fmt.Errorf("checking session %s: %w", c.sessionName, sessErr)
			result.Errors = append(result.Errors, item.Error)
			result.Stale = append(result.Stale, item)
			continue
		}
		item.SessionAlive = alive

		hb := polecat.ReadSessionHeartbeat(townRoot, c.sessionName)
		if hb == nil {
			item.HeartbeatMissing = true
			if !alive {
				// No heartbeat AND no session — the agent is intentionally
				// off (rig docked, operator stop, never started). Not a
				// staleness alarm. The witness's own scans handle dispatch
				// gaps; this detector only flags running-but-stuck agents.
				item.Action = "skip-no-session"
				result.Stale = append(result.Stale, item)
				continue
			}
			// Session is alive but no heartbeat at all. This is the
			// pre-gu-0nmw case where refinery/witness sessions never wrote
			// a heartbeat. Treat as stale so we surface the gap.
			escalateStaleRigAgent(&item, router, t, townRoot, rigName, staleThreshold, notifyCooldown, correlationWindow, now, 1, true)
			result.Stale = append(result.Stale, item)
			continue
		}

		item.HeartbeatAge = now.Sub(hb.Timestamp)
		if item.HeartbeatAge < staleThreshold {
			item.Action = "skip-fresh"
			result.Stale = append(result.Stale, item)
			continue
		}

		// Idle refinery suppression (gs-ecdg): a refinery whose merge queue is
		// empty legitimately stops refreshing its heartbeat when idle — its
		// patrol loop only wakes on MQ activity. When the session is ALIVE and
		// the rig's queue has no actionable MRs, a stale heartbeat is a FALSE
		// alarm: there is nothing to merge, so "idle-quiet" is healthy, not
		// wedged. Suppress it. We still escalate when the session is dead
		// (handled below — SessionAlive is false) or the queue is non-empty
		// (the gu-rh0g signature: work waiting, refinery not draining it). A
		// query error falls through to escalate — never suppress a potential
		// wedge on a transient signal failure.
		if c.role == "refinery" && item.SessionAlive && mqProber != nil {
			if pending, probeErr := mqProber.PendingMergeRequestCount(rigName); probeErr == nil && pending == 0 {
				item.Action = "skip-idle-empty-mq"
				result.Stale = append(result.Stale, item)
				continue
			}
		}

		// Heartbeat exceeds threshold. Escalate regardless of session state:
		//   - Session alive + stale heartbeat = stuck process (gu-rh0g signature)
		//   - Session dead + stale heartbeat = died, supervisor missed restart
		// Both are mayor's problem. The cooldown gate (gu-z8qzq) suppresses
		// re-notifying every cycle for an unchanged condition, but re-fires when
		// the staleness band worsens or the cooldown elapses.
		band := staleAgentBand(item.HeartbeatAge, staleThreshold)
		escalateStaleRigAgent(&item, router, t, townRoot, rigName, staleThreshold, notifyCooldown, correlationWindow, now, band, false)
		result.Stale = append(result.Stale, item)
	}

	return result
}

// escalateStaleRigAgent applies the gu-z8qzq dedup/cooldown gate and the
// gu-nejgh cross-rig correlation gate, then either sends the STALE_RIG_AGENT
// mail (recording the new notify state) or records a "skip-*" no-op. It mutates
// item.Action, item.CorrelatedInto, and item.MailSent in place.
//
// Gate order:
//  1. Cooldown (per-(rig,session)): suppress the SAME agent re-firing every
//     patrol cycle for an unchanged condition (skip-cooldown).
//  2. Correlation (town-wide): once an agent passes the cooldown gate and would
//     escalate, fold it into a concurrent escalation from another rig for the
//     same root-cause window — only the window's lead sends (skip-correlated).
//
// Cooldown is applied first so a stale agent's own re-fires never count as new
// members of a correlation window; correlation only collapses genuinely fresh
// escalations from DISTINCT agents within a short window.
//
// band is the staleness band (1 for a missing heartbeat or [1x,2x) threshold,
// 2 for [2x,3x), ...); missing indicates a fully-absent heartbeat vs a
// present-but-stale one. Both feed shouldNotifyStaleAgent's material-change
// detection.
func escalateStaleRigAgent(item *StaleRigAgentResult, router *mail.Router, t *tmux.Tmux, townRoot, rigName string, threshold, cooldown, correlationWindow time.Duration, now time.Time, band int, missing bool) {
	prev := readStaleAgentState(townRoot, rigName, item.SessionName)
	if !shouldNotifyStaleAgent(prev, now, cooldown, band, missing) {
		// Same alarm, already reported recently, condition unchanged — suppress
		// the duplicate mail that was interrupting the Mayor every cycle.
		item.Action = "skip-cooldown"
		return
	}

	// Cross-rig correlation (gu-nejgh): the cooldown gate has cleared this as a
	// genuinely fresh escalation. If another rig already opened a correlation
	// window for the same town-wide incident, fold into its thread instead of
	// sending an independent HIGH mail to mayor.
	decision := joinOrLeadStaleAgentCorrelation(townRoot, rigName, item.SessionName, now, correlationWindow)
	if !decision.IsLead {
		item.Action = "skip-correlated"
		item.CorrelatedInto = decision.FoldedInto
		// Record the notify state anyway: this agent's condition WAS observed and
		// folded, so its per-(rig,session) cooldown should start now. Otherwise
		// the next cycle treats it as a brand-new first observation and the
		// cooldown gate would wave it through again the moment the correlation
		// window closes, defeating both gates.
		if cooldown > 0 {
			writeStaleAgentState(townRoot, rigName, item.SessionName, &staleAgentNotifyState{
				LastNotifiedAt: now,
				LastBand:       band,
				LastMissing:    missing,
			})
		}
		return
	}

	item.Action = "escalated"
	item.MailSent = sendStaleRigAgentMail(router, t, rigName, *item, threshold)

	// Record the notification so subsequent cycles can suppress duplicates.
	// We record on the decision to notify (not only on successful send): a
	// transient mail failure should not defeat the cooldown and reopen the
	// flood. The nudge fallback inside sendStaleRigAgentMail still surfaces the
	// alarm out-of-band.
	if cooldown > 0 {
		writeStaleAgentState(townRoot, rigName, item.SessionName, &staleAgentNotifyState{
			LastNotifiedAt: now,
			LastBand:       band,
			LastMissing:    missing,
		})
	}
}

// sendStaleRigAgentMail emits a HIGH-priority mail to mayor describing the
// staleness. Returns true on successful delivery (mail or nudge fallback).
func sendStaleRigAgentMail(router *mail.Router, t *tmux.Tmux, rigName string, item StaleRigAgentResult, threshold time.Duration) bool {
	if router == nil {
		return false
	}

	var subject, body string
	if item.HeartbeatMissing {
		subject = fmt.Sprintf("STALE_RIG_AGENT %s/%s (no heartbeat, session_alive=%v)",
			rigName, item.AgentRole, item.SessionAlive)
		body = fmt.Sprintf(`Rig-level agent %s/%s has no heartbeat file at all
(.runtime/heartbeats/%s.json is missing).

Session alive: %v
Threshold: heartbeats older than %s are considered stale.

This usually means one of:
  - The agent process is up but has not run any gt command since starting,
    so the heartbeat was never written. Pre-gu-0nmw builds did not touch
    heartbeats for refinery/witness; an old binary may still be running.
  - The agent crashed before the initial heartbeat write and the daemon
    has not yet restarted it.

Recovery:
  - gt session status %s/%s --json
  - gt %s status --json %s   (if applicable)
  - gt session restart %s/%s

Dedup (gu-z8qzq): this alarm is suppressed on subsequent patrol cycles while
the condition is unchanged. It re-fires only if the staleness worsens
materially or after the notify-cooldown window elapses.`,
			rigName, item.AgentRole, item.SessionName,
			item.SessionAlive, threshold,
			rigName, item.AgentRole,
			item.AgentRole, rigName,
			rigName, item.AgentRole)
	} else {
		subject = fmt.Sprintf("STALE_RIG_AGENT %s/%s (heartbeat age=%s, session_alive=%v)",
			rigName, item.AgentRole, item.HeartbeatAge.Round(time.Second), item.SessionAlive)
		body = fmt.Sprintf(`Rig-level agent %s/%s heartbeat is %s old (threshold %s).

Session alive: %v

If the session is alive, the process is up but the agent loop is wedged —
e.g. stuck mid-merge for refinery, blocked on a prompt for witness. This is
the gu-rh0g signature: process running, work loop frozen.

If the session is dead, the daemon supervisor missed a restart cycle.

Recovery:
  - gt session status %s/%s --json
  - gt %s status --json %s
  - gt session restart %s/%s

Dedup (gu-z8qzq): this alarm is suppressed on subsequent patrol cycles while
the condition is unchanged. It re-fires only if the staleness worsens
materially (crosses a new threshold multiple) or after the notify-cooldown
window elapses.`,
			rigName, item.AgentRole, item.HeartbeatAge.Round(time.Second), threshold,
			item.SessionAlive,
			rigName, item.AgentRole,
			item.AgentRole, rigName,
			rigName, item.AgentRole)
	}

	msg := &mail.Message{
		From:     fmt.Sprintf("%s/witness", rigName),
		To:       "mayor/",
		Subject:  subject,
		Priority: mail.PriorityHigh,
		Body:     body,
	}
	if err := router.Send(msg); err == nil {
		return true
	}

	// Mail flake fallback — nudge mayor with the subject line. Even if the
	// mail bus is down, the operator should see the alarm somewhere.
	if t != nil {
		if nudgeErr := t.NudgeSession(session.MayorSessionName(), subject); nudgeErr == nil {
			return true
		} else {
			fmt.Fprintf(os.Stderr, "witness: nudge fallback to mayor failed for %s: %v\n", item.SessionName, nudgeErr)
		}
	}
	return false
}
