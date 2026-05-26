// Main CI-break daemon dog.
//
// Watches for main_branch_test escalation beads, parses commit attribution,
// resolves the breaking commit to its MR bead (if any), and fires a
// MainCIBreakEvent to the registered handler when the MR bead carries the
// gt:auto-test-pr label (i.e., the rig opted into auto-test-pr).
//
// Architectural pattern: identical to mr_cycle_close_dog — poll-list-by-label,
// ack via labels written back onto the escalation bead, handler injection
// with a noop default.
//
// This is the "Part A" substrate for Phase 0 task 11 / D16 SEV-1 auto-revert.
// Part B (the actual D16 response handler) is wired in via
// SetMainCIBreakHandler once available.
//
// See: gu-15c8 (this bead) and gu-h1fn (mr_cycle_close_dog analog).
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

	"github.com/steveyegge/gastown/internal/beads"
)

const (
	defaultMainCIBreakInterval = 60 * time.Second

	// mainCIBreakAckLabel is written back onto the escalation bead after the
	// dog dispatches. Re-ticks see this label and skip the bead.
	mainCIBreakAckLabel = "acked-by-ci-break-dog"
)

// MainCIBreakConfig holds configuration for the main_ci_break patrol.
//
// The dog is opt-in (disabled by default), in line with the rest of the
// auto-test-pr surface. Phase 1 entry flips it on once D16 handler lands.
type MainCIBreakConfig struct {
	// Enabled controls whether the main_ci_break_dog runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "60s", "1m").
	// Default: 60 seconds.
	IntervalStr string `json:"interval,omitempty"`
}

// mainCIBreakInterval returns the configured interval, or the default (60s).
func mainCIBreakInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.MainCIBreak != nil {
		if config.Patrols.MainCIBreak.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.MainCIBreak.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultMainCIBreakInterval
}

// MainCIBreakEvent is the structured payload handed to the CI-break handler.
// Fields are populated from the escalation bead and the resolved MR bead.
type MainCIBreakEvent struct {
	// RigName is the rig whose main branch broke.
	RigName string

	// CommitSHA is the breaking commit on origin/<default_branch>.
	CommitSHA string

	// PreviousSHA is the last passing commit (or "unknown" if first run).
	PreviousSHA string

	// MRBeadID is the resolved MR bead ID for CommitSHA. Empty if the commit
	// was pushed directly (not via an MR), or if the MR bead was already reaped.
	MRBeadID string

	// EscalationID is the source escalation bead ID.
	EscalationID string

	// Body is the full escalation description.
	Body string
}

// MainCIBreakHandler is the callback invoked once per qualifying CI-break
// event per dog tick (after dedup and rig filter). Phase 0 task 11 Part B
// implements this; this dog ships with a no-op default so it can be enabled
// before Part B lands without panicking.
type MainCIBreakHandler func(MainCIBreakEvent)

// noopMainCIBreakHandler is the default handler — logs and drops the event.
func (d *Daemon) noopMainCIBreakHandler(ev MainCIBreakEvent) {
	d.logger.Printf("main_ci_break_dog: no handler registered, dropping event rig=%s commit=%s escalation=%s",
		ev.RigName, ev.CommitSHA, ev.EscalationID)
}

// SetMainCIBreakHandler installs the CI-break handler. Phase 0 task 11 Part B
// calls this from daemon startup once the D16 auto-revert handler is
// initialized. Passing nil resets to the no-op handler.
func (d *Daemon) SetMainCIBreakHandler(handler MainCIBreakHandler) {
	d.mainCIBreakHandler = handler
}

// mainCIBreakFingerprint produces a stable, human-recognizable identifier for
// an (escalation, rig, commit) triple. Used in log lines for traceability.
func mainCIBreakFingerprint(escalationID, rigName, commitSHA string) string {
	h := fnv.New128a()
	fmt.Fprintf(h, "%s::%s::%s", escalationID, rigName, commitSHA)
	return fmt.Sprintf("%x", h.Sum(nil))[:12]
}

// mainCIBreakEscalation holds parsed data from a main_branch_test escalation
// for the CI-break dog's consumption.
type mainCIBreakEscalation struct {
	ID          string
	Title       string
	Description string
	Labels      []string
}

