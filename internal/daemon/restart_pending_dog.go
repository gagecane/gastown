package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/version"
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

	// Pre-compute the forward-only ancestry verdict ONCE per tick (gu-8ni5o):
	// the running daemon's commit vs the freshly-fetched repo tip. The friction
	// was that the responder had to fetch the bare repo + run
	// `git merge-base --is-ancestor` by hand for every restart-pending — and on
	// one occasion the new commit wasn't even fetched locally yet, so the check
	// failed until they fetched. We fetch + compute here so the escalation
	// carries the answer. All pending beads in a tick share one daemon binary
	// and one repo, so the verdict is identical across them.
	forwardCheck := d.computeRestartForwardCheck()

	for _, b := range pending {
		msg := d.buildRestartEscalationMessage(b, forwardCheck)
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
// daemon's uptime/stale state, and the pre-computed forward-only ancestry
// verdict (fwd) so the responder need not fetch the repo and run
// `git merge-base --is-ancestor` by hand (gu-8ni5o).
func (d *Daemon) buildRestartEscalationMessage(b restartPendingBead, fwd restartForwardCheck) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Daemon restart pending — agent action required (NOT auto-restart).\n\n")
	fmt.Fprintf(&sb, "Pending bead: %s — %s\n", b.ID, b.Title)
	if b.Description != "" {
		fmt.Fprintf(&sb, "\n%s\n", b.Description)
	}
	fmt.Fprintf(&sb, "\nThe on-disk gt binary has been upgraded but the running daemon is still\n")
	fmt.Fprintf(&sb, "executing its OLD in-memory image. Daemon-resident fixes (dispatch,\n")
	fmt.Fprintf(&sb, "patrols, main_branch_test, etc.) will NOT deploy until the daemon restarts.\n")
	fmt.Fprintf(&sb, "\nFORWARD-ONLY CHECK (pre-computed — fetched the repo, ran the ancestry check):\n")
	fmt.Fprintf(&sb, "%s", fwd.render())
	fmt.Fprintf(&sb, "\nRECOMMENDED ACTION (apply the safety gate, then restart):\n")
	fmt.Fprintf(&sb, "  1. Confirm in-flight work is safe to interrupt (scheduler reservations clear).\n")
	fmt.Fprintf(&sb, "  2. Forward-only check: see the pre-computed verdict above.\n")
	fmt.Fprintf(&sb, "  3. gt daemon stop && gt daemon start\n")
	fmt.Fprintf(&sb, "  4. Verify the running daemon advanced, then close bead %s.\n", b.ID)
	return sb.String()
}

// restartForwardCheck is the pre-computed forward-only ancestry verdict for a
// pending daemon restart: is the new on-disk binary's commit a descendant of
// the commit the running daemon was built from? "Forward" means the restart
// advances to newer code (safe); "not forward" means the new binary is at an
// older or diverged commit — restarting would DOWNGRADE or risk a crash loop,
// the failure mode the forward-only gate exists to prevent.
type restartForwardCheck struct {
	// Computed is false when the verdict could not be determined (dev build with
	// no embedded commit, repo not locatable, fetch/ancestry error). The
	// responder then falls back to the manual check.
	Computed bool
	// Forward is the verdict: true when the repo tip is a descendant of the
	// running daemon's commit (or already equal — nothing to advance to).
	Forward bool
	// RunningCommit is the commit the running daemon was built from.
	RunningCommit string
	// RepoCommit is the commit at the compared build-branch ref after fetch.
	RepoCommit string
	// CompareRef is the ref the running commit was compared against.
	CompareRef string
	// Detail is a human-readable explanation when Computed is false, or extra
	// context (e.g. "already up to date") when it is true.
	Detail string
}

// render formats the forward-check verdict for the escalation body.
func (f restartForwardCheck) render() string {
	if !f.Computed {
		return fmt.Sprintf("  verdict:  UNKNOWN — could not pre-compute (%s).\n"+
			"            Fall back: fetch the repo, then\n"+
			"            git merge-base --is-ancestor <running> <new> in the gt source.\n", f.Detail)
	}
	var sb strings.Builder
	if f.Forward {
		fmt.Fprintf(&sb, "  verdict:  FORWARD-ONLY ✓ — new commit is a descendant of the running one; safe to restart.\n")
	} else {
		fmt.Fprintf(&sb, "  verdict:  NOT FORWARD-ONLY ✗ — new commit is NOT a descendant of the running one.\n")
		fmt.Fprintf(&sb, "            Restarting may DOWNGRADE or diverge — investigate before restarting.\n")
	}
	fmt.Fprintf(&sb, "  running:  %s\n", version.ShortCommit(f.RunningCommit))
	fmt.Fprintf(&sb, "  new (%s): %s\n", f.CompareRef, version.ShortCommit(f.RepoCommit))
	if f.Detail != "" {
		fmt.Fprintf(&sb, "  note:     %s\n", f.Detail)
	}
	return sb.String()
}

// computeRestartForwardCheck fetches the gt source repo and pre-computes the
// forward-only ancestry verdict for the running daemon (gu-8ni5o). It fetches
// first so the freshly-built commit is local before the ancestry check — the
// observed failure where the responder's check failed because the new commit
// wasn't fetched yet.
//
// The running daemon process IS the OLD binary, so version.ResolveBinaryCommit
// returns the running commit and version.CheckStaleBinary compares it against
// the repo's build-branch tip — exactly the forward-only verdict the responder
// needs. Every path here is best-effort: any failure yields a not-Computed
// result and the escalation tells the responder to fall back to the manual check.
func (d *Daemon) computeRestartForwardCheck() restartForwardCheck {
	repoRoot, err := version.GetRepoRoot()
	if err != nil || repoRoot == "" {
		return restartForwardCheck{Detail: "could not locate gt source repo"}
	}

	// Fetch so the new commit is local before the ancestry check. A fetch
	// failure is non-fatal — CheckStaleBinary still runs against whatever is
	// already local, and the verdict notes the fetch was best-effort.
	fetchDetail := ""
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, "git", "fetch", "--quiet", "origin") //nolint:gosec // G204: static args
	cmd.Dir = repoRoot
	cmd.Env = gitChildEnv()
	setSysProcAttr(cmd)
	if out, ferr := cmd.CombinedOutput(); ferr != nil {
		fetchDetail = fmt.Sprintf("fetch failed (%v: %s); checked against already-local refs", ferr, strings.TrimSpace(string(out)))
	}
	cancel()

	info := version.CheckStaleBinary(repoRoot)
	if info.Error != nil {
		return restartForwardCheck{Detail: fmt.Sprintf("staleness check error: %v", info.Error)}
	}
	if info.Skipped {
		return restartForwardCheck{Detail: info.SkipReason}
	}

	fc := restartForwardCheck{
		Computed:      true,
		RunningCommit: info.BinaryCommit,
		RepoCommit:    info.RepoCommit,
		CompareRef:    info.CompareRef,
		Detail:        fetchDetail,
	}
	if !info.IsStale {
		// Repo tip equals the running commit — there is nothing newer to advance
		// to. Treat as forward (a no-op restart is always safe), and say so.
		fc.Forward = true
		if fc.Detail == "" {
			fc.Detail = "running daemon is already at the repo tip (no newer commit to advance to)"
		}
		return fc
	}
	fc.Forward = info.IsForward
	return fc
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
