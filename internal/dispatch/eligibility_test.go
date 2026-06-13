package dispatch

import (
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestIsDeferredBead(t *testing.T) {
	tests := []struct {
		name string
		info *BeadInfo
		want bool
	}{
		{"open bead is not deferred", &BeadInfo{Status: "open", Description: "some task"}, false},
		{"in_progress bead is not deferred", &BeadInfo{Status: "in_progress", Description: "working on it"}, false},
		{"deferred status", &BeadInfo{Status: "deferred", Description: "some task"}, true},
		{"description says deferred to post-launch", &BeadInfo{Status: "open", Description: "deferred to post-launch"}, true},
		{"description says deferred to post launch", &BeadInfo{Status: "open", Description: "deferred to post launch"}, true},
		{"description says status: deferred", &BeadInfo{Status: "open", Description: "status: deferred\nsome other notes"}, true},
		{"case insensitive description", &BeadInfo{Status: "open", Description: "Deferred to Post-Launch"}, true},
		{"deferred keyword not in deferral phrase", &BeadInfo{Status: "open", Description: "the user deferred this action"}, false},
		{"empty description", &BeadInfo{Status: "open", Description: ""}, false},
		{"hooked bead not deferred", &BeadInfo{Status: "hooked", Description: "some work"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDeferredBead(tt.info); got != tt.want {
				t.Errorf("IsDeferredBead(%+v) = %v, want %v", tt.info, got, tt.want)
			}
		})
	}
}

// TestIsDeferredBeadDeferUntil exercises the future-defer_until branch (gu-fyey5)
// with a fixed clock so the result is deterministic. `bd update --defer` keeps
// status="open" while hiding the bead from `bd ready`; the sling dispatch guard
// must treat a future defer_until as deferred.
func TestIsDeferredBeadDeferUntil(t *testing.T) {
	now := time.Date(2026, 6, 13, 1, 4, 51, 0, time.UTC)
	tests := []struct {
		name string
		info *BeadInfo
		want bool
	}{
		{
			"open bead with future defer_until is deferred",
			&BeadInfo{Status: "open", DeferUntil: now.Add(24 * time.Hour).Format(time.RFC3339)},
			true,
		},
		{
			"open bead with far-future defer_until is deferred (gu-27art repro)",
			&BeadInfo{Status: "open", DeferUntil: "2026-09-11T00:00:00Z"},
			true,
		},
		{
			"open bead with expired defer_until is dispatchable",
			&BeadInfo{Status: "open", DeferUntil: now.Add(-time.Hour).Format(time.RFC3339)},
			false,
		},
		{
			"open bead with defer_until exactly now is dispatchable",
			&BeadInfo{Status: "open", DeferUntil: now.Format(time.RFC3339)},
			false,
		},
		{
			"empty defer_until is not deferred",
			&BeadInfo{Status: "open", DeferUntil: ""},
			false,
		},
		{
			"unparseable defer_until falls through to not-deferred",
			&BeadInfo{Status: "open", DeferUntil: "not-a-timestamp"},
			false,
		},
		{
			"deferred status still wins regardless of defer_until",
			&BeadInfo{Status: "deferred", DeferUntil: now.Add(-time.Hour).Format(time.RFC3339)},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDeferredBeadAt(tt.info, now); got != tt.want {
				t.Errorf("isDeferredBeadAt(%+v) = %v, want %v", tt.info, got, tt.want)
			}
		})
	}
}

