package mail

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/tmux"
)

// notifyRecipient sends a notification to a recipient's tmux session.
//
// Notification strategy (idle-aware):
//  1. If the session is idle (prompt visible), send an immediate nudge.
//  2. If the session is busy, enqueue a nudge for cooperative delivery at
//     the next turn boundary.
//  3. For the overseer (human operator), always use a visible banner.
//
// After a successful notification, a deferred reply-reminder nudge is also
// enqueued (after a configurable delay, default 30s) to prompt the recipient
// to reply via gt mail send rather than in chat.
//
// Supports mayor/, deacon/, rig/crew/name, rig/polecats/name, and rig/name addresses.
// Respects agent DND/muted state - skips notification if recipient has DND enabled.
func (r *Router) notifyRecipient(msg *Message) error {
	// Check DND status before attempting notification
	if r.townRoot != "" {
		if r.isRecipientMuted(msg.To) {
			return nil // Recipient has DND enabled, skip notification
		}
	}

	sessionIDs := AddressToSessionIDs(msg.To)
	if len(sessionIDs) == 0 {
		return nil // Unable to determine session ID
	}

	timeout := r.IdleNotifyTimeout
	if timeout == 0 {
		timeout = DefaultIdleNotifyTimeout
	}

	// Try each possible session ID until we find one that exists.
	// This handles the ambiguity where canonical addresses (rig/name) don't
	// distinguish between crew workers (gt-rig-crew-name) and polecats (gt-rig-name).
	for _, sessionID := range sessionIDs {
		hasSession, err := r.tmux.HasSession(sessionID)
		if err != nil || !hasSession {
			continue
		}

		// Overseer is a human operator - use a visible banner instead of NudgeSession
		// (which types into Claude's input and would disrupt the human's terminal).
		if msg.To == "overseer" {
			return r.tmux.SendNotificationBanner(sessionID, msg.From, msg.Subject)
		}

		notification := formatNotificationMessage(msg)
		priority := nudgePriorityForMailPriority(msg.Priority)

		// Wait-idle-first delivery: try direct nudge if the agent is idle,
		// fall back to cooperative queue if busy. WaitForIdle requires 2
		// consecutive idle polls (prompt visible + no "esc to interrupt"
		// in the status bar) to distinguish genuine idle from brief
		// inter-tool-call gaps. See: https://github.com/steveyegge/gastown/issues/2032
		waitErr := r.tmux.WaitForIdle(sessionID, timeout)
		if waitErr == nil {
			// Agent is idle — deliver directly for immediate wakeup.
			if err := r.tmux.NudgeSession(sessionID, notification); err == nil {
				r.enqueueReplyReminder(msg, sessionID)
				return nil
			} else if errors.Is(err, tmux.ErrSessionNotFound) {
				continue
			} else if errors.Is(err, tmux.ErrNoServer) {
				return nil
			}
		} else if errors.Is(waitErr, tmux.ErrSessionNotFound) {
			continue
		} else if errors.Is(waitErr, tmux.ErrNoServer) {
			return nil
		} else if r.townRoot != "" {
			// Timeout (agent busy) — queue for cooperative delivery
			// at the next turn boundary.
			if err := nudge.Enqueue(r.townRoot, sessionID, nudge.QueuedNudge{
				Sender:   msg.From,
				Message:  notification,
				Priority: priority,
				Kind:     nudgeKindForMessage(msg),
				ThreadID: msg.ThreadID,
				Severity: prioritySeverityLabel(msg.Priority),
			}); err != nil {
				return err
			}
			r.enqueueReplyReminder(msg, sessionID)
			return nil
		}
		// No town root available — last resort direct delivery.
		err = r.tmux.NudgeSession(sessionID, notification)
		if err == nil {
			r.enqueueReplyReminder(msg, sessionID)
		}
		return err
	}
	// No tmux session found - enqueue nudge for ACP/propeller delivery
	// This handles headless ACP mode where there's no tmux session
	if r.townRoot != "" && len(sessionIDs) > 0 {
		notification := formatNotificationMessage(msg)
		return nudge.Enqueue(r.townRoot, sessionIDs[0], nudge.QueuedNudge{
			Sender:   msg.From,
			Message:  notification,
			Priority: nudgePriorityForMailPriority(msg.Priority),
			Kind:     nudgeKindForMessage(msg),
			ThreadID: msg.ThreadID,
			Severity: prioritySeverityLabel(msg.Priority),
		})
	}

	return nil // No active session found
}

