package capacity

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrOnSuccessFailed wraps dispatch-succeeded-but-cleanup-failed errors.
// Used to distinguish "polecat launched, context close failed" from
// "polecat never launched" in the OnFailure callback.
type ErrOnSuccessFailed struct{ Err error }

func (e *ErrOnSuccessFailed) Error() string {
	return "dispatch succeeded but OnSuccess failed: " + e.Err.Error()
}
func (e *ErrOnSuccessFailed) Unwrap() error { return e.Err }

// ErrCrossRigPrefix is returned when a bead's ID prefix does not match the
// target rig's registered prefix. This protects against cross-rig dispatch
// where, e.g., an `hq-` bead would be handed to a rig polecat whose DB only
// resolves `gt-` prefixes (gt-el4 / gastownhall/gastown#3800).
var ErrCrossRigPrefix = errors.New("cross-rig prefix dispatch refused")

// BeadIDPrefix returns the prefix of a bead ID — the substring before the
// first '-'. Returns "" if the ID has no dash.
//
// Examples: "gt-abc" -> "gt", "hq-uejt" -> "hq", "wisp-xyz" -> "wisp".
func BeadIDPrefix(beadID string) string {
	idx := strings.Index(beadID, "-")
	if idx < 0 {
		return ""
	}
	return beadID[:idx]
}

// AcceptsPrefix reports whether a bead ID's prefix matches the target rig's
// registered prefix. Empty rigPrefix means "unknown / accept" (the dispatcher
// degrades open rather than refusing dispatch when rig config is unavailable).
func AcceptsPrefix(rigPrefix, beadID string) bool {
	if rigPrefix == "" {
		return true
	}
	return BeadIDPrefix(beadID) == rigPrefix
}

// DispatchCycle is a capacity-controlled dispatch orchestrator.
// The core loop is generic — all domain logic is injected via callbacks.
type DispatchCycle struct {
	// AvailableCapacity returns the number of free dispatch slots.
	// Positive = that many slots available. Zero or negative = no capacity.
	AvailableCapacity func() (int, error)

	// QueryPending returns work items eligible for dispatch.
	// The implementation handles querying, readiness checks, and filtering.
	QueryPending func() ([]PendingBead, error)

	// Validate is an optional pre-dispatch hook called before Execute. A
	// non-nil return value short-circuits dispatch for that bead — Execute is
	// not called and OnFailure is invoked with the error. Used for fast
	// invariant checks (e.g., cross-rig prefix guard) that should not consume
	// failure quota or trigger expensive dispatch machinery.
	Validate func(PendingBead) error

	// Execute dispatches a single item. Called for each planned item.
	Execute func(PendingBead) error

	// OnSuccess is called after successful dispatch.
	OnSuccess func(PendingBead) error

	// OnFailure is called after failed dispatch.
	OnFailure func(PendingBead, error)

	// BatchSize caps items dispatched per cycle.
	BatchSize int

	// SpawnDelay between dispatches.
	SpawnDelay time.Duration

	// Deadline bounds how long the execute loop may keep launching new
	// dispatches. When non-zero, Run stops starting NEW dispatches once Now()
	// is at or past Deadline, returns the partial progress made so far, and
	// sets the report reason to "deadline". The currently-running Execute is
	// allowed to finish (we only gate BEFORE starting the next one), so a bead
	// is never left half-dispatched by the deadline itself.
	//
	// This exists because the daemon runs `gt scheduler run` under a hard 5m
	// SIGKILL (daemon.dispatchQueuedWork). Without a softer internal deadline,
	// a slow cycle (large queue → slow query + many sequential spawns) is
	// killed mid-loop and the whole process dies — the backlog never drains
	// and every subsequent cycle is just as slow (gu-t6jqq death spiral).
	// Exiting cleanly under the SIGKILL preserves the contexts already closed
	// by OnSuccess, so each cycle makes durable forward progress.
	//
	// Zero value (the default) disables the deadline — Run behaves exactly as
	// before, dispatching the full plan.
	Deadline time.Time

	// Now is a clock seam for tests. Nil means use time.Now.
	Now func() time.Time
}

// now returns the current time via the injected clock seam, or time.Now.
func (c *DispatchCycle) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// DispatchReport summarizes the result of one dispatch cycle.
type DispatchReport struct {
	Dispatched int
	Failed     int
	Skipped    int
	Reason     string // "capacity" | "batch" | "ready" | "none"
}

// Plan returns the dispatch plan without executing. Used for dry-run.
func (c *DispatchCycle) Plan() (DispatchPlan, error) {
	cap, err := c.AvailableCapacity()
	if err != nil {
		return DispatchPlan{}, fmt.Errorf("checking capacity: %w", err)
	}

	pending, err := c.QueryPending()
	if err != nil {
		return DispatchPlan{}, fmt.Errorf("querying pending: %w", err)
	}

	return PlanDispatch(cap, c.BatchSize, pending), nil
}

// onSuccessRetries is the number of times to retry OnSuccess before giving up.
const onSuccessRetries = 2

// Run executes one dispatch cycle: query → plan → execute → report.
func (c *DispatchCycle) Run() (DispatchReport, error) {
	plan, err := c.Plan()
	if err != nil {
		return DispatchReport{}, err
	}

	report := DispatchReport{
		Skipped: plan.Skipped,
		Reason:  plan.Reason,
	}

	for i, b := range plan.ToDispatch {
		// Stop launching NEW dispatches once we're past the deadline. The
		// remaining beads stay queued (their sling-contexts are untouched) and
		// dispatch on a later cycle. Reporting them as Skipped keeps the
		// accounting honest and lets the daemon exit cleanly before its 5m
		// SIGKILL instead of being killed mid-spawn with zero durable progress
		// (gu-t6jqq).
		if !c.Deadline.IsZero() && !c.now().Before(c.Deadline) {
			report.Skipped += len(plan.ToDispatch) - i
			report.Reason = "deadline"
			return report, nil
		}

		if c.Validate != nil {
			if err := c.Validate(b); err != nil {
				report.Failed++
				if c.OnFailure != nil {
					c.OnFailure(b, err)
				}
				continue
			}
		}

		if err := c.Execute(b); err != nil {
			report.Failed++
			if c.OnFailure != nil {
				c.OnFailure(b, err)
			}
			continue
		}

		// OnSuccess must succeed (e.g., closing the sling context) to prevent
		// re-dispatch on the next cycle. Retry before giving up.
		if c.OnSuccess != nil {
			var successErr error
			for attempt := 0; attempt <= onSuccessRetries; attempt++ {
				successErr = c.OnSuccess(b)
				if successErr == nil {
					break
				}
				if attempt < onSuccessRetries {
					time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
				}
			}
			if successErr != nil {
				// OnSuccess failed after retries — do NOT count as dispatched.
				// The dispatch ran but we couldn't close the context, so treat
				// it as a failure to prevent double-dispatch on the next cycle.
				report.Failed++
				if c.OnFailure != nil {
					c.OnFailure(b, &ErrOnSuccessFailed{Err: successErr})
				}
				continue
			}
		}

		report.Dispatched++

		// Inter-spawn delay (skip after last item)
		if c.SpawnDelay > 0 && i < len(plan.ToDispatch)-1 {
			time.Sleep(c.SpawnDelay)
		}
	}

	return report, nil
}
