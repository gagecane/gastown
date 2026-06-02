package completion

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
)

// refineryHeartbeatStaleThreshold is how old the refinery's heartbeat file
// can be before `gt done` treats refinery as dead and refuses direct-push
// fallback on merge-queue rigs (gu-8edz).
//
// 5 minutes matches the witness's HeartbeatStartupGrace floor for non-polecat
// agents and is comfortably longer than the refinery's normal patrol cycle
// (~30s-1m), so transient pauses don't spuriously trip the guard.
const refineryHeartbeatStaleThreshold = 5 * time.Minute

// awaitingRefineryRecoveryLabel marks beads whose polecat completed work but
// could not submit because refinery is dead and direct-push is blocked on
// merge-queue rigs. Refinery recovery sweeps these on startup. (gu-8edz)
const awaitingRefineryRecoveryLabel = "awaiting_refinery_recovery"

// awaitingRefineryMergeLabel marks beads whose polecat finished and submitted
// a merge request but the refinery has not yet merged it to origin/main.
// Distinct from awaiting_refinery_recovery (which guards the dead-refinery /
// no-MR case) and stranded-merge (push/MR failure). The bead stays open until
// the refinery's PostMerge path closes it with the real on-main commit_sha,
// preserving the audit trail and preventing Pattern B false-closes when the
// refinery is wedged or slow. (gu-treq, supersedes gu-wvgt)
const awaitingRefineryMergeLabel = "awaiting_refinery_merge"

// allowDirectPushEnv is the explicit override that lets a polecat bypass the
// merge-queue direct-push block. When set to "1", the polecat must ALSO set
// GT_SKIP_PREPUSH_REASON (per gu-zy57) so the audit trail is preserved.
const allowDirectPushEnv = "GT_ALLOW_DIRECT_PUSH"

// IsMergeQueueRig reports whether the given rig has merge_queue.enabled=true
// in its settings/config.json. Returns false when settings can't be loaded;
// callers treat that as "not merge-queue managed" so unknown rigs preserve
// existing behavior.
func IsMergeQueueRig(townRoot, rigName string) bool {
	settingsPath := filepath.Join(townRoot, rigName, "settings", "config.json")
	settings, err := config.LoadRigSettings(settingsPath)
	if err != nil || settings == nil || settings.MergeQueue == nil {
		return false
	}
	return settings.MergeQueue.Enabled
}

// IsRefineryHeartbeatStale reports whether the rig's refinery heartbeat is
// older than refineryHeartbeatStaleThreshold (or missing). Returns
// (stale, reason): reason describes the staleness for diagnostic output.
//
// Missing heartbeat counts as stale: a refinery that has never written a
// heartbeat has either never run, or its file was nuked along with a session
// teardown. Either way, dispatching new MRs into a queue with no live
// processor is the failure mode this guard exists to prevent.
func IsRefineryHeartbeatStale(townRoot, rigName string) (bool, string) {
	rigPrefix := session.PrefixFor(rigName)
	refinerySession := session.RefinerySessionName(rigPrefix)

	hb := polecat.ReadSessionHeartbeat(townRoot, refinerySession)
	if hb == nil {
		return true, fmt.Sprintf("no heartbeat file for %s", refinerySession)
	}
	age := time.Since(hb.Timestamp)
	if age >= refineryHeartbeatStaleThreshold {
		return true, fmt.Sprintf("heartbeat for %s is %s old (threshold %s)", refinerySession, age.Truncate(time.Second), refineryHeartbeatStaleThreshold)
	}
	return false, ""
}

// directPushOverrideAllowed reports whether the operator has explicitly opted
// into direct-push on a merge-queue rig. Requires both:
//   - GT_ALLOW_DIRECT_PUSH=1
//   - GT_SKIP_PREPUSH_REASON=<text> (per gu-zy57: every skip leaves an audit reason)
//
// Returns the reason on success, or empty string if the override is not in effect.
func directPushOverrideAllowed() (allowed bool, reason string) {
	if os.Getenv(allowDirectPushEnv) != "1" {
		return false, ""
	}
	r := os.Getenv("GT_SKIP_PREPUSH_REASON")
	if r == "" {
		return false, ""
	}
	return true, r
}

