// Copyright (c) Steve Yegge. Licensed under the MIT License.

package daemon

import (
	"errors"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/witness"
)

// RestartPolecatSubjectPrefix is the subject prefix the stuck-agent-dog plugin
// (and any other caller) uses to signal that a polecat session should be
// restarted. Subject format: "RESTART_POLECAT: <rig>/<polecat>" optionally
// followed by a parenthesized suffix such as " (zombie cleared)".
const RestartPolecatSubjectPrefix = "RESTART_POLECAT:"

// restartPolecatProcessed is returned per message from the inner loop. Used
// by tests to assert which path was taken without having to grep the logger.
type restartPolecatProcessed struct {
	MessageID string
	Subject   string
	Rig       string
	Polecat   string
	Outcome   string // "restarted", "stale", "unparseable", "restart-failed", "skipped-backoff"
	Err       error
}

// processRestartPolecatRequests scans the deacon inbox for RESTART_POLECAT
// mail (sent by the stuck-agent-dog plugin when it detects a dead, zombie,
// or stalled-alive polecat session), claims each message by deletion, and
// restarts the referenced polecat via witness.RestartPolecatWithBackoff.
//
// Why the daemon and not the deacon agent: the deacon is an LLM agent that
// historically dropped these messages on the floor — its dispatch reported
// "0 new" even when dogs had filed multiple RESTART_POLECAT requests in a
// single cycle (gu-nep2, 2026-05-07). RESTART_POLECAT requests are fully
// mechanical (parse subject → call witness restart), so handling them in Go
// closes the self-healing loop deterministically and lets the daemon log
// exactly which requests were picked up vs skipped and why.
//
// Routing note: the stuck-agent-dog plugin sends mail to "deacon/" (not
// "<rig>/witness") so this handler can claim the message before any LLM
// agent touches it. See plugins/stuck-agent-dog/run.sh.
//
// Backoff: this path uses witness.RestartPolecatWithBackoff, the same
// gs-549-aware primitive the witness patrol uses, so a polecat that keeps
// dying on startup does not get hammered into a crash loop by re-fired
// dog mail.
func (d *Daemon) processRestartPolecatRequests() {
	messages, err := d.fetchDeaconInbox()
	if err != nil {
		d.logger.Printf("RestartPolecat: failed to fetch deacon inbox: %v", err)
		return
	}
	if len(messages) == 0 {
		return
	}

	d.processRestartPolecatMessageList(messages, d.closeMessage, witness.RestartPolecatWithBackoff)
}

// processRestartPolecatMessageList is the inner loop of
// processRestartPolecatRequests, factored out for testability. The
// restartFn argument is the function actually used to restart the polecat
// session — tests can pass a stub that records calls without exec'ing
// `gt session restart`. deleteMessage may be nil to skip deletion during
// tests.
//
// The claim-then-execute ordering mirrors ProcessLifecycleRequests: we
// delete the mail bead BEFORE invoking restartFn so a failure (stale hook,
// tmux wedged, etc.) does not cause the same request to be re-claimed on
// every subsequent heartbeat. The dog's next cycle is the recovery path
// for a still-dead polecat, not our internal loop. Subjects that fail to
// parse are also deleted — leaving a malformed mail pinned to the inbox
// would just cause the same failure repeatedly.
func (d *Daemon) processRestartPolecatMessageList(
	messages []BeadsMessage,
	deleteMessage func(id string) error,
	restartFn func(workDir, rigName, polecatName string) error,
) []restartPolecatProcessed {
	maxAge := d.loadOperationalConfig().GetDaemonConfig().MaxLifecycleMessageAgeD()
	if maxAge <= 0 {
		maxAge = MaxLifecycleMessageAge
	}

	considered := 0
	picked := 0
	skippedStale := 0
	skippedUnparseable := 0
	skippedBackoff := 0
	failed := 0
	var processed []restartPolecatProcessed

	for _, msg := range messages {
		if msg.Read {
			continue
		}
		if !IsRestartPolecatMessage(msg.Subject) {
			continue
		}
		considered++

		// Age gate: stale restart requests likely reference sessions that
		// have since been restarted by another path (manual intervention,
		// daemon crash-restart, etc.). Dropping them prevents double-start.
		if msgTime, err := time.Parse(time.RFC3339, msg.Timestamp); err == nil {
			if age := time.Since(msgTime); age > maxAge {
				d.logger.Printf("RestartPolecat: skipping stale request %s from %s (age %v > max %v) — deleting",
					msg.ID, msg.From, age.Round(time.Minute), maxAge)
				if deleteMessage != nil {
					if err := deleteMessage(msg.ID); err != nil {
						d.logger.Printf("RestartPolecat: warning: failed to delete stale message %s: %v", msg.ID, err)
					}
				}
				skippedStale++
				processed = append(processed, restartPolecatProcessed{
					MessageID: msg.ID, Subject: msg.Subject, Outcome: "stale",
				})
				continue
			}
		}

		rigName, polecatName, ok := ParseRestartPolecatSubject(msg.Subject)
		if !ok {
			d.logger.Printf("RestartPolecat: could not parse rig/polecat from subject %q (msg %s) — deleting",
				msg.Subject, msg.ID)
			if deleteMessage != nil {
				if err := deleteMessage(msg.ID); err != nil {
					d.logger.Printf("RestartPolecat: warning: failed to delete unparseable message %s: %v", msg.ID, err)
				}
			}
			skippedUnparseable++
			processed = append(processed, restartPolecatProcessed{
				MessageID: msg.ID, Subject: msg.Subject, Outcome: "unparseable",
			})
			continue
		}

		// Claim the mail before acting. Even if restartFn fails, the
		// message is gone and the dog is expected to re-fire on its next
		// cycle if the polecat is still down.
		if deleteMessage != nil {
			if err := deleteMessage(msg.ID); err != nil {
				d.logger.Printf("RestartPolecat: warning: failed to delete message %s before execution: %v", msg.ID, err)
				// Continue anyway — the alternative (skip restart) is
				// worse: the polecat stays dead and the message stays
				// pinned.
			}
		}

		d.logger.Printf("RestartPolecat: picking up %s for %s/%s (from=%s, msg=%s)",
			msg.Subject, rigName, polecatName, msg.From, msg.ID)

		err := restartFn(d.config.TownRoot, rigName, polecatName)
		switch {
		case err == nil:
			picked++
			processed = append(processed, restartPolecatProcessed{
				MessageID: msg.ID, Subject: msg.Subject,
				Rig: rigName, Polecat: polecatName,
				Outcome: "restarted",
			})
			d.logger.Printf("RestartPolecat: restarted %s/%s (msg=%s)", rigName, polecatName, msg.ID)
			_ = events.LogAudit(events.TypeRestartPolecatHandled, "daemon",
				restartPolecatPayload(rigName, polecatName, msg.From, "restarted", ""))

		case errors.Is(err, witness.ErrPolecatInStartupBackoff),
			errors.Is(err, witness.ErrPolecatSessionTooYoung):
			// Deliberate skip — the witness backoff layer refused to
			// hammer a polecat that just spawned or that has been
			// crashing repeatedly. Audit but do not count as failure.
			skippedBackoff++
			processed = append(processed, restartPolecatProcessed{
				MessageID: msg.ID, Subject: msg.Subject,
				Rig: rigName, Polecat: polecatName,
				Outcome: "skipped-backoff", Err: err,
			})
			d.logger.Printf("RestartPolecat: skipped restart of %s/%s — %v", rigName, polecatName, err)
			_ = events.LogAudit(events.TypeRestartPolecatHandled, "daemon",
				restartPolecatPayload(rigName, polecatName, msg.From, "skipped-backoff", err.Error()))

		default:
			failed++
			processed = append(processed, restartPolecatProcessed{
				MessageID: msg.ID, Subject: msg.Subject,
				Rig: rigName, Polecat: polecatName,
				Outcome: "restart-failed", Err: err,
			})
			d.logger.Printf("RestartPolecat: restart of %s/%s failed: %v", rigName, polecatName, err)
			_ = events.LogAudit(events.TypeRestartPolecatHandled, "daemon",
				restartPolecatPayload(rigName, polecatName, msg.From, "restart-failed", err.Error()))
		}
	}

	if considered > 0 {
		d.logger.Printf("RestartPolecat: cycle summary — considered=%d restarted=%d stale=%d unparseable=%d skipped-backoff=%d failed=%d",
			considered, picked, skippedStale, skippedUnparseable, skippedBackoff, failed)
	}
	return processed
}