// listUnackedMainCIBreakEscalations returns open escalation beads whose title
// starts with "main_branch_test:" and that have not yet been acked by this dog.
func (d *Daemon) listUnackedMainCIBreakEscalations() ([]mainCIBreakEscalation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.bdPath, //nolint:gosec // G204: args constructed internally
		"list",
		"--label=gt:escalation",
		"--status=open",
		"--json",
		"--limit=100",
	)
	cmd.Dir = d.config.TownRoot
	cmd.Env = os.Environ()
	setSysProcAttr(cmd)

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bd list: %w", err)
	}

	var all []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Labels      []string `json:"labels"`
	}
	if err := json.Unmarshal(out, &all); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}

	var result []mainCIBreakEscalation
	for _, issue := range all {
		if !strings.HasPrefix(issue.Title, "main_branch_test:") {
			continue
		}
		if sliceContains(issue.Labels, mainCIBreakAckLabel) {
			continue
		}
		result = append(result, mainCIBreakEscalation{
			ID:          issue.ID,
			Title:       issue.Title,
			Description: issue.Description,
			Labels:      issue.Labels,
		})
	}
	return result, nil
}

// addMainCIBreakAckLabel writes the ack label back onto the escalation bead
// so subsequent ticks skip it.
func (d *Daemon) addMainCIBreakAckLabel(escalationID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.bdPath, //nolint:gosec // G204: args constructed internally
		"update",
		escalationID,
		"--add-label="+mainCIBreakAckLabel,
	)
	cmd.Dir = d.config.TownRoot
	cmd.Env = append(os.Environ(), "BD_ACTOR=main-ci-break-dog")
	setSysProcAttr(cmd)

	return cmd.Run()
}

// runMainCIBreakDog is the patrol entry point. Polls open main_branch_test
// escalation beads, parses commit attribution, resolves MR bead, checks the
// gt:auto-test-pr label, dispatches to the registered handler, and writes
// back an ack label.
func (d *Daemon) runMainCIBreakDog() {
	if !d.isPatrolActive("main_ci_break") {
		return
	}

	d.logger.Printf("main_ci_break_dog: starting patrol cycle")

	escalations, err := d.listUnackedMainCIBreakEscalations()
	if err != nil {
		d.logger.Printf("main_ci_break_dog: failed to list escalations: %v", err)
		return
	}

	if len(escalations) == 0 {
		d.logger.Printf("main_ci_break_dog: no unacked main_branch_test escalations")
		return
	}

	handler := d.mainCIBreakHandler
	if handler == nil {
		handler = d.noopMainCIBreakHandler
	}

	knownRigs := d.getKnownRigs()

	dispatched, skipped, noAttribution := d.classifyAndDispatchMainCIBreakEscalations(
		escalations, handler, knownRigs)

	d.logger.Printf("main_ci_break_dog: cycle complete — total=%d dispatched=%d skipped=%d no_attribution=%d",
		len(escalations), dispatched, skipped, noAttribution)
}

