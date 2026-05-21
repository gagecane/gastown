// MR cycle-close daemon dog.
//
// Background: gastown today has no native MR-bead state-change subscription
// primitive. Phase 0 task 3c (gu-xrxm6) — the Mayor cycle-close handler for
// auto-test-pr — needs an event-like surface that fires when an MR labeled
// gt:auto-test-pr is closed. The realistic substrate is poll-list-by-label,
// the same primitive failure_classifier_dog uses.
//
// This dog polls Beads.ListMergeRequests({Label:"gt:auto-test-pr",
// Status:"closed"}) on a tick, classifies each closed MR by its close_reason
// field and rig:<target_rig> label, builds a structured MRCycleCloseEvent,
// and hands it off to a registered handler. Idempotency is via a
// "fingerprint:cycle-close-<close_reason>" label written back on the MR
// bead — re-ticks see the label and skip the bead.
//
// See: gu-h1fn (this bead) and gu-grkl (the analog dog for main-CI-break /
// Phase 0 task 11). Both confirm the same architectural truth: poll the
// merge-request bead label set; ack via labels written back on the bead.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultMRCycleCloseInterval = 60 * time.Second

	// MRCycleCloseAutoTestPRLabel is the label that identifies an MR bead as
	// belonging to the auto-test-pr feature. Set by polecat at gt done time
	// per Phase 0 task 3a, round 3 fix #6.
	MRCycleCloseAutoTestPRLabel = "gt:auto-test-pr"

	// MRCycleCloseRigLabelPrefix is the per-rig label prefix on the MR bead.
	// The handler reads `rig:<target_rig>` to resolve the per-rig
	// auto-test-pr state bead in O(1) (round 3 fix #6).
	MRCycleCloseRigLabelPrefix = "rig:"

	// mrCycleCloseAckLabelPrefix is written back onto the MR bead after the
	// dog dispatches a close event. Re-ticks see the label and skip the
	// bead. The full label is mrCycleCloseAckLabelPrefix + close_reason
	// (e.g., "fingerprint:cycle-close-merged"). Including close_reason in
	// the fingerprint means a hypothetical close→reopen→close-with-different-
	// reason on the same MR would still dispatch — this is intentional, the
	// handler treats each close-reason as a distinct cycle outcome.
	mrCycleCloseAckLabelPrefix = "fingerprint:cycle-close-"
)

// MRCycleCloseConfig holds configuration for the mr_cycle_close_dog patrol.
//
// The dog is opt-in (disabled by default), in line with the rest of the
// auto-test-pr surface. Phase 1 entry (D9 in synthesis.md) flips it on once
// task 3c lands and the dispatch handler is wired.
type MRCycleCloseConfig struct {
	// Enabled controls whether the mr_cycle_close_dog runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "60s", "1m").
	// Default: 60 seconds. The cycle wall-clock budget is ≤30 min so
	// sub-minute polling is more than enough — no event-bus needed.
	IntervalStr string `json:"interval,omitempty"`
}

// mrCycleCloseInterval returns the configured interval, or the default (60s).
func mrCycleCloseInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.MRCycleClose != nil {
		if config.Patrols.MRCycleClose.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.MRCycleClose.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultMRCycleCloseInterval
}

// MRCycleCloseEvent is the structured payload handed to the cycle-close
// handler. Fields are populated from the closed MR bead: MRID is the bead
// ID; TargetRig is parsed from the rig:<target_rig> label; CloseReason is
// parsed from the description's close_reason: line; Body is the full
// description (so the handler can parse BUG-DISCOVERED: NOTES per D2a).
//
// Phase 0 task 3c (gu-xrxm6) consumes this struct directly — no further
// plumbing needed for that task.
type MRCycleCloseEvent struct {
	// MRID is the merge-request bead ID (e.g., "gt-mr1").
	MRID string

	// TargetRig is the rig owning the auto-test-pr cycle (parsed from the
	// rig:<target_rig> label set on the MR bead at gt done time).
	TargetRig string

	// CloseReason is the merge outcome ("merged", "rejected", "conflict",
	// "superseded"). Parsed from the description's close_reason: field.
	CloseReason string

	// Body is the full MR-bead description, including BUG-DISCOVERED:
	// NOTES (if any) for the bug-discovery path in 3c.
	Body string
}

// MRCycleCloseHandler is the callback invoked once per closed MR per dog
// tick (after dedup). Phase 0 task 3c implements this; this dog ships with
// a no-op default so it can be enabled before 3c lands without panicking.
type MRCycleCloseHandler func(MRCycleCloseEvent)

// noopMRCycleCloseHandler is the default handler — logs and drops the
// event. The handler is registered via SetMRCycleCloseHandler.
func (d *Daemon) noopMRCycleCloseHandler(ev MRCycleCloseEvent) {
	d.logger.Printf("mr_cycle_close_dog: no handler registered, dropping event mr=%s rig=%s reason=%s",
		ev.MRID, ev.TargetRig, ev.CloseReason)
}

