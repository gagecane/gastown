package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
	"github.com/steveyegge/gastown/internal/sling"
	"github.com/steveyegge/gastown/internal/style"
)

// crossRigEscalationDebounce is the minimum interval between cross-rig prefix
// escalations for the same (rig, prefix) pair. Prevents alert spam when a
// stuck context keeps re-appearing on every dispatch tick.
const crossRigEscalationDebounce = time.Hour

// crossRigEscalationState tracks last-escalation timestamps per (rig, prefix).
// Process-local — debounce resets on daemon restart, which is fine: a new
// process should be allowed to surface the issue once.
var (
	crossRigEscalationMu   sync.Mutex
	crossRigEscalationLast = map[string]time.Time{}
)

// crossRigEscalationKey returns the debounce key for a (rig, prefix) pair.
func crossRigEscalationKey(rig, prefix string) string {
	return rig + "/" + prefix
}

// shouldFireCrossRigEscalation reports whether enough time has elapsed since
// the last escalation for this (rig, prefix) pair to fire a new one. Updates
// the timestamp on a positive answer.
func shouldFireCrossRigEscalation(rig, prefix string, now time.Time) bool {
	crossRigEscalationMu.Lock()
	defer crossRigEscalationMu.Unlock()
	key := crossRigEscalationKey(rig, prefix)
	if last, ok := crossRigEscalationLast[key]; ok && now.Sub(last) < crossRigEscalationDebounce {
		return false
	}
	crossRigEscalationLast[key] = now
	return true
}

// resetCrossRigEscalationStateForTest clears the debounce map. Test-only.
func resetCrossRigEscalationStateForTest() {
	crossRigEscalationMu.Lock()
	defer crossRigEscalationMu.Unlock()
	crossRigEscalationLast = map[string]time.Time{}
}

// fireCrossRigEscalation invokes `gt escalate` with a MEDIUM severity. Best
// effort — escalation failure is logged but does not block the dispatch path.
var fireCrossRigEscalation = func(rig, prefix, beadID string) {
	msg := fmt.Sprintf("cross-rig dispatch refused: rig=%s prefix=%s bead=%s — see gt-el4", rig, prefix, beadID)
	cmd := exec.Command("gt", "escalate", "--severity", "medium", "--reason", "cross-rig-prefix", msg)
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s cross-rig escalation failed: %v\n", style.Warning.Render("⚠"), err)
	}
}

// maxDispatchFailures is the maximum number of consecutive dispatch failures
// before a sling context is closed as circuit-broken.
const maxDispatchFailures = 3

// daemonDispatchBudget bounds how long the daemon-invoked dispatch loop may keep
// launching new spawns. The daemon runs `gt scheduler run` under a hard 5m
// SIGKILL (daemon.dispatchQueuedWork); this budget leaves headroom so the loop
// stops launching new spawns and returns cleanly — durably closing the contexts
// it already dispatched — well before the SIGKILL would kill the process with
// zero forward progress (gu-t6jqq). Only applied under GT_DAEMON=1; the
// interactive `gt scheduler run` path stays unbounded.
const daemonDispatchBudget = 4*time.Minute + 30*time.Second

// --- Capacity-exhaustion monitor (hq-ly5yj) --------------------------------
//
// When the pool can't place ANY ready work — every slot recovery_blocked, with
// working+reusable_idle==0 — it is only LOGGED ("zero capacity") every cycle and
// nothing escalates it. stuck-agent-dog inspects live tmux sessions, so it is
// structurally blind to sessionless recovery_blocked slots; a real outage sat
// ~9.5h silently (hq-uzubf), masked only because persistent crews kept flowing.
// This monitor counts CONSECUTIVE dispatch cycles where the pool is exhausted
// while ready beads are being skipped, and escalates HIGH once sustained. A
// single blip is normal (a slot frees next cycle); sustained-with-skips is the
// outage signature.

// capacityExhaustionThreshold is the number of consecutive exhausted dispatch
// cycles before escalating. At the daemon's ~3-4min heartbeat this is ~10-15min
// of fully-dead pool — long enough to ignore transient blips, short enough to
// catch a real outage early instead of hours later.
const capacityExhaustionThreshold = 3

// capacityExhaustionState persists across `gt scheduler run` invocations (each
// is a separate process) in <town>/.runtime/capacity-exhaustion.json.
type capacityExhaustionState struct {
	Consecutive int    `json:"consecutive"`
	FirstSeen   string `json:"first_seen,omitempty"`
	Escalated   bool   `json:"escalated"`
}

// evaluateCapacityExhaustion is the pure state machine: given the prior state,
// whether this cycle is exhausted, and a timestamp for a fresh episode, it
// returns the next state and whether THIS cycle should fire an escalation
// (true only on the cycle that first crosses the threshold within an episode).
func evaluateCapacityExhaustion(prev capacityExhaustionState, exhausted bool, now string) (capacityExhaustionState, bool) {
	if !exhausted {
		return capacityExhaustionState{}, false // recovered → re-arm
	}
	next := prev
	next.Consecutive++
	if next.FirstSeen == "" {
		next.FirstSeen = now
	}
	escalate := next.Consecutive >= capacityExhaustionThreshold && !next.Escalated
	if escalate {
		next.Escalated = true
	}
	return next, escalate
}

func capacityExhaustionStatePath(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "capacity-exhaustion.json")
}

func loadCapacityExhaustionState(path string) capacityExhaustionState {
	var st capacityExhaustionState
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed internally
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, &st)
	return st
}

func saveCapacityExhaustionState(path string, st capacityExhaustionState) {
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	if data, err := json.Marshal(st); err == nil {
		_ = os.WriteFile(path, data, 0644)
	}
}

// poolDegraded reports whether the pool effectively cannot keep up with ready
// work. Two shapes count (hq-q943s):
//   - hard zero: no working and no reusable_idle slots — nothing can take work;
//   - chronic degradation: a strict MAJORITY of slots wedged in recovery_blocked.
//     The original alarm only tripped on hard-zero, so a trickle dispatch (one
//     bead placed per cycle keeping working at 0-2) intermittently broke the
//     working+reusable_idle==0 condition and reset the counter — the alarm never
//     fired through ~hours of recovery_blocked 5-8/8. Tripping on a sustained
//     recovery_blocked majority catches that degraded-throughput starvation too.
func poolDegraded(s polecatCapacitySnapshot) bool {
	if s.Working+s.ReusableIdle == 0 {
		return true
	}
	return s.Max > 0 && s.RecoveryBlocked > s.Max/2
}

// monitorCapacityExhaustion advances the consecutive-exhaustion counter for this
// dispatch cycle and escalates HIGH when the pool has been unable to place ready
// work for capacityExhaustionThreshold consecutive cycles. It is called EVERY
// cycle (not only when everything is skipped) so a trickle dispatch — one bead
// placed while dozens are skipped and the pool stays wedged — keeps the counter
// climbing instead of resetting it. The counter only resets when ready work is
// actually flowing (nothing skipped, or the pool is no longer degraded).
// Best-effort: state and escalation failures never block dispatch.
func monitorCapacityExhaustion(townRoot string, snapshot polecatCapacitySnapshot, skipped int) {
	exhausted := skipped > 0 && poolDegraded(snapshot)
	path := capacityExhaustionStatePath(townRoot)
	next, escalate := evaluateCapacityExhaustion(loadCapacityExhaustionState(path), exhausted, time.Now().UTC().Format(time.RFC3339))
	if escalate {
		fireCapacityExhaustionEscalation(snapshot, skipped, next)
	}
	saveCapacityExhaustionState(path, next)
}

// resetCapacityExhaustion clears the counter after a successful dispatch so a
// later episode re-arms and re-escalates.
func resetCapacityExhaustion(townRoot string) {
	saveCapacityExhaustionState(capacityExhaustionStatePath(townRoot), capacityExhaustionState{})
}

// fireCapacityExhaustionEscalation raises a HIGH escalation to the Mayor with the
// capacity snapshot. The fingerprint lets `gt escalate`'s close-aware dedup
// (gu-ah40) suppress repeats within an open episode. Overridable in tests.
var fireCapacityExhaustionEscalation = func(snapshot polecatCapacitySnapshot, skipped int, st capacityExhaustionState) {
	msg := fmt.Sprintf("Pool capacity exhausted: %d ready bead(s) skipped, pool unable to place work for %d consecutive cycles (since %s)",
		skipped, st.Consecutive, st.FirstSeen)
	reason := fmt.Sprintf("working=%d recovery_blocked=%d reusable_idle=%d pending_mr=%d reservations=%d max=%d — pool degraded (hard-zero dispatchable slots, or a recovery_blocked majority) while ready work queues; includes trickle starvation where a bead dispatches occasionally but throughput can't keep up. Likely recovery_blocked debris (hq-uzubf). Inspect: gt scheduler status --json; gt polecat list --all --json.",
		snapshot.Working, snapshot.RecoveryBlocked, snapshot.ReusableIdle, snapshot.PendingMR, snapshot.Reservations, snapshot.Max)
	cmd := exec.Command("gt", "escalate", "--severity", "high",
		"--source", "scheduler:capacity",
		"--fingerprint", "pool:capacity-exhaustion",
		"--reason", reason, msg)
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s capacity-exhaustion escalation failed: %v\n", style.Warning.Render("⚠"), err)
	}
}