func nudgeKindForMessage(msg *Message) string {
	if msg.Type == TypeEscalation {
		return "escalation"
	}
	return "mail"
}

func nudgePriorityForMailPriority(priority Priority) string {
	switch priority {
	case PriorityUrgent, PriorityHigh:
		return nudge.PriorityUrgent
	default:
		return nudge.PriorityNormal
	}
}

func formatNotificationMessage(msg *Message) string {
	if msg.Type == TypeEscalation {
		return fmt.Sprintf("🚨 Escalation mail from %s. ID: %s. Severity: %s. Subject: %s. Run 'gt mail read %s' or 'gt escalate ack %s'.", msg.From, msg.ThreadID, prioritySeverityLabel(msg.Priority), msg.Subject, msg.ThreadID, msg.ThreadID)
	}
	// Plugin-dispatch subjects are informational — use non-mail phrasing so the
	// system "reply to mail" reminder heuristic does not fire. See gt-swirk.
	if isPluginDispatchSubject(msg.Subject) {
		return fmt.Sprintf("🔌 %s dispatched from %s.", msg.Subject, msg.From)
	}
	return fmt.Sprintf("📬 You have new mail from %s. Subject: %s. Run 'gt mail inbox' to read.", msg.From, msg.Subject)
}

// isPluginDispatchSubject returns true when the message subject is a plugin
// dispatch ("Plugin: <name>"). Plugin dispatches are informational and do not
// require a reply, so reminder nudges should be suppressed for them.
// See gt-swirk (nudge storm) — deacon cycles were losing ~30% context to
// phantom "reply via mail" reminders fired on plugin dispatches.
func isPluginDispatchSubject(subject string) bool {
	return strings.HasPrefix(subject, "Plugin: ")
}

func prioritySeverityLabel(priority Priority) string {
	switch priority {
	case PriorityUrgent:
		return "critical"
	case PriorityHigh:
		return "high"
	case PriorityLow:
		return "low"
	default:
		return "medium"
	}
}

// enqueueReplyReminder queues a deferred nudge reminding the recipient to reply
// via gt mail send rather than in chat. Best-effort: errors are logged, not returned.
//
// Skipped when:
//   - No town root (can't use nudge queue)
//   - Message type is TypeReply (recipient is already replying)
//   - Configured delay is zero or negative (feature disabled)
func (r *Router) enqueueReplyReminder(msg *Message, sessionID string) {
	if r.townRoot == "" {
		return
	}
	if msg.Type == TypeReply {
		return // Already a reply — reminder would be redundant
	}
	if isPluginDispatchSubject(msg.Subject) {
		return // Plugin-dispatch is informational — no reply expected. See gt-swirk.
	}
	delay := config.LoadOperationalConfig(r.townRoot).GetMailConfig().ReplyReminderDelayD()
	if delay <= 0 {
		return // Disabled by config
	}
	reminder := nudge.QueuedNudge{
		Sender:       "system",
		Message:      fmt.Sprintf("Remember to reply to %s (subject: %q) via `gt mail send %s` — not in chat.", msg.From, msg.Subject, msg.From),
		Priority:     nudge.PriorityNormal,
		DeliverAfter: time.Now().Add(delay),
	}
	if err := nudge.Enqueue(r.townRoot, sessionID, reminder); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to enqueue reply reminder for %s: %v\n", sessionID, err)
	}
}

// IsRecipientMuted checks if a mail recipient has DND/muted notifications enabled.
// Returns true if the recipient is muted and should not receive tmux nudges.
// Fails open (returns false) if the agent bead cannot be found or the town root is not set.
func (r *Router) IsRecipientMuted(address string) bool {
	if r.townRoot == "" {
		return false
	}
	return r.isRecipientMuted(address)
}

// isRecipientMuted checks if a mail recipient has DND/muted notifications enabled.
// Returns true if the recipient is muted and should not receive tmux nudges.
// Fails open (returns false) if the agent bead cannot be found.
func (r *Router) isRecipientMuted(address string) bool {
	agentBeadID := addressToAgentBeadID(address)
	if agentBeadID == "" {
		return false // Can't determine agent bead, allow notification
	}

	bd := beads.New(r.townRoot)
	level, err := bd.GetAgentNotificationLevel(agentBeadID)
	if err != nil {
		return false // Agent bead might not exist, allow notification
	}

	return level == beads.NotifyMuted
}
