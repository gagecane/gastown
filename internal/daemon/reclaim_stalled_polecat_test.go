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