// fireDispatchCloseEscalation raises a HIGH escalation to the Mayor when the
// last-resort sling-context close fails after a successful dispatch (gu-i0oaq).
// The polecat has already launched but the context could not be marked closed
// — usually because the wisp bead was TTL-reaped out from under the close
// ("issue not found"), indicating bead-DB degradation. Left unescalated, the
// next dispatch cycle could re-dispatch the same work. Fingerprinted per
// work-bead so gt escalate's close-aware dedup suppresses repeats for the same
// stranded context. Overridable in tests.
var fireDispatchCloseEscalation = func(workBeadID, contextID, rig string, closeErr error) {
	args := dispatchCloseEscalationArgs(workBeadID, contextID, rig, closeErr)
	if err := exec.Command(args[0], args[1:]...).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s dispatch-close escalation failed: %v\n", style.Warning.Render("⚠"), err)
	}
}

// dispatchCloseEscalationArgs builds the `gt escalate` command line for a
// stranded-context double-dispatch risk. Pure so the severity, source, and
// per-work-bead fingerprint can be asserted without shelling out.
func dispatchCloseEscalationArgs(workBeadID, contextID, rig string, closeErr error) []string {
	msg := fmt.Sprintf("Dispatch of %s succeeded but context %s could not be closed: %v — risk of double-dispatch on next cycle", workBeadID, contextID, closeErr)
	reason := fmt.Sprintf("rig=%s work_bead=%s context=%s: last-resort CloseSlingContext failed after OnSuccess retries. Commonly wisp TTL-reaping racing the dispatch close (bead-DB degradation). The polecat launched but the sling context is still open, so the next scheduler cycle may re-dispatch %s. Inspect: gt scheduler status --json; bd show %s.",
		rig, workBeadID, contextID, workBeadID, contextID)
	return []string{"gt", "escalate", "--severity", "high",
		"--source", "scheduler:dispatch-close",
		"--fingerprint", fmt.Sprintf("dispatch-close:%s", workBeadID),
		"--reason", reason, msg}
}

// dispatchPhaseSlowThreshold mirrors the daemon heartbeat threshold: a single
// pre-dispatch maintenance pass over this duration is logged as slow even
// without GT_HEARTBEAT_PROFILE. The whole `gt scheduler run` runs under a 5m
// daemon budget (gu-t6jqq); a pass eating multiple seconds while placing zero
// work is exactly what blew that budget invisibly (gu-pjrz3), so surface it.
const dispatchPhaseSlowThreshold = 5 * time.Second

// dispatchPhase times one of dispatchScheduledWork's pre-dispatch maintenance
// passes (cleanupStaleContexts, releaseExpiredDeferredBeads, recoverZombie-
// Molecules, reconcileOrphanMolecules) — each a per-rig bd fan-out that runs
// before any bead is placed. These were opaque: the dispatch could burn its
// whole 5m budget here with no per-pass visibility. Logs to stderr (so it lands
// in daemon.log when the daemon shells `gt scheduler run`) when a pass exceeds
// dispatchPhaseSlowThreshold, or for every pass under GT_HEARTBEAT_PROFILE.
func dispatchPhase(name string, fn func()) {
	start := time.Now()
	fn()
	elapsed := time.Since(start)
	if elapsed >= dispatchPhaseSlowThreshold {
		fmt.Fprintf(os.Stderr, "%s dispatch maintenance %q SLOW: %s\n",
			style.Warning.Render("⚠"), name, elapsed.Round(time.Millisecond))
	} else if os.Getenv("GT_HEARTBEAT_PROFILE") != "" {
		fmt.Fprintf(os.Stderr, "%s dispatch maintenance %q: %s\n",
			style.Dim.Render("○"), name, elapsed.Round(time.Millisecond))
	}
}

// dispatchMaintenanceInterval bounds how often the pre-dispatch maintenance
// passes (cleanupStaleContexts, releaseExpiredDeferredBeads, recoverZombie-
// Molecules, reconcileOrphanMolecules) run — at most once per interval, rather
// than every dispatch tick (gu-pjrz3 option-b). 5 min is well under the slowest
// staleness window these passes service (stale-context TTL, defer-release, wisp
// reap) so nothing it cleans up lingers materially longer, while the dispatch
// tick itself — the latency-critical "place ready work" path — runs free of the
// per-rig fan-out cost on every other tick. Tunable via GT_DISPATCH_MAINT_INTERVAL.
const dispatchMaintenanceInterval = 5 * time.Minute

// dispatchMaintenanceDue reports whether the pre-dispatch maintenance passes are
// due (>= dispatchMaintenanceInterval since the last run) and, if so, stamps the
// run time. The stamp lives in <town>/.runtime/dispatch-maintenance.stamp.
// Fail-open: if the stamp can't be read (first run, unreadable), maintenance
// runs — we never SKIP maintenance due to a stamp error, only defer it when we
// can prove it ran recently. This makes the common dispatch tick skip the
// 4-pass per-rig fan-out that was blowing the 5m budget (gu-pjrz3).
func dispatchMaintenanceDue(townRoot string) bool {
	interval := dispatchMaintenanceInterval
	if v := os.Getenv("GT_DISPATCH_MAINT_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			interval = d
		}
	}
	now := timeNowForDispatchMaint()
	stamp := filepath.Join(townRoot, ".runtime", "dispatch-maintenance.stamp")
	if data, err := os.ReadFile(stamp); err == nil {
		if last, perr := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data))); perr == nil {
			if now.Sub(last) < interval {
				return false // ran recently — skip the passes this tick
			}
		}
	}
	// Due (or stamp unreadable → fail-open). Stamp now so concurrent/next ticks
	// defer. Best-effort: a write failure just means the next tick also runs them.
	_ = os.MkdirAll(filepath.Dir(stamp), 0755)
	_ = os.WriteFile(stamp, []byte(now.Format(time.RFC3339Nano)), 0644)
	return true
}

// timeNowForDispatchMaint is a seam for tests to control the maintenance clock.
var timeNowForDispatchMaint = time.Now

