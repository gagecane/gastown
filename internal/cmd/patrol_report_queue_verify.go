package cmd

import (
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// queueScanVerifyMessage is the prefix used in the error returned by
// verifyRefineryQueueScan. Tests assert against this prefix and the
// formula references it in the queue-scan guidance, so it is part of
// the public contract of this gate. See gu-6hzv / gu-weki.
const queueScanVerifyMessage = "queue-scan EMPTY but real MRs exist"

// mrLister abstracts beads.ListMergeRequests for testability.
type mrLister interface {
	ListMergeRequests(opts beads.ListOptions) ([]*beads.Issue, error)
}

// verifyRefineryQueueScan refuses to accept a patrol report that claims
// queue-scan:EMPTY when real merge-request beads exist for the rig.
//
// This is the server-side enforcement for gu-6hzv: a freshly-restarted
// refinery agent ran `bd list --label=mr` (wrong label) instead of
// `gt mq list <rig>` (the formula contract), got 0 results, and reported
// "queue-scan EMPTY" while 8-19 MRs were ready in the queue. Each cycle
// confirmed itself and the queue stalled for 14h.
//
// The patrol's --steps audit is the natural choke point: if the agent
// claims queue-scan:EMPTY, we can independently check the queue. If
// pending MRs exist, fail the report so the patrol stays open and the
// agent has to actually look at the queue on the next cycle.
//
// The check is best-effort:
//   - reportedSteps is empty / no queue-scan entry → skip (nothing to verify)
//   - queue-scan reported as OK / anything else → skip (agent did its job)
//   - listing fails → skip with no error (don't gate on transient bd failures)
//   - queue empty → pass
//   - queue non-empty → fail with the missed MR IDs surfaced in the error
//
// Only the EMPTY case is gated. The bug pattern is reporting EMPTY, not
// SKIP; gating SKIP risks blocking legitimate abbreviated-patrol cycles
// and is out of scope for this regression fix.
func verifyRefineryQueueScan(rigName string, reportedSteps string, lister mrLister) error {
	if rigName == "" || lister == nil {
		return nil
	}

	results := parseStepResults(reportedSteps)
	status, ok := results["queue-scan"]
	if !ok {
		// queue-scan not in the audit — nothing to verify.
		return nil
	}
	if status != "EMPTY" {
		return nil
	}

	issues, err := lister.ListMergeRequests(beads.ListOptions{
		Label:    "gt:merge-request",
		Status:   "open",
		Priority: -1,
	})
	if err != nil {
		// Be conservative: a transient bd failure should not block the
		// patrol from closing. The agent's report stands; the next
		// patrol cycle will catch any real backlog.
		return nil
	}

	pending := pendingMRsForRig(issues, rigName)
	if len(pending) == 0 {
		// Genuinely empty — the report is honest.
		return nil
	}

	return fmt.Errorf(
		"%s: rig=%s missed_mrs=%d ids=[%s]\n"+
			"  use `gt mq list %s` (NOT `bd list --label=mr` — the real label is `gt:merge-request`).\n"+
			"  see formula step queue-scan and gu-weki/gu-6hzv for context",
		queueScanVerifyMessage,
		rigName,
		len(pending),
		strings.Join(pending, ","),
		rigName,
	)
}

// pendingMRsForRig filters merge-request beads to those that belong to the
// given rig and are actionable (open, not blocked). Returns the bead IDs
// for inclusion in the verification error message.
//
// The rig filter mirrors `gt mq list`: wisps are shared across the Dolt
// server so we MUST scope by the MRFields.Rig metadata. Without this
// filter, a refinery for rig A would be blocked by MRs queued for rig B.
func pendingMRsForRig(issues []*beads.Issue, rigName string) []string {
	var ids []string
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		if issue.Status != "open" {
			continue
		}
		if len(issue.BlockedBy) > 0 || issue.BlockedByCount > 0 {
			// Blocked MRs aren't actionable this cycle — the agent
			// reporting EMPTY here would not have processed them either.
			continue
		}
		fields := beads.ParseMRFields(issue)
		if fields == nil {
			// Unscoped MR — count it. Better a false positive that
			// surfaces a real query bug than a silent miss.
			ids = append(ids, issue.ID)
			continue
		}
		if fields.Rig != "" && !strings.EqualFold(fields.Rig, rigName) {
			continue
		}
		ids = append(ids, issue.ID)
	}
	return ids
}
