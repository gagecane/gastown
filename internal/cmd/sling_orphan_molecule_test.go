package cmd

import "testing"

// TestIsOrphanMolecule_HookedNoAssignee covers gh-3697: a bead stuck in
// status=hooked with empty assignee was not auto-burnable, so any further
// sling to the rig refused with "bead already has N attached molecule(s)".
// The fix treats this state as orphaned so sling can self-heal.
func TestIsOrphanMolecule_HookedNoAssignee(t *testing.T) {
	prev := isHookedAgentDeadFn
	t.Cleanup(func() { isHookedAgentDeadFn = prev })
	isHookedAgentDeadFn = func(string) bool { return false }

	info := &beadInfo{Status: "hooked", Assignee: ""}
	if !isOrphanMolecule(info) {
		t.Errorf("isOrphanMolecule(status=hooked, assignee='') = false, want true (gh-3697)")
	}
}

func TestIsOrphanMolecule_TableDriven(t *testing.T) {
	prev := isHookedAgentDeadFn
	t.Cleanup(func() { isHookedAgentDeadFn = prev })

	tests := []struct {
		name     string
		info     *beadInfo
		deadFn   func(string) bool
		expected bool
	}{
		{
			name:     "nil info",
			info:     nil,
			deadFn:   func(string) bool { return false },
			expected: false,
		},
		{
			name:     "open, no assignee — orphan from sling crash",
			info:     &beadInfo{Status: "open", Assignee: ""},
			deadFn:   func(string) bool { return false },
			expected: true,
		},
		{
			name:     "in_progress, no assignee — orphan from sling crash",
			info:     &beadInfo{Status: "in_progress", Assignee: ""},
			deadFn:   func(string) bool { return false },
			expected: true,
		},
		{
			name:     "hooked, no assignee — gh-3697 wedge",
			info:     &beadInfo{Status: "hooked", Assignee: ""},
			deadFn:   func(string) bool { return false },
			expected: true,
		},
		{
			name:     "closed, no assignee — keep refuse path",
			info:     &beadInfo{Status: "closed", Assignee: ""},
			deadFn:   func(string) bool { return false },
			expected: false,
		},
		{
			name:     "blocked, no assignee — keep refuse path",
			info:     &beadInfo{Status: "blocked", Assignee: ""},
			deadFn:   func(string) bool { return false },
			expected: false,
		},
		{
			name:     "hooked, assignee, session alive — refuse",
			info:     &beadInfo{Status: "hooked", Assignee: "rig/polecats/Toast"},
			deadFn:   func(string) bool { return false },
			expected: false,
		},
		{
			name:     "hooked, assignee, session dead — auto-burn",
			info:     &beadInfo{Status: "hooked", Assignee: "rig/polecats/Toast"},
			deadFn:   func(string) bool { return true },
			expected: true,
		},
		// gu-koi7: operator workaround `bd update --assignee none` stores the
		// literal string "none". Without normalization, isHookedAgentDeadFn("none")
		// returns false (unknown format), so the bead would refuse re-sling.
		// Status=open is itself sufficient to declare the molecule orphan.
		{
			name:     "open, assignee=none — operator reset workaround (gu-koi7)",
			info:     &beadInfo{Status: "open", Assignee: "none"},
			deadFn:   func(string) bool { return false },
			expected: true,
		},
		{
			name:     "open, assignee=NONE (case) — operator reset workaround (gu-koi7)",
			info:     &beadInfo{Status: "open", Assignee: "NONE"},
			deadFn:   func(string) bool { return false },
			expected: true,
		},
		{
			name:     "open, stale assignee from prior dead polecat — gu-koi7",
			info:     &beadInfo{Status: "open", Assignee: "rig/polecats/dead"},
			deadFn:   func(string) bool { return false }, // even if not detected dead
			expected: true,
		},
		{
			name:     "in_progress, assignee=none — sentinel honored",
			info:     &beadInfo{Status: "in_progress", Assignee: "none"},
			deadFn:   func(string) bool { return false },
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isHookedAgentDeadFn = tt.deadFn
			got := isOrphanMolecule(tt.info)
			if got != tt.expected {
				t.Errorf("isOrphanMolecule() = %v, want %v", got, tt.expected)
			}
		})
	}
}