// dispatchScheduledWork is the main dispatch loop for the capacity scheduler.
// Called by both `gt scheduler run` and the daemon heartbeat.
func dispatchScheduledWork(townRoot, actor string, batchOverride int, dryRun bool) (int, error) {
	// Stamp the dispatch start so the daemon deadline budget covers the whole
	// invocation (maintenance scans + query + spawns), all of which run under
	// the daemon's single 5m SIGKILL (gu-t6jqq).
	dispatchStart := time.Now()

	// Acquire exclusive lock to prevent concurrent dispatch
	runtimeDir := filepath.Join(townRoot, ".runtime")
	_ = os.MkdirAll(runtimeDir, 0755)
	lockFile := filepath.Join(runtimeDir, "scheduler-dispatch.lock")
	fileLock := flock.New(lockFile)
	locked, err := fileLock.TryLock()
	if err != nil {
		return 0, fmt.Errorf("acquiring dispatch lock: %w", err)
	}
	if !locked {
		return 0, nil
	}
	defer func() { _ = fileLock.Unlock() }()

	// Load scheduler state
	state, err := capacity.LoadState(townRoot)
	if err != nil {
		return 0, fmt.Errorf("loading scheduler state: %w", err)
	}

	if state.Paused {
		if !dryRun {
			fmt.Printf("%s Scheduler is paused (by %s), skipping dispatch\n", style.Dim.Render("⏸"), state.PausedBy)
		}
		return 0, nil
	}

	// Load town settings for scheduler config
	settingsPath := config.TownSettingsPath(townRoot)
	settings, err := config.LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		return 0, fmt.Errorf("loading town settings: %w", err)
	}

	schedulerCfg := settings.Scheduler
	if schedulerCfg == nil {
		schedulerCfg = capacity.DefaultSchedulerConfig()
	}

	// Nothing to dispatch when scheduler is in direct dispatch or disabled mode.
	maxPolecats := schedulerCfg.GetMaxPolecats()
	if maxPolecats <= 0 {
		if !dryRun && !isDaemonDispatch() {
			staleBeads, _ := getReadySlingContexts(townRoot)
			if len(staleBeads) > 0 {
				fmt.Printf("%s %d context bead(s) still open from a previous deferred mode\n",
					style.Warning.Render("⚠"), len(staleBeads))
				fmt.Printf("  Use: gt scheduler clear  (close all sling context beads)\n")
				fmt.Printf("  Or:  gt config set scheduler.max_polecats N  (re-enable deferred dispatch)\n")
			}
		}
		return 0, nil
	}

	// Determine limits
	batchSize := schedulerCfg.GetBatchSize()
	if batchOverride > 0 {
		batchSize = batchOverride
	}
	spawnDelay := schedulerCfg.GetSpawnDelay()

	// Pre-dispatch maintenance passes (gu-pjrz3 option-b): these four per-rig bd
	// fan-outs (stale-context cleanup, deferred-release, zombie-molecule recovery,
	// orphan-wisp reconcile) are CLEANUP/reconcile, not dispatch-critical — yet
	// they ran on EVERY dispatch tick, BEFORE any bead was placed. Under bd-read
	// throttle + Dolt load, four stacked serial 18-dir fan-outs blew the 5m daemon
	// dispatch budget (gu-t6jqq) and SIGKILLed dispatch before it placed anything —
	// a town-wide dispatch outage where ready work never spawned despite free
	// capacity. Decouple them from the hot path: run them at most once per
	// dispatchMaintenanceInterval (timestamped in .runtime), so the COMMON dispatch
	// tick skips straight to find-ready-contexts + place and stays sub-second.
	// Maintenance still runs (correctness preserved — a stale context lingers at
	// most one interval longer), just not blocking every placement. Skipped in
	// dry-run (never mutates).
	if !dryRun && dispatchMaintenanceDue(townRoot) {
		dispatchPhase("cleanupStaleContexts", func() { cleanupStaleContexts(townRoot) })

		dispatchPhase("releaseExpiredDeferredBeads", func() {
			if released := releaseExpiredDeferredBeads(townRoot); released > 0 {
				fmt.Printf("%s Auto-released %d bead(s) from defer\n", style.Dim.Render("○"), released)
			}
		})

		dispatchPhase("recoverZombieMolecules", func() {
			if recovered := recoverZombieMolecules(townRoot); recovered > 0 {
				fmt.Printf("%s Recovered %d work bead(s) from zombie-molecule wedge\n",
					style.Dim.Render("○"), recovered)
			}
		})

		dispatchPhase("reconcileOrphanMolecules", func() {
			if reconciled := reconcileOrphanMolecules(townRoot); reconciled > 0 {
				fmt.Printf("%s Reconciled %d orphaned molecule wisp(s)\n",
					style.Dim.Render("○"), reconciled)
			}
		})
	}

	// Wire up the DispatchCycle
	successfulRigs := make(map[string]bool)
	// Track polecat names from dispatch results, keyed by context bead ID.
	polecatNames := make(map[string]string)
	lastCapacitySnapshot := polecatCapacitySnapshot{Max: maxPolecats}
	cycle := &capacity.DispatchCycle{
		AvailableCapacity: func() (int, error) {
			snapshot, err := polecatCapacitySnapshotForTown(townRoot)
			if err != nil {
				return 0, err
			}
			lastCapacitySnapshot = snapshot
			if snapshot.Free <= 0 {
				return 0, nil // No free slots — PlanDispatch treats <= 0 as no capacity
			}
			return snapshot.Free, nil
		},
		QueryPending: func() ([]capacity.PendingBead, error) {
			pending, err := getReadySlingContexts(townRoot)
			if err != nil {
				return nil, err
			}
			// Per-rig cap filter (gu-1lvs): skip beads whose target rig is at
			// its configured polecat.max_concurrent limit. Preserves order so
			// older queued beads stay first; dispatched beads leaves slots for
			// other rigs on the next cycle.
			return filterByPerRigCapacity(townRoot, pending), nil
		},
		Validate: func(b capacity.PendingBead) error {
			return validatePendingBeadForDispatch(townRoot, b, true)
		},
		Execute: func(b capacity.PendingBead) error {
			result, err := dispatchSingleBead(b, townRoot, actor)
			if err != nil {
				return err
			}
			// Track side effects here (Execute runs exactly once, never retried).
			if result != nil && result.PolecatName != "" {
				polecatNames[b.ID] = result.PolecatName
			}
			if b.TargetRig != "" {
				successfulRigs[b.TargetRig] = true
			}
			_ = events.LogFeed(events.TypeSchedulerDispatch, actor,
				events.SchedulerDispatchPayload(b.WorkBeadID, b.TargetRig, polecatNames[b.ID]))
			return nil
		},
		OnSuccess: func(b capacity.PendingBead) error {
			// OnSuccess may be retried — only do the close here, no side effects.
			// Route to the correct rig's beads dir (GH#3468).
			return beadsForPendingContext(townRoot, b).CloseSlingContext(b.ID, "dispatched")
		},
		OnFailure: func(b capacity.PendingBead, err error) {
			var onSuccessErr *capacity.ErrOnSuccessFailed
			var admissionErr *polecatCapacityAdmissionError
			if errors.As(err, &onSuccessErr) {
				// Polecat launched but context close failed — not a true dispatch failure.
				// Log a distinct warning so operators can distinguish from "polecat never launched".
				fmt.Fprintf(os.Stderr, "%s Dispatch of %s succeeded but context close failed: %v\n",
					style.Warning.Render("⚠"), b.WorkBeadID, err)
				// Last-resort close attempt to prevent double-dispatch on next cycle.
				// OnSuccess already retried 2x; this is a final attempt before circuit-breaking.
				ctxBeads := beadsForPendingContext(townRoot, b)
				if closeErr := ctxBeads.CloseSlingContext(b.ID, "dispatch-close-failed"); closeErr != nil {
					fmt.Fprintf(os.Stderr, "%s CRITICAL: last-resort close of %s failed — risk of double-dispatch for %s: %v\n",
						style.Warning.Render("⚠"), b.ID, b.WorkBeadID, closeErr)
					// Feed event so dashboards detect the stranded-context / bead-DB degradation.
					_ = events.LogFeed(events.TypeSchedulerCloseFailed, actor,
						events.SchedulerDispatchFailedPayload(b.WorkBeadID, b.TargetRig, closeErr.Error()))
					// Escalate HIGH to the Mayor: the context is open and the work may re-dispatch.
					fireDispatchCloseEscalation(b.WorkBeadID, b.ID, b.TargetRig, closeErr)
					// Skip recordDispatchFailure: the context is in an unknown state and
					// the bead DB is degraded — writing to it is unreliable and the
					// escalation already routes this to a human/Mayor.
					return
				} else {
					// Last-resort close succeeded — context is now closed.
					// Log feed event so dashboards can detect bead DB degradation.
					_ = events.LogFeed(events.TypeSchedulerCloseRetry, actor,
						events.SchedulerDispatchPayload(b.WorkBeadID, b.TargetRig, polecatNames[b.ID]))
					// Skip recordDispatchFailure to avoid writing to a closed context.
					return
				}
			} else if errors.As(err, &admissionErr) {
				fmt.Fprintf(os.Stderr, "%s Capacity full while dispatching %s; leaving context queued: %v\n",
					style.Dim.Render("○"), b.WorkBeadID, err)
				return
			} else {
				// Surface the failure REASON to the operator, not just the
				// events feed (gu-lokoi). Without this an interactive
				// `gt scheduler run` prints "Dispatching X →" then a bare
				// "failed N" with no why — the reason was only ever written
				// to .events.jsonl. The other OnFailure branches above already
				// print to stderr; this generic spawn-failure case must too.
				fmt.Fprintf(os.Stderr, "%s Dispatch of %s → %s failed: %v\n",
					style.Warning.Render("⚠"), b.WorkBeadID, b.TargetRig, err)
				_ = events.LogFeed(events.TypeSchedulerDispatchFailed, actor,
					events.SchedulerDispatchFailedPayload(b.WorkBeadID, b.TargetRig, err.Error()))
			}
			recordDispatchFailure(townRoot, beadsForPendingContext(townRoot, b), b, err)
		},
		BatchSize:  batchSize,
		SpawnDelay: spawnDelay,
	}

	// Under the daemon, bound the execute loop so it stops launching new spawns
	// and returns cleanly before the daemon's hard 5m SIGKILL. Without this a
	// slow cycle (large queue → slow query + many sequential worktree spawns) is
	// killed mid-loop with zero durable progress, and the backlog never drains —
	// the gu-t6jqq death spiral. Interactive `gt scheduler run` stays unbounded.
	if isDaemonDispatch() {
		cycle.Deadline = dispatchStart.Add(daemonDispatchBudget)
	}

	if dryRun {
		plan, planErr := cycle.Plan()
		if planErr != nil {
			return 0, fmt.Errorf("planning dispatch: %w", planErr)
		}
		plan = validateDryRunDispatchPlan(townRoot, plan)
		printDryRunPlan(plan, lastCapacitySnapshot, batchSize)
		return 0, nil
	}

	report, err := cycle.Run()
	if err != nil {
		return 0, fmt.Errorf("dispatch cycle failed: %w", err)
	}

	// Wake rig agents for each unique rig that had successful dispatches.
	for rig := range successfulRigs {
		wakeRigAgents(rig)
	}

	// Update runtime state with fresh read to avoid clobbering concurrent pause.
	if report.Dispatched > 0 {
		freshState, err := capacity.LoadState(townRoot)
		if err != nil {
			fmt.Printf("%s Could not reload scheduler state: %v\n", style.Dim.Render("Warning:"), err)
		} else {
			freshState.RecordDispatch(report.Dispatched)
			if err := capacity.SaveState(townRoot, freshState); err != nil {
				fmt.Printf("%s Could not save scheduler state: %v\n", style.Dim.Render("Warning:"), err)
			}
		}
	}

	// Snapshot the pool once so both the log line and the exhaustion monitor see
	// the same picture (hq-q943s).
	snapshot, snapErr := polecatCapacitySnapshotForTown(townRoot)
	if snapErr != nil {
		snapshot = lastCapacitySnapshot
	}

	if report.Dispatched > 0 || report.Failed > 0 {
		fmt.Printf("\n%s Dispatched %d, failed %d (reason: %s)\n",
			style.Bold.Render("✓"), report.Dispatched, report.Failed, report.Reason)
	}
	if report.Skipped > 0 {
		if report.Reason == "deadline" {
			// Deadline cutoff (gu-t6jqq): the dispatch budget elapsed before the
			// full plan ran. The skipped beads stay queued and dispatch next
			// cycle — this is the spiral-breaking clean exit, NOT a capacity
			// shortage. Logged distinctly so it isn't mistaken for zero capacity.
			fmt.Printf("\n%s Dispatch budget elapsed — deferred %d bead(s) to next cycle (dispatched %d this cycle)\n",
				style.Dim.Render("○"), report.Skipped, report.Dispatched)
		} else {
			fmt.Printf("\n%s Skipped %d bead(s) — zero capacity (working: %d recovery_blocked: %d reservations: %d reusable_idle: %d pending_mr: %d)\n",
				style.Dim.Render("○"), report.Skipped, snapshot.Working, snapshot.RecoveryBlocked, snapshot.Reservations, snapshot.ReusableIdle, snapshot.PendingMR)
		}
	}

	// hq-q943s: run the exhaustion monitor EVERY cycle with the full picture.
	// A trickle dispatch (one bead placed while dozens are skipped and the pool
	// stays wedged) must keep the counter climbing, not reset it — the previous
	// reset-on-any-dispatch let chronic degraded-throughput starvation slip past
	// the alarm for hours. The monitor resets the counter itself only when ready
	// work is actually flowing (nothing skipped, or the pool no longer degraded).
	monitorCapacityExhaustion(townRoot, snapshot, report.Skipped)

	return report.Dispatched, nil
}

