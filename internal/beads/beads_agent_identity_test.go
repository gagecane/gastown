package beads

import (
	"testing"
)

func TestParseAgentBeadID(t *testing.T) {
	tests := []struct {
		input    string
		wantRig  string
		wantRole string
		wantName string
		wantOK   bool
	}{
		// Town-level agents
		{"gt-mayor", "", "mayor", "", true},
		{"gt-deacon", "", "deacon", "", true},
		// Rig-level singletons
		{"gt-gastown-witness", "gastown", "witness", "", true},
		{"gt-gastown-refinery", "gastown", "refinery", "", true},
		// Rig-level named agents
		{"gt-gastown-crew-joe", "gastown", "crew", "joe", true},
		{"gt-gastown-crew-max", "gastown", "crew", "max", true},
		{"gt-gastown-polecat-capable", "gastown", "polecat", "capable", true},
		// Names with hyphens
		{"gt-gastown-polecat-my-agent", "gastown", "polecat", "my-agent", true},
		// Worker name collides with role keyword
		{"gt-gastown-polecat-witness", "gastown", "polecat", "witness", true},
		{"gt-gastown-polecat-refinery", "gastown", "polecat", "refinery", true},
		{"gt-gastown-crew-witness", "gastown", "crew", "witness", true},
		{"gt-gastown-crew-refinery", "gastown", "crew", "refinery", true},
		{"gt-gastown-polecat-crew", "gastown", "polecat", "crew", true},
		{"gt-gastown-crew-polecat", "gastown", "crew", "polecat", true},
		// Worker name collides with role keyword + hyphenated rig
		{"gt-my-rig-polecat-witness", "my-rig", "polecat", "witness", true},
		// Collapsed form: prefix == rig (e.g., rig "ff" with prefix "ff")
		{"ff-witness", "ff", "witness", "", true},                // collapsed rig-level singleton
		{"ff-refinery", "ff", "refinery", "", true},              // collapsed rig-level singleton
		{"ff-polecat-nux", "ff", "polecat", "nux", true},         // collapsed named agent
		{"ff-crew-dave", "ff", "crew", "dave", true},             // collapsed named agent
		{"ff-polecat-war-boy", "ff", "polecat", "war-boy", true}, // collapsed named with hyphen
		// Parseable but not valid agent roles (IsAgentSessionBead will reject)
		{"gt-abc123", "", "abc123", "", true}, // Parses as town-level but not valid role
		// Other prefixes (bd-, hq-)
		{"bd-mayor", "", "mayor", "", true},                           // bd prefix town-level
		{"bd-beads-witness", "beads", "witness", "", true},            // bd prefix rig-level singleton
		{"bd-beads-polecat-pearl", "beads", "polecat", "pearl", true}, // bd prefix rig-level named
		{"hq-mayor", "", "mayor", "", true},                           // hq prefix town-level
		// Truly invalid patterns
		{"x-mayor", "", "", "", false},    // Prefix too short (1 char)
		{"abcd-mayor", "", "", "", false}, // Prefix too long (4 chars)
		{"", "", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			rig, role, name, ok := ParseAgentBeadID(tt.input)
			if ok != tt.wantOK {
				t.Errorf("ParseAgentBeadID(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
				return
			}
			if rig != tt.wantRig {
				t.Errorf("ParseAgentBeadID(%q) rig = %q, want %q", tt.input, rig, tt.wantRig)
			}
			if role != tt.wantRole {
				t.Errorf("ParseAgentBeadID(%q) role = %q, want %q", tt.input, role, tt.wantRole)
			}
			if name != tt.wantName {
				t.Errorf("ParseAgentBeadID(%q) name = %q, want %q", tt.input, name, tt.wantName)
			}
		})
	}
}

