package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// scheduler_stuck_dog watches for the "classic stall" scheduler signature and
// ESCALATES it to an agent — closing the gap from gu-n0hvf where a wedged
// scheduler (ready work + free capacity, but nothing dispatching/draining) was
// only ever surfaced by a one-line warning on a manual `gt scheduler run`, or
// by a human noticing. No monitor watched the PATTERN.
//
// Detected signature (the "15 ready, 0 in-progress, 44 free" shape that
// recurred all of gu-n0hvf night): the scheduler is NOT paused, there is ready
// work queued, NO polecats are working, and there is free pool capacity — yet
// the queue is not draining. A single tick of this is normal back-pressure
// (a dispatch wave just landed); it only escalates once the signature has held
// continuously past schedulerStuckEscalateAge, separating a transient gap from
// a genuine wedge.
//
// Design decision (gu-n0hvf, following the gu-muj66 precedent): escalate, do
// NOT auto-remediate. Auto-restarting the scheduler / nuking polecats from a
// daemon risks the restart-loop class of failure (worse than the manual gap).
// Instead the dog hands an agent the state snapshot (queue depth, capacity
// breakdown, last-dispatch age) so the agent diagnoses and acts with judgment.
//
//	gt scheduler dispatch loop
//	      │  (wedges: ready>0, working==0, free>0, not draining)
//	      ▼
//	scheduler_stuck_dog (this patrol)        ← detector (NEW, gu-n0hvf)
//	      │  escalates to agent w/ state once sustained past threshold
//	      ▼
//	agent diagnoses + acts (no auto-remediate)
//
// SCOPE NOTE (gu-n0hvf): this dog covers the non-draining-ready-queue /
// 0-in-progress-with-capacity signature. The inverse signature — a
// recovery_blocked majority draining the pool — is already covered by the
// doctor `scheduler-capacity-drain` check. The "repeated circuit-breaks on the
// same bead/context" signature is deferred: circuit-break state lives
// per-sling-context (DispatchFailures), not aggregated in scheduler status, so
// it needs a different data source (tracked for the Curio fold-in, gc-tt4p9).

const (
	defaultSchedulerStuckInterval = 5 * time.Minute
	schedulerStuckSource          = "scheduler_stuck_dog"

	// schedulerStuckEscalateAge is how long the stall signature must hold
	// continuously before the dog escalates. The dispatch loop runs every
	// heartbeat (~3 min), so a genuine dispatch should place ready work within
	// a cycle or two; 10 min (~3 dispatch cycles) clears that bar while still
	// catching a real wedge promptly. Mirrors the doctor drain check's
	// transient-vs-sustained split (gu-01ef).
	schedulerStuckEscalateAge = 10 * time.Minute
)

// SchedulerStuckConfig holds configuration for the scheduler_stuck patrol.
type SchedulerStuckConfig struct {
	// Enabled controls whether the scheduler-stuck monitor runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "5m").
	IntervalStr string `json:"interval,omitempty"`
}

// schedulerStuckInterval returns the configured interval, or the default (5m).
func schedulerStuckInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.SchedulerStuck != nil {
		if config.Patrols.SchedulerStuck.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.SchedulerStuck.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultSchedulerStuckInterval
}

// schedulerStuckSnapshot is the subset of `gt scheduler status --json` the dog
// reasons about. Kept narrow so unrelated schema additions don't break parsing.
type schedulerStuckSnapshot struct {
	Paused         bool   `json:"paused"`
	QueuedTotal    int    `json:"queued_total"`
	QueuedReady    int    `json:"queued_ready"`
	ActivePolecats int    `json:"active_polecats"`
	LastDispatchAt string `json:"last_dispatch_at,omitempty"`
	Capacity       struct {
		Max             int `json:"max"`
		Working         int `json:"working"`
		RecoveryBlocked int `json:"recovery_blocked"`
		ReusableIdle    int `json:"reusable_idle"`
		PendingMR       int `json:"pending_mr"`
		Reservations    int `json:"reservations"`
		Free            int `json:"free"`
	} `json:"capacity"`
}