// IsRestartPolecatMessage reports whether the given subject is a
// RESTART_POLECAT request. The prefix must be followed by something so we
// don't match an unrelated subject that happens to start with the same
// string (belt-and-suspenders; current senders always use a colon).
func IsRestartPolecatMessage(subject string) bool {
	trimmed := strings.TrimSpace(subject)
	if !strings.HasPrefix(trimmed, RestartPolecatSubjectPrefix) {
		return false
	}
	return strings.TrimSpace(trimmed[len(RestartPolecatSubjectPrefix):]) != ""
}

// ParseRestartPolecatSubject extracts the rig and polecat names from a
// RESTART_POLECAT subject. The canonical format emitted by
// stuck-agent-dog/run.sh is:
//
//	"RESTART_POLECAT: <rig>/<polecat>"
//	"RESTART_POLECAT: <rig>/<polecat> (<suffix>)"   // e.g. "(zombie cleared)"
//
// Returns ok=false if either component is missing or empty. The function
// is deliberately strict: we prefer to skip a malformed subject and let
// the sender retry rather than attempt a restart against the wrong
// polecat.
func ParseRestartPolecatSubject(subject string) (rig, polecat string, ok bool) {
	trimmed := strings.TrimSpace(subject)
	if !strings.HasPrefix(trimmed, RestartPolecatSubjectPrefix) {
		return "", "", false
	}
	rest := strings.TrimSpace(trimmed[len(RestartPolecatSubjectPrefix):])
	if rest == "" {
		return "", "", false
	}

	// Drop any parenthesized suffix like " (zombie cleared)".
	if paren := strings.IndexByte(rest, '('); paren >= 0 {
		rest = strings.TrimSpace(rest[:paren])
	}
	// Also tolerate a trailing token after whitespace.
	if space := strings.IndexAny(rest, " \t"); space >= 0 {
		rest = rest[:space]
	}

	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	rig = strings.TrimSpace(parts[0])
	polecat = strings.TrimSpace(parts[1])
	if rig == "" || polecat == "" {
		return "", "", false
	}
	return rig, polecat, true
}

// restartPolecatPayload builds an audit event payload with the standard
// fields the observability pipeline expects. Kept small and additive so
// consumers can ignore unknown keys.
func restartPolecatPayload(rig, polecat, from, outcome, errMsg string) map[string]interface{} {
	payload := map[string]interface{}{
		"rig":     rig,
		"polecat": polecat,
		"from":    from,
		"outcome": outcome,
	}
	if errMsg != "" {
		payload["error"] = errMsg
	}
	return payload
}
