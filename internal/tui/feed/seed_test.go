package feed

import (
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// mockAgentBeadSource is a test double for AgentBeadSource.
type mockAgentBeadSource struct {
	beads map[string]*beads.Issue
	err   error
}

func (m *mockAgentBeadSource) ListAgentBeads() (map[string]*beads.Issue, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.beads, nil
}

// newAgentBead creates a minimal agent bead Issue for tests.
// The ID matters for ParseAgentBeadID; other fields are populated just
// enough to satisfy seed code paths.
func newAgentBead(id string) *beads.Issue {
	return &beads.Issue{
		ID:     id,
		Status: "open",
		Type:   "agent",
		Labels: []string{"gt:agent"},
	}
}

// TestSeedAgents_PopulatesRigsWithIdleAgents verifies the core behavior:
// every agent bead in the supplied sources appears in m.rigs with a nil
// LastEvent and empty Status (i.e., renders as idle).
func TestSeedAgents_PopulatesRigsWithIdleAgents(t *testing.T) {
	m := NewModel(nil)

	greenplace := &mockAgentBeadSource{
		beads: map[string]*beads.Issue{
			// Polecats
			"gu-greenplace-polecat-chrome": newAgentBead("gu-greenplace-polecat-chrome"),
			"gu-greenplace-polecat-guzzle": newAgentBead("gu-greenplace-polecat-guzzle"),
			// Singletons
			"gu-greenplace-witness":  newAgentBead("gu-greenplace-witness"),
			"gu-greenplace-refinery": newAgentBead("gu-greenplace-refinery"),
		},
	}

	m.SeedAgents(nil, map[string]AgentBeadSource{
		"greenplace": greenplace,
	})

	m.mu.RLock()
	defer m.mu.RUnlock()

	rig, ok := m.rigs["greenplace"]
	if !ok {
		t.Fatalf("rig greenplace not seeded; m.rigs keys = %v", keys(m.rigs))
	}

	wantActors := map[string]string{
		"greenplace/polecats/chrome": "polecat",
		"greenplace/polecats/guzzle": "polecat",
		"greenplace/witness":         "witness",
		"greenplace/refinery":        "refinery",
	}
	if len(rig.Agents) != len(wantActors) {
		t.Errorf("got %d agents, want %d; agents = %v",
			len(rig.Agents), len(wantActors), rig.Agents)
	}
	for actor, wantRole := range wantActors {
		agent, ok := rig.Agents[actor]
		if !ok {
			t.Errorf("actor %q not seeded; agents = %v", actor, agentActorKeys(rig.Agents))
			continue
		}
		if agent.Role != wantRole {
			t.Errorf("actor %q role = %q, want %q", actor, agent.Role, wantRole)
		}
		if agent.Rig != "greenplace" {
			t.Errorf("actor %q rig = %q, want greenplace", actor, agent.Rig)
		}
		if agent.LastEvent != nil {
			t.Errorf("actor %q LastEvent = %+v, want nil (idle)", actor, agent.LastEvent)
		}
		if !agent.LastUpdate.IsZero() {
			t.Errorf("actor %q LastUpdate = %v, want zero (idle)", actor, agent.LastUpdate)
		}
		if agent.Status != "" {
			t.Errorf("actor %q Status = %q, want empty", actor, agent.Status)
		}
	}
}

// TestSeedAgents_DoesNotClobberLiveEventData verifies idempotency: when an
// agent already exists in m.rigs (populated by a recent event), re-seeding
// must not overwrite LastEvent/LastUpdate/Status.
func TestSeedAgents_DoesNotClobberLiveEventData(t *testing.T) {
	m := NewModel(nil)

	// Simulate a live event having already populated the tree.
	liveEvent := Event{
		Time:    time.Now(),
		Type:    "update",
		Actor:   "greenplace/polecats/chrome",
		Rig:     "greenplace",
		Role:    "polecat",
		Message: "working hard",
	}
	m.addEvent(liveEvent)

	src := &mockAgentBeadSource{
		beads: map[string]*beads.Issue{
			"gu-greenplace-polecat-chrome": newAgentBead("gu-greenplace-polecat-chrome"),
			"gu-greenplace-polecat-guzzle": newAgentBead("gu-greenplace-polecat-guzzle"),
		},
	}

	m.SeedAgents(nil, map[string]AgentBeadSource{"greenplace": src})

	m.mu.RLock()
	defer m.mu.RUnlock()

	rig := m.rigs["greenplace"]
	chrome := rig.Agents["greenplace/polecats/chrome"]
	if chrome == nil {
		t.Fatalf("chrome missing after seed")
	}
	if chrome.LastEvent == nil {
		t.Errorf("chrome LastEvent was clobbered by seed; want preserved")
	}
	if chrome.Status != AgentStatusWorking {
		t.Errorf("chrome Status = %q after seed, want preserved %q",
			chrome.Status, AgentStatusWorking)
	}

	guzzle := rig.Agents["greenplace/polecats/guzzle"]
	if guzzle == nil {
		t.Fatalf("guzzle missing after seed; should have been newly added")
	}
	if guzzle.LastEvent != nil {
		t.Errorf("guzzle was seeded idle but has LastEvent = %+v", guzzle.LastEvent)
	}
}

// TestSeedAgents_SkipsTownLevelAgents verifies that town-level agents
// (mayor, deacon) are not injected into any rig bucket.
func TestSeedAgents_SkipsTownLevelAgents(t *testing.T) {
	m := NewModel(nil)

	townSrc := &mockAgentBeadSource{
		beads: map[string]*beads.Issue{
			"gt-mayor":  newAgentBead("gt-mayor"),
			"gt-deacon": newAgentBead("gt-deacon"),
		},
	}

	m.SeedAgents(townSrc, nil)

	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.rigs) != 0 {
		t.Errorf("town-level agents leaked into m.rigs: %v", keys(m.rigs))
	}
}

