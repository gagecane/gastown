package feed

import (
	"strings"
	"testing"
	"time"
)

// TestStatusForEventType exercises the event-type → agent-status mapping,
// including the stickiness rules that keep terminal states from being
// reanimated by trailing updates.
func TestStatusForEventType(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		prev      string
		want      string
	}{
		// Ordinary activity → working
		{"create from empty", "create", "", AgentStatusWorking},
		{"update from empty", "update", "", AgentStatusWorking},
		{"update from working", "update", AgentStatusWorking, AgentStatusWorking},
		{"sling", "sling", "", AgentStatusWorking},
		{"hook", "hook", "", AgentStatusWorking},
		{"spawn", "spawn", "", AgentStatusWorking},
		{"boot", "boot", "", AgentStatusWorking},

		// Death → dead (sticky)
		{"session_death", "session_death", "", AgentStatusDead},
		{"crash from working", "crash", AgentStatusWorking, AgentStatusDead},
		{"zombie", "zombie", "", AgentStatusDead},
		{"dead", "dead", "", AgentStatusDead},

		// Terminal success → idle (also sticky against reanimation)
		{"done from working", "done", AgentStatusWorking, AgentStatusIdle},
		{"complete", "complete", "", AgentStatusIdle},
		{"merged", "merged", "", AgentStatusIdle},
		{"patrol_complete", "patrol_complete", "", AgentStatusIdle},

		// Sticky dead: trailing updates must not clobber dead status
		{"update after dead stays dead", "update", AgentStatusDead, AgentStatusDead},
		{"done after dead stays dead", "done", AgentStatusDead, AgentStatusDead},
		{"complete after dead stays dead", "complete", AgentStatusDead, AgentStatusDead},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := statusForEventType(tc.eventType, tc.prev)
			if got != tc.want {
				t.Errorf("statusForEventType(%q, %q) = %q; want %q",
					tc.eventType, tc.prev, got, tc.want)
			}
		})
	}
}

// TestIsAgentActive covers the recency gate applied at render time.
func TestIsAgentActive(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name  string
		agent *Agent
		want  bool
	}{
		{"nil agent", nil, false},
		{"zero update", &Agent{Status: AgentStatusWorking}, false},
		{"idle status", &Agent{Status: AgentStatusIdle, LastUpdate: now}, false},
		{"dead status", &Agent{Status: AgentStatusDead, LastUpdate: now}, false},
		{"empty status", &Agent{Status: "", LastUpdate: now}, false},
		{"working recent", &Agent{Status: AgentStatusWorking, LastUpdate: now.Add(-1 * time.Minute)}, true},
		{"working at boundary", &Agent{Status: AgentStatusWorking, LastUpdate: now.Add(-agentActiveWindow + time.Second)}, true},
		{"working stale", &Agent{Status: AgentStatusWorking, LastUpdate: now.Add(-agentActiveWindow - time.Second)}, false},
		// Legacy/foreign "running" value still treated as active for backwards compat.
		{"running recent", &Agent{Status: "running", LastUpdate: now.Add(-1 * time.Minute)}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isAgentActive(tc.agent)
			if got != tc.want {
				t.Errorf("isAgentActive(%+v) = %v; want %v", tc.agent, got, tc.want)
			}
		})
	}
}

// TestAddEventSetsAgentStatus verifies that addEvent populates
// Agent.Status — the original bug was that the field was always "".
func TestAddEventSetsAgentStatus(t *testing.T) {
	m := NewModel(nil)
	m.mu.Lock()
	m.width = 80
	m.height = 40
	m.mu.Unlock()

	now := time.Now()
	m.addEvent(Event{
		Time:   now,
		Type:   "update",
		Actor:  "gastown/crew/joe",
		Target: "gt-xyz",
		Rig:    "gastown",
		Role:   "crew",
	})

	m.mu.RLock()
	rig := m.rigs["gastown"]
	m.mu.RUnlock()
	if rig == nil {
		t.Fatalf("expected rig %q to exist", "gastown")
	}
	agent := rig.Agents["gastown/crew/joe"]
	if agent == nil {
		t.Fatalf("expected agent %q to exist", "gastown/crew/joe")
	}
	if agent.Status != AgentStatusWorking {
		t.Errorf("Status = %q; want %q", agent.Status, AgentStatusWorking)
	}
	if !isAgentActive(agent) {
		t.Errorf("fresh working agent should render as active")
	}
}