func TestCollectExistingMoleculesFiltersClosedMolecules(t *testing.T) {
	tests := []struct {
		name string
		info *BeadInfo
		want []string
	}{
		{
			name: "open molecule is collected",
			info: &BeadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "bd-wisp-abc", Status: "open"},
				},
			},
			want: []string{"bd-wisp-abc"},
		},
		{
			name: "closed molecule is skipped",
			info: &BeadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "bd-wisp-abc", Status: "closed"},
				},
			},
			want: nil,
		},
		{
			name: "tombstone molecule is skipped",
			info: &BeadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "bd-wisp-abc", Status: "tombstone"},
				},
			},
			want: nil,
		},
		{
			name: "mixed: open kept, closed skipped",
			info: &BeadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "bd-wisp-dead", Status: "closed"},
					{ID: "bd-wisp-live", Status: "in_progress"},
				},
			},
			want: []string{"bd-wisp-live"},
		},
		{
			name: "non-wisp dependency ignored regardless of status",
			info: &BeadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "bd-regular-dep", Status: "open"},
				},
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CollectExistingMolecules(tt.info)
			if len(got) != len(tt.want) {
				t.Fatalf("CollectExistingMolecules() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("CollectExistingMolecules()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestIsAgentBead(t *testing.T) {
	tests := []struct {
		name string
		info *BeadInfo
		want bool
	}{
		{"nil", nil, false},
		{"empty", &BeadInfo{}, false},
		{"task with no agent signal", &BeadInfo{IssueType: "task", Labels: []string{"gt:task"}}, false},
		{"bug bead", &BeadInfo{IssueType: "bug", Labels: []string{"gt:bug", "infra"}}, false},
		{"gt:agent label (current standard)", &BeadInfo{IssueType: "task", Labels: []string{"gt:agent"}}, true},
		{"gt:agent label among others", &BeadInfo{IssueType: "task", Labels: []string{"idle:3", "gt:agent", "role:polecat"}}, true},
		{"legacy issue_type=agent", &BeadInfo{IssueType: "agent"}, true},
		{"legacy type + label (both)", &BeadInfo{IssueType: "agent", Labels: []string{"gt:agent"}}, true},
		{"similar label does not match", &BeadInfo{IssueType: "task", Labels: []string{"gt:agentless", "agent"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAgentBead(tt.info); got != tt.want {
				t.Errorf("IsAgentBead(%+v) = %v, want %v", tt.info, got, tt.want)
			}
		})
	}
}

// TestIsIdentityBeadInfo verifies the broader dispatch-gate filter (gu-3znx).
// Identity beads — by label, closed status, or polecat/refinery title regex —
// must never be dispatched as work via any sling path. A real task bead
// (no agent signal, open/in_progress, non-identity title) must still pass.
func TestIsIdentityBeadInfo(t *testing.T) {
	tests := []struct {
		name string
		info *BeadInfo
		want bool
	}{
		{"nil", nil, false},
		{"empty", &BeadInfo{}, false},

		// Real work beads — must NOT be classified as identity.
		{"plain open task", &BeadInfo{Title: "Fix bug in parser", Status: "open", IssueType: "task"}, false},
		{"in_progress bug", &BeadInfo{Title: "Implement feature X", Status: "in_progress", IssueType: "bug"}, false},
		{"hooked task", &BeadInfo{Title: "Add retry logic", Status: "hooked", IssueType: "task"}, false},

		// Label criterion.
		{"gt:agent label", &BeadInfo{Title: "any", Status: "open", Labels: []string{"gt:agent"}}, true},
		{"legacy type=agent", &BeadInfo{Title: "any", Status: "open", IssueType: "agent"}, true},

		// Status criterion.
		{"closed status", &BeadInfo{Title: "any", Status: "closed", IssueType: "task"}, true},

		// Title regex criterion (the path sling missed in gu-3znx).
		{"cadk refinery identity", &BeadInfo{Title: "cadk-casc_cdk-refinery", Status: "open", IssueType: "task"}, true},
		{"ta witness-style polecat", &BeadInfo{Title: "ta-talontriage-polecat-nux", Status: "open", IssueType: "task"}, true},
		{"ro polecat", &BeadInfo{Title: "ro-ralph-polecat-jasper", Status: "open", IssueType: "task"}, true},

		// Widened title regex (gu-huta): witness / crew / dog / mayor / deacon.
		{"witness identity", &BeadInfo{Title: "gu-gastown-witness", Status: "open", IssueType: "task"}, true},
		{"bd-prefixed witness", &BeadInfo{Title: "bd-beads-witness", Status: "open", IssueType: "task"}, true},
		{"crew identity", &BeadInfo{Title: "gu-gastown-crew-joe", Status: "open", IssueType: "task"}, true},
		{"town dog identity", &BeadInfo{Title: "hq-dog-alpha", Status: "open", IssueType: "task"}, true},
		{"mayor identity", &BeadInfo{Title: "hq-mayor", Status: "open", IssueType: "task"}, true},
		{"deacon identity", &BeadInfo{Title: "hq-deacon", Status: "open", IssueType: "task"}, true},

		// Combined matches.
		{"label + closed", &BeadInfo{Title: "any", Status: "closed", Labels: []string{"gt:agent"}}, true},
		{"all three criteria", &BeadInfo{Title: "af-agentforge-polecat-quartz", Status: "closed", Labels: []string{"gt:agent"}, IssueType: "agent"}, true},

		// Rig identity beads (gs-2j6). Title is just the rig name, which the
		// identity title regex does not match — must be caught by label/type.
		{"gt:rig label", &BeadInfo{Title: "gastown", Status: "open", IssueType: "rig", Labels: []string{"gt:rig"}}, true},
		{"rig type alone", &BeadInfo{Title: "lia_web", Status: "open", IssueType: "rig"}, true},
		{"gt:rig label, missing type", &BeadInfo{Title: "lia_iac", Status: "open", IssueType: "task", Labels: []string{"gt:rig"}}, true},

		// Role definition beads.
		{"gt:role label", &BeadInfo{Title: "crew", Status: "open", IssueType: "role", Labels: []string{"gt:role"}}, true},
		{"role type alone", &BeadInfo{Title: "deacon", Status: "open", IssueType: "role"}, true},

		// role_type criterion (gs-fwu). The per-rig refinery/witness identity
		// beads carry a prose title, no gt:agent label, issue_type=task, and
		// OPEN status — every other filter misses them. role_type is the
		// authoritative marker.
		{"refinery identity by role_type (gs-fwu)", &BeadInfo{Title: "Refinery for gastown - processes merge queue.", Status: "open", IssueType: "task", Description: "Refinery for gastown - processes merge queue.\n\nrole_type: refinery\nrig: gastown\nagent_state: idle"}, true},
		{"crew identity by role_type", &BeadInfo{Title: "Crew worker gagecane in gastown - human-managed persistent workspace.", Status: "open", IssueType: "task", Description: "role_type: crew\nrig: gastown"}, true},
		{"witness identity by role_type, deferred", &BeadInfo{Title: "Witness for lia_web", Status: "deferred", IssueType: "task", Description: "role_type: witness\nrig: lia_web"}, true},

		// Near misses.
		{"title has refinery mid-string but not at end", &BeadInfo{Title: "af-refinery-feature-work", Status: "open", IssueType: "task"}, false},
		{"label looks like agent but is not", &BeadInfo{Title: "Regular work", Status: "open", Labels: []string{"gt:agentless"}}, false},
		{"label looks like rig but is not", &BeadInfo{Title: "Regular work", Status: "open", Labels: []string{"gt:rigid"}}, false},
		// role_type mentioned mid-prose (not a leading key) must NOT trip the
		// guard — otherwise a real work bead describing this very fix would
		// refuse to dispatch (the gs-2no failure mode in reverse).
		{"role_type mentioned mid-sentence is not identity", &BeadInfo{Title: "Fix scheduler so beads with role_type set are excluded", Status: "open", IssueType: "bug", Description: "FIX: the scheduler must hard-exclude beads with role_type set (e.g. role_type:refinery) from the candidate set."}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsIdentityBeadInfo(tt.info); got != tt.want {
				t.Errorf("IsIdentityBeadInfo(%+v) = %v, want %v", tt.info, got, tt.want)
			}
		})
	}
}

// TestIsEpicLikeBeadInfo verifies the gu-smr1 dispatch gate: beads with an
// "EPIC:" title prefix but non-epic issue_type must be rejected. Real epics
// (type=epic) are routed through a different path and should NOT match.
func TestIsEpicLikeBeadInfo(t *testing.T) {
	tests := []struct {
		name string
		info *BeadInfo
		want bool
	}{
		{"nil", nil, false},
		{"empty", &BeadInfo{}, false},

		// Positive: slingable type with EPIC-like title.
		{"task with EPIC: prefix (ta-823 case)", &BeadInfo{
			Title:     "EPIC: Triage Queue...",
			IssueType: "task",
			Status:    "open",
		}, true},
		{"bug with Epic: prefix", &BeadInfo{
			Title:     "Epic: rewrite bug tracker",
			IssueType: "bug",
			Status:    "open",
		}, true},
		{"task with emoji + EPIC prefix", &BeadInfo{
			Title:     "🪺 EPIC: nest overhaul",
			IssueType: "task",
			Status:    "open",
		}, true},
		{"empty type (defaults to task) with EPIC: prefix", &BeadInfo{
			Title:  "EPIC: cleanup",
			Status: "open",
		}, true},

		// Negative: real epics are handled by the epic path, not this gate.
		{"real epic with EPIC: title", &BeadInfo{
			Title:     "EPIC: Proper epic bead",
			IssueType: "epic",
			Status:    "open",
		}, false},

		// Negative: ordinary work beads.
		{"plain task", &BeadInfo{
			Title:     "Fix parser bug",
			IssueType: "task",
			Status:    "open",
		}, false},
		{"task mentions EPIC mid-title", &BeadInfo{
			Title:     "Fix EPIC: handling in parser",
			IssueType: "task",
			Status:    "open",
		}, false},
		{"task with Episodic word", &BeadInfo{
			Title:     "Episodic streaming support",
			IssueType: "task",
			Status:    "open",
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsEpicLikeBeadInfo(tt.info); got != tt.want {
				t.Errorf("IsEpicLikeBeadInfo(%+v) = %v, want %v", tt.info, got, tt.want)
			}
		})
	}
}

// TestIsEpicLikeBeadInfo_PhaseEpicLabel verifies the gu-fs88 extension:
// beads carrying the "phase:epic" label are epic containers even when the
// title doesn't start with "EPIC:" and the issue_type is task/bug.
func TestIsEpicLikeBeadInfo_PhaseEpicLabel(t *testing.T) {
	tests := []struct {
		name string
		info *BeadInfo
		want bool
	}{
		// Positive: phase:epic label + slingable type.
		{"task with phase:epic label", &BeadInfo{
			Title:     "Triage Queue",
			IssueType: "task",
			Status:    "open",
			Labels:    []string{"phase:epic"},
		}, true},
		{"bug with phase:epic label", &BeadInfo{
			Title:     "Bug triage backlog",
			IssueType: "bug",
			Status:    "open",
			Labels:    []string{"phase:epic"},
		}, true},
		{"task with phase:epic among other labels", &BeadInfo{
			Title:     "ta-823 mirror",
			IssueType: "task",
			Status:    "open",
			Labels:    []string{"gt:coord", "phase:epic", "needs-triage"},
		}, true},
		// ta-823 shape: both signals present.
		{"ta-823 shape (EPIC: title + phase:epic label)", &BeadInfo{
			Title:     "EPIC: Triage Queue",
			IssueType: "task",
			Status:    "open",
			Labels:    []string{"phase:epic"},
		}, true},

		// Negative: real epics short-circuit ahead of this helper's purpose.
		{"real epic with phase:epic label", &BeadInfo{
			Title:     "Cleanup",
			IssueType: "epic",
			Status:    "open",
			Labels:    []string{"phase:epic"},
		}, false},

		// Negative: similar-looking labels that are not the exact marker.
		{"similar label phase:prep", &BeadInfo{
			Title:     "Prep work",
			IssueType: "task",
			Status:    "open",
			Labels:    []string{"phase:prep"},
		}, false},
		{"label contains phase:epic substring but isn't exact", &BeadInfo{
			Title:     "Work",
			IssueType: "task",
			Status:    "open",
			Labels:    []string{"phase:epics"},
		}, false},
		{"empty labels + non-epic title", &BeadInfo{
			Title:     "Regular work",
			IssueType: "task",
			Status:    "open",
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsEpicLikeBeadInfo(tt.info); got != tt.want {
				t.Errorf("IsEpicLikeBeadInfo(%+v) = %v, want %v", tt.info, got, tt.want)
			}
		})
	}
}

// TestIsContainerBeadInfo verifies the gu-9j93s container gate: real epics and
// convoys (by issue_type or gt:epic/gt:convoy label) are non-dispatchable
// containers. Unlike IsEpicLikeBeadInfo (which fires only when a non-epic TYPE
// has an "EPIC:" title), this catches type=epic/convoy directly — the case
// bd ready failed to filter, surfacing phantom ready work that sling refused.
func TestIsContainerBeadInfo(t *testing.T) {
	tests := []struct {
		name string
		info *BeadInfo
		want bool
	}{
		{"nil", nil, false},
		{"empty", &BeadInfo{}, false},

		// Positive: real containers by type.
		{"type=epic", &BeadInfo{Title: "Real epic", IssueType: "epic"}, true},
		{"type=convoy", &BeadInfo{Title: "Convoy", IssueType: "convoy"}, true},
		{"type=molecule", &BeadInfo{Title: "Witness Patrol", IssueType: "molecule"}, true},
		{"type=epic with phase:epic label", &BeadInfo{Title: "Real epic", IssueType: "epic", Labels: []string{"phase:epic"}}, true},

		// Positive: containers by label even when type is slingable.
		{"gt:epic label on task", &BeadInfo{Title: "Plain", IssueType: "task", Labels: []string{"gt:epic"}}, true},
		{"gt:convoy label on task", &BeadInfo{Title: "Plain", IssueType: "task", Labels: []string{"gt:convoy"}}, true},
		{"gt:molecule label on task", &BeadInfo{Title: "Plain", IssueType: "task", Labels: []string{"gt:molecule"}}, true},

		// Negative: ordinary work beads.
		{"plain task", &BeadInfo{Title: "Fix bug", IssueType: "task"}, false},
		{"bug", &BeadInfo{Title: "Crash on save", IssueType: "bug"}, false},
		// EPIC:-titled task is NOT caught here (that's IsEpicLikeBeadInfo's job).
		{"EPIC: title task not caught by container gate", &BeadInfo{Title: "EPIC: thing", IssueType: "task"}, false},
		// Near-miss label must not match.
		{"phase:epic label alone (not gt:epic)", &BeadInfo{Title: "Work", IssueType: "task", Labels: []string{"phase:epic"}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsContainerBeadInfo(tt.info); got != tt.want {
				t.Errorf("IsContainerBeadInfo(%+v) = %v, want %v", tt.info, got, tt.want)
			}
		})
	}
}

// TestIsMayorOnlyBeadInfo verifies the gu-bk6e dispatch gate: beads carrying
// the mayor-only or no-polecat label must be rejected from polecat dispatch
// regardless of title, type, or status.
func TestIsMayorOnlyBeadInfo(t *testing.T) {
	tests := []struct {
		name string
		info *BeadInfo
		want bool
	}{
		{"nil", nil, false},
		{"empty", &BeadInfo{}, false},

		// Positive: either label on a slingable-looking bead.
		{"task with mayor-only label (ta-wisp-1z3 shape)", &BeadInfo{
			Title:     "Escalation: origin config broken",
			IssueType: "task",
			Status:    "open",
			Labels:    []string{"escalation", "mayor-only"},
		}, true},
		{"bug with no-polecat alias", &BeadInfo{
			Title:     "Town-root symlink drift",
			IssueType: "bug",
			Status:    "open",
			Labels:    []string{"no-polecat"},
		}, true},
		{"bug with human-only alias (gu-utpl3 shape)", &BeadInfo{
			Title:     "Meta-investigation: re-dispatch pattern",
			IssueType: "bug",
			Status:    "open",
			Labels:    []string{"human-only"},
		}, true},
		{"both labels at once", &BeadInfo{
			Title:     "Cross-rig coordination",
			IssueType: "task",
			Status:    "open",
			Labels:    []string{"mayor-only", "no-polecat"},
		}, true},
		{"mayor-only among unrelated labels", &BeadInfo{
			Title:     "Fix origin",
			IssueType: "task",
			Status:    "open",
			Labels:    []string{"gt:coord", "mayor-only", "needs-review"},
		}, true},
		{"in_progress bead still rejected by label", &BeadInfo{
			Title:     "Requires mayor",
			IssueType: "task",
			Status:    "in_progress",
			Labels:    []string{"mayor-only"},
		}, true},

		// Negative: ordinary work beads pass through.
		{"plain task without labels", &BeadInfo{
			Title:     "Fix parser bug",
			IssueType: "task",
			Status:    "open",
		}, false},
		{"unrelated labels only", &BeadInfo{
			Title:     "Regular escalation",
			IssueType: "task",
			Status:    "open",
			Labels:    []string{"escalation", "gt:coord"},
		}, false},

		// Negative: substring / case / prefix collisions must not trigger.
		{"mayor-only-v2 not matched", &BeadInfo{
			Title:     "Work",
			IssueType: "task",
			Status:    "open",
			Labels:    []string{"mayor-only-v2"},
		}, false},
		{"no-polecat-prep not matched", &BeadInfo{
			Title:     "Work",
			IssueType: "task",
			Status:    "open",
			Labels:    []string{"no-polecat-prep"},
		}, false},
		{"human-only-followup not matched", &BeadInfo{
			Title:     "Work",
			IssueType: "task",
			Status:    "open",
			Labels:    []string{"human-only-followup"},
		}, false},
		{"Mayor-Only (wrong case) not matched", &BeadInfo{
			Title:     "Work",
			IssueType: "task",
			Status:    "open",
			Labels:    []string{"Mayor-Only"},
		}, false},
		{"polecat label alone not matched (unrelated namespace)", &BeadInfo{
			Title:     "Work",
			IssueType: "task",
			Status:    "open",
			Labels:    []string{"polecat"},
		}, false},

		// Negative: legitimate epic-phase label must not collide.
		{"phase:epic without mayor-only", &BeadInfo{
			Title:     "Phase work",
			IssueType: "task",
			Status:    "open",
			Labels:    []string{"phase:epic"},
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsMayorOnlyBeadInfo(tt.info); got != tt.want {
				t.Errorf("IsMayorOnlyBeadInfo(%+v) = %v, want %v", tt.info, got, tt.want)
			}
		})
	}
}

// TestIsHumanOnlyBeadInfo verifies the gs-4pe6 guard: beads marked human-only
// by a bracketed [HUMAN] / [HUMAN-ONLY] title tag (or the human-only label) must
// be refused so the convoy/auto-dispatch path stops sweeping them to polecats.
func TestIsHumanOnlyBeadInfo(t *testing.T) {
	tests := []struct {
		name string
		info *BeadInfo
		want bool
	}{
		{"nil", nil, false},
		{"empty", &BeadInfo{}, false},

		// Positive: [HUMAN] title tag (the gap this closes — no label set).
		{"[HUMAN] prefix tag (lb-wcdw.15 shape)", &BeadInfo{
			Title:     "[HUMAN] Run a 20-min user-observation study",
			IssueType: "task",
			Status:    "open",
		}, true},
		{"[HUMAN] after another bracket tag", &BeadInfo{
			Title:     "[BUG][HUMAN] verify by watching real users",
			IssueType: "bug",
			Status:    "open",
		}, true},
		{"[HUMAN-ONLY] variant", &BeadInfo{
			Title:     "[HUMAN-ONLY] sign-off on launch copy",
			IssueType: "task",
			Status:    "open",
		}, true},
		{"[Human only] case + space variant", &BeadInfo{
			Title:     "[Human only] judgment call on pricing",
			IssueType: "task",
			Status:    "open",
		}, true},

		// Positive: human-only label alone (also covered by mayor-only gate).
		{"human-only label, no title tag", &BeadInfo{
			Title:     "Meta-investigation: re-dispatch pattern",
			IssueType: "bug",
			Status:    "open",
			Labels:    []string{"human-only"},
		}, true},

		// Negative: ordinary work beads pass through.
		{"plain task", &BeadInfo{
			Title:     "Fix parser bug",
			IssueType: "task",
			Status:    "open",
		}, false},
		{"prose 'human' without bracket tag", &BeadInfo{
			Title:     "Add human-readable error messages",
			IssueType: "task",
			Status:    "open",
		}, false},
		{"humanoid is not a tag", &BeadInfo{
			Title:     "Render humanoid avatar",
			IssueType: "task",
			Status:    "open",
		}, false},
		// gs-4pe6 itself: its title mentions "[HUMAN]" mid-prose while
		// describing the marker, and its description quotes "Do NOT
		// auto-dispatch". It is a real, slingable bug and must NOT self-filter —
		// the tag is only honored in the leading bracket run, and description
		// text is never matched.
		{"gs-4pe6 self-reference: [HUMAN] mid-title, not a leading tag", &BeadInfo{
			Title:       "Convoy/auto-dispatch ignores [HUMAN]/human-only beads",
			IssueType:   "bug",
			Status:      "open",
			Description: "lb-wcdw.15 is marked [HUMAN]; 'Do NOT auto-dispatch'.",
		}, false},
		{"[HUMAN] after a non-tag word is not a leading tag", &BeadInfo{
			Title:     "[BUG] fix [HUMAN] glyph rendering",
			IssueType: "bug",
			Status:    "open",
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsHumanOnlyBeadInfo(tt.info); got != tt.want {
				t.Errorf("IsHumanOnlyBeadInfo(%+v) = %v, want %v", tt.info, got, tt.want)
			}
		})
	}
}

// TestIsReferenceTripwireBeadInfo verifies the hq-9jeyo guard: reference /
// gate tripwire beads (do-not-dispatch / pinned labels, or issue_type=reference)
// must never be slung.
func TestIsReferenceTripwireBeadInfo(t *testing.T) {
	if IsReferenceTripwireBeadInfo(nil) {
		t.Error("nil should not be a tripwire")
	}
	if IsReferenceTripwireBeadInfo(&BeadInfo{Status: "open", Labels: []string{"bug"}}) {
		t.Error("plain bug bead should not be a tripwire")
	}
	if !IsReferenceTripwireBeadInfo(&BeadInfo{Labels: []string{"do-not-dispatch"}}) {
		t.Error("do-not-dispatch label should be a tripwire")
	}
	if !IsReferenceTripwireBeadInfo(&BeadInfo{Labels: []string{"pinned"}}) {
		t.Error("pinned label should be a tripwire")
	}
	if !IsReferenceTripwireBeadInfo(&BeadInfo{IssueType: "reference"}) {
		t.Error("issue_type=reference should be a tripwire")
	}
}

// TestIsAwaitingMergeBeadInfo verifies the gu-ea25u guard: a source bead
// carrying the awaiting_refinery_merge label has an MR in flight and must not
// be re-dispatched to a fresh polecat.
func TestIsAwaitingMergeBeadInfo(t *testing.T) {
	tests := []struct {
		name string
		info *BeadInfo
		want bool
	}{
		{"nil", nil, false},
		{"empty", &BeadInfo{}, false},

		// Positive: the in-flight label, even alongside an open status (the
		// bead stays open until the refinery's PostMerge closes it).
		{"awaiting_refinery_merge label", &BeadInfo{Title: "Fix bug", Status: "in_progress", Labels: []string{"awaiting_refinery_merge"}}, true},
		{"label among others", &BeadInfo{Title: "Fix bug", Status: "open", Labels: []string{"bug", "awaiting_refinery_merge"}}, true},

		// Negative: ordinary work beads and near-miss labels.
		{"plain task", &BeadInfo{Title: "Fix bug", Status: "open", Labels: []string{"bug"}}, false},
		{"no labels", &BeadInfo{Title: "Fix bug", Status: "open"}, false},
		// awaiting_refinery_recovery is a DIFFERENT label (dead-refinery/no-MR
		// case) and must not match this guard.
		{"awaiting_refinery_recovery is not a match", &BeadInfo{Title: "Fix bug", Status: "open", Labels: []string{"awaiting_refinery_recovery"}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAwaitingMergeBeadInfo(tt.info); got != tt.want {
				t.Errorf("IsAwaitingMergeBeadInfo(%+v) = %v, want %v", tt.info, got, tt.want)
			}
		})
	}
}

// TestIsSlingContextBeadInfo verifies the gu-hfr3 guard that prevents a
// sling-context wrapper from being re-scheduled (which would nest wrappers).
func TestIsSlingContextBeadInfo(t *testing.T) {
	tests := []struct {
		name string
		info *BeadInfo
		want bool
	}{
		{"nil", nil, false},
		{"empty", &BeadInfo{}, false},

		// Positive: has the sling-context label.
		{"has sling-context label", &BeadInfo{Labels: []string{"gt:sling-context"}}, true},
		{"sling-context among other labels", &BeadInfo{Labels: []string{"gt:ephemeral", "gt:sling-context", "gt:scheduler"}}, true},

		// Negative: real work and other ephemeral beads.
		{"plain work bead", &BeadInfo{Title: "Fix bug", Status: "open", IssueType: "task"}, false},
		{"agent bead but no sling label", &BeadInfo{Labels: []string{"gt:agent"}}, false},
		{"similar-sounding label", &BeadInfo{Labels: []string{"gt:sling"}}, false},
		{"message bead", &BeadInfo{Labels: []string{"gt:message"}}, false},
		{"convoy bead", &BeadInfo{Labels: []string{"gt:convoy"}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSlingContextBeadInfo(tt.info); got != tt.want {
				t.Errorf("IsSlingContextBeadInfo(%+v) = %v, want %v", tt.info, got, tt.want)
			}
		})
	}
}

func TestIsPolecatOwnedBeadInfo(t *testing.T) {
	tests := []struct {
		name string
		info *BeadInfo
		want bool
	}{
		{"nil", nil, false},
		{"empty info", &BeadInfo{}, false},

		// Positive: canonical "<rig>/polecats/<name>" addresses.
		{"casc_lambda obsidian (cala-akl shape)", &BeadInfo{
			Title: "Replace SigV4 stub",
			Owner: "casc_lambda/polecats/obsidian",
		}, true},
		{"gastown_upstream fury", &BeadInfo{
			Title: "Some bead",
			Owner: "gastown_upstream/polecats/fury",
		}, true},
		{"hyphenated rig + name", &BeadInfo{
			Owner: "my-rig/polecats/quartz-2",
		}, true},

		// Negative: plain user owners (the common case).
		{"email owner", &BeadInfo{
			Title: "Real work",
			Owner: "canewiw@amazon.com",
		}, false},
		{"mayor owner (trailing slash, 2 segments)", &BeadInfo{
			Owner: "mayor/",
		}, false},
		{"empty owner", &BeadInfo{
			Title: "No owner set",
			Owner: "",
		}, false},
		{"whitespace-only owner", &BeadInfo{
			Owner: "   ",
		}, false},

		// Negative: non-polecat sublevels under a rig.
		{"witness sublevel", &BeadInfo{
			Owner: "gastown/witness",
		}, false},
		{"refinery sublevel", &BeadInfo{
			Owner: "gastown/refinery",
		}, false},
		{"crew (3 segments, but middle segment is not 'polecats')", &BeadInfo{
			Owner: "gastown/crew/canewiw",
		}, false},

		// Negative: malformed shapes that look polecat-ish but aren't canonical.
		{"deeper path beyond 3 segments", &BeadInfo{
			Owner: "rig/polecats/name/extra",
		}, false},
		{"missing rig segment", &BeadInfo{
			Owner: "/polecats/name",
		}, false},
		{"missing polecat name segment", &BeadInfo{
			Owner: "rig/polecats/",
		}, false},
		{"polecats-prefixed but not the literal segment", &BeadInfo{
			Owner: "rig/polecats-and-friends/name",
		}, false},
		{"plural typo (polecat singular)", &BeadInfo{
			Owner: "rig/polecat/name",
		}, false},
		{"capitalization differs (Polecats)", &BeadInfo{
			Owner: "rig/Polecats/name",
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPolecatOwnedBeadInfo(tt.info); got != tt.want {
				t.Errorf("IsPolecatOwnedBeadInfo(%+v) = %v, want %v", tt.info, got, tt.want)
			}
		})
	}
}

// TestIsWrongRigBeadForTarget verifies the gu-mhfs cross-rig mis-routing
// guard: dispatching to a rig already labeled "wrong-rig:<rig>" must be
// refused, while dispatching to other rigs must remain unaffected.
func TestIsWrongRigBeadForTarget(t *testing.T) {
	tests := []struct {
		name      string
		info      *BeadInfo
		targetRig string
		want      bool
	}{
		{"nil info", nil, "casc_lambda", false},
		{"empty target rig", &BeadInfo{Labels: []string{"wrong-rig:casc_lambda"}}, "", false},
		{
			"matching wrong-rig label",
			&BeadInfo{
				Title:  "auth-enforcement test failing",
				Labels: []string{"wrong-rig:casc_lambda"},
			},
			"casc_lambda",
			true,
		},
		{
			"different rig — should still dispatch",
			&BeadInfo{
				Title:  "auth-enforcement test failing",
				Labels: []string{"wrong-rig:casc_lambda"},
			},
			"casc_crud",
			false,
		},
		{
			"multiple wrong rigs, target matches one",
			&BeadInfo{
				Labels: []string{"wrong-rig:casc_lambda", "wrong-rig:casc_crud"},
			},
			"casc_lambda",
			true,
		},
		{
			"multiple wrong rigs, target unaffected",
			&BeadInfo{
				Labels: []string{"wrong-rig:casc_lambda", "wrong-rig:casc_crud"},
			},
			"casc_cdk",
			false,
		},
		// Substring collisions must not trigger.
		{
			"label rig is substring of target — no match",
			&BeadInfo{Labels: []string{"wrong-rig:casc"}},
			"casc_lambda",
			false,
		},
		{
			"target is substring of label rig — no match",
			&BeadInfo{Labels: []string{"wrong-rig:casc_lambda_old"}},
			"casc_lambda",
			false,
		},
		// Negatives: unrelated labels must not trigger.
		{
			"only mayor-only — no match",
			&BeadInfo{Labels: []string{"mayor-only"}},
			"casc_lambda",
			false,
		},
		{
			"empty labels — no match",
			&BeadInfo{Labels: []string{}},
			"casc_lambda",
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsWrongRigBeadForTarget(tt.info, tt.targetRig); got != tt.want {
				t.Errorf("IsWrongRigBeadForTarget(%+v, %q) = %v, want %v", tt.info, tt.targetRig, got, tt.want)
			}
		})
	}
}

// TestIsOrphanMolecule_HookedNoAssignee covers gh-3697: a bead stuck in
// status=hooked with empty assignee must be treated as orphaned so sling can
// self-heal.
func TestIsOrphanMolecule_HookedNoAssignee(t *testing.T) {
	info := &BeadInfo{Status: "hooked", Assignee: ""}
	if !IsOrphanMolecule(info, func(string) bool { return false }) {
		t.Errorf("IsOrphanMolecule(status=hooked, assignee='') = false, want true (gh-3697)")
	}
}

func TestIsOrphanMolecule_TableDriven(t *testing.T) {
	tests := []struct {
		name     string
		info     *BeadInfo
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
			info:     &BeadInfo{Status: "open", Assignee: ""},
			deadFn:   func(string) bool { return false },
			expected: true,
		},
		{
			name:     "in_progress, no assignee — orphan from sling crash",
			info:     &BeadInfo{Status: "in_progress", Assignee: ""},
			deadFn:   func(string) bool { return false },
			expected: true,
		},
		{
			name:     "hooked, no assignee — gh-3697 wedge",
			info:     &BeadInfo{Status: "hooked", Assignee: ""},
			deadFn:   func(string) bool { return false },
			expected: true,
		},
		{
			name:     "closed, no assignee — keep refuse path",
			info:     &BeadInfo{Status: "closed", Assignee: ""},
			deadFn:   func(string) bool { return false },
			expected: false,
		},
		{
			name:     "blocked, no assignee — keep refuse path",
			info:     &BeadInfo{Status: "blocked", Assignee: ""},
			deadFn:   func(string) bool { return false },
			expected: false,
		},
		{
			name:     "hooked, assignee, session alive — refuse",
			info:     &BeadInfo{Status: "hooked", Assignee: "rig/polecats/Toast"},
			deadFn:   func(string) bool { return false },
			expected: false,
		},
		{
			name:     "hooked, assignee, session dead — auto-burn",
			info:     &BeadInfo{Status: "hooked", Assignee: "rig/polecats/Toast"},
			deadFn:   func(string) bool { return true },
			expected: true,
		},
		// gu-koi7: operator workaround `bd update --assignee none` stores the
		// literal string "none". Status=open is sufficient to declare orphan.
		{
			name:     "open, assignee=none — operator reset workaround (gu-koi7)",
			info:     &BeadInfo{Status: "open", Assignee: "none"},
			deadFn:   func(string) bool { return false },
			expected: true,
		},
		{
			name:     "open, assignee=NONE (case) — operator reset workaround (gu-koi7)",
			info:     &BeadInfo{Status: "open", Assignee: "NONE"},
			deadFn:   func(string) bool { return false },
			expected: true,
		},
		{
			name:     "open, stale assignee from prior dead polecat — gu-koi7",
			info:     &BeadInfo{Status: "open", Assignee: "rig/polecats/dead"},
			deadFn:   func(string) bool { return false }, // even if not detected dead
			expected: true,
		},
		{
			name:     "in_progress, assignee=none — sentinel honored",
			info:     &BeadInfo{Status: "in_progress", Assignee: "none"},
			deadFn:   func(string) bool { return false },
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsOrphanMolecule(tt.info, tt.deadFn)
			if got != tt.expected {
				t.Errorf("IsOrphanMolecule() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestIsEmptyAssignee verifies the sentinel-normalizing assignee check
// (gu-koi7): empty, "none", and "NONE" all read as unassigned.
func TestIsEmptyAssignee(t *testing.T) {
	tests := []struct {
		assignee string
		want     bool
	}{
		{"", true},
		{"   ", true},
		{"none", true},
		{"NONE", true},
		{" None ", true},
		{"rig/polecats/Toast", false},
		{"canewiw@amazon.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.assignee, func(t *testing.T) {
			if got := IsEmptyAssignee(tt.assignee); got != tt.want {
				t.Errorf("IsEmptyAssignee(%q) = %v, want %v", tt.assignee, got, tt.want)
			}
		})
	}
}

// TestIsRefineryWorkflowStepID verifies the gu-pi35l variant-2 guard: bead IDs
// matching the `*-wfs-*` refinery workflow-step convention must be recognized
// so the dispatcher never slings them to the polecat lane.
func TestIsRefineryWorkflowStepID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		// Positive: the cacr-wfs-xegy2 variant and other workflow-step IDs.
		{"cacr-wfs-xegy2", true},
		{"gu-wfs-abc123", true},
		{"casc_crud-wfs-merge", true},

		// Negative: ordinary work-bead IDs (the common case).
		{"gu-pi35l", false},
		{"cacr-r8ne", false},
		{"gu-wisp-88fp", false},
		{"", false},

		// Negative: substrings that resemble but are not the `-wfs-` marker.
		{"gu-wfsx", false},
		{"gu-awfs-1", false},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if got := IsRefineryWorkflowStepID(tt.id); got != tt.want {
				t.Errorf("IsRefineryWorkflowStepID(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

// TestIsRefineryOwnedBeadInfo verifies the gu-pi35l guard: beads owned by a
// `<rig>/refinery` address track merge-queue / patrol state and must not be
// dispatched to a polecat.
func TestIsRefineryOwnedBeadInfo(t *testing.T) {
	tests := []struct {
		name string
		info *BeadInfo
		want bool
	}{
		{"nil", nil, false},
		{"empty info", &BeadInfo{}, false},

		// Positive: canonical "<rig>/refinery" addresses.
		{"casc_crud refinery (cacr-wfs-xegy2 shape)", &BeadInfo{
			Title: "Merge and push",
			Owner: "casc_crud/refinery",
		}, true},
		{"gastown_upstream refinery", &BeadInfo{
			Owner: "gastown_upstream/refinery",
		}, true},
		{"hyphenated rig", &BeadInfo{
			Owner: "my-rig/refinery",
		}, true},

		// Negative: plain user owners and other agents.
		{"email owner", &BeadInfo{
			Owner: "canewiw@amazon.com",
		}, false},
		{"empty owner", &BeadInfo{
			Owner: "",
		}, false},
		{"whitespace-only owner", &BeadInfo{
			Owner: "   ",
		}, false},
		{"witness sublevel", &BeadInfo{
			Owner: "gastown/witness",
		}, false},
		{"polecat owner (3 segments)", &BeadInfo{
			Owner: "gastown/polecats/fury",
		}, false},

		// Negative: malformed shapes that resemble but are not canonical.
		{"deeper path beyond 2 segments", &BeadInfo{
			Owner: "rig/refinery/extra",
		}, false},
		{"missing rig segment", &BeadInfo{
			Owner: "/refinery",
		}, false},
		{"refinery-prefixed but not the literal segment", &BeadInfo{
			Owner: "rig/refinery-backup",
		}, false},
		{"capitalization differs (Refinery)", &BeadInfo{
			Owner: "rig/Refinery",
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRefineryOwnedBeadInfo(tt.info); got != tt.want {
				t.Errorf("IsRefineryOwnedBeadInfo(%+v) = %v, want %v", tt.info, got, tt.want)
			}
		})
	}
}