// isStalled reports whether the snapshot matches the non-draining-ready-queue /
// 0-in-progress-with-capacity stall signature: not paused, ready work queued,
// nothing working, and free pool capacity available. Requires pool mode
// (max > 0) — direct-dispatch mode (max <= 0) has no pool to wedge.
func (s schedulerStuckSnapshot) isStalled() bool {
	if s.Paused {
		return false
	}
	if s.Capacity.Max <= 0 {
		return false
	}
	return s.QueuedReady > 0 && s.Capacity.Working == 0 && s.Capacity.Free > 0
}

// schedulerStuckStateFile is the persisted "stall started at" marker. Lives
// under .runtime so it's wiped with other ephemeral state.
func schedulerStuckStateFile(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "daemon", "scheduler-stuck.json")
}

// schedulerStuckState persists when the current stall episode was first
// detected and whether it has already been escalated, so a transient gap does
// not escalate and a single episode escalates at most once.
type schedulerStuckState struct {
	FirstDetectedAt time.Time `json:"first_detected_at"`
	Escalated       bool      `json:"escalated"`
}

// runSchedulerStuckDog is the main patrol function. It reads scheduler status,
// detects the stall signature, and (once the stall has held past the escalate
// threshold) escalates to an agent with the state snapshot. Healthy snapshots
// clear the persisted marker so the check auto-recovers and re-arms.
func (d *Daemon) runSchedulerStuckDog() {
	if !d.isPatrolActive("scheduler_stuck") {
		return
	}

	// Gate on the shared Dolt circuit breaker: `gt scheduler status` queries
	// scheduled beads (Dolt). When Dolt is degraded, skip and resume next tick.
	if d.doltBreaker != nil && !d.doltBreaker.Allow() {
		d.logger.Printf("scheduler_stuck: dolt-degraded — skipping tick (circuit breaker open)")
		return
	}

	snap, err := d.readSchedulerStatus()
	if d.doltBreaker != nil {
		d.doltBreaker.Record(err)
	}
	if err != nil {
		d.logger.Printf("scheduler_stuck: failed to read scheduler status: %v", err)
		return
	}

	stateFile := schedulerStuckStateFile(d.config.TownRoot)

	if !snap.isStalled() {
		// Healthy (or paused, or direct-dispatch): clear any episode marker so
		// the next stall starts the timer fresh. Best-effort — a stale marker
		// is harmless until the next stalled snapshot overwrites it.
		if _, statErr := os.Stat(stateFile); statErr == nil {
			_ = os.Remove(stateFile)
		}
		return
	}

	now := time.Now().UTC()
	state, loadErr := loadSchedulerStuckState(stateFile)
	if loadErr != nil || state.FirstDetectedAt.IsZero() {
		state = schedulerStuckState{FirstDetectedAt: now}
	}

	age := now.Sub(state.FirstDetectedAt)
	if age < schedulerStuckEscalateAge {
		// Stall forming but not yet sustained — record the first-detected time
		// and wait. A write failure just re-arms the timer next tick.
		_ = saveSchedulerStuckState(stateFile, state)
		d.logger.Printf("scheduler_stuck: stall signature forming (ready=%d working=%d free=%d, age=%s) — watching",
			snap.QueuedReady, snap.Capacity.Working, snap.Capacity.Free, age.Round(time.Second))
		return
	}

	if state.Escalated {
		// Already escalated this episode; d.escalate dedups anyway, but skip the
		// repeat work and just log that the wedge persists.
		d.logger.Printf("scheduler_stuck: stall persists (ready=%d working=%d free=%d, age=%s) — already escalated",
			snap.QueuedReady, snap.Capacity.Working, snap.Capacity.Free, age.Round(time.Second))
		return
	}

	d.logger.Printf("scheduler_stuck: stall sustained %s (ready=%d working=%d free=%d) — escalating to agent",
		age.Round(time.Second), snap.QueuedReady, snap.Capacity.Working, snap.Capacity.Free)
	d.escalate(schedulerStuckSource, d.buildSchedulerStuckMessage(snap, age))

	state.Escalated = true
	if err := saveSchedulerStuckState(stateFile, state); err != nil {
		d.logger.Printf("scheduler_stuck: escalated but failed to mark episode handled: %v", err)
	}
}