// classifyAndDispatchMainCIBreakEscalations is the inner loop of the
// main_ci_break_dog. For each escalation:
//
//  1. Parse commit attribution (commit: / previous_commit: lines).
//     If no commit attribution is present, ack and skip (legacy escalation).
//  2. Identify rigs from the escalation body.
//  3. For each rig, resolve the commit SHA to its MR bead.
//  4. Check if the MR bead carries the gt:auto-test-pr label (opted-in rig).
//  5. If opted-in, dispatch the event to the handler.
//  6. Always ack the escalation (prevents re-processing on next tick).
//
// Returns (dispatched, skipped, noAttribution) counts.
func (d *Daemon) classifyAndDispatchMainCIBreakEscalations(
	escalations []mainCIBreakEscalation,
	handler MainCIBreakHandler,
	knownRigs []string,
) (int, int, int) {
	var dispatched, skipped, noAttribution int

	for _, esc := range escalations {
		attr := ParseCommitAttribution(esc.Description)
		if !attr.HasCommit() {
			d.logger.Printf("main_ci_break_dog: %s: no commit attribution — skipping (legacy escalation)", esc.ID)
			noAttribution++
			d.ackMainCIBreakEscalation(esc.ID)
			continue
		}

		// Identify affected rigs from the escalation body.
		rigs := parseMainBranchEscalation(esc.Description, knownRigs)
		if len(rigs) == 0 {
			d.logger.Printf("main_ci_break_dog: %s: no known rigs identified in escalation body", esc.ID)
			skipped++
			d.ackMainCIBreakEscalation(esc.ID)
			continue
		}

		anyDispatched := false
		for _, rigName := range rigs {
			rigDir := beads.GetRigDirForName(d.config.TownRoot, rigName)
			if rigDir == "" {
				d.logger.Printf("main_ci_break_dog: %s: rig=%s: no beads dir found, skipping", esc.ID, rigName)
				continue
			}

			// Resolve commit SHA to its MR bead.
			mrBeadID, err := d.LookupMRBeadByCommit(rigDir, attr.Commit)
			if err != nil {
				d.logger.Printf("main_ci_break_dog: %s: rig=%s: MR lookup error: %v", esc.ID, rigName, err)
				continue
			}

			// Check if the MR bead carries gt:auto-test-pr label.
			// If no MR bead found (direct push), check cannot be performed — skip.
			if mrBeadID == "" {
				d.logger.Printf("main_ci_break_dog: %s: rig=%s: commit %s has no MR bead (direct push?) — skipping",
					esc.ID, rigName, attr.Commit[:minInt(12, len(attr.Commit))])
				continue
			}

			if !d.mrBeadHasAutoTestPRLabel(rigDir, mrBeadID) {
				d.logger.Printf("main_ci_break_dog: %s: rig=%s: MR %s does not carry gt:auto-test-pr — skipping (not opted-in)",
					esc.ID, rigName, mrBeadID)
				continue
			}

			ev := MainCIBreakEvent{
				RigName:      rigName,
				CommitSHA:    attr.Commit,
				PreviousSHA:  attr.Previous,
				MRBeadID:     mrBeadID,
				EscalationID: esc.ID,
				Body:         esc.Description,
			}

			fp := mainCIBreakFingerprint(esc.ID, rigName, attr.Commit)
			d.logger.Printf("main_ci_break_dog: dispatching rig=%s commit=%s mr=%s escalation=%s fp=%s",
				ev.RigName, ev.CommitSHA[:minInt(12, len(ev.CommitSHA))], ev.MRBeadID, ev.EscalationID, fp)
			handler(ev)
			anyDispatched = true
		}

		if anyDispatched {
			dispatched++
		} else {
			skipped++
		}

		// Always ack — prevents escalation pile-up regardless of dispatch result.
		d.ackMainCIBreakEscalation(esc.ID)
	}

	return dispatched, skipped, noAttribution
}

// mrBeadHasAutoTestPRLabel checks whether the specified MR bead carries the
// gt:auto-test-pr label. This is the rig opt-in gate: only MRs submitted
// via an auto-test-pr-enabled rig will trigger the D16 auto-revert chain.
func (d *Daemon) mrBeadHasAutoTestPRLabel(rigDir, mrBeadID string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.bdPath, //nolint:gosec // G204: args constructed internally
		"show",
		mrBeadID,
		"--json",
	)
	cmd.Dir = rigDir
	cmd.Env = os.Environ()
	setSysProcAttr(cmd)

	out, err := cmd.Output()
	if err != nil {
		d.logger.Printf("main_ci_break_dog: failed to show MR bead %s: %v", mrBeadID, err)
		return false
	}

	var issue struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(out, &issue); err != nil {
		d.logger.Printf("main_ci_break_dog: failed to parse MR bead %s: %v", mrBeadID, err)
		return false
	}

	return sliceContains(issue.Labels, MRCycleCloseAutoTestPRLabel)
}

// ackMainCIBreakEscalation writes the ack label back onto the escalation
// bead so subsequent ticks skip it.
func (d *Daemon) ackMainCIBreakEscalation(escalationID string) {
	if err := d.addMainCIBreakAckLabel(escalationID); err != nil {
		d.logger.Printf("main_ci_break_dog: failed to ack %s: %v", escalationID, err)
	}
}

// minInt returns the smaller of a and b.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
