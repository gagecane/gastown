package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// restart_pending_dog watches for daemon-restart-pending beads (filed by
// rebuild-gt after it upgrades the on-disk gt binary) and ESCALATES them to an
// agent so the operator/mayor performs a gated daemon restart with judgment.
//
// Why this exists (gu-muj66): rebuild-gt rebuilds + installs the binary, but
// the running daemon keeps executing its OLD in-memory image until a manual
// restart. rebuild-gt's only "signal" (gu-wcxv) was to file a
// type:daemon-restart-pending bead — but NOTHING consumed it, so the bead was
// silently dropped (3 piled up unconsumed; one sat deferred ~8h while the
// daemon ran stale code). The deploy of every daemon-resident fix silently
// failed until someone happened to restart manually.
//
// Design decision (operator, gu-muj66): do NOT auto-restart. A daemon that
// restarts itself risks a restart loop if the safety gate is wrong, which is
// worse than the current manual gap. Instead, make the pending restart LOUD by
// escalating to an agent. The agent then applies the safety gate (drain
// in-flight, confirm reservations clear, forward-only binary check) and
// performs `gt daemon stop && gt daemon start`, then closes the bead.
//
//	rebuild-gt builds binary
//	      │
//	      ▼
//	files type:daemon-restart-pending bead   ← producer (existing, gu-wcxv)
//	      │
//	      ▼
//	restart_pending_dog (this patrol)        ← consumer (NEW, gu-muj66)
//	      │  escalates to agent w/ state
//	      ▼
//	agent gates + restarts + closes bead     ← actor (human/mayor, NOT the daemon)

const (
	defaultRestartPendingInterval = 5 * time.Minute
	restartPendingLabel           = "type:daemon-restart-pending"
	// restartPendingEscalatedLabel marks a pending bead we've already escalated
	// so we don't re-escalate it on every tick. The escalation itself dedups via
	// gt escalate --dedup, but the label also lets `bd list` skip handled beads
	// cheaply and makes the handled state visible to operators.
	restartPendingEscalatedLabel = "restart-escalated"
	restartPendingSource         = "restart_pending_dog"
)

// RestartPendingConfig holds configuration for the restart_pending patrol.
type RestartPendingConfig struct {
	// Enabled controls whether the restart-pending consumer runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "5m").
	IntervalStr string `json:"interval,omitempty"`
}

// restartPendingInterval returns the configured interval, or the default (5m).
func restartPendingInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.RestartPending != nil {
		if config.Patrols.RestartPending.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.RestartPending.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultRestartPendingInterval
}

// restartPendingBead is a parsed daemon-restart-pending bead.
type restartPendingBead struct {
	ID          string
	Title       string
	Description string
}

// runRestartPendingDog is the main patrol function. It lists open
// daemon-restart-pending beads that have not yet been escalated, escalates each
// to an agent with enough state to safely gate the restart, and labels them as
// escalated so they are not re-escalated on the next tick.
func (d *Daemon) runRestartPendingDog() {
	if !d.isPatrolActive("restart_pending") {
		return
	}

	// Gate on the shared Dolt circuit breaker: when Dolt is degraded, skip the
	// bd list/escalate work and resume on the next tick.
	if d.doltBreaker != nil && !d.doltBreaker.Allow() {
		d.logger.Printf("restart_pending: dolt-degraded — skipping tick (circuit breaker open)")
		return
	}

	pending, err := d.listUnescalatedRestartPending()
	if d.doltBreaker != nil {
		d.doltBreaker.Record(err)
	}
	if err != nil {
		d.logger.Printf("restart_pending: failed to list pending beads: %v", err)
		return
	}

	if len(pending) == 0 {
		return
	}

	d.logger.Printf("restart_pending: %d un-escalated daemon-restart-pending bead(s)", len(pending))

	for _, b := range pending {
		msg := d.buildRestartEscalationMessage(b)
		// d.escalate dedups on signature, so repeated ticks before the label
		// lands won't spam; the label is the durable handled-marker.
		d.escalate(restartPendingSource, msg)
		if err := d.markRestartPendingEscalated(b.ID); err != nil {
			d.logger.Printf("restart_pending: %s: escalated but failed to mark handled: %v", b.ID, err)
		} else {
			d.logger.Printf("restart_pending: %s: escalated to agent and marked handled", b.ID)
		}
	}
}

// buildRestartEscalationMessage assembles the multi-line escalation body with
// the state an agent needs to safely gate the restart: the pending bead, the
// daemon's uptime/stale state, and current scheduler reservation/queue state.
func (d *Daemon) buildRestartEscalationMessage(b restartPendingBead) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Daemon restart pending — agent action required (NOT auto-restart).\n\n")
	fmt.Fprintf(&sb, "Pending bead: %s — %s\n", b.ID, b.Title)
	if b.Description != "" {
		fmt.Fprintf(&sb, "\n%s\n", b.Description)
	}
	fmt.Fprintf(&sb, "\nThe on-disk gt binary has been upgraded but the running daemon is still\n")
	fmt.Fprintf(&sb, "executing its OLD in-memory image. Daemon-resident fixes (dispatch,\n")
	fmt.Fprintf(&sb, "patrols, main_branch_test, etc.) will NOT deploy until the daemon restarts.\n")
	fmt.Fprintf(&sb, "\nRECOMMENDED ACTION (apply the safety gate, then restart):\n")
	fmt.Fprintf(&sb, "  1. Confirm in-flight work is safe to interrupt (scheduler reservations clear).\n")
	fmt.Fprintf(&sb, "  2. Forward-only check: new binary commit is a descendant of the running one.\n")
	fmt.Fprintf(&sb, "  3. gt daemon stop && gt daemon start\n")
	fmt.Fprintf(&sb, "  4. Verify the running daemon advanced, then close bead %s.\n", b.ID)
	return sb.String()
}

// listUnescalatedRestartPending returns open daemon-restart-pending beads that
// have not yet been marked escalated.
func (d *Daemon) listUnescalatedRestartPending() ([]restartPendingBead, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.bdPath, //nolint:gosec // G204: args constructed internally
		"list",
		"--label="+restartPendingLabel,
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

	var result []restartPendingBead
	for _, issue := range all {
		if sliceContains(issue.Labels, restartPendingEscalatedLabel) {
			continue
		}
		result = append(result, restartPendingBead{
			ID:          issue.ID,
			Title:       issue.Title,
			Description: issue.Description,
		})
	}
	return result, nil
}

// markRestartPendingEscalated adds the escalated label to a pending bead so it
// is not re-escalated on the next tick. The bead stays OPEN — the agent closes
// it after performing the restart.
func (d *Daemon) markRestartPendingEscalated(beadID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.bdPath, //nolint:gosec // G204: args constructed internally
		"label", "add", beadID, restartPendingEscalatedLabel,
	)
	cmd.Dir = d.config.TownRoot
	cmd.Env = os.Environ()
	setSysProcAttr(cmd)

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bd label add: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
