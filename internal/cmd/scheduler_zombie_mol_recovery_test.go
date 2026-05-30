package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestIsZombieMoleculeCandidate covers the predicate that decides whether
// a work bead is in the gu-w49a "queued but stuck behind an unclaimed
// molecule" state. The contract is intentionally narrow: only act on
// status=open beads that isOrphanMolecule already considers safe to burn.
func TestIsZombieMoleculeCandidate(t *testing.T) {
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
			name:     "open + isOrphan(unassigned) — the gu-w49a wedge",
			info:     &beadInfo{Status: "open", Assignee: ""},
			deadFn:   func(string) bool { return false },
			expected: true,
		},
		{
			name:     "open + isOrphan(literal 'none' assignee) — covers gu-koi7 reset path",
			info:     &beadInfo{Status: "open", Assignee: "none"},
			deadFn:   func(string) bool { return false },
			expected: true,
		},
		{
			name:     "hooked + dead worker — handled elsewhere, not a zombie-mol candidate",
			info:     &beadInfo{Status: "hooked", Assignee: "rig/polecats/dead"},
			deadFn:   func(string) bool { return true },
			expected: false, // status != open
		},
		{
			name:     "in_progress + unassigned — handled by sling burn, not recovery",
			info:     &beadInfo{Status: "in_progress", Assignee: ""},
			deadFn:   func(string) bool { return false },
			expected: false, // status != open
		},
		{
			name:     "blocked — leave alone, real upstream dep may exist",
			info:     &beadInfo{Status: "blocked", Assignee: ""},
			deadFn:   func(string) bool { return false },
			expected: false,
		},
		{
			name:     "closed — never touch closed beads",
			info:     &beadInfo{Status: "closed", Assignee: ""},
			deadFn:   func(string) bool { return false },
			expected: false,
		},
		{
			name:     "open + live worker — isOrphanMolecule returns false",
			info:     &beadInfo{Status: "open", Assignee: "rig/polecats/alive"},
			deadFn:   func(string) bool { return false },
			expected: true, // open path returns true regardless of assignee (sling.go semantics)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isHookedAgentDeadFn = tt.deadFn
			got := isZombieMoleculeCandidate(tt.info)
			if got != tt.expected {
				t.Errorf("isZombieMoleculeCandidate() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestOpenMoleculeDeps verifies the helper that extracts open molecule
// wisp IDs from a bead's dependencies + description. Recovery only burns
// active wisps so closed-but-still-bonded entries must be skipped.
func TestOpenMoleculeDeps(t *testing.T) {
	tests := []struct {
		name string
		info *beadInfo
		want []string
	}{
		{
			name: "nil info",
			info: nil,
			want: nil,
		},
		{
			name: "no deps",
			info: &beadInfo{},
			want: nil,
		},
		{
			name: "single open wisp dep",
			info: &beadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "gu-wisp-zajy", Status: "open"},
				},
			},
			want: []string{"gu-wisp-zajy"},
		},
		{
			name: "skip closed wisp",
			info: &beadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "gu-wisp-stale", Status: "closed"},
					{ID: "gu-wisp-live", Status: "open"},
				},
			},
			want: []string{"gu-wisp-live"},
		},
		{
			name: "skip non-wisp dep (real upstream issue)",
			info: &beadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "gu-abc123", Status: "open"}, // not a wisp
					{ID: "gu-wisp-real", Status: "open"},
				},
			},
			want: []string{"gu-wisp-real"},
		},
		{
			name: "include hooked / in_progress wisps too",
			info: &beadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "gu-wisp-hooked", Status: "hooked"},
					{ID: "gu-wisp-prog", Status: "in_progress"},
				},
			},
			want: []string{"gu-wisp-hooked", "gu-wisp-prog"},
		},
		{
			name: "dedup deps + description pointer",
			info: &beadInfo{
				Description: "attached_molecule: gu-wisp-zajy\n",
				Dependencies: []beads.IssueDep{
					{ID: "gu-wisp-zajy", Status: "open"},
				},
			},
			want: []string{"gu-wisp-zajy"},
		},
		{
			name: "description pointer not in deps — still picked up",
			info: &beadInfo{
				Description: "attached_molecule: gu-wisp-orphan\n",
			},
			want: []string{"gu-wisp-orphan"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := openMoleculeDeps(tt.info)
			if !equalStringSlice(got, tt.want) {
				t.Errorf("openMoleculeDeps() = %v, want %v", got, tt.want)
			}
		})
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
