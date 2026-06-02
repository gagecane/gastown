package cmd

import "testing"

// The pure orphan-molecule decision (status/assignee table) now lives in
// internal/dispatch (gu-y5z8d); see dispatch.TestIsOrphanMolecule_TableDriven.
// This test covers only the cmd-side wrapper, which injects isHookedAgentDeadFn
// (the tmux-session liveness check) into dispatch.IsOrphanMolecule.
func TestIsOrphanMolecule_WrapperInjectsDeadFn(t *testing.T) {
	prev := isHookedAgentDeadFn
	t.Cleanup(func() { isHookedAgentDeadFn = prev })

	// Session reported alive → bead with a live assignee is not an orphan.
	isHookedAgentDeadFn = func(string) bool { return false }
	if isOrphanMolecule(&beadInfo{Status: "hooked", Assignee: "rig/polecats/Toast"}) {
		t.Error("isOrphanMolecule(alive assignee) = true, want false")
	}

	// Session reported dead → bead with that assignee is an orphan.
	isHookedAgentDeadFn = func(string) bool { return true }
	if !isOrphanMolecule(&beadInfo{Status: "hooked", Assignee: "rig/polecats/Toast"}) {
		t.Error("isOrphanMolecule(dead assignee) = false, want true")
	}
}
