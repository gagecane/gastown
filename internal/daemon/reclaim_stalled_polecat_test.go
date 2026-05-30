package daemon

import (
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestIsStalledReclaimCandidate locks down exactly which agent states the
// cleanly-stalled reclaim patrol (gs-9wz) will act on. Only the active states a
// polecat should have transitioned out of on a clean gt done qualify; every
// terminal/idle/deliberate-hold/in-flight state must be excluded so the reaper
// never destroys a warm-pool slot, an intentional hold, or a spawning polecat.
func TestIsStalledReclaimCandidate(t *testing.T) {
	cases := []struct {
		state beads.AgentState
		want  bool
		why   string
	}{
		// The stalled signature: active states a dead-session polecat never
		// transitioned out of. These count as recovery_blocked and are the debris.
		{beads.AgentStateWorking, true, "working+dead is the stalled signature"},
		{beads.AgentStateRunning, true, "running+dead is the stalled signature"},

		// Already counted correctly — never reclaim these.
		{beads.AgentStateIdle, false, "idle is a reusable warm-pool slot"},
		{beads.AgentStateDone, false, "done is terminal"},
		{beads.AgentStateNuked, false, "nuked is terminal"},

		// Deliberate holds / in-flight — must never be auto-nuked.
		{beads.AgentStateStuck, false, "stuck is a deliberate help-needed hold"},
		{beads.AgentStateEscalated, false, "escalated is a deliberate hold"},
		{beads.AgentStateAwaitingGate, false, "awaiting-gate is a deliberate hold"},
		{beads.AgentStatePaused, false, "paused is a deliberate hold"},
		{beads.AgentStateSpawning, false, "spawning is in-flight"},

		// Unknown/empty state is ambiguous — exclude (don't destroy on a guess).
		{beads.AgentState(""), false, "empty state is ambiguous"},
		{beads.AgentState("patrolling"), false, "non-polecat state, exclude"},
	}

	for _, tc := range cases {
		if got := isStalledReclaimCandidate(tc.state); got != tc.want {
			t.Errorf("isStalledReclaimCandidate(%q) = %v, want %v (%s)", tc.state, got, tc.want, tc.why)
		}
	}
}