// printDryRunPlan displays a dry-run dispatch plan.
func printDryRunPlan(plan capacity.DispatchPlan, snapshot polecatCapacitySnapshot, batchSize int) {
	if plan.Reason == "none" {
		fmt.Println("No ready beads scheduled for dispatch")
		return
	}

	capStr := "unlimited"
	if snapshot.Max > 0 {
		capStr = fmt.Sprintf("%d free of %d (working: %d, recovery_blocked: %d, reservations: %d, reusable_idle: %d, pending_mr: %d)",
			snapshot.Free, snapshot.Max, snapshot.Working, snapshot.RecoveryBlocked, snapshot.Reservations, snapshot.ReusableIdle, snapshot.PendingMR)
	}

	totalReady := len(plan.ToDispatch) + plan.Skipped
	if len(plan.ToDispatch) == 0 {
		fmt.Printf("No capacity: %s, %d ready bead(s) waiting\n", capStr, totalReady)
		return
	}

	fmt.Printf("%s Would dispatch %d bead(s) (capacity: %s, batch: %d, ready: %d, reason: %s)\n",
		style.Bold.Render("📋"), len(plan.ToDispatch), capStr, batchSize, totalReady, plan.Reason)
	for _, b := range plan.ToDispatch {
		fmt.Printf("  Would dispatch: %s → %s\n", b.WorkBeadID, b.TargetRig)
	}
}

// beadsForContext returns a Beads instance that can operate on a sling context
// bead. Sling contexts live in the target rig's beads dir (GH#3468), so we
// resolve the dir from the context's TargetRig field. Falls back to HQ if
// the target rig is unknown (e.g., invalid context with nil fields).
func beadsForContext(townRoot string, fields *capacity.SlingContextFields) *beads.Beads {
	if fields != nil && fields.TargetRig != "" {
		rigBeadsDir := doltserver.FindRigBeadsDir(townRoot, fields.TargetRig)
		if rigBeadsDir != "" {
			return beads.NewWithBeadsDir(townRoot, rigBeadsDir)
		}
	}
	// Fallback to HQ for contexts without a valid TargetRig
	return beads.NewWithBeadsDir(townRoot, filepath.Join(townRoot, ".beads"))
}

func beadsForPendingContext(townRoot string, b capacity.PendingBead) *beads.Beads {
	if b.ContextBeadsDir != "" {
		workDir := b.ContextWorkDir
		if workDir == "" {
			workDir = filepath.Dir(b.ContextBeadsDir)
		}
		return beads.NewWithBeadsDir(workDir, b.ContextBeadsDir)
	}
	return beadsForContext(townRoot, b.Context)
}

type slingContextRecord struct {
	issue    *beads.Issue
	workDir  string
	beadsDir string
}

func beadsForContextRecord(rec slingContextRecord) *beads.Beads {
	return beads.NewWithBeadsDir(rec.workDir, rec.beadsDir)
}

// cleanupStaleContexts closes invalid and stale sling context beads.
// Called explicitly before the dispatch cycle to separate cleanup from querying.
func cleanupStaleContexts(townRoot string) {
	contexts := listAllSlingContextRecords(townRoot)

	// First pass: close invalid and circuit-broken contexts, collect work bead IDs
	// that need status checks for stale detection.
	var staleCheckContexts []slingContextRecord
	var staleCheckFields []*capacity.SlingContextFields
	for _, ctx := range contexts {
		fields := beads.ParseSlingContextFields(ctx.issue.Description)
		if fields == nil {
			_ = beadsForContextRecord(ctx).CloseSlingContext(ctx.issue.ID, "invalid-context")
			continue
		}
		if fields.DispatchFailures >= maxDispatchFailures {
			_ = beadsForContextRecord(ctx).CloseSlingContext(ctx.issue.ID, "circuit-broken")
			// Backstop log: the primary recordDispatchFailure path normally
			// logs the break, but if that process died after incrementing the
			// counter but before closing, this is where the break is observed.
			// The monitor dedups by distinct context_id, so a double-log of the
			// same context is harmless (gu-ixo67).
			logCircuitBreak(townRoot, fields.WorkBeadID, ctx.issue.ID, fields.TargetRig, fields.LastFailure)
			continue
		}
		staleCheckContexts = append(staleCheckContexts, ctx)
		staleCheckFields = append(staleCheckFields, fields)
	}

	if len(staleCheckContexts) == 0 {
		return
	}

	// Collect work bead IDs to fetch
	workBeadIDs := make([]string, 0, len(staleCheckFields))
	for _, fields := range staleCheckFields {
		workBeadIDs = append(workBeadIDs, fields.WorkBeadID)
	}

	// Batch-fetch work bead info for only the specific IDs we need
	workBeadInfo := batchFetchBeadInfoByIDs(townRoot, workBeadIDs)

	// Second pass: close contexts whose work beads are stale.
	// Note: in_progress is intentionally excluded — the work bead is being
	// actively worked, and bd ready won't return it, so the dispatch query
	// already prevents re-dispatch. The context stays open until the polecat
	// finishes and the bead transitions to closed/tombstone.
	//
	// Missing work bead (gu-hfr3): if the work bead can't be found at all,
	// treat it as stale too — but only after the context has aged past the
	// TTL so we don't race with in-flight bead creation. A deleted or reaped
	// work bead leaves its sling-context dangling forever otherwise, which
	// confused convoys and caused them to track the wrapper instead.
	now := time.Now()
	for i, ctx := range staleCheckContexts {
		fields := staleCheckFields[i]
		info, found := workBeadInfo[fields.WorkBeadID]
		if found {
			if info.Status == "hooked" || info.Status == "closed" || info.Status == "tombstone" {
				b := beadsForContext(townRoot, fields)
				_ = b.CloseSlingContext(ctx.issue.ID, "stale-work-bead")
			}
			continue
		}
		// Work bead not found. Only close if the context has aged past the
		// TTL — guards against transient bd show failures and against
		// closing a context before its work bead finishes committing.
		if isContextOlderThan(ctx.issue, now, slingContextTTL) {
			b := beadsForContext(townRoot, fields)
			_ = b.CloseSlingContext(ctx.issue.ID, "missing-work-bead")
		}
	}
}

// isContextOlderThan delegates to sling.ContextOlderThan: reports whether the
// context's CreatedAt timestamp is older than the given TTL relative to now.
// Unparseable or empty timestamps return false (fail-closed). Kept as a thin
// cmd wrapper so existing callsites and tests stay unchanged.
func isContextOlderThan(ctx *beads.Issue, now time.Time, ttl time.Duration) bool {
	return sling.ContextOlderThan(ctx, now, ttl)
}

// beadStatusInfo holds batch-fetched bead status, title, and labels.
type beadStatusInfo struct {
	Status     string
	Title      string
	Labels     []string
	DeferUntil string
	Type       string
}

