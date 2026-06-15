// Copyright (c) Steve Yegge. Licensed under the MIT License.

package daemon

import (
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/events"
)

// RecoveredBeadSubjectToken is the marker the Witness places in a RECOVERED_BEAD
// mail subject. The Witness emits two shapes (internal/witness/handlers.go):
//
//	"RECOVERED_BEAD <bead-id>"
//	"SPAWN_STORM RECOVERED_BEAD <bead-id> (respawned Nx)"
//
// so we match the token anywhere in the subject rather than only as a prefix.
const RecoveredBeadSubjectToken = "RECOVERED_BEAD"

// recoveredBeadProcessed records the outcome of one RECOVERED_BEAD mail the
// fallback handler considered. Used by tests to assert which path was taken
// without scraping the logger.
type recoveredBeadProcessed struct {
	MessageID string
	Subject   string
	BeadID    string
	Outcome   string // mirrors RedispatchResult.Action, plus "unparseable" / "within-grace"
	Deleted   bool
}

// processRecoveredBeadFallback is the daemon-side backstop for RECOVERED_BEAD
// mail. The Witness mails RECOVERED_BEAD to the Deacon when it recovers an
// abandoned bead from a dead polecat; the ONLY consumer is the Deacon agent
// running `gt deacon redispatch` during patrol (internal/cmd/deacon.go ->
// internal/deacon.Redispatch). If the Deacon is down, paused, or crash-looping,
// recovered beads sit as un-drained mail forever and are never re-slung — and
// the escalate-to-Mayor backstop, which lives inside Redispatch, never fires
// either. Combined with mountain rollup running independently, the convoy can
// hang around an ownerless leg.
//
// This handler closes that gap deterministically in Go, mirroring
// processRestartPolecatRequests (gu-nep2): the daemon picks up RECOVERED_BEAD
// mail that has sat past a grace window and runs the SAME deacon.Redispatch
// the Deacon would have, which either re-slings the bead or escalates to Mayor
// after too many failures. See gu-jbcag.
func (d *Daemon) processRecoveredBeadFallback() {
	messages, err := d.fetchDeaconInbox()
	if err != nil {
		d.logger.Printf("RecoveredBeadFallback: failed to fetch deacon inbox: %v", err)
		return
	}
	if len(messages) == 0 {
		return
	}

	grace := d.loadOperationalConfig().GetDaemonConfig().RecoveredBeadFallbackGraceD()
	d.processRecoveredBeadMessageList(messages, grace, d.closeMessage, d.redispatchRecoveredBead)
}

// redispatchRecoveredBead is the production redispatch func passed to
// processRecoveredBeadMessageList. It defers to deacon.Redispatch with the
// daemon's defaults (auto-detect rig from the bead prefix when the mail body
// gives no hint; default max-attempts and cooldown). Tests substitute a stub.
func (d *Daemon) redispatchRecoveredBead(beadID, sourceRig string) *deacon.RedispatchResult {
	return deacon.Redispatch(d.config.TownRoot, beadID, sourceRig, 0, 0)
}