// SetMRCycleCloseHandler installs the cycle-close handler. Phase 0 task 3c
// calls this from daemon startup once the Mayor cycle-close handler is
// initialized. Passing nil resets to the no-op handler.
func (d *Daemon) SetMRCycleCloseHandler(handler MRCycleCloseHandler) {
	d.mrCycleCloseHandler = handler
}

// mrCycleCloseAckLabel returns the per-close-reason ack label written back
// on the MR bead after dispatch. Re-ticks see this label and skip the bead.
func mrCycleCloseAckLabel(closeReason string) string {
	return mrCycleCloseAckLabelPrefix + sanitizeLabelValue(closeReason)
}

// sanitizeLabelValue lowercases and strips characters that bd does not
// accept inside label values. We only allow [a-z0-9-_], everything else
// becomes "-". Invariant: identical close_reason inputs always produce
// identical labels (so dedup holds).
func sanitizeLabelValue(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "unknown"
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= '0' && c <= '9',
			c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

// mrCycleCloseFingerprint produces a stable, human-recognizable identifier
// for an (MR, close_reason) pair. Used in log lines for traceability; the
// dedup label itself is mrCycleCloseAckLabel(closeReason) which does not
// include the MR ID (the label is on the MR bead, so the bead provides the
// MR-ID dimension implicitly).
func mrCycleCloseFingerprint(mrID, closeReason string) string {
	h := fnv.New128a()
	fmt.Fprintf(h, "%s::%s", mrID, closeReason)
	return fmt.Sprintf("%x", h.Sum(nil))[:12]
}

// extractRigFromLabels returns the value of the first label of the form
// "rig:<name>" (no space). Returns "" if no rig label is present.
//
// Round 3 fix #6: the rig:<target_rig> label is the O(1) link from MR bead
// to per-rig auto-test-pr state bead. Without this label, the cycle-close
// handler would have to walk the bead graph back through the dispatch bead
// to resolve the rig — a regression the round 3 fix was specifically added
// to prevent. The label is set by polecat at gt done time per Phase 0 task
// 3a, and the dog relies on it being present.
func extractRigFromLabels(labels []string) string {
	for _, l := range labels {
		l = strings.TrimSpace(l)
		// Tolerate both "rig:gastown" and " rig: gastown" (the second form
		// is what FormatDogDescription uses in the description body, but
		// labels themselves are stored compact).
		if strings.HasPrefix(l, MRCycleCloseRigLabelPrefix) {
			return strings.TrimSpace(strings.TrimPrefix(l, MRCycleCloseRigLabelPrefix))
		}
	}
	return ""
}

// extractCloseReasonFromBody pulls the close_reason value from the MR
// description body. The MR-bead format (see beads.MRFields) carries
// close_reason on a `close_reason: <value>` line. Falls back to "" if not
// present (the dog skips the MR in that case — a closed MR with no close
// reason is malformed and shouldn't dispatch).
func extractCloseReasonFromBody(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		const key = "close_reason:"
		if strings.HasPrefix(strings.ToLower(line), key) {
			return strings.TrimSpace(line[len(key):])
		}
	}
	return ""
}

// mrCycleCloseRow mirrors the JSON shape returned by `bd list --json`. We
// shell out to bd (rather than the Beads.ListMergeRequests wrapper) for
// the same reason failure_classifier_dog does: the daemon already has
// d.bdPath wired and the wrapper would require constructing a *beads.Beads
// against the town root with all of its routing paraphernalia. The cost is
// one subprocess per tick, which is dwarfed by the 60s patrol interval.
type mrCycleCloseRow struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	Labels      []string `json:"labels"`
}

// listClosedAutoTestPRMergeRequests shells out to `bd list` with the
// auto-test-pr label and closed status. Includes both issues-table and
// wisp-table MRs (bd list does NOT include wisps by default; auto-test-pr
// MRs may be ephemeral wisps as gt mq submit creates wisps — so we make
// two passes and union the results).
func (d *Daemon) listClosedAutoTestPRMergeRequests() ([]mrCycleCloseRow, error) {
	rows, err := d.runBdListClosedAutoTestPR(false)
	if err != nil {
		return nil, fmt.Errorf("bd list (issues): %w", err)
	}

	wispRows, err := d.runBdListClosedAutoTestPR(true)
	if err != nil {
		// Non-fatal: ephemeral query may fail on older bd versions; the
		// issues-table query above is the primary path. Log and continue.
		d.logger.Printf("mr_cycle_close_dog: ephemeral bd list failed (non-fatal): %v", err)
	} else {
		seen := make(map[string]bool, len(rows))
		for _, r := range rows {
			seen[r.ID] = true
		}
		for _, r := range wispRows {
			if !seen[r.ID] {
				rows = append(rows, r)
			}
		}
	}

	return rows, nil
}

// runBdListClosedAutoTestPR runs a single `bd list` invocation against
// either the issues table or the wisps table, returning the parsed JSON
// rows.
func (d *Daemon) runBdListClosedAutoTestPR(ephemeral bool) ([]mrCycleCloseRow, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	args := []string{
		"list",
		"--label=" + MRCycleCloseAutoTestPRLabel,
		"--status=closed",
		"--json",
		"--limit=100",
	}
	if ephemeral {
		args = append(args, "--ephemeral")
	}

	cmd := exec.CommandContext(ctx, d.bdPath, args...) //nolint:gosec // G204: args constructed internally
	cmd.Dir = d.config.TownRoot
	cmd.Env = os.Environ()
	setSysProcAttr(cmd)

	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var rows []mrCycleCloseRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}
	return rows, nil
}