// TestAddEventDeathStickiness verifies that a dead event locks the agent
// status, so subsequent update events cannot reanimate the agent.
func TestAddEventDeathStickiness(t *testing.T) {
	m := NewModel(nil)
	m.mu.Lock()
	m.width = 80
	m.height = 40
	m.mu.Unlock()

	base := time.Now()
	m.addEvent(Event{
		Time: base, Type: "update", Actor: "gastown/polecats/dom",
		Target: "gt-xyz", Rig: "gastown", Role: "polecat",
	})
	m.addEvent(Event{
		Time: base.Add(1 * time.Second), Type: "session_death",
		Actor: "gastown/polecats/dom", Target: "gt-xyz",
		Rig: "gastown", Role: "polecat",
	})
	// Housekeeping update after death — must not revive.
	m.addEvent(Event{
		Time: base.Add(2 * time.Second), Type: "update",
		Actor: "gastown/polecats/dom", Target: "gt-xyz",
		Rig: "gastown", Role: "polecat",
	})

	m.mu.RLock()
	agent := m.rigs["gastown"].Agents["gastown/polecats/dom"]
	m.mu.RUnlock()
	if agent.Status != AgentStatusDead {
		t.Errorf("Status after session_death + update = %q; want %q",
			agent.Status, AgentStatusDead)
	}
	if isAgentActive(agent) {
		t.Errorf("dead agent should never render as active")
	}
}

// TestRenderAgentStatusIndicator verifies that the activity indicator
// reflects agent status — this is the symptom the bug describes.
func TestRenderAgentStatusIndicator(t *testing.T) {
	m := NewModel(nil)
	m.mu.Lock()
	m.width = 120
	m.height = 40
	m.mu.Unlock()

	now := time.Now()
	tests := []struct {
		name     string
		agent    *Agent
		wantHas  string
		wantNots []string
	}{
		{
			name: "working recent has arrow",
			agent: &Agent{
				Name:       "joe",
				Status:     AgentStatusWorking,
				LastUpdate: now,
				LastEvent:  &Event{Time: now, Message: "x"},
			},
			wantHas:  "→",
			wantNots: []string{"✗"},
		},
		{
			name: "idle has no indicator",
			agent: &Agent{
				Name:       "joe",
				Status:     AgentStatusIdle,
				LastUpdate: now,
				LastEvent:  &Event{Time: now, Message: "x"},
			},
			wantNots: []string{"→", "✗"},
		},
		{
			name: "dead has cross",
			agent: &Agent{
				Name:       "joe",
				Status:     AgentStatusDead,
				LastUpdate: now,
				LastEvent:  &Event{Time: now, Message: "x"},
			},
			wantHas:  "✗",
			wantNots: []string{"→"},
		},
		{
			name: "working stale falls back to idle",
			agent: &Agent{
				Name:       "joe",
				Status:     AgentStatusWorking,
				LastUpdate: now.Add(-2 * agentActiveWindow),
				LastEvent:  &Event{Time: now.Add(-2 * agentActiveWindow), Message: "x"},
			},
			wantNots: []string{"→", "✗"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			line := m.renderAgent("", tc.agent, 5)
			if tc.wantHas != "" && !strings.Contains(line, tc.wantHas) {
				t.Errorf("rendered line missing %q:\n%s", tc.wantHas, line)
			}
			for _, bad := range tc.wantNots {
				if strings.Contains(line, bad) {
					t.Errorf("rendered line should not contain %q:\n%s", bad, line)
				}
			}
		})
	}
}