// GuardDirectPushOnMergeQueue checks whether a polecat is allowed to push
// directly to the rig's default branch. On merge-queue-enabled rigs, the
// merge queue is the merge gatekeeper and direct-push from polecats bypasses
// the gate suite — exactly the failure mode that left main green only by
// human-driven manual pushes (gu-rh0g, gu-8edz).
//
// Returns nil when direct push is allowed (non-merge-queue rig, override
// engaged, or non-polecat caller). Returns an error explaining why direct
// push is refused otherwise.
func GuardDirectPushOnMergeQueue(townRoot, rigName, contextLabel string) error {
	// Non-polecats (crew, mayor, deacon manual interventions) are unaffected.
	// They sometimes push direct to recover from refinery outages, and the
	// rationale is human-judged, not automated dispatch.
	if os.Getenv("GT_POLECAT") == "" {
		return nil
	}
	if !IsMergeQueueRig(townRoot, rigName) {
		return nil
	}
	if allowed, reason := directPushOverrideAllowed(); allowed {
		fmt.Fprintf(os.Stderr, "WARN: direct-push override engaged for %s: GT_ALLOW_DIRECT_PUSH=1 with reason=%q\n", contextLabel, reason)
		return nil
	}
	return fmt.Errorf("direct push to default branch refused on merge-queue rig %q (%s); merge queue is the merge gatekeeper.\n"+
		"To override (emergencies only): set GT_ALLOW_DIRECT_PUSH=1 and GT_SKIP_PREPUSH_REASON=<text>",
		rigName, contextLabel)
}

// MarkAwaitingRefineryMerge labels the hooked bead and records an audit note
// when the polecat has successfully pushed/created an MR on a merge-queue
// rig. The bead stays open (in_progress) until the refinery's PostMerge path
// closes it with the real on-main commit_sha — that's the only event that
// proves the work actually shipped to origin/main. (gu-treq)
//
// Distinct from MarkAwaitingRefineryRecovery: the MR exists and the queue is
// healthy from the polecat's vantage point. This guard catches the variant
// where the refinery later wedges or is slow, so the bead is not falsely
// marked shipped before the merge actually happens.
//
// Best-effort: failures to label/note are warned, not fatal — the polecat
// must still exit cleanly. The refinery's PostMerge will close the bead even
// if these audit calls failed.
func MarkAwaitingRefineryMerge(bd *beads.Beads, beadID, mrID, branch string) {
	if bd == nil || beadID == "" {
		return
	}
	if err := bd.Update(beadID, beads.UpdateOptions{AddLabels: []string{awaitingRefineryMergeLabel}}); err != nil {
		style.PrintWarning("could not add %s label to %s: %v", awaitingRefineryMergeLabel, beadID, err)
	}
	parts := []string{"awaiting_refinery_merge (gu-treq): polecat submitted MR; refinery has not yet merged to origin/main."}
	if mrID != "" {
		parts = append(parts, fmt.Sprintf("mr_id: %s", mrID))
	}
	if branch != "" {
		parts = append(parts, fmt.Sprintf("source_branch: %s", branch))
	}
	noteBody := strings.Join(parts, "\n")
	if _, err := bd.Run("note", beadID, noteBody); err != nil {
		style.PrintWarning("could not add awaiting_refinery_merge note to %s: %v", beadID, err)
	}
}

// MarkAwaitingRefineryRecovery labels the hooked bead and records an audit
// note when MR submission is suppressed because refinery is dead. The bead
// stays open so refinery recovery can sweep it. (gu-8edz)
//
// This is a best-effort helper: failures to label/note are warned, not fatal,
// because we still want the polecat to exit cleanly so it can be re-dispatched.
func MarkAwaitingRefineryRecovery(bd *beads.Beads, beadID, reason string) {
	if bd == nil || beadID == "" {
		return
	}
	if err := bd.Update(beadID, beads.UpdateOptions{AddLabels: []string{awaitingRefineryRecoveryLabel}}); err != nil {
		style.PrintWarning("could not add %s label to %s: %v", awaitingRefineryRecoveryLabel, beadID, err)
	}
	noteBody := fmt.Sprintf("awaiting_refinery_recovery (gu-8edz): %s", reason)
	if _, err := bd.Run("note", beadID, noteBody); err != nil {
		style.PrintWarning("could not add awaiting_refinery_recovery note to %s: %v", beadID, err)
	}
}