func TestIsAgentSessionBead(t *testing.T) {
	tests := []struct {
		beadID string
		want   bool
	}{
		// Agent session beads with gt- prefix (should return true)
		{"gt-mayor", true},
		{"gt-deacon", true},
		{"gt-gastown-witness", true},
		{"gt-gastown-refinery", true},
		{"gt-gastown-crew-joe", true},
		{"gt-gastown-polecat-capable", true},
		// Agent session beads with bd- prefix (should return true)
		{"bd-mayor", true},
		{"bd-deacon", true},
		{"bd-beads-witness", true},
		{"bd-beads-refinery", true},
		{"bd-beads-crew-joe", true},
		{"bd-beads-polecat-pearl", true},
		// Regular work beads (should return false)
		{"gt-abc123", false},
		{"gt-sb6m4", false},
		{"gt-u7dxq", false},
		{"bd-abc123", false},
		// Invalid beads
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.beadID, func(t *testing.T) {
			got := IsAgentSessionBead(tt.beadID)
			if got != tt.want {
				t.Errorf("IsAgentSessionBead(%q) = %v, want %v", tt.beadID, got, tt.want)
			}
		})
	}
}

// TestResetAgentBeadForReuse_NukeRespawnCycle tests the preferred nuke→respawn
// lifecycle using ResetAgentBeadForReuse (gt-14b8o fix). This keeps the bead open
// with agent_state="nuked", avoiding the close/reopen cycle
// that fails on Dolt backends.
func TestResetAgentBeadForReuse_NukeRespawnCycle(t *testing.T) {
	t.Skip("bd CLI 0.47.2 bug: database writes don't commit")

	tmpDir := t.TempDir()
	bd := NewIsolated(tmpDir)
	if err := bd.Init("test"); err != nil {
		t.Fatalf("bd init: %v", err)
	}

	agentID := "test-testrig-polecat-reset"

	// Spawn 1: Create agent bead
	issue1, err := bd.CreateOrReopenAgentBead(agentID, agentID, &AgentFields{
		RoleType:   "polecat",
		Rig:        "testrig",
		AgentState: "spawning",
		HookBead:   "test-task-1",
	})
	if err != nil {
		t.Fatalf("Spawn 1: %v", err)
	}
	if issue1.Status != "open" {
		t.Errorf("Spawn 1: status = %q, want 'open'", issue1.Status)
	}

	// Nuke 1: Reset for reuse (bead stays open with cleared fields)
	err = bd.ResetAgentBeadForReuse(agentID, "polecat nuked")
	if err != nil {
		t.Fatalf("Nuke 1 - ResetAgentBeadForReuse: %v", err)
	}

	// Verify bead is still open with cleared fields
	nukedIssue, err := bd.Show(agentID)
	if err != nil {
		t.Fatalf("Show after nuke: %v", err)
	}
	if nukedIssue.Status != "open" {
		t.Errorf("After nuke: status = %q, want 'open' (bead should stay open)", nukedIssue.Status)
	}
	nukedFields := ParseAgentFields(nukedIssue.Description)
	if nukedFields.AgentState != "nuked" {
		t.Errorf("After nuke: agent_state = %q, want 'nuked'", nukedFields.AgentState)
	}
	if nukedFields.HookBead != "" {
		t.Errorf("After nuke: hook_bead = %q, want empty", nukedFields.HookBead)
	}

	// Spawn 2: CreateOrReopenAgentBead should detect open bead and update it
	issue2, err := bd.CreateOrReopenAgentBead(agentID, agentID, &AgentFields{
		RoleType:   "polecat",
		Rig:        "testrig",
		AgentState: "spawning",
		HookBead:   "test-task-2",
	})
	if err != nil {
		t.Fatalf("Spawn 2: %v", err)
	}
	if issue2.Status != "open" {
		t.Errorf("Spawn 2: status = %q, want 'open'", issue2.Status)
	}
	fields := ParseAgentFields(issue2.Description)
	if fields.HookBead != "test-task-2" {
		t.Errorf("Spawn 2: hook_bead = %q, want 'test-task-2'", fields.HookBead)
	}
	if fields.AgentState != "spawning" {
		t.Errorf("Spawn 2: agent_state = %q, want 'spawning'", fields.AgentState)
	}

	// Nuke 2: Reset again
	err = bd.ResetAgentBeadForReuse(agentID, "polecat nuked again")
	if err != nil {
		t.Fatalf("Nuke 2: %v", err)
	}

	// Spawn 3: Should still work
	issue3, err := bd.CreateOrReopenAgentBead(agentID, agentID, &AgentFields{
		RoleType:   "polecat",
		Rig:        "testrig",
		AgentState: "spawning",
		HookBead:   "test-task-3",
	})
	if err != nil {
		t.Fatalf("Spawn 3: %v", err)
	}
	fields = ParseAgentFields(issue3.Description)
	if fields.HookBead != "test-task-3" {
		t.Errorf("Spawn 3: hook_bead = %q, want 'test-task-3'", fields.HookBead)
	}

	t.Log("LIFECYCLE TEST PASSED: spawn → reset → respawn works without close/reopen")
}