// processRecoveredBeadMessageList is the testable inner loop of
// processRecoveredBeadFallback. redispatchFn performs the re-dispatch (real or
// stubbed); deleteMessage may be nil to skip deletion during tests.
//
// Claim ordering differs deliberately from processRestartPolecatRequests.
// deacon.Redispatch is self-guarded (per-bead cooldown, open-status check,
// escalate-after-N), so calling it more than once is safe. We therefore run it
// FIRST and only delete the mail on a TERMINAL outcome (re-dispatched,
// escalated, already-escalated, or skipped-because-not-open). On a transient
// "cooldown" or "error" we leave the mail pinned so a later cycle retries —
// claim-first would silently drop the bead on a transient failure, which is
// exactly the no-redispatch hole this handler exists to close.
func (d *Daemon) processRecoveredBeadMessageList(
	messages []BeadsMessage,
	grace time.Duration,
	deleteMessage func(id string) error,
	redispatchFn func(beadID, sourceRig string) *deacon.RedispatchResult,
) []recoveredBeadProcessed {
	if grace <= 0 {
		grace = config.DefaultRecoveredBeadFallbackGrace
	}

	considered := 0
	redispatched := 0
	escalated := 0
	skipped := 0
	withinGrace := 0
	deferred := 0 // cooldown/error — left pinned for a later cycle
	var processed []recoveredBeadProcessed

	for _, msg := range messages {
		if msg.Read {
			continue
		}
		beadID, ok := ParseRecoveredBeadSubject(msg.Subject)
		if !ok {
			continue
		}
		considered++

		// Grace gate: only take over mail the Deacon has had a fair chance to
		// drain. A fresh RECOVERED_BEAD belongs to the Deacon; we step in only
		// once it has sat past the grace window (Deacon down/paused/looping).
		// A subject we recognize but whose timestamp won't parse is treated as
		// past-grace — better to act on a recovered bead than to strand it.
		if msgTime, perr := time.Parse(time.RFC3339, msg.Timestamp); perr == nil {
			if age := time.Since(msgTime); age < grace {
				withinGrace++
				processed = append(processed, recoveredBeadProcessed{
					MessageID: msg.ID, Subject: msg.Subject, BeadID: beadID,
					Outcome: "within-grace",
				})
				continue
			}
		}

		sourceRig := deacon.ParseRecoveredBeadBody(msg.Body)

		d.logger.Printf("RecoveredBeadFallback: Deacon did not drain %s (bead=%s, from=%s, msg=%s) within grace %v — taking over",
			msg.Subject, beadID, msg.From, msg.ID, grace)

		result := redispatchFn(beadID, sourceRig)
		entry := recoveredBeadProcessed{
			MessageID: msg.ID, Subject: msg.Subject, BeadID: beadID,
			Outcome: result.Action,
		}

		// Delete the mail only on a terminal outcome; leave transient ones
		// (cooldown/error) pinned for a later cycle to retry.
		terminal := false
		switch result.Action {
		case "redispatched":
			redispatched++
			terminal = true
			d.logger.Printf("RecoveredBeadFallback: re-dispatched %s: %s", beadID, result.Message)
		case "escalated", "already-escalated":
			escalated++
			terminal = true
			d.logger.Printf("RecoveredBeadFallback: %s for %s: %s", result.Action, beadID, result.Message)
		case "skipped":
			// Bead is no longer open (already claimed/closed) — nothing to do.
			skipped++
			terminal = true
			d.logger.Printf("RecoveredBeadFallback: skipped %s: %s", beadID, result.Message)
		default: // "cooldown", "error"
			deferred++
			if result.Error != nil {
				d.logger.Printf("RecoveredBeadFallback: deferring %s (%s): %v", beadID, result.Action, result.Error)
			} else {
				d.logger.Printf("RecoveredBeadFallback: deferring %s (%s): %s", beadID, result.Action, result.Message)
			}
		}

		if terminal && deleteMessage != nil {
			if err := deleteMessage(msg.ID); err != nil {
				d.logger.Printf("RecoveredBeadFallback: warning: failed to delete message %s after %s: %v",
					msg.ID, result.Action, err)
			} else {
				entry.Deleted = true
			}
		}

		_ = events.LogAudit(events.TypeRecoveredBeadFallbackHandled, "daemon",
			recoveredBeadFallbackPayload(beadID, sourceRig, msg.From, result.Action, result.Message))

		processed = append(processed, entry)
	}

	if considered > 0 {
		d.logger.Printf("RecoveredBeadFallback: cycle summary — considered=%d redispatched=%d escalated=%d skipped=%d within-grace=%d deferred=%d",
			considered, redispatched, escalated, skipped, withinGrace, deferred)
	}
	return processed
}

// ParseRecoveredBeadSubject extracts the bead ID from a RECOVERED_BEAD mail
// subject. It tolerates both the plain Witness subject and the SPAWN_STORM
// variant:
//
//	"RECOVERED_BEAD gu-abc123"
//	"SPAWN_STORM RECOVERED_BEAD gu-abc123 (respawned 3x)"
//
// Returns ok=false if the token is absent or no bead token follows it.
func ParseRecoveredBeadSubject(subject string) (beadID string, ok bool) {
	trimmed := strings.TrimSpace(subject)
	idx := strings.Index(trimmed, RecoveredBeadSubjectToken)
	if idx < 0 {
		return "", false
	}
	rest := strings.TrimSpace(trimmed[idx+len(RecoveredBeadSubjectToken):])
	if rest == "" {
		return "", false
	}
	// The bead ID is the first whitespace-delimited token; drop any trailing
	// parenthesized suffix such as " (respawned 3x)".
	if space := strings.IndexAny(rest, " \t"); space >= 0 {
		rest = rest[:space]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", false
	}
	return rest, true
}

// recoveredBeadFallbackPayload builds an audit event payload. Kept small and
// additive so consumers can ignore unknown keys.
func recoveredBeadFallbackPayload(beadID, sourceRig, from, outcome, message string) map[string]interface{} {
	payload := map[string]interface{}{
		"bead":    beadID,
		"from":    from,
		"outcome": outcome,
	}
	if sourceRig != "" {
		payload["source_rig"] = sourceRig
	}
	if message != "" {
		payload["message"] = message
	}
	return payload
}