// addMRCycleCloseAckLabel writes the ack fingerprint label back onto the
// MR bead so subsequent ticks skip it. Mirrors the label-write pattern
// failure_classifier_dog uses to dedup auto-filed beads.
func (d *Daemon) addMRCycleCloseAckLabel(mrID, label string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.bdPath, //nolint:gosec // G204: args constructed internally
		"update",
		mrID,
		"--add-label="+label,
	)
	cmd.Dir = d.config.TownRoot
	cmd.Env = append(os.Environ(), "BD_ACTOR=mr-cycle-close-dog")
	setSysProcAttr(cmd)

	return cmd.Run()
}

// runMRCycleCloseDog is the patrol entry point. Polls closed MR beads
// labeled gt:auto-test-pr, classifies each, dispatches to the registered
// handler, and writes back an ack label.
func (d *Daemon) runMRCycleCloseDog() {
	if !d.isPatrolActive("mr_cycle_close") {
		return
	}

	d.logger.Printf("mr_cycle_close_dog: starting patrol cycle")

	rows, err := d.listClosedAutoTestPRMergeRequests()
	if err != nil {
		d.logger.Printf("mr_cycle_close_dog: failed to list MRs: %v", err)
		return
	}

	if len(rows) == 0 {
		d.logger.Printf("mr_cycle_close_dog: no closed gt:auto-test-pr MRs")
		return
	}

	handler := d.mrCycleCloseHandler
	if handler == nil {
		handler = d.noopMRCycleCloseHandler
	}

	dispatched, deduped, malformed := classifyAndDispatchMRCycleCloseRows(
		rows, handler, d.addMRCycleCloseAckLabel, d.logger.Printf)

	d.logger.Printf("mr_cycle_close_dog: cycle complete — total=%d dispatched=%d deduped=%d malformed=%d",
		len(rows), dispatched, deduped, malformed)
}

// ackWriter is the dependency injected into classifyAndDispatchMRCycleCloseRows
// so unit tests can observe label writes without spawning bd subprocesses.
type ackWriter func(mrID, label string) error

// logfn is the structured log signature so tests can observe log lines.
type logfn func(format string, args ...interface{})

// classifyAndDispatchMRCycleCloseRows is the pure inner loop, factored out
// of runMRCycleCloseDog so it can be unit-tested without a live daemon or
// bd subprocess. For each row:
//
//  1. If the row already carries the ack label, skip it (deduped).
//  2. Parse close_reason from the body and target_rig from the labels.
//     If either is missing, log and skip (malformed).
//  3. Hand the constructed MRCycleCloseEvent to handler.
//  4. Write the ack label back via ackWriter; on error, log and continue
//     (the next tick will retry — duplicate dispatch is a worse failure
//     mode than transient bd flakiness here, but the handler ought to be
//     idempotent on its own).
//
// Returns (dispatched, deduped, malformed) counts.
func classifyAndDispatchMRCycleCloseRows(
	rows []mrCycleCloseRow,
	handler MRCycleCloseHandler,
	ack ackWriter,
	log logfn,
) (int, int, int) {
	var dispatched, deduped, malformed int

	for _, row := range rows {
		closeReason := extractCloseReasonFromBody(row.Description)
		if closeReason == "" {
			log("mr_cycle_close_dog: %s: malformed — closed bead with no close_reason field", row.ID)
			malformed++
			continue
		}

		ackLabel := mrCycleCloseAckLabel(closeReason)
		if sliceContains(row.Labels, ackLabel) {
			log("mr_cycle_close_dog: %s: deduped (label %s already present)", row.ID, ackLabel)
			deduped++
			continue
		}

		targetRig := extractRigFromLabels(row.Labels)
		if targetRig == "" {
			log("mr_cycle_close_dog: %s: malformed — closed bead with no rig:<target_rig> label", row.ID)
			malformed++
			continue
		}

		ev := MRCycleCloseEvent{
			MRID:        row.ID,
			TargetRig:   targetRig,
			CloseReason: closeReason,
			Body:        row.Description,
		}

		log("mr_cycle_close_dog: dispatching mr=%s rig=%s reason=%s fp=%s",
			ev.MRID, ev.TargetRig, ev.CloseReason,
			mrCycleCloseFingerprint(ev.MRID, ev.CloseReason))
		handler(ev)

		if err := ack(row.ID, ackLabel); err != nil {
			log("mr_cycle_close_dog: %s: failed to write ack label %s: %v", row.ID, ackLabel, err)
			// Don't increment dispatched if we couldn't ack — the next tick
			// will see the same MR and dispatch again. The handler must be
			// idempotent for this case to be safe; this is documented on
			// MRCycleCloseHandler. We still increment "dispatched" because
			// the handler did run.
		}
		dispatched++
	}

	return dispatched, deduped, malformed
}