// TestIsAgentBead verifies the IsAgentBead function correctly identifies agent
// beads by checking both the gt:agent label (preferred) and the legacy type field.
func TestIsAgentBead(t *testing.T) {
	tests := []struct {
		name  string
		issue *Issue
		want  bool
	}{
		{
			name:  "nil issue",
			issue: nil,
			want:  false,
		},
		{
			name: "agent with legacy type",
			issue: &Issue{
				ID:     "gt-gastown-polecat-toast",
				Type:   "agent",
				Labels: []string{},
			},
			want: true,
		},
		{
			name: "agent with gt:agent label",
			issue: &Issue{
				ID:     "gt-gastown-polecat-toast",
				Type:   "task",
				Labels: []string{"gt:agent"},
			},
			want: true,
		},
		{
			name: "agent with both type and label",
			issue: &Issue{
				ID:     "gt-gastown-polecat-toast",
				Type:   "agent",
				Labels: []string{"gt:agent", "other-label"},
			},
			want: true,
		},
		{
			name: "not an agent - task type without label",
			issue: &Issue{
				ID:     "gt-abc123",
				Type:   "task",
				Labels: []string{},
			},
			want: false,
		},
		{
			name: "not an agent - bug type with other labels",
			issue: &Issue{
				ID:     "gt-xyz456",
				Type:   "bug",
				Labels: []string{"priority-high", "blocked"},
			},
			want: false,
		},
		{
			name: "agent with gt:agent label and other labels",
			issue: &Issue{
				ID:     "gt-gastown-witness",
				Type:   "task",
				Labels: []string{"priority-high", "gt:agent", "status-running"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsAgentBead(tt.issue)
			if got != tt.want {
				t.Errorf("IsAgentBead() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIsIdentityBeadTitle verifies the naming-convention regex used by the
// ghost-dispatch filter (gu-ypjm / gu-3znx / gu-huta). Matches identity beads
// with titles like "<prefix>-<rig>-polecat-<name>", "<prefix>-<rig>-refinery",
// "<prefix>-<rig>-witness", "<prefix>-<rig>-crew-<name>", "<prefix>-dog-<name>",
// "<prefix>-mayor", "<prefix>-deacon".
func TestIsIdentityBeadTitle(t *testing.T) {
	tests := []struct {
		title string
		want  bool
	}{
		// Match — polecat (rig-level named)
		{"af-agentforge-polecat-quartz", true},
		{"ta-talontriage-polecat-nux", true},
		{"ro-ralph-polecat-jasper", true},
		{"gu-gastown-polecat-guzzle", true},

		// Match — refinery (rig-level singleton)
		{"af-agentforge-refinery", true},
		{"gu-gastown-refinery", true},
		{"cadk-casc_cdk-refinery", true},

		// Match — witness (rig-level singleton, gu-huta extension)
		{"gu-gastown-witness", true},
		{"bd-beads-witness", true},
		{"af-agentforge-witness", true},

		// Match — crew (rig-level named, gu-huta extension)
		{"gu-gastown-crew-joe", true},
		{"bd-beads-crew-maxwell", true},

		// Match — town-level named agent (gu-huta extension)
		{"hq-dog-alpha", true},
		{"gt-dog-compactor", true},

		// Match — town-level singleton (gu-huta extension)
		{"hq-mayor", true},
		{"gt-mayor", true},
		{"hq-deacon", true},
		{"gt-deacon", true},

		// No match — not enough structure
		{"refinery", false},
		{"witness", false},
		{"mayor", false},
		{"polecat-research", false},
		{"dog-alpha", false},

		// No match — wrong structure (role not at end / mid-string)
		{"af-refinery-feature-work", false},
		{"gu-witness-task", false},
		{"hq-mayor-meeting-notes", false},
		{"", false},

		// No match — upper case prefix
		{"AF-agentforge-refinery", false},
		{"HQ-mayor", false},

		// No match — spaces or non-canonical prose
		{"Fix bug in parser", false},
		{"Add witness support to feature", false},

		// No match — similar-looking but not identity patterns
		{"gu-gastown-dogfood-feature", false}, // dog-.+ must come directly after prefix
		{"gt-refinery-proposal", false},       // refinery must follow "<rig>-"
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got := IsIdentityBeadTitle(tt.title)
			if got != tt.want {
				t.Errorf("IsIdentityBeadTitle(%q) = %v, want %v", tt.title, got, tt.want)
			}
		})
	}
}

// TestIsIdentityBeadFields covers the three filter criteria in gu-ypjm/gu-3znx:
// gt:agent label, status=closed, and identity-naming title regex. Matching
// ANY criterion classifies the bead as identity (not dispatchable as work).
func TestIsIdentityBeadFields(t *testing.T) {
	tests := []struct {
		name   string
		title  string
		status string
		labels []string
		want   bool
	}{
		// Label match
		{"gt:agent label alone", "random-title", "open", []string{"gt:agent"}, true},
		{"gt:agent among others", "random-title", "open", []string{"priority-high", "gt:agent"}, true},
		{"similar label does not match", "random-title", "open", []string{"gt:agentless", "agent"}, false},

		// Status match
		{"closed status triggers", "some-task", "closed", nil, true},
		{"in_progress status does not trigger", "some-task", "in_progress", nil, false},
		{"tombstone status does not trigger", "some-task", "tombstone", nil, false},

		// Title regex match
		{"polecat title matches", "af-agentforge-polecat-quartz", "open", nil, true},
		{"refinery title matches", "cadk-casc_cdk-refinery", "open", nil, true},
		{"empty title does not match", "", "open", nil, false},
		{"refinery without rig prefix does not match", "refinery", "open", nil, false},

		// Combined / none
		{"real task bead not identity", "Fix bug in parser", "open", []string{"priority-high"}, false},
		{"all three criteria together", "af-agentforge-polecat-quartz", "closed", []string{"gt:agent"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsIdentityBeadFields(tt.title, tt.status, tt.labels)
			if got != tt.want {
				t.Errorf("IsIdentityBeadFields(title=%q, status=%q, labels=%v) = %v, want %v",
					tt.title, tt.status, tt.labels, got, tt.want)
			}
		})
	}
}

// TestIsIdentityBead verifies the Issue-shaped variant of the ghost-dispatch
// filter. This must match IsIdentityBeadFields for equivalent inputs.
func TestIsIdentityBead(t *testing.T) {
	tests := []struct {
		name  string
		issue *Issue
		want  bool
	}{
		{"nil issue", nil, false},
		{"empty issue", &Issue{}, false},
		{
			name:  "task bead is not identity",
			issue: &Issue{Title: "Fix bug", Status: "open", Type: "task"},
			want:  false,
		},
		{
			name:  "gt:agent label",
			issue: &Issue{Title: "any", Status: "open", Labels: []string{"gt:agent"}},
			want:  true,
		},
		{
			name:  "legacy type=agent",
			issue: &Issue{Title: "any", Status: "open", Type: "agent"},
			want:  true,
		},
		{
			name:  "closed status",
			issue: &Issue{Title: "any", Status: "closed", Type: "task"},
			want:  true,
		},
		{
			name:  "identity title regex",
			issue: &Issue{Title: "af-agentforge-polecat-quartz", Status: "open", Type: "task"},
			want:  true,
		},
		{
			name:  "refinery title regex",
			issue: &Issue{Title: "cadk-casc_cdk-refinery", Status: "open", Type: "task"},
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsIdentityBead(tt.issue); got != tt.want {
				t.Errorf("IsIdentityBead(%+v) = %v, want %v", tt.issue, got, tt.want)
			}
		})
	}
}