// TestSeedAgents_IgnoresCrossRigBeads verifies that a rig source whose
// beads claim to belong to a different rig are skipped. The map key is
// authoritative.
func TestSeedAgents_IgnoresCrossRigBeads(t *testing.T) {
	m := NewModel(nil)

	// Source indexed under "greenplace" but accidentally contains a bead
	// from rig "other".
	crossed := &mockAgentBeadSource{
		beads: map[string]*beads.Issue{
			"gu-greenplace-polecat-chrome": newAgentBead("gu-greenplace-polecat-chrome"),
			"gu-other-polecat-nux":         newAgentBead("gu-other-polecat-nux"),
		},
	}

	m.SeedAgents(nil, map[string]AgentBeadSource{"greenplace": crossed})

	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, ok := m.rigs["other"]; ok {
		t.Errorf("cross-rig bead leaked a rig bucket; m.rigs = %v", keys(m.rigs))
	}
	if rig, ok := m.rigs["greenplace"]; !ok {
		t.Fatalf("expected rig was not seeded")
	} else if len(rig.Agents) != 1 {
		t.Errorf("greenplace rig has %d agents, want 1; agents = %v",
			len(rig.Agents), agentActorKeys(rig.Agents))
	}
}

// TestSeedAgents_HandlesListError verifies that a failing source does not
// abort seeding for the remaining sources.
func TestSeedAgents_HandlesListError(t *testing.T) {
	m := NewModel(nil)

	broken := &mockAgentBeadSource{err: errors.New("dolt down")}
	healthy := &mockAgentBeadSource{
		beads: map[string]*beads.Issue{
			"gu-healthy-witness": newAgentBead("gu-healthy-witness"),
		},
	}

	m.SeedAgents(nil, map[string]AgentBeadSource{
		"broken":  broken,
		"healthy": healthy,
	})

	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, ok := m.rigs["broken"]; ok {
		t.Errorf("broken rig should not have been seeded")
	}
	rig, ok := m.rigs["healthy"]
	if !ok {
		t.Fatalf("healthy rig not seeded")
	}
	if _, ok := rig.Agents["healthy/witness"]; !ok {
		t.Errorf("witness not seeded; agents = %v", agentActorKeys(rig.Agents))
	}
}

// TestSeedAgents_NilSourceMapSkips verifies that a nil rigSrcs map is
// tolerated (no panic, no rigs seeded).
func TestSeedAgents_NilSourceMapSkips(t *testing.T) {
	m := NewModel(nil)

	m.SeedAgents(nil, nil)

	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.rigs) != 0 {
		t.Errorf("SeedAgents(nil, nil) populated rigs: %v", keys(m.rigs))
	}
}

// TestSeedAgents_EventsDecorateSeededAgents verifies the full lifecycle:
// seeded idle agents get decorated (not replaced) when events arrive.
func TestSeedAgents_EventsDecorateSeededAgents(t *testing.T) {
	m := NewModel(nil)

	src := &mockAgentBeadSource{
		beads: map[string]*beads.Issue{
			"gu-greenplace-polecat-chrome": newAgentBead("gu-greenplace-polecat-chrome"),
		},
	}
	m.SeedAgents(nil, map[string]AgentBeadSource{"greenplace": src})

	// Simulate an event arriving for the seeded agent.
	eventTime := time.Now()
	m.addEvent(Event{
		Time:    eventTime,
		Type:    "update",
		Actor:   "greenplace/polecats/chrome",
		Rig:     "greenplace",
		Role:    "polecat",
		Message: "now working",
	})

	m.mu.RLock()
	defer m.mu.RUnlock()

	rig := m.rigs["greenplace"]
	chrome := rig.Agents["greenplace/polecats/chrome"]
	if chrome == nil {
		t.Fatalf("chrome missing")
	}
	if chrome.LastEvent == nil {
		t.Fatalf("event did not decorate seeded agent")
	}
	if chrome.Status != AgentStatusWorking {
		t.Errorf("chrome Status = %q after event, want %q",
			chrome.Status, AgentStatusWorking)
	}
	// The seeded agent should have been reused, not duplicated.
	if len(rig.Agents) != 1 {
		t.Errorf("expected 1 agent after event, got %d; agents = %v",
			len(rig.Agents), agentActorKeys(rig.Agents))
	}
}

// TestBuildActor exercises the actor-key format contract. If this test
// changes, addEventLocked must change in lockstep.
func TestBuildActor(t *testing.T) {
	tests := []struct {
		name  string
		rig   string
		role  string
		aName string
		want  string
	}{
		{"polecat", "gp", "polecat", "chrome", "gp/polecats/chrome"},
		{"crew", "gp", "crew", "joe", "gp/crew/joe"},
		{"witness", "gp", "witness", "", "gp/witness"},
		{"refinery", "gp", "refinery", "", "gp/refinery"},
		{"polecat missing name", "gp", "polecat", "", ""},
		{"crew missing name", "gp", "crew", "", ""},
		{"unknown role", "gp", "mystery", "x", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildActor(tc.rig, tc.role, tc.aName)
			if got != tc.want {
				t.Errorf("buildActor(%q, %q, %q) = %q, want %q",
					tc.rig, tc.role, tc.aName, got, tc.want)
			}
		})
	}
}

// keys returns map keys in unspecified order; used for diagnostic output.
func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// agentActorKeys returns the keys of a map[string]*Agent for diagnostics.
func agentActorKeys(m map[string]*Agent) []string {
	return keys(m)
}
