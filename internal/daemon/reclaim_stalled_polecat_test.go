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
