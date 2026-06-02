// D18 cooldown-release transition for the auto-test-pr cycle.
//
// Phase 0 task 4 (gu-2n7xi). PRD scenario S1 ("twice a week the
// mechanism wakes up") requires the cycle to re-fire on a per-rig
// cadence after a prior MR lands. The state machine as originally drawn
// had no edge out of `cooled-down`, so a rig would fire once and never
// again. D18 adds a Mayor-driven step, run before the per-rig state
// read each tick: for each opted-in rig in `cooled-down`, if
// `now - last_transition.at >= cadence_days * 24h`, CAS-transition
// `cooled-down → idle`.
//
// Crucially, a rig in `paused-by-circuit-breaker` (D16) does NOT
// auto-release — it requires an explicit operator resume.
//
// Design context:
//   - .designs/auto-test-pr/synthesis.md §D18, §R22.
package autotestpr

import (
	"errors"
	"fmt"
	"time"
)

// ShouldReleaseCooldown reports whether a rig in `cooled-down` is
// eligible to transition back to `idle` on this tick. The decision is a
// pure function of the current state, the timestamp of the last
// transition, the cadence, and now:
//
//   - state must be exactly `cooled-down`. `paused-by-circuit-breaker`
//     returns false (never auto-releases — D18); any other state is not
//     in cooldown and returns false.
//   - lastTransitionAt must be non-zero (a rig with no recorded
//     transition cannot have its cadence evaluated — returns false,
//     leaving it cooled-down until a transition is recorded).
//   - released iff now - lastTransitionAt >= cadenceDays * 24h.
func ShouldReleaseCooldown(state PerRigCycleState, lastTransitionAt time.Time, cadenceDays int, now time.Time) bool {
	if state != PerRigCycleStateCooledDown {
		return false
	}
	if lastTransitionAt.IsZero() {
		return false
	}
	if cadenceDays <= 0 {
		cadenceDays = DefaultCadenceDaysFallback
	}
	elapsed := now.Sub(lastTransitionAt)
	return elapsed >= time.Duration(cadenceDays)*24*time.Hour
}

// DefaultCadenceDaysFallback mirrors config.DefaultAutoTestPRCadenceDays
// for the rare case ShouldReleaseCooldown is called with a non-positive
// cadence. Duplicated as a small constant to avoid an import cycle with
// the config package's loader.
const DefaultCadenceDaysFallback = 7

// ReleaseCooldownIfElapsed evaluates the D18 cooldown-release condition
// for a single rig and, if eligible, CAS-transitions cooled-down→idle
// with `cadence-elapsed` as the transition trigger.
//
// Returns (released, err):
//   - released=true  → the rig was transitioned to idle this tick.
//   - released=false, err=nil → not eligible (wrong state, cadence not
//     elapsed, or a benign concurrent transition won the CAS race).
//   - err!=nil → an unexpected store error the caller should log.
//
// A lost CAS race (ErrTransitionConflict) is benign per D18 — next tick
// retries — so it is reported as released=false, err=nil.
func ReleaseCooldownIfElapsed(
	store RigStateStore,
	rig string,
	lastTransitionAt time.Time,
	cadenceDays int,
	now time.Time,
) (bool, error) {
	if store == nil {
		return false, fmt.Errorf("ReleaseCooldownIfElapsed: nil store")
	}

	s, err := store.LoadRigState(rig)
	if err != nil {
		return false, fmt.Errorf("loading rig state for %s: %w", rig, err)
	}

	if !ShouldReleaseCooldown(s.State, lastTransitionAt, cadenceDays, now) {
		return false, nil
	}

	err = CASTransition(store, rig,
		PerRigCycleStateCooledDown, PerRigCycleStateIdle,
		"mayor", now,
		func(rs *RigState) {
			// Cooldown release clears any in-flight cycle pointer.
			rs.CurrentCycle = nil
		})
	if err != nil {
		// A concurrent transition (another tick beat us) is benign.
		if errors.Is(err, ErrTransitionConflict) {
			return false, nil
		}
		return false, fmt.Errorf("releasing cooldown for %s: %w", rig, err)
	}
	return true, nil
}