// batchFetchBeadInfoByIDs returns a map of bead ID → status+title+labels for specific beads.
// Uses `bd show` with multiple IDs per rig directory instead of fetching all beads.
// This avoids the O(minutes) latency of `bd list --all --json --limit=0` on large repos.
func batchFetchBeadInfoByIDs(townRoot string, ids []string) map[string]beadStatusInfo {
	result := make(map[string]beadStatusInfo)
	if len(ids) == 0 {
		return result
	}

	idsByBeadsDir := groupBeadIDsByResolvedBeadsDir(townRoot, ids)

	// Parallelize per-beads-dir bd show calls behind the same bounded
	// semaphore as listAllSlingContextRecords (gu-1h3ur/gu-el5bx). The serial
	// iteration was a dominant cost on the dispatch hot path: N beads dirs ×
	// ~0.8s each ≈ several seconds of serial subprocess overhead that now
	// collapses to wall-clock of the slowest single dir. (gu-adbef)
	type dirResult struct {
		items []struct {
			ID         string   `json:"id"`
			Status     string   `json:"status"`
			Title      string   `json:"title"`
			Labels     []string `json:"labels"`
			DeferUntil string   `json:"defer_until"`
			Type       string   `json:"issue_type"`
		}
	}
	dirs := make([]string, 0, len(idsByBeadsDir))
	for d := range idsByBeadsDir {
		dirs = append(dirs, d)
	}
	results := make([]dirResult, len(dirs))
	sem := make(chan struct{}, dispatchScanConcurrency())
	var wg sync.WaitGroup
	for i, beadsDir := range dirs {
		wg.Add(1)
		go func(i int, beadsDir string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			b := beads.NewWithBeadsDir(filepath.Dir(beadsDir), beadsDir)
			args := append([]string{"show", "--json"}, idsByBeadsDir[beadsDir]...)
			out, err := b.Run(args...)
			if err != nil {
				return
			}
			var r dirResult
			_ = json.Unmarshal(out, &r.items)
			results[i] = r
		}(i, beadsDir)
	}
	wg.Wait()

	for _, r := range results {
		for _, item := range r.items {
			result[item.ID] = beadStatusInfo{
				Status:     item.Status,
				Title:      item.Title,
				Labels:     item.Labels,
				DeferUntil: item.DeferUntil,
				Type:       item.Type,
			}
		}
	}
	return result
}

func groupBeadIDsByResolvedBeadsDir(townRoot string, ids []string) map[string][]string {
	townBeadsDir := filepath.Join(townRoot, ".beads")
	idsByBeadsDir := make(map[string][]string)
	seen := make(map[string]bool)
	for _, id := range ids {
		if id == "" {
			continue
		}
		beadsDir := beads.ResolveBeadsDirForID(townBeadsDir, id)
		key := beadsDir + "\x00" + id
		if seen[key] {
			continue
		}
		seen[key] = true
		idsByBeadsDir[beadsDir] = append(idsByBeadsDir[beadsDir], id)
	}
	return idsByBeadsDir
}

// getReadySlingContexts queries for sling context beads whose work beads are ready.
// This is a pure query — no destructive side effects. Call cleanupStaleContexts()
// before this function to handle invalid/stale contexts.
//
// Sling contexts are queried from HQ only (authoritative). Work bead readiness
// is checked across all rig dirs since work beads live in rig-local DBs.
func getReadySlingContexts(townRoot string) ([]capacity.PendingBead, error) {
	// 1. List all open sling context beads from HQ (authoritative)
	allContexts := listAllSlingContextRecords(townRoot)

	if len(allContexts) == 0 {
		return nil, nil
	}

	// 2. Batch-fetch work bead status/labels/type via targeted `bd show` for
	// exactly the work beads referenced by the open contexts. This single batch
	// supplies everything the readiness gate and the agent-bead filter need —
	// status, blocked-ness (combined with bd blocked below), labels, and type.
	//
	// We deliberately do NOT call bd ready across every town dir here (gu-k5sul):
	// bd ready walks the full ready graph and was the dominant cost of the ~79s
	// fanout across ~29 dirs. Its only consumer was the agent-bead guard, and the
	// gt:agent label / issue_type=agent it detected is already present in this
	// targeted batch. isAgentBeadInfo reads it directly.
	workBeadIDs := make([]string, 0, len(allContexts))
	for _, ctx := range allContexts {
		fields := beads.ParseSlingContextFields(ctx.issue.Description)
		if fields == nil {
			continue
		}
		workBeadIDs = append(workBeadIDs, fields.WorkBeadID)
	}
	workBeadInfo := batchFetchBeadInfoByIDs(townRoot, workBeadIDs)
	blockedWorkIDs, blockedErr := listBlockedWorkBeadIDsWithError(townRoot, workBeadIDs)
	if blockedErr != nil {
		return nil, blockedErr
	}

	// 3. Build PendingBead list — pure filtering, no mutations.
	// Sort by EnqueuedAt for deterministic deduplication: when concurrent
	// scheduleBead calls create multiple contexts for the same work bead,
	// the oldest context always wins.
	sort.Slice(allContexts, func(i, j int) bool {
		fi := beads.ParseSlingContextFields(allContexts[i].issue.Description)
		fj := beads.ParseSlingContextFields(allContexts[j].issue.Description)
		if fi == nil || fj == nil {
			return fi != nil // valid contexts sort before invalid
		}
		if fi.EnqueuedAt != fj.EnqueuedAt {
			return fi.EnqueuedAt < fj.EnqueuedAt
		}
		return allContexts[i].issue.ID < allContexts[j].issue.ID // deterministic tiebreaker
	})

	seenWork := make(map[string]bool)
	var result []capacity.PendingBead
	for _, ctx := range allContexts {
		fields := beads.ParseSlingContextFields(ctx.issue.Description)
		if fields == nil {
			continue // Skip invalid — cleanupStaleContexts handles these
		}

		// Circuit breaker filter
		if fields.DispatchFailures >= maxDispatchFailures {
			continue
		}

		// Only include open, unblocked work beads. This uses the fast blocked
		// cache plus targeted show output instead of shelling out to bd ready for
		// every rig, which is prohibitively expensive in large towns.
		info, found := workBeadInfo[fields.WorkBeadID]
		if !isScheduledWorkBeadReady(fields.WorkBeadID, info, found, blockedWorkIDs) {
			continue
		}

		// Safety net (gu-7gm): never dispatch agent state beads as work.
		// The scheduleBead path already rejects these up-front, but stale
		// contexts from older code paths or manual bd writes may still be in
		// the queue — skip them here instead of handing a polecat a state
		// bead whose "work" is to resubmit some prior auto-save branch.
		if isAgentBeadInfo(info) {
			fmt.Fprintf(os.Stderr, "%s Skipping sling context %s: work bead %s is an agent state bead (gt:agent), not a work item\n",
				style.Warning.Render("⚠"), ctx.issue.ID, fields.WorkBeadID)
			continue
		}

		// Deduplicate: one dispatch per work bead (oldest context wins)
		if seenWork[fields.WorkBeadID] {
			continue
		}
		seenWork[fields.WorkBeadID] = true

		// Defensive filter: messaging beads (gt:message / gt:handoff /
		// gt:merge-request) must never reach a rig polecat. Log the skip so
		// the gap is observable and operators can chase the upstream cause.
		workLabels := info.Labels
		if capacity.IsMessagingBead(workLabels) {
			fmt.Fprintf(os.Stderr, "%s dispatch_skip reason=messaging_label bead=%s labels=%v\n",
				style.Dim.Render("○"), fields.WorkBeadID, workLabels)
			continue
		}

		// Belt-and-suspenders: handoff memos titled "🤝 HANDOFF" that lack the
		// gt:handoff/gt:message label (agent-authored as bare type=task) would
		// otherwise be dispatched as work and re-dispatched every cycle forever
		// (gu-a76gk). Catch them by title.
		if capacity.IsHandoffTitle(info.Title) {
			fmt.Fprintf(os.Stderr, "%s dispatch_skip reason=handoff_title bead=%s title=%q\n",
				style.Dim.Render("○"), fields.WorkBeadID, info.Title)
			continue
		}

		// Container filter (gu-r8b0q): a work bead with open children is an
		// epic/container, not dispatchable work — the children track the real
		// work. executeSling rejects these at dispatch time, but the rejection
		// circuit-breaks (closes) only the one context while the epic stays in
		// `bd ready`, so it gets re-selected every cycle → re-fails → re-breaks
		// forever. Excluding containers from the candidate set here means they
		// never reach dispatch in the first place, so there is no repeated
		// circuit-break churn. Runs after dedup so we pay at most one
		// `bd children` subprocess per unique ready work bead. The children
		// themselves dispatch normally — only the container parent is filtered.
		if isParentOfOpenChildren(fields.WorkBeadID) {
			fmt.Fprintf(os.Stderr, "%s dispatch_skip reason=open_children bead=%s (container — children track the work)\n",
				style.Dim.Render("○"), fields.WorkBeadID)
			continue
		}

		result = append(result, capacity.PendingBead{
			ID:              ctx.issue.ID,
			WorkBeadID:      fields.WorkBeadID,
			Title:           ctx.issue.Title,
			TargetRig:       fields.TargetRig,
			Description:     ctx.issue.Description,
			Labels:          workLabels,
			Context:         fields,
			ContextWorkDir:  ctx.workDir,
			ContextBeadsDir: ctx.beadsDir,
		})
	}

	return result, nil
}

