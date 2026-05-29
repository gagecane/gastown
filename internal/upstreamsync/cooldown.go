// Adaptive cooldown evaluator for upstream sync.
//
// Phase 3 (gu-nw6g). The deacon patrol calls IsDue once per rig per
// patrol tick to decide whether to invoke `gt upstream sync`. This file
// is pure-function, side-effect-free policy: it consumes the per-rig
// state metadata (loaded from the pinned bead) plus the rig config and
// returns a verdict. All I/O — git fetch, bead reads, sync execution —
// happens at the call sites.
//
// The cooldown is "adaptive" in the design's sense (cv-2s6tq/scale.md
// §"Option 3"): the base cadence comes from rig config, and the
// effective cadence shortens when the upstream is hot, lengthens when
// the upstream is dormant, and lengthens further when consecutive
// failures accumulate (so a wedged rig doesn't burn polecat slots
// retrying every 6h).
//
// The verdict also carries a SkipReason so observability is preserved:
// the deacon patrol logs why it didn't sync rather than dropping the
// decision on the floor.
//
// Design context: .designs/cv-2s6tq/scale.md §"Adaptive cooldown" and
// .designs/cv-2s6tq/data.md §"Cooldown gate".
package upstreamsync

import (
	"fmt"
	"time"
)

// CooldownDecision captures the verdict produced by IsDue: should the
// patrol trigger a sync now, and if not, why not?
type CooldownDecision struct {
	// Due indicates whether the rig is eligible for a sync attempt now.
	Due bool

	// SkipReason is a short human-readable reason emitted when Due is
	// false. Empty when Due is true. Stable strings keep observability
	// dashboards predictable.
	SkipReason string

	// EffectiveCadence is the adaptive cadence the evaluator picked
	// (base * adjustments, clamped to bounds). Reported even when Due
	// is true so operators can see what the next interval will be.
	EffectiveCadence time.Duration

	// NextDueAt is the timestamp at which the rig will next be eligible
	// for a sync (computed from the last attempt's CompletedAt plus
	// EffectiveCadence). Zero when there is no recorded last attempt.
	NextDueAt time.Time
}

// CooldownPolicy tunes the adaptive evaluator. The zero value uses the
// design's recommended defaults; callers usually only override the
// floor/ceiling for tests.
type CooldownPolicy struct {
	// MinCadence is the floor on the effective cadence — the patrol
	// will never sync more often than this even if the upstream is
	// extremely hot. Default: BaseCadence / 4.
	MinCadence time.Duration

	// MaxCadence is the ceiling on the effective cadence — long
	// dormant periods or stuck failure runs cap here so an operator
	// can always observe at least one attempt per MaxCadence.
	// Default: BaseCadence * 4.
	MaxCadence time.Duration

	// FailureBackoffFactor multiplies the cadence by this number for
	// each consecutive failure beyond the first. Default: 2.0.
	// Example: 0 failures → no backoff, 1 failure → 1x, 2 → 2x, 3 → 4x.
	FailureBackoffFactor float64

	// HotMultiplier shrinks the cadence when the upstream is hot
	// (last sync had material commits). Default: 0.66 (≈ 6h → 4h).
	HotMultiplier float64

	// DormantMultiplier stretches the cadence when the upstream is
	// dormant (last attempt skipped because nothing to do). Default:
	// 1.5 (≈ 6h → 9h).
	DormantMultiplier float64

	// HotCommitThreshold is the number of upstream commits an attempt
	// must have caught up to count as "hot". Below this, the upstream
	// is treated as warm/normal (no multiplier). Default: 3.
	HotCommitThreshold int
}

// DefaultCooldownPolicy returns the design-default tuning. Callers
// typically pass this unchanged; tests pin values for determinism.
func DefaultCooldownPolicy() CooldownPolicy {
	return CooldownPolicy{
		FailureBackoffFactor: 2.0,
		HotMultiplier:        2.0 / 3.0,
		DormantMultiplier:    1.5,
		HotCommitThreshold:   3,
	}
}

// resolveBounds fills in the floor/ceiling from the base cadence when
// the policy left them at zero.
func (p CooldownPolicy) resolveBounds(base time.Duration) (min, max time.Duration) {
	min = p.MinCadence
	max = p.MaxCadence
	if min <= 0 {
		min = base / 4
		if min <= 0 {
			min = time.Minute
		}
	}
	if max <= 0 {
		max = base * 4
	}
	if min > max {
		min = max
	}
	return min, max
}

// resolveMultipliers fills in any zero defaults so callers can leave
// the whole struct at zero and get sensible behavior.
func (p CooldownPolicy) resolveMultipliers() CooldownPolicy {
	out := p
	if out.FailureBackoffFactor <= 0 {
		out.FailureBackoffFactor = 2.0
	}
	if out.HotMultiplier <= 0 {
		out.HotMultiplier = 2.0 / 3.0
	}
	if out.DormantMultiplier <= 0 {
		out.DormantMultiplier = 1.5
	}
	if out.HotCommitThreshold <= 0 {
		out.HotCommitThreshold = 3
	}
	return out
}