// buildSchedulerStuckMessage assembles the multi-line escalation body with the
// state an agent needs to diagnose the wedge: queue depth, the full capacity
// breakdown, and how long the stall has held / how stale the last dispatch is.
func (d *Daemon) buildSchedulerStuckMessage(s schedulerStuckSnapshot, age time.Duration) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Scheduler stuck — agent diagnosis required (NOT auto-remediated).\n\n")
	fmt.Fprintf(&sb, "The scheduler has ready work and free capacity but is not draining:\n")
	fmt.Fprintf(&sb, "ready=%d queued, working=%d, free=%d of max=%d — sustained for %s.\n",
		s.QueuedReady, s.Capacity.Working, s.Capacity.Free, s.Capacity.Max, age.Round(time.Second))
	fmt.Fprintf(&sb, "\nCapacity snapshot:\n")
	fmt.Fprintf(&sb, "  queued: %d total, %d ready\n", s.QueuedTotal, s.QueuedReady)
	fmt.Fprintf(&sb, "  working: %d, recovery_blocked: %d, reusable_idle: %d, reservations: %d, pending_mr: %d, free: %d\n",
		s.Capacity.Working, s.Capacity.RecoveryBlocked, s.Capacity.ReusableIdle,
		s.Capacity.Reservations, s.Capacity.PendingMR, s.Capacity.Free)
	fmt.Fprintf(&sb, "  active_polecats: %d\n", s.ActivePolecats)
	if s.LastDispatchAt != "" {
		lastDispatch := s.LastDispatchAt
		if t, err := time.Parse(time.RFC3339, s.LastDispatchAt); err == nil {
			lastDispatch = fmt.Sprintf("%s (%s ago)", s.LastDispatchAt, time.Since(t).Round(time.Second))
		}
		fmt.Fprintf(&sb, "  last_dispatch: %s\n", lastDispatch)
	} else {
		fmt.Fprintf(&sb, "  last_dispatch: never\n")
	}
	fmt.Fprintf(&sb, "\nRECOMMENDED ACTION (diagnose, then act with judgment — do NOT blind-restart):\n")
	fmt.Fprintf(&sb, "  1. gt scheduler status --json — confirm the wedge is current.\n")
	fmt.Fprintf(&sb, "  2. gt polecat list --all --json — find orphaned/dead-PID admission\n")
	fmt.Fprintf(&sb, "     reservations or stalled-clean polecats leaking capacity.\n")
	fmt.Fprintf(&sb, "  3. Check for a parked sling-context (dispatch_failure) or a context\n")
	fmt.Fprintf(&sb, "     circuit-breaking every run (deterministic failure).\n")
	fmt.Fprintf(&sb, "  4. gt scheduler run --dry-run — see why ready work is not placing.\n")
	return sb.String()
}

// readSchedulerStatus shells out to `gt scheduler status --json` and parses the
// stall-relevant fields. Mirrors restart_pending_dog's subprocess pattern.
func (d *Daemon) readSchedulerStatus() (schedulerStuckSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.gtPath, //nolint:gosec // G204: args constructed internally
		"scheduler", "status", "--json",
	)
	cmd.Dir = d.config.TownRoot
	cmd.Env = os.Environ()
	setSysProcAttr(cmd)

	out, err := cmd.Output()
	if err != nil {
		return schedulerStuckSnapshot{}, fmt.Errorf("gt scheduler status --json: %w", err)
	}

	var snap schedulerStuckSnapshot
	if err := json.Unmarshal(out, &snap); err != nil {
		return schedulerStuckSnapshot{}, fmt.Errorf("parsing scheduler status JSON: %w", err)
	}
	return snap, nil
}

func loadSchedulerStuckState(path string) (schedulerStuckState, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path derived from trusted townRoot
	if err != nil {
		return schedulerStuckState{}, err
	}
	var s schedulerStuckState
	if err := json.Unmarshal(data, &s); err != nil {
		return schedulerStuckState{}, err
	}
	return s, nil
}

func saveSchedulerStuckState(path string, state schedulerStuckState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating daemon state dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling scheduler-stuck state: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}