// filterByPerRigCapacity drops pending beads whose target rig is already at
// its configured per-rig polecat cap (settings/config.json:polecat.max_concurrent).
// Rigs without a configured cap or with cap<=0 are never filtered.
// Called by the scheduler's QueryPending callback so the deferred dispatch
// path respects per-rig caps during fair-share distribution.
//
// Note: This does not reorder the queue or prefer rigs with the most headroom;
// FIFO order is preserved. PlanDispatch further caps total dispatch by batch
// size and town-wide capacity. A rig's cap is re-evaluated each cycle, so
// skipped beads stay in the queue and dispatch as slots free up.
func filterByPerRigCapacity(townRoot string, pending []capacity.PendingBead) []capacity.PendingBead {
	if len(pending) == 0 {
		return pending
	}

	// Per-rig caps from settings (cached across this call)
	rigCaps := make(map[string]int)
	// Per-rig remaining capacity; populated lazily so we only probe rigs that
	// actually have a cap configured.
	rigRemaining := make(map[string]int)

	// Pre-fetch rig-level working counts once so we don't pay the tmux+beads
	// cost per-bead.
	var workingByRig map[string]int
	if counts, ok := countWorkingPolecatsByRig(); ok {
		workingByRig = counts
	}

	result := make([]capacity.PendingBead, 0, len(pending))
	for _, b := range pending {
		rig := b.TargetRig
		if rig == "" {
			result = append(result, b)
			continue
		}

		cap, known := rigCaps[rig]
		if !known {
			cap = loadRigPolecatMaxConcurrent(filepath.Join(townRoot, rig))
			rigCaps[rig] = cap
		}
		if cap <= 0 {
			result = append(result, b)
			continue
		}

		remaining, seen := rigRemaining[rig]
		if !seen {
			working := 0
			if workingByRig != nil {
				working = workingByRig[rig]
			}
			remaining = cap - working
			if remaining < 0 {
				remaining = 0
			}
			rigRemaining[rig] = remaining
		}

		if remaining <= 0 {
			// At cap — drop this bead from this cycle; it stays queued.
			continue
		}
		rigRemaining[rig] = remaining - 1
		result = append(result, b)
	}
	return result
}

// Context fields are already parsed (from PendingBead.Context).
// Returns the SlingResult (including PolecatName) on success.
func dispatchSingleBead(b capacity.PendingBead, townRoot, _ string) (*SlingResult, error) {
	if b.Context == nil {
		return nil, fmt.Errorf("missing sling context for %s", b.ID)
	}

	dp := capacity.ReconstructFromContext(b.Context)
	params := SlingParams{
		BeadID:           dp.BeadID,
		RigName:          dp.RigName,
		FormulaName:      dp.FormulaName,
		Args:             dp.Args,
		Vars:             dp.Vars,
		Merge:            dp.Merge,
		BaseBranch:       dp.BaseBranch,
		ResumeBranch:     dp.ResumeBranch,
		NoMerge:          dp.NoMerge,
		ReviewOnly:       dp.ReviewOnly,
		Account:          dp.Account,
		Agent:            dp.Agent,
		HookRawBead:      dp.HookRawBead,
		Mode:             dp.Mode,
		FormulaFailFatal: true,
		CallerContext:    "scheduler-dispatch",
		NoConvoy:         true,
		NoBoot:           true,
		TownRoot:         townRoot,
		BeadsDir:         filepath.Join(townRoot, ".beads"),
	}

	fmt.Printf("  Dispatching %s → %s...\n", b.WorkBeadID, b.TargetRig)
	result, err := executeSling(params)
	if err != nil {
		return nil, fmt.Errorf("sling failed: %w", err)
	}

	return result, nil
}

func validateDryRunDispatchPlan(townRoot string, plan capacity.DispatchPlan) capacity.DispatchPlan {
	if len(plan.ToDispatch) == 0 {
		return plan
	}
	validated := make([]capacity.PendingBead, 0, len(plan.ToDispatch))
	for _, b := range plan.ToDispatch {
		if err := validatePendingBeadForDispatch(townRoot, b, false); err != nil {
			fmt.Fprintf(os.Stderr, "%s dry-run_skip reason=validation bead=%s target_rig=%s: %v\n",
				style.Dim.Render("○"), b.WorkBeadID, b.TargetRig, err)
			plan.Skipped++
			continue
		}
		if _, err := getBeadInfoFromTownRoot(townRoot, b.WorkBeadID); err != nil {
			fmt.Fprintf(os.Stderr, "%s dry-run_skip reason=bead_lookup bead=%s target_rig=%s: %v\n",
				style.Dim.Render("○"), b.WorkBeadID, b.TargetRig, err)
			plan.Skipped++
			continue
		}
		if b.TargetRig != "" {
			if err := verifyBeadExistsInTargetRigDatabase(b.WorkBeadID, b.TargetRig, townRoot); err != nil {
				fmt.Fprintf(os.Stderr, "%s dry-run_skip reason=target_db bead=%s target_rig=%s: %v\n",
					style.Dim.Render("○"), b.WorkBeadID, b.TargetRig, err)
				plan.Skipped++
				continue
			}
		}
		validated = append(validated, b)
	}
	plan.ToDispatch = validated
	if len(plan.ToDispatch) == 0 && plan.Reason != "none" {
		plan.Reason = "validation"
	}
	return plan
}

func validatePendingBeadForDispatch(townRoot string, b capacity.PendingBead, escalate bool) error {
	// Cross-rig prefix guard (gt-el4). A bead whose ID prefix does not match the
	// target rig's registered prefix must not be dispatched — the polecat would
	// land in a rig DB that cannot resolve the bead and hang in prime.
	if b.TargetRig == "" {
		return nil
	}
	rigPath := filepath.Join(townRoot, b.TargetRig)
	rigPrefix := rigBeadsPrefix(townRoot, rigPath, b.TargetRig)
	if capacity.AcceptsPrefix(rigPrefix, b.WorkBeadID) {
		return nil
	}
	gotPrefix := capacity.BeadIDPrefix(b.WorkBeadID)
	fmt.Fprintf(os.Stderr,
		"%s dispatch_refused reason=cross_rig_prefix bead=%s target_rig=%s rig_prefix=%s bead_prefix=%s\n",
		style.Warning.Render("⚠"), b.WorkBeadID, b.TargetRig, rigPrefix, gotPrefix)
	if escalate && shouldFireCrossRigEscalation(b.TargetRig, gotPrefix, time.Now()) {
		fireCrossRigEscalation(b.TargetRig, gotPrefix, b.WorkBeadID)
	}
	return capacity.ErrCrossRigPrefix
}

// isDaemonDispatch returns true when dispatch is triggered by the daemon heartbeat.
func isDaemonDispatch() bool {
	return os.Getenv("GT_DAEMON") == "1"
}

// isAlreadyDispatchedError returns true if the dispatch error indicates the bead
// is already hooked or in_progress to a live agent. This is a healthy state (the
// work is being performed), not a spawn failure — the respawn counter must NOT
// increment for these errors. (Fixes gu-cqmw: spurious circuit-breaks from
// convoy re-feeding beads that are actively being worked.)
func isAlreadyDispatchedError(err error) bool {
	msg := err.Error()
	return strings.HasPrefix(msg, "already hooked") ||
		strings.HasPrefix(msg, "already in_progress")
}

// recordDispatchFailure increments the dispatch failure counter on the sling context bead.
// Skips increment for "already hooked/in_progress" errors which indicate the bead
// is actively being worked — not a true dispatch failure (gu-cqmw).
func recordDispatchFailure(townRoot string, townBeads *beads.Beads, b capacity.PendingBead, dispatchErr error) {
	if b.Context == nil {
		return
	}

	// "Already hooked/in_progress" means the work is being performed by a live
	// agent. This is not a failure — skip counter increment to avoid spurious
	// circuit-breaks when convoy feeders re-feed active beads.
	if isAlreadyDispatchedError(dispatchErr) {
		return
	}

	b.Context.DispatchFailures++
	b.Context.LastFailure = dispatchErr.Error()

	if err := townBeads.UpdateSlingContextFields(b.ID, b.Context); err != nil {
		fmt.Printf("  %s Failed to record dispatch failure for %s: %v\n",
			style.Warning.Render("⚠"), b.ID, err)
	}

	if b.Context.DispatchFailures >= maxDispatchFailures {
		if err := townBeads.CloseSlingContext(b.ID, "circuit-broken"); err != nil {
			fmt.Printf("  %s Failed to close circuit-broken context %s: %v\n",
				style.Warning.Render("⚠"), b.ID, err)
		}
		fmt.Printf("  %s Context %s (work: %s) failed %d times, circuit-broken\n",
			style.Warning.Render("⚠"), b.ID, b.WorkBeadID, b.Context.DispatchFailures)
		logCircuitBreak(townRoot, b.WorkBeadID, b.ID, b.TargetRig, dispatchErr.Error())
	}
}

// logCircuitBreak appends a circuit-break record to the town-wide log so the
// circuit_break_dog daemon patrol can detect repeated breaks on the same work
// bead (gu-ixo67). Best-effort: a log failure only delays detection, so it is
// logged at warning level and never blocks dispatch.
func logCircuitBreak(townRoot, workBeadID, contextID, targetRig, lastFailure string) {
	if townRoot == "" {
		return
	}
	if err := events.LogCircuitBreak(townRoot, events.CircuitBreakRecord{
		WorkBeadID:  workBeadID,
		ContextID:   contextID,
		TargetRig:   targetRig,
		LastFailure: lastFailure,
	}); err != nil {
		fmt.Printf("  %s Failed to log circuit-break for %s: %v\n",
			style.Warning.Render("⚠"), contextID, err)
	}
}