// IsDue reports whether `state` is eligible for a sync attempt at `now`
// under the given base cadence (from rig config) and policy. It does
// NOT touch git; it only looks at the bead state. Callers should
// short-circuit further on `state.State == StatePaused` (the deacon
// patrol does this before calling IsDue, but defense in depth is
// cheap).
//
// The verdict's EffectiveCadence is the cadence the patrol should use
// when scheduling the *next* eligibility check. NextDueAt is the
// derived timestamp (zero when the rig has never synced — first run is
// always "Due").
func IsDue(state SyncStateMetadata, baseCadence time.Duration, policy CooldownPolicy, now time.Time) CooldownDecision {
	policy = policy.resolveMultipliers()
	min, max := policy.resolveBounds(baseCadence)

	// Never sync a paused rig.
	if state.State == StatePaused {
		return CooldownDecision{
			Due:              false,
			SkipReason:       "paused",
			EffectiveCadence: clampDuration(baseCadence, min, max),
		}
	}

	// Never sync a busy rig — the in-progress attempt holds the lock.
	switch state.State {
	case StateChecking, StateSyncing, StateResolving, StateGating, StatePushing:
		return CooldownDecision{
			Due:              false,
			SkipReason:       fmt.Sprintf("busy:%s", state.State),
			EffectiveCadence: clampDuration(baseCadence, min, max),
		}
	}

	// Compute the effective cadence from the design's three signals:
	// upstream heat, dormant streak, and consecutive failures.
	effective := adjustCadence(state, baseCadence, policy)
	effective = clampDuration(effective, min, max)

	// First-run: no recorded attempt → always Due. Use baseCadence as
	// the projected next interval after the upcoming sync completes.
	last := lastCompletedAttempt(state)
	if last == nil {
		return CooldownDecision{
			Due:              true,
			EffectiveCadence: effective,
		}
	}

	completedAt, err := time.Parse(time.RFC3339, last.CompletedAt)
	if err != nil || completedAt.IsZero() {
		// Malformed timestamp — treat as Due rather than wedge the rig.
		// This matches the auto-test-pr robustness pattern: corrupted
		// bookkeeping should not block forward progress.
		return CooldownDecision{
			Due:              true,
			EffectiveCadence: effective,
		}
	}

	nextDue := completedAt.Add(effective)
	if !now.Before(nextDue) {
		return CooldownDecision{
			Due:              true,
			EffectiveCadence: effective,
			NextDueAt:        nextDue,
		}
	}

	return CooldownDecision{
		Due:              false,
		SkipReason:       fmt.Sprintf("cooldown:%s remaining", nextDue.Sub(now).Round(time.Minute)),
		EffectiveCadence: effective,
		NextDueAt:        nextDue,
	}
}

// adjustCadence applies the heat/dormancy/failure multipliers to the
// base cadence. The math is intentionally simple — composability over
// elegance — so operators can predict behavior from the input signals.
func adjustCadence(state SyncStateMetadata, base time.Duration, policy CooldownPolicy) time.Duration {
	if base <= 0 {
		return 0
	}
	out := float64(base)

	last := lastCompletedAttempt(state)
	switch {
	case last == nil:
		// No history yet; base cadence governs.
	case last.Outcome == "skipped":
		// Upstream was dormant on the last check — back off.
		out *= policy.DormantMultiplier
	case last.Outcome == "success":
		// Successful sync. If we caught up many commits, the upstream
		// is hot — pull the next check forward.
		if commitsCaughtUp(last) >= policy.HotCommitThreshold {
			out *= policy.HotMultiplier
		}
	}

	// Consecutive failures stretch the cadence. The first failure does
	// not back off (operators may want to retry quickly after a
	// flake); from the second onward we double per extra failure.
	if state.ConsecutiveFailures > 1 {
		exponent := state.ConsecutiveFailures - 1
		factor := pow(policy.FailureBackoffFactor, exponent)
		out *= factor
	}

	return time.Duration(out)
}

// commitsCaughtUp is a best-effort count of how many upstream commits
// an attempt absorbed. We don't store this directly — the bead has
// pre/post SHAs and a strategy — so we approximate based on whether
// the attempt actually moved HEAD. If not, we treat it as zero
// (skipped/no-op syncs do not signal heat).
//
// A future enhancement (when the SyncAttempt schema gains a
// CommitsBehind field) would replace this approximation with the real
// count.
func commitsCaughtUp(a *SyncAttempt) int {
	if a == nil || a.PreSyncSHA == "" || a.PostSyncSHA == "" {
		return 0
	}
	if a.PreSyncSHA == a.PostSyncSHA {
		return 0
	}
	// Pre and Post differ → attempt moved HEAD. We can't recover the
	// commit count from SHAs alone here, so we use the design's
	// HotCommitThreshold as the lower bound: if HEAD moved at all,
	// treat the upstream as at least "warm-hot" so multipliers fire.
	// This intentionally biases toward shorter cadences when sync is
	// productive — the cost of an extra fetch is trivial vs. running
	// behind upstream.
	return 999
}

// lastCompletedAttempt returns the most recent attempt with a
// CompletedAt timestamp, or nil if there is no history. The Attempts
// slice is FIFO with the newest entry last.
func lastCompletedAttempt(state SyncStateMetadata) *SyncAttempt {
	for i := len(state.Attempts) - 1; i >= 0; i-- {
		if state.Attempts[i].CompletedAt != "" {
			a := state.Attempts[i] // copy to detach from slice
			return &a
		}
	}
	return nil
}

// clampDuration constrains d to [min, max].
func clampDuration(d, min, max time.Duration) time.Duration {
	if d < min {
		return min
	}
	if d > max {
		return max
	}
	return d
}

// pow is a small integer-exponent power (no math.Pow dependency for
// such a tiny use case; also keeps result deterministic).
func pow(base float64, exp int) float64 {
	if exp <= 0 {
		return 1
	}
	out := 1.0
	for i := 0; i < exp; i++ {
		out *= base
	}
	return out
}
