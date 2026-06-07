package daemon

import (
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestIsStalledReclaimCandidate locks down which agent states the cleanly-stalled
// reclaim patrol (gs-9wz) will OFFER to `gt polecat nuke`. It's a coarse
// exclusion filter — nuke's safety check is the real authority — so the contract
// is: preserve idle/terminal/in-flight/deliberate-hold states, and offer every
// other (abnormal/crashed) state for reclaim. The earlier allow-list of only
// working/running silently missed real debris (e.g. a clean, no-hook,
// dead-session polecat left in agent_state=stuck — observed live as
// lia_bac/rictus), so stuck/escalated/stalled/empty must now be candidates.
func TestIsStalledReclaimCandidate(t *testing.T) {
	cases := []struct {
		state beads.AgentState
		want  bool
		why   string
	}{
		// Abnormal/crashed states whose dead-session husk is debris. nuke still
		// gates the actual destroy on clean+no-hook+no-MR.
		{beads.AgentStateWorking, true, "working+dead is debris"},
		{beads.AgentStateRunning, true, "running+dead is debris"},
		{beads.AgentStateStuck, true, "stuck+dead+clean is reclaimable debris (rictus)"},
		{beads.AgentStateEscalated, true, "escalated+dead+clean is reclaimable debris"},
		{beads.AgentState("stalled"), true, "computed stalled state is debris"},
		{beads.AgentState(""), true, "empty/unknown is offered; nuke arbitrates"},

		// Preserve these — never offered for reclaim.
		{beads.AgentStateIdle, false, "idle is a reusable warm-pool slot"},
		{beads.AgentStateDone, false, "done is terminal"},
		{beads.AgentStateNuked, false, "nuked is terminal/already reclaimed"},
		{beads.AgentStateSpawning, false, "spawning is an in-flight sling launch"},
		{beads.AgentStatePaused, false, "paused is a deliberate operator hold"},
		{beads.AgentStateAwaitingGate, false, "awaiting-gate is a deliberate protocol wait"},
	}

	for _, tc := range cases {
		if got := isStalledReclaimCandidate(tc.state); got != tc.want {
			t.Errorf("isStalledReclaimCandidate(%q) = %v, want %v (%s)", tc.state, got, tc.want, tc.why)
		}
	}
}

// TestIsReclaimCandidate covers the hq-uzubf addition: idle polecats that are
// WEDGED (recovery_blocked via a residual) must be offered for reclaim, while
// reusable-idle warm slots (clean, no failure flags) must be preserved. nuke's
// own safety check is still the authority on whether the residual is benign.
func TestIsReclaimCandidate(t *testing.T) {
	cases := []struct {
		name string
		info *AgentBeadInfo
		want bool
	}{
		// Reusable-idle warm slot — clean, no flags, no residual: PRESERVE.
		{"idle clean reusable", &AgentBeadInfo{State: "idle", CleanupStatus: "clean"}, false},
		{"idle empty cleanup (unknown — leave to nuke)", &AgentBeadInfo{State: "idle", CleanupStatus: ""}, false},

		// Idle but WEDGED — a residual makes it recovery_blocked: RECLAIM.
		{"idle has_uncommitted", &AgentBeadInfo{State: "idle", CleanupStatus: "has_uncommitted"}, true},
		{"idle has_unpushed", &AgentBeadInfo{State: "idle", CleanupStatus: "has_unpushed"}, true},
		{"idle push-failed flag", &AgentBeadInfo{State: "idle", CleanupStatus: "clean", PushFailed: true}, true},
		{"idle mr-failed flag", &AgentBeadInfo{State: "idle", CleanupStatus: "clean", MRFailed: true}, true},

		// Non-idle abnormal states still qualify via isStalledReclaimCandidate.
		{"working", &AgentBeadInfo{State: "working"}, true},
		{"stuck", &AgentBeadInfo{State: "stuck"}, true},

		// Preserve states are never candidates even with a flag.
		{"done", &AgentBeadInfo{State: "done", PushFailed: true}, false},
		{"nuked", &AgentBeadInfo{State: "nuked"}, false},
		{"spawning", &AgentBeadInfo{State: "spawning"}, false},
		{"paused", &AgentBeadInfo{State: "paused"}, false},
	}
	for _, tc := range cases {
		if got := isReclaimCandidate(tc.info); got != tc.want {
			t.Errorf("%s: isReclaimCandidate(%+v) = %v, want %v", tc.name, tc.info, got, tc.want)
		}
	}
}

// TestWarmPoolTrimBudget locks down the pool-pressure policy (gu-kii6z): no
// trimming below the high-water mark (warm pool preserved for fast reuse), and
// once at/above it, trim down to the low-water mark — never below. This is what
// keeps idle-clean dead-session husks (skipped by both reapers) from filling
// the maxPolecatDirsPerRig spawn cap and wedging dispatch.
func TestWarmPoolTrimBudget(t *testing.T) {
	cases := []struct {
		dirCount int
		want     int
	}{
		{0, 0},
		{polecatWarmPoolLowWater, 0},      // below high-water: preserve
		{polecatWarmPoolHighWater - 1, 0}, // just below: preserve
		{polecatWarmPoolHighWater, polecatWarmPoolHighWater - polecatWarmPoolLowWater}, // at high-water: trim to low
		{30, 30 - polecatWarmPoolLowWater},                                             // at spawn cap: trim hard to low-water
	}
	for _, tc := range cases {
		got := warmPoolTrimBudget(tc.dirCount)
		if got != tc.want {
			t.Errorf("warmPoolTrimBudget(%d) = %d, want %d", tc.dirCount, got, tc.want)
		}
		// Invariant: trimming never drops a rig below the low-water mark.
		if remaining := tc.dirCount - got; got > 0 && remaining < polecatWarmPoolLowWater {
			t.Errorf("warmPoolTrimBudget(%d)=%d leaves %d dirs, below low-water %d",
				tc.dirCount, got, remaining, polecatWarmPoolLowWater)
		}
	}
}

// TestIsWarmPoolHusk verifies the husk classifier matches exactly the
// reusable-idle warm slot (state=idle, clean, no flags, no active MR) — the
// population isReclaimCandidate deliberately preserves but that the
// pool-pressure trim removes when a rig is oversized (gu-kii6z). Anything with a
// residual, a non-idle state, or an active MR is NOT a husk (left to the
// existing debris/recovery paths).
func TestIsWarmPoolHusk(t *testing.T) {
	cases := []struct {
		name string
		info *AgentBeadInfo
		want bool
	}{
		{"idle clean reusable husk", &AgentBeadInfo{State: "idle", CleanupStatus: "clean"}, true},

		{"idle empty cleanup (unknown — not a clean husk)", &AgentBeadInfo{State: "idle", CleanupStatus: ""}, false},
		{"idle has_uncommitted (residual)", &AgentBeadInfo{State: "idle", CleanupStatus: "has_uncommitted"}, false},
		{"idle push-failed flag", &AgentBeadInfo{State: "idle", CleanupStatus: "clean", PushFailed: true}, false},
		{"idle mr-failed flag", &AgentBeadInfo{State: "idle", CleanupStatus: "clean", MRFailed: true}, false},
		{"idle active MR", &AgentBeadInfo{State: "idle", CleanupStatus: "clean", ActiveMR: "gt-mr-1"}, false},

		{"working", &AgentBeadInfo{State: "working", CleanupStatus: "clean"}, false},
		{"stuck", &AgentBeadInfo{State: "stuck", CleanupStatus: "clean"}, false},
		{"done", &AgentBeadInfo{State: "done", CleanupStatus: "clean"}, false},
		{"nuked", &AgentBeadInfo{State: "nuked", CleanupStatus: "clean"}, false},
	}
	for _, tc := range cases {
		if got := isWarmPoolHusk(tc.info); got != tc.want {
			t.Errorf("%s: isWarmPoolHusk(%+v) = %v, want %v", tc.name, tc.info, got, tc.want)
		}
	}
}