// listAllSlingContexts returns all open sling context beads across all rig
// beads dirs. Sling contexts are created in the target rig's beads dir
// (GH#3468), so we scan HQ plus all rig dirs.
// Used by scheduler list/status/clear, cleanupStaleContexts, and areScheduled.
// Does NOT filter by readiness or circuit breaker.
//
// Deduplicates by context ID alone: different search dirs can resolve to the
// same underlying beads DB (e.g., when a rig's top-level .beads is a redirect
// to mayor/rig/.beads), AND `bd list --label` with prefix routing in
// routes.jsonl returns the same bead from multiple BEADS_DIR pins (a bead
// with prefix routed to a sibling DB shows up under both the host DB and the
// routed-to DB). Keying dedup by ID alone collapses both cases. (gu-38ov)
func listAllSlingContexts(townRoot string) []*beads.Issue {
	records := listAllSlingContextRecords(townRoot)
	all := make([]*beads.Issue, 0, len(records))
	for _, rec := range records {
		all = append(all, rec.issue)
	}
	return all
}

// listAllScheduledBeadIDs returns the work bead IDs of all currently scheduled
// (open) sling contexts, deduplicated. Used by listBlockedWorkBeadIDs to
// scope its blocker scan instead of asking the beads layer for the entire
// town's blocker state.
func listAllScheduledBeadIDs(townRoot string) []string {
	allContexts := listAllSlingContexts(townRoot)

	var ids []string
	seen := make(map[string]bool)
	for _, ctx := range allContexts {
		fields := beads.ParseSlingContextFields(ctx.Description)
		if fields == nil {
			continue
		}
		if !seen[fields.WorkBeadID] {
			seen[fields.WorkBeadID] = true
			ids = append(ids, fields.WorkBeadID)
		}
	}

	return ids
}

// dispatchScanConcurrency bounds how many per-rig sling-context reads run
// concurrently in listAllSlingContextRecords (gu-1h3ur). These reads bypass the
// bd-list-read throttle (gu-pug66's lock-free dispatch path), so the semaphore
// is what keeps them from storming the single shared Dolt server. Default 6 —
// the dispatch scan runs under scheduler-dispatch.lock on the heartbeat budget,
// so it gets slightly more parallelism than the capacity snapshot's default of 4
// to drain the ~19-dir fan-out well inside budget. Tunable via
// GT_DISPATCH_SCAN_FANOUT.
func dispatchScanConcurrency() int {
	const def = 6
	if v := os.Getenv("GT_DISPATCH_SCAN_FANOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return n
		}
	}
	return def
}

func listAllSlingContextRecords(townRoot string) []slingContextRecord {
	var records []slingContextRecord
	// Scan each rig's sling-contexts concurrently behind a bounded semaphore.
	// This per-rig `bd` fan-out (one ListOpenSlingContexts cold-start per dir,
	// ~0.65s each × ~19 dirs ≈ 12s serial) runs on the dispatch hot path inside
	// dispatchScheduledWork while holding scheduler-dispatch.lock — so under a
	// large town it blows the heartbeat dispatch budget and starves auto-dispatch
	// (gu-1h3ur). Same collapse-the-serial-fan-out fix as the capacity snapshot
	// (gu-el5bx): the semaphore — not the read throttle — bounds Dolt load, so we
	// keep WithoutReadThrottle (gu-pug66's deliberately lock-free dispatch path).
	dirs := beadsSearchDirs(townRoot)
	type dirResult struct {
		dir      string
		beadsDir string
		contexts []*beads.Issue
	}
	results := make([]dirResult, len(dirs))
	sem := make(chan struct{}, dispatchScanConcurrency())
	var wg sync.WaitGroup
	for i, dir := range dirs {
		wg.Add(1)
		go func(i int, dir string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			beadsDir := beads.ResolveBeadsDir(dir)
			results[i] = dirResult{dir: dir, beadsDir: beadsDir}
			b := beads.NewWithBeadsDir(dir, beadsDir).WithoutReadThrottle()
			contexts, err := b.ListOpenSlingContexts()
			if err != nil {
				return // Partial failure is acceptable — skip unavailable dirs
			}
			results[i].contexts = contexts
		}(i, dir)
	}
	wg.Wait()

	// Fold results in dir order (deterministic): dedup by ctx.ID alone, since bd
	// prefix routing can return the same bead from multiple BEADS_DIR pins when
	// routes.jsonl maps the bead's prefix to a sibling DB (gu-38ov). Iterating
	// results in stable order keeps the oldest-context-wins dedup deterministic.
	seen := make(map[string]bool)
	for _, r := range results {
		for _, ctx := range r.contexts {
			if seen[ctx.ID] {
				continue
			}
			seen[ctx.ID] = true
			records = append(records, slingContextRecord{issue: ctx, workDir: r.dir, beadsDir: r.beadsDir})
		}
	}
	return records
}

// listBlockedWorkBeadIDsWithError returns a set of work bead IDs that have active blockers.
// Returns an error only when ALL dirs fail (partial success is acceptable).
func listBlockedWorkBeadIDsWithError(townRoot string, workBeadIDs []string) (map[string]bool, error) {
	blockedIDs := make(map[string]bool)
	idsByBeadsDir := groupBeadIDsByResolvedBeadsDir(townRoot, workBeadIDs)
	if len(idsByBeadsDir) == 0 {
		return blockedIDs, nil
	}

	// Parallelize per-beads-dir bd blocked calls behind the same bounded
	// semaphore as the sling-context scan (gu-adbef). Same motivation as
	// batchFetchBeadInfoByIDs: N serial subprocess calls ≈ N×0.8s that now
	// collapse to the wall-clock of the slowest single dir.
	type dirResult struct {
		beadsDir string
		blocked  []struct {
			ID string `json:"id"`
		}
		err error
	}
	dirs := make([]string, 0, len(idsByBeadsDir))
	for d := range idsByBeadsDir {
		dirs = append(dirs, d)
	}
	results := make([]dirResult, len(dirs))
	sem := make(chan struct{}, dispatchScanConcurrency())
	var wg sync.WaitGroup
	for i, beadsDir := range dirs {
		wg.Add(1)
		go func(i int, beadsDir string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			b := beads.NewWithBeadsDir(filepath.Dir(beadsDir), beadsDir)
			blockedOut, err := b.Run("blocked", "--json")
			if err != nil {
				results[i] = dirResult{beadsDir: beadsDir, err: err}
				return
			}
			var r dirResult
			r.beadsDir = beadsDir
			_ = json.Unmarshal(blockedOut, &r.blocked)
			results[i] = r
		}(i, beadsDir)
	}
	wg.Wait()

	failCount := 0
	var lastErr error
	for _, r := range results {
		if r.err != nil {
			failCount++
			lastErr = r.err
			fmt.Fprintf(os.Stderr, "%s Warning: bd blocked failed for %s: %v\n",
				style.Dim.Render("⚠"), filepath.Dir(r.beadsDir), r.err)
			continue
		}
		for _, b := range r.blocked {
			blockedIDs[b.ID] = true
		}
	}
	if failCount == len(dirs) && failCount > 0 {
		return nil, fmt.Errorf("all %d bd blocked queries failed (last: %w)", failCount, lastErr)
	}
	return blockedIDs, nil
}

// listBlockedWorkBeadIDs returns a set of work bead IDs that have active blockers.
// Convenience wrapper that ignores errors (used by listScheduledBeads for display).
func listBlockedWorkBeadIDs(townRoot string) map[string]bool {
	ids, _ := listBlockedWorkBeadIDsWithError(townRoot, listAllScheduledBeadIDs(townRoot))
	if ids == nil {
		return make(map[string]bool)
	}
	return ids
}

// isBeadBlockedByOpenDeps reports whether a single bead has open blocking
// dependencies, using the same `bd blocked` authority the deferred dispatcher
// trusts (listBlockedWorkBeadIDsWithError → isScheduledWorkBeadReady). It is the
// single-bead analog used by the direct-dispatch guard in executeSling.
//
// Fails OPEN: on any query error the bead is treated as NOT blocked. The guard
// exists to surface a known-blocked bead loudly instead of silently dropping it;
// it must never turn a transient `bd blocked` failure into a refusal that a plain
// `gt sling` would not otherwise have hit.
func isBeadBlockedByOpenDeps(townRoot, beadID string) bool {
	blocked, err := listBlockedWorkBeadIDsWithError(townRoot, []string{beadID})
	if err != nil {
		return false // fail open — never block dispatch on a query error
	}
	return blocked[beadID]
}

// isBeadBlockedByOpenDepsFn and shouldDeferDispatchFn are injectable seams for
// the direct-dispatch blocked-deps guard in executeSling (gu-gzng2). Injected as
// variables so unit tests can drive the guard without a real `bd blocked`
// subprocess or an on-disk town settings file.
var (
	isBeadBlockedByOpenDepsFn = isBeadBlockedByOpenDeps
	shouldDeferDispatchFn     = shouldDeferDispatch
)

func isScheduledWorkBeadReady(workBeadID string, info beadStatusInfo, found bool, blockedWorkIDs map[string]bool) bool {
	if !found || blockedWorkIDs[workBeadID] {
		return false
	}
	if info.Status != "open" {
		return false
	}
	// Never dispatch a bead marked as not-work (hq-9jeyo). Reference/gate
	// tripwires carry do-not-dispatch / pinned labels (and issue_type=reference)
	// and are meant to stay OPEN forever as live tripwires. Without this guard
	// the scheduler hooked an open tripwire to a polecat, which then ran
	// `gt done` (ESCALATED) and CLOSED the tripwire — taking the safety gate
	// down and re-triggering the exact spawn-storm the tripwire guards against.
	if isNonDispatchableBead(info) {
		return false
	}
	// Skip beads flagged no-auto-dispatch (gs-b2a). Unlike the permanent
	// tripwire labels above, this only blocks the AUTOMATIC scheduler path: a
	// human may still dispatch the bead via `gt sling`, and `gt done` may still
	// close it. So this guard lives here in the dispatch readiness gate rather
	// than in isNonDispatchableBead (which also governs the gt done close guard).
	if hasLabel(info.Labels, labelNoAutoDispatch) {
		return false
	}
	// Skip beads deferred to a future time (gs-o5f). `gt done --status DEFERRED`
	// sets defer_until WITHOUT flipping status off "open", so the status check
	// alone lets a future-deferred bead through and the scheduler re-dispatches
	// it before its defer window elapses. Mirror the `bd ready` filter
	// (defer_until > now → hidden). On an unparseable defer_until we fall back
	// to dispatchable rather than stranding the bead.
	now := time.Now()
	if nowForDeferRelease != nil {
		now = nowForDeferRelease()
	}
	if expired, err := isDeferUntilExpired(info.DeferUntil, now); err == nil && info.DeferUntil != "" && !expired {
		return false
	}
	return true
}

// isAgentBeadInfo reports whether a batch-fetched bead is an agent state bead
// (polecat/witness/refinery/mayor/dog) rather than a work item. Identification
// mirrors isAgentBead / beads.IsAgentBead: the "gt:agent" label (current
// standard) or the legacy issue_type == "agent" (pre-migration beads).
//
// This is the beadStatusInfo form, used by getReadySlingContexts to filter
// agent beads from the targeted bd-show batch (workBeadInfo) instead of from a
// separate bd-ready fanout across every town dir (gu-k5sul).
func isAgentBeadInfo(info beadStatusInfo) bool {
	if info.Type == "agent" {
		return true
	}
	return hasLabel(info.Labels, "gt:agent")
}

// Labels / issue type that mark a bead as a permanent reference or gate
// tripwire (hq-9jeyo). These beads stay OPEN forever by design and must never
// be dispatched, hooked, spawned, or closed via the dispatch path.
const (
	labelDoNotDispatch = "do-not-dispatch"
	labelPinned        = "pinned"
	issueTypeReference = "reference"
	// labelNoAutoDispatch blocks the automatic scheduler from picking up a bead
	// while still allowing manual `gt sling` and `gt done` (gs-b2a). Unlike the
	// tripwire labels above it is checked only in the dispatch readiness gate.
	labelNoAutoDispatch = "no-auto-dispatch"
)

// isNonDispatchableLabelSet is the canonical reference/tripwire test on a raw
// (issue_type, labels) pair: a bead the dispatch machinery must never feed,
// schedule, or hook. Matched by issue_type=reference OR the do-not-dispatch /
// pinned labels — a tripwire typically carries all three, but any one suffices.
// Every dispatch-path guard delegates here so the convoy-feed, scheduler,
// executeSling, and gt-done guards share ONE definition (gs-0cj).
func isNonDispatchableLabelSet(issueType string, labels []string) bool {
	if strings.EqualFold(issueType, issueTypeReference) {
		return true
	}
	return hasLabel(labels, labelDoNotDispatch) || hasLabel(labels, labelPinned)
}

// isNonDispatchableBead reports whether a bead is a reference/tripwire that the
// scheduler must never dispatch.
func isNonDispatchableBead(info beadStatusInfo) bool {
	return isNonDispatchableLabelSet(info.Type, info.Labels)
}

// isNonDispatchableIssue is the *beads.Issue form of isNonDispatchableBead,
// used by the gt done guard (hq-9jeyo) to refuse closing a mis-hooked tripwire.
func isNonDispatchableIssue(issue *beads.Issue) bool {
	if issue == nil {
		return false
	}
	return isNonDispatchableLabelSet(issue.Type, issue.Labels)
}

// nowForDeferRelease is a clock seam that lets tests inject a deterministic
// "current time" for the auto-release pass. Production callers leave it nil
// so we use the wall clock.
var nowForDeferRelease func() time.Time

// isDeferUntilExpired reports whether a defer_until string is non-empty and
// represents a moment at or before `now`. Returns (false, nil) when the field
// is empty (not deferred). Returns (false, err) when the string can't be
// parsed by either RFC3339 or RFC3339Nano — callers log and skip.
func isDeferUntilExpired(deferUntil string, now time.Time) (bool, error) {
	if deferUntil == "" {
		return false, nil
	}
	t, err := time.Parse(time.RFC3339, deferUntil)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, deferUntil)
		if err != nil {
			return false, err
		}
	}
	return !t.After(now), nil
}

// releaseExpiredDeferredBeads scans every rig's beads dir for status=deferred
// beads whose defer_until is in the past and flips them back to status=open
// (clearing defer_until). Returns the count of beads released across the town.
//
// This implements the scheduler half of the deferred-bead lifecycle (gu-0i09):
// `bd ready` already hides deferred beads with future defer_until, but nothing
// in the dispatch loop ever transitioned them back to open once the timer
// expired. Without this pass, beads deferred via `gt done --status DEFERRED`
// (or any --until=...) accumulated forever and the scheduler never noticed.
//
// Best-effort by design — per-bead errors are logged to stderr and skipped so
// a single bad bead doesn't stall the whole dispatch tick. Errors are also
// emitted at the dir level so an unreachable rig db doesn't silently swallow
// every bead in that rig.
func releaseExpiredDeferredBeads(townRoot string) int {
	now := time.Now()
	if nowForDeferRelease != nil {
		now = nowForDeferRelease()
	}

	// Scan + release each rig's deferred beads concurrently behind the same
	// bounded semaphore as the sling-context scan (gu-1h3ur/gu-el5bx). This per-rig
	// `bd list` (plus per-expired-bead `bd update`) fan-out is a maintenance pass
	// that, before gu-pjrz3 decoupled it from the per-tick path, serial-forked one
	// cold-start per dir × ~19 dirs and contributed to the 5m dispatch budget
	// blowout (gu-rz169). Collapsing it to a capped-parallel scan keeps the pass
	// sub-second. The semaphore — not the read throttle — bounds Dolt load, so we
	// keep WithoutReadThrottle (gu-pug66's lock-free dispatch path). Each dir's
	// reads and writes stay within its own goroutine; only the released count is
	// aggregated after the barrier.
	dirs := beadsSearchDirs(townRoot)
	perDir := make([]int, len(dirs))
	sem := make(chan struct{}, dispatchScanConcurrency())
	var wg sync.WaitGroup
	for i, dir := range dirs {
		wg.Add(1)
		go func(i int, dir string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			beadsDir := filepath.Join(dir, ".beads")
			b := beads.NewWithBeadsDir(dir, beadsDir).WithoutReadThrottle()
			out, err := b.Run("list", "--status=deferred", "--json", "--limit=0")
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s Warning: bd list deferred failed for %s: %v\n",
					style.Dim.Render("⚠"), dir, err)
				return
			}
			// `bd list` may return non-JSON sentinel text ("No issues found.") on empty results.
			if len(out) == 0 || (len(out) > 0 && out[0] != '[' && out[0] != '{') {
				return
			}
			var deferred []*beads.Issue
			if jerr := json.Unmarshal(out, &deferred); jerr != nil {
				fmt.Fprintf(os.Stderr, "%s Warning: parsing bd list deferred output for %s: %v\n",
					style.Dim.Render("⚠"), dir, jerr)
				return
			}
			dirReleased := 0
			for _, issue := range deferred {
				if issue == nil {
					continue
				}
				expired, perr := isDeferUntilExpired(issue.DeferUntil, now)
				if perr != nil {
					fmt.Fprintf(os.Stderr, "%s Warning: unparseable defer_until %q on %s: %v\n",
						style.Dim.Render("⚠"), issue.DeferUntil, issue.ID, perr)
					continue
				}
				if !expired {
					continue
				}
				// Flip the bead back to open and clear the defer marker.
				if _, uerr := b.Run("update", issue.ID, "--status=open", "--defer="); uerr != nil {
					fmt.Fprintf(os.Stderr, "%s Warning: could not auto-release deferred bead %s: %v\n",
						style.Dim.Render("⚠"), issue.ID, uerr)
					continue
				}
				dirReleased++
				_ = events.LogFeed(events.TypeSchedulerDeferReleased, "scheduler",
					events.SchedulerDeferReleasedPayload(issue.ID, issue.DeferUntil))
			}
			perDir[i] = dirReleased
		}(i, dir)
	}
	wg.Wait()

	released := 0
	for _, n := range perDir {
		released += n
	}
	return released
}
