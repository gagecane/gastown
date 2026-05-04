package feed

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
)

// mockHealthSource is a test double for HealthDataSource
type mockHealthSource struct {
	agents      map[string]*beads.Issue
	sessions    map[string]bool
	listErr     error
	sessionErr  error           // if set, IsSessionAlive returns this error
	knownRigs   map[string]bool // registered rig prefixes (with trailing hyphen); empty disables phantom detection
	knownRigErr error           // if set, KnownRigPrefixes returns this error
}

func newMockHealthSource() *mockHealthSource {
	return &mockHealthSource{
		agents:   make(map[string]*beads.Issue),
		sessions: make(map[string]bool),
	}
}

func (m *mockHealthSource) ListAgentBeads() (map[string]*beads.Issue, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.agents, nil
}

func (m *mockHealthSource) IsSessionAlive(sessionName string) (bool, error) {
	if m.sessionErr != nil {
		return false, m.sessionErr
	}
	return m.sessions[sessionName], nil
}

func (m *mockHealthSource) KnownRigPrefixes() (map[string]bool, error) {
	if m.knownRigErr != nil {
		return nil, m.knownRigErr
	}
	if m.knownRigs == nil {
		return nil, nil
	}
	return m.knownRigs, nil
}

// TestAgentStateString tests the String() method for all AgentState values
func TestAgentStateString(t *testing.T) {
	tests := []struct {
		state    AgentState
		expected string
	}{
		{StateGUPPViolation, "gupp"},
		{StateStalled, "stalled"},
		{StateWorking, "working"},
		{StateIdle, "idle"},
		{StateZombie, "zombie"},
		{StatePhantom, "phantom"},
		{AgentState(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.state.String(); got != tt.expected {
				t.Errorf("AgentState(%d).String() = %q, want %q", tt.state, got, tt.expected)
			}
		})
	}
}

// TestAgentStatePriority tests that priorities are ordered correctly
func TestAgentStatePriority(t *testing.T) {
	if StateGUPPViolation.Priority() >= StateStalled.Priority() {
		t.Error("GUPP violation should have higher priority than stalled")
	}
	if StateStalled.Priority() >= StateWorking.Priority() {
		t.Error("Stalled should have higher priority than working")
	}
	if StateWorking.Priority() >= StateIdle.Priority() {
		t.Error("Working should have higher priority than idle")
	}
	if StateIdle.Priority() >= StateZombie.Priority() {
		t.Error("Idle should have higher priority than zombie")
	}
	if StateZombie.Priority() >= StatePhantom.Priority() {
		t.Error("Zombie should have higher priority than phantom")
	}
}

// TestAgentStateNeedsAttention tests which states require user attention
func TestAgentStateNeedsAttention(t *testing.T) {
	needsAttention := []AgentState{
		StateGUPPViolation,
		StateStalled,
		StateZombie,
		StatePhantom,
	}
	noAttention := []AgentState{
		StateWorking,
		StateIdle,
	}

	for _, state := range needsAttention {
		if !state.NeedsAttention() {
			t.Errorf("%s.NeedsAttention() = false, want true", state)
		}
	}
	for _, state := range noAttention {
		if state.NeedsAttention() {
			t.Errorf("%s.NeedsAttention() = true, want false", state)
		}
	}
}

// TestAgentStateSymbol tests the display symbols
func TestAgentStateSymbol(t *testing.T) {
	tests := []struct {
		state    AgentState
		expected string
	}{
		{StateGUPPViolation, "🔥"},
		{StateStalled, "⚠"},
		{StateWorking, "●"},
		{StateIdle, "○"},
		{StateZombie, "💀"},
		{StatePhantom, "🪦"},
		{AgentState(99), "?"},
	}

	for _, tt := range tests {
		t.Run(tt.state.String(), func(t *testing.T) {
			if got := tt.state.Symbol(); got != tt.expected {
				t.Errorf("AgentState(%d).Symbol() = %q, want %q", tt.state, got, tt.expected)
			}
		})
	}
}

// TestAgentStateLabel tests the display labels
func TestAgentStateLabel(t *testing.T) {
	tests := []struct {
		state    AgentState
		expected string
	}{
		{StateGUPPViolation, "GUPP!"},
		{StateStalled, "STALL"},
		{StateWorking, "work"},
		{StateIdle, "idle"},
		{StateZombie, "dead"},
		{StatePhantom, "phant"},
		{AgentState(99), "???"},
	}

	for _, tt := range tests {
		t.Run(tt.state.String(), func(t *testing.T) {
			if got := tt.state.Label(); got != tt.expected {
				t.Errorf("AgentState(%d).Label() = %q, want %q", tt.state, got, tt.expected)
			}
		})
	}
}

// TestIsGUPPViolation tests the GUPP violation detection
func TestIsGUPPViolation(t *testing.T) {
	tests := []struct {
		name          string
		hasHookedWork bool
		minutes       int
		expected      bool
	}{
		{"no work, no time", false, 0, false},
		{"no work, long time", false, 60, false},
		{"has work, short time", true, 10, false},
		{"has work, at threshold", true, 30, true},
		{"has work, over threshold", true, 45, true},
		{"has work, just under threshold", true, 29, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsGUPPViolation(tt.hasHookedWork, tt.minutes); got != tt.expected {
				t.Errorf("IsGUPPViolation(%v, %d) = %v, want %v",
					tt.hasHookedWork, tt.minutes, got, tt.expected)
			}
		})
	}
}

// TestProblemAgentDurationDisplay tests the human-readable duration formatting
func TestProblemAgentDurationDisplay(t *testing.T) {
	tests := []struct {
		minutes  int
		expected string
	}{
		{0, "<1m"},
		{1, "1m"},
		{5, "5m"},
		{59, "59m"},
		{60, "1h"},
		{61, "1h1m"},
		{90, "1h30m"},
		{120, "2h"},
		{125, "2h5m"},
		{180, "3h"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			agent := &ProblemAgent{IdleMinutes: tt.minutes}
			if got := agent.DurationDisplay(); got != tt.expected {
				t.Errorf("ProblemAgent{IdleMinutes: %d}.DurationDisplay() = %q, want %q",
					tt.minutes, got, tt.expected)
			}
		})
	}
}

// TestProblemAgentNeedsAttention tests the NeedsAttention delegation
func TestProblemAgentNeedsAttention(t *testing.T) {
	tests := []struct {
		state    AgentState
		expected bool
	}{
		{StateGUPPViolation, true},
		{StateStalled, true},
		{StateZombie, true},
		{StateWorking, false},
		{StateIdle, false},
	}

	for _, tt := range tests {
		t.Run(tt.state.String(), func(t *testing.T) {
			agent := &ProblemAgent{State: tt.state}
			if got := agent.NeedsAttention(); got != tt.expected {
				t.Errorf("ProblemAgent{State: %s}.NeedsAttention() = %v, want %v",
					tt.state, got, tt.expected)
			}
		})
	}
}

// TestThresholdConstants verifies the threshold constants are reasonable
func TestThresholdConstants(t *testing.T) {
	if GUPPViolationMinutes != 30 {
		t.Errorf("GUPPViolationMinutes = %d, want 30", GUPPViolationMinutes)
	}
	if StalledThresholdMinutes != 15 {
		t.Errorf("StalledThresholdMinutes = %d, want 15", StalledThresholdMinutes)
	}
	if GUPPViolationMinutes <= StalledThresholdMinutes {
		t.Error("GUPP violation threshold should be longer than stalled threshold")
	}
}

// TestCheckAll_GUPPViolation tests that agents with hook + >30min stale are detected as GUPP
func TestCheckAll_GUPPViolation(t *testing.T) {
	mock := newMockHealthSource()
	mock.agents["gt-gastown-polecat-Toast"] = &beads.Issue{
		ID:        "gt-gastown-polecat-Toast",
		HookBead:  "gt-abc12",
		UpdatedAt: time.Now().Add(-45 * time.Minute).Format(time.RFC3339),
	}
	mock.sessions["gt-Toast"] = true // session alive

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].State != StateGUPPViolation {
		t.Errorf("expected StateGUPPViolation, got %s", agents[0].State)
	}
	if !agents[0].HasHookedWork {
		t.Error("expected HasHookedWork to be true")
	}
	if agents[0].Name != "Toast" {
		t.Errorf("expected name 'Toast', got %q", agents[0].Name)
	}
}

// TestCheckAll_Stalled tests that agents with hook + >15min stale are detected as stalled
func TestCheckAll_Stalled(t *testing.T) {
	mock := newMockHealthSource()
	mock.agents["gt-gastown-polecat-Pearl"] = &beads.Issue{
		ID:        "gt-gastown-polecat-Pearl",
		HookBead:  "gt-def34",
		UpdatedAt: time.Now().Add(-20 * time.Minute).Format(time.RFC3339),
	}
	mock.sessions["gt-Pearl"] = true

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].State != StateStalled {
		t.Errorf("expected StateStalled, got %s", agents[0].State)
	}
}

// TestCheckAll_Working tests that agents with hook + recent update are working
func TestCheckAll_Working(t *testing.T) {
	mock := newMockHealthSource()
	mock.agents["gt-gastown-polecat-Max"] = &beads.Issue{
		ID:        "gt-gastown-polecat-Max",
		HookBead:  "gt-xyz89",
		UpdatedAt: time.Now().Add(-2 * time.Minute).Format(time.RFC3339),
	}
	mock.sessions["gt-Max"] = true

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].State != StateWorking {
		t.Errorf("expected StateWorking, got %s", agents[0].State)
	}
}

// TestCheckAll_Idle tests that agents with no hook are idle
func TestCheckAll_Idle(t *testing.T) {
	mock := newMockHealthSource()
	mock.agents["gt-gastown-polecat-Joe"] = &beads.Issue{
		ID:        "gt-gastown-polecat-Joe",
		HookBead:  "", // no hooked work
		UpdatedAt: time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
	}
	mock.sessions["gt-Joe"] = true

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].State != StateIdle {
		t.Errorf("expected StateIdle, got %s", agents[0].State)
	}
}

// TestCheckAll_Zombie tests that agents with dead sessions are zombies
func TestCheckAll_Zombie(t *testing.T) {
	mock := newMockHealthSource()
	mock.agents["gt-gastown-polecat-Dead"] = &beads.Issue{
		ID:        "gt-gastown-polecat-Dead",
		HookBead:  "gt-work1",
		UpdatedAt: time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
	}
	// session NOT alive (not in mock.sessions)

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].State != StateZombie {
		t.Errorf("expected StateZombie, got %s", agents[0].State)
	}
}

// TestCheckAll_MultipleAgents tests sorting with multiple agents in different states
func TestCheckAll_MultipleAgents(t *testing.T) {
	mock := newMockHealthSource()
	now := time.Now()

	// GUPP violation agent
	mock.agents["gt-gastown-polecat-Stuck"] = &beads.Issue{
		ID:        "gt-gastown-polecat-Stuck",
		HookBead:  "gt-work1",
		UpdatedAt: now.Add(-40 * time.Minute).Format(time.RFC3339),
	}
	mock.sessions["gt-Stuck"] = true

	// Working agent
	mock.agents["gt-gastown-polecat-Happy"] = &beads.Issue{
		ID:        "gt-gastown-polecat-Happy",
		HookBead:  "gt-work2",
		UpdatedAt: now.Add(-2 * time.Minute).Format(time.RFC3339),
	}
	mock.sessions["gt-Happy"] = true

	// Idle agent
	mock.agents["gt-gastown-polecat-Lazy"] = &beads.Issue{
		ID:        "gt-gastown-polecat-Lazy",
		HookBead:  "",
		UpdatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339),
	}
	mock.sessions["gt-Lazy"] = true

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(agents))
	}

	// Should be sorted: GUPP first, then Working, then Idle
	if agents[0].State != StateGUPPViolation {
		t.Errorf("first agent should be GUPP violation, got %s", agents[0].State)
	}
	if agents[1].State != StateWorking {
		t.Errorf("second agent should be Working, got %s", agents[1].State)
	}
	if agents[2].State != StateIdle {
		t.Errorf("third agent should be Idle, got %s", agents[2].State)
	}
}

// TestCheckAll_Empty tests with no agent beads
func TestCheckAll_Empty(t *testing.T) {
	mock := newMockHealthSource()

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
}

// TestCheckAll_ListError tests error handling when ListAgentBeads fails
func TestCheckAll_ListError(t *testing.T) {
	mock := newMockHealthSource()
	mock.listErr = beads.ErrNotInstalled

	detector := NewStuckDetectorWithSource(mock)
	_, err := detector.CheckAll()
	if err == nil {
		t.Error("expected error from CheckAll")
	}
}

// TestCheckAll_TownLevelAgent tests detection of town-level agents (mayor, deacon)
func TestCheckAll_TownLevelAgent(t *testing.T) {
	mock := newMockHealthSource()
	mock.agents["hq-mayor"] = &beads.Issue{
		ID:        "hq-mayor",
		HookBead:  "",
		UpdatedAt: time.Now().Add(-3 * time.Minute).Format(time.RFC3339),
	}
	mock.sessions["hq-mayor"] = true

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Role != "mayor" {
		t.Errorf("expected role 'mayor', got %q", agents[0].Role)
	}
	if agents[0].SessionID != "hq-mayor" {
		t.Errorf("expected session 'hq-mayor', got %q", agents[0].SessionID)
	}
	if agents[0].State != StateIdle {
		t.Errorf("expected StateIdle, got %s", agents[0].State)
	}
}

// TestCheckAll_RigSingleton tests detection of rig-level singletons (witness, refinery)
func TestCheckAll_RigSingleton(t *testing.T) {
	mock := newMockHealthSource()
	mock.agents["gt-gastown-witness"] = &beads.Issue{
		ID:        "gt-gastown-witness",
		HookBead:  "",
		UpdatedAt: time.Now().Add(-1 * time.Minute).Format(time.RFC3339),
	}
	mock.sessions["gt-witness"] = true

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Role != "witness" {
		t.Errorf("expected role 'witness', got %q", agents[0].Role)
	}
	if agents[0].Rig != "gastown" {
		t.Errorf("expected rig 'gastown', got %q", agents[0].Rig)
	}
	if agents[0].SessionID != "gt-witness" {
		t.Errorf("expected session 'gt-witness', got %q", agents[0].SessionID)
	}
}

// TestCheckAll_CrewAgent tests detection of crew agents
func TestCheckAll_CrewAgent(t *testing.T) {
	mock := newMockHealthSource()
	mock.agents["gt-gastown-crew-joe"] = &beads.Issue{
		ID:        "gt-gastown-crew-joe",
		HookBead:  "gt-task1",
		UpdatedAt: time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
	}
	mock.sessions["gt-crew-joe"] = true

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Role != "crew" {
		t.Errorf("expected role 'crew', got %q", agents[0].Role)
	}
	if agents[0].SessionID != "gt-crew-joe" {
		t.Errorf("expected session 'gt-crew-joe', got %q", agents[0].SessionID)
	}
	if agents[0].State != StateWorking {
		t.Errorf("expected StateWorking, got %s", agents[0].State)
	}
}

// TestDeriveSessionName tests the session name derivation for all agent types
func TestDeriveSessionName(t *testing.T) {
	tests := []struct {
		name     string
		rig      string
		role     string
		agentNm  string
		expected string
	}{
		{"mayor", "", "mayor", "", "hq-mayor"},
		{"deacon", "", "deacon", "", "hq-deacon"},
		{"witness", "gastown", "witness", "", "gt-witness"},
		{"refinery", "gastown", "refinery", "", "gt-refinery"},
		{"crew", "gastown", "crew", "joe", "gt-crew-joe"},
		{"polecat", "gastown", "polecat", "Toast", "gt-Toast"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveSessionName(tt.rig, tt.role, tt.agentNm)
			if got != tt.expected {
				t.Errorf("deriveSessionName(%q, %q, %q) = %q, want %q",
					tt.rig, tt.role, tt.agentNm, got, tt.expected)
			}
		})
	}
}

// TestCheckAll_InvalidBeadID tests that invalid bead IDs are skipped
func TestCheckAll_InvalidBeadID(t *testing.T) {
	mock := newMockHealthSource()
	mock.agents["x-invalid"] = &beads.Issue{
		ID:        "x-invalid",
		UpdatedAt: time.Now().Format(time.RFC3339),
	}

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	// Invalid bead ID should be skipped (ParseAgentBeadID returns ok=false for single-char prefix)
	// "x-invalid" has prefix "x" which is < 2 chars, so ParseAgentBeadID will return false
	if len(agents) != 0 {
		t.Errorf("expected 0 agents for invalid bead ID, got %d", len(agents))
	}
}

// TestCheckAll_SessionError tests that IsSessionAlive errors don't cause false zombies
func TestCheckAll_SessionError(t *testing.T) {
	mock := newMockHealthSource()
	mock.agents["gt-gastown-polecat-Alpha"] = &beads.Issue{
		ID:        "gt-gastown-polecat-Alpha",
		HookBead:  "gt-work1",
		UpdatedAt: time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
	}
	// Session error (e.g., tmux socket contention) - should NOT mark as zombie
	mock.sessionErr = fmt.Errorf("tmux: socket not found")

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].State == StateZombie {
		t.Errorf("agent should NOT be zombie when IsSessionAlive returns error, got %s", agents[0].State)
	}
	// Should be Working (has hook, 5 min idle < 15 min stalled threshold)
	if agents[0].State != StateWorking {
		t.Errorf("expected StateWorking, got %s", agents[0].State)
	}
}

// TestCheckAll_RalphcatNotStalled tests that a ralphcat with 45min idle is NOT stalled
// (would be stalled for a normal polecat at the 15min threshold)
func TestCheckAll_RalphcatNotStalled(t *testing.T) {
	mock := newMockHealthSource()
	mock.agents["gt-gastown-polecat-Ralph"] = &beads.Issue{
		ID:       "gt-gastown-polecat-Ralph",
		HookBead: "gt-abc12",
		// 45 minutes idle — stalled for normal polecat, but fine for ralphcat
		UpdatedAt: time.Now().Add(-45 * time.Minute).Format(time.RFC3339),
		// Description contains mode: ralph (agent fields)
		Description: "Polecat Ralph\n\nrole_type: polecat\nrig: gastown\nagent_state: working\nhook_bead: gt-abc12\ncleanup_status: null\nactive_mr: null\nnotification_level: null\nmode: ralph",
	}
	mock.sessions["gt-Ralph"] = true // session alive

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	// At 45 min, normal polecat would be GUPP (>=30min). Ralphcat threshold is 240min.
	// At 45 min idle, ralphcat should be Working (< 120min stalled threshold).
	if agents[0].State != StateWorking {
		t.Errorf("expected StateWorking for ralphcat at 45min idle, got %s", agents[0].State)
	}
}

// TestCheckAll_RalphcatStalled tests that a ralphcat IS stalled after 2+ hours
func TestCheckAll_RalphcatStalled(t *testing.T) {
	mock := newMockHealthSource()
	mock.agents["gt-gastown-polecat-Ralph2"] = &beads.Issue{
		ID:          "gt-gastown-polecat-Ralph2",
		HookBead:    "gt-def34",
		UpdatedAt:   time.Now().Add(-150 * time.Minute).Format(time.RFC3339), // 2.5 hours
		Description: "Polecat Ralph2\n\nrole_type: polecat\nrig: gastown\nagent_state: working\nhook_bead: gt-def34\ncleanup_status: null\nactive_mr: null\nnotification_level: null\nmode: ralph",
	}
	mock.sessions["gt-Ralph2"] = true

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	// 150 min > 120 min stalled threshold for ralphcat
	if agents[0].State != StateStalled {
		t.Errorf("expected StateStalled for ralphcat at 150min idle, got %s", agents[0].State)
	}
}

// TestCheckAll_RalphcatGUPP tests that a ralphcat with 5h idle IS in GUPP violation
func TestCheckAll_RalphcatGUPP(t *testing.T) {
	mock := newMockHealthSource()
	mock.agents["gt-gastown-polecat-Ralph3"] = &beads.Issue{
		ID:          "gt-gastown-polecat-Ralph3",
		HookBead:    "gt-ghi56",
		UpdatedAt:   time.Now().Add(-300 * time.Minute).Format(time.RFC3339), // 5 hours
		Description: "Polecat Ralph3\n\nrole_type: polecat\nrig: gastown\nagent_state: working\nhook_bead: gt-ghi56\ncleanup_status: null\nactive_mr: null\nnotification_level: null\nmode: ralph",
	}
	mock.sessions["gt-Ralph3"] = true

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	// 300 min > 240 min GUPP threshold for ralphcat
	if agents[0].State != StateGUPPViolation {
		t.Errorf("expected StateGUPPViolation for ralphcat at 300min idle, got %s", agents[0].State)
	}
}

// TestIsRalphMode tests the ralph mode detection from agent bead description
func TestIsRalphMode(t *testing.T) {
	tests := []struct {
		name     string
		issue    *beads.Issue
		expected bool
	}{
		{
			name:     "nil issue",
			issue:    nil,
			expected: false,
		},
		{
			name:     "empty description",
			issue:    &beads.Issue{Description: ""},
			expected: false,
		},
		{
			name:     "no mode field",
			issue:    &beads.Issue{Description: "role_type: polecat\nrig: gastown"},
			expected: false,
		},
		{
			name:     "mode ralph",
			issue:    &beads.Issue{Description: "role_type: polecat\nmode: ralph"},
			expected: true,
		},
		{
			name:     "mode other",
			issue:    &beads.Issue{Description: "role_type: polecat\nmode: other"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRalphMode(tt.issue); got != tt.expected {
				t.Errorf("isRalphMode() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestNudgeTarget tests the nudge target format for all agent types
func TestNudgeTarget(t *testing.T) {
	tests := []struct {
		name     string
		agent    *ProblemAgent
		expected string
	}{
		{
			name:     "mayor",
			agent:    &ProblemAgent{Role: "mayor", Name: "mayor", Rig: ""},
			expected: "mayor",
		},
		{
			name:     "deacon",
			agent:    &ProblemAgent{Role: "deacon", Name: "deacon", Rig: ""},
			expected: "deacon",
		},
		{
			name:     "witness",
			agent:    &ProblemAgent{Role: "witness", Name: "witness", Rig: "gastown"},
			expected: "gastown/witness",
		},
		{
			name:     "refinery",
			agent:    &ProblemAgent{Role: "refinery", Name: "refinery", Rig: "gastown"},
			expected: "gastown/refinery",
		},
		{
			name:     "crew",
			agent:    &ProblemAgent{Role: "crew", Name: "joe", Rig: "gastown"},
			expected: "gastown/crew/joe",
		},
		{
			name:     "polecat",
			agent:    &ProblemAgent{Role: "polecat", Name: "Toast", Rig: "gastown"},
			expected: "gastown/Toast",
		},
		{
			name:     "unknown role falls back to session ID",
			agent:    &ProblemAgent{Role: "custom", Name: "x", Rig: "r", SessionID: "gt-r-custom-x"},
			expected: "gt-r-custom-x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nudgeTarget(tt.agent)
			if got != tt.expected {
				t.Errorf("nudgeTarget() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// ----- Tests for gu-aadt: false-positive stuck-detection fixes -----
//
// Three fixes:
//  1. Post-nuke idle polecats (no hook + dead session) → StateIdle, not zombie.
//  2. Crew (humans) never zombie-flagged via dead-session timer.
//  3. Rig-level agents whose prefix isn't in routes.jsonl → StatePhantom.
//
// Each category is tested here with and without the gating conditions so we
// verify the logic, not just the happy path.

// TestCheckAll_PolecatIdlePostNuke: polecat with no hook + dead session is
// the documented "Nuked, identity persists" state. It should read as idle,
// NOT zombie.
func TestCheckAll_PolecatIdlePostNuke(t *testing.T) {
	mock := newMockHealthSource()
	mock.agents["gt-gastown-polecat-furiosa"] = &beads.Issue{
		ID:        "gt-gastown-polecat-furiosa",
		HookBead:  "", // no hooked work — post-nuke
		UpdatedAt: time.Now().Add(-125 * time.Hour).Format(time.RFC3339),
	}
	// Session NOT alive (not in mock.sessions) — nuked after completion.

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].State != StateIdle {
		t.Errorf("post-nuke polecat should be StateIdle, got %s", agents[0].State)
	}
	if agents[0].NeedsAttention() {
		t.Error("post-nuke idle polecat should NOT need attention")
	}
}

// TestCheckAll_PolecatZombieWithHook: the OTHER side of the polecat/idle
// fix — if a polecat's session is dead but it HAS hooked work, that's a
// real mid-task crash and should still be flagged as zombie.
func TestCheckAll_PolecatZombieWithHook(t *testing.T) {
	mock := newMockHealthSource()
	mock.agents["gt-gastown-polecat-crashed"] = &beads.Issue{
		ID:        "gt-gastown-polecat-crashed",
		HookBead:  "gu-work1", // hooked work abandoned
		UpdatedAt: time.Now().Add(-20 * time.Minute).Format(time.RFC3339),
	}
	// Session NOT alive.

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].State != StateZombie {
		t.Errorf("polecat with hook + dead session should be zombie, got %s", agents[0].State)
	}
}

// TestCheckAll_CrewLoggedOffNoHook: crew with no hook + dead session is
// just "human is logged off" → idle, NOT zombie.
func TestCheckAll_CrewLoggedOffNoHook(t *testing.T) {
	mock := newMockHealthSource()
	mock.agents["ro-ralph-crew-canewiw"] = &beads.Issue{
		ID:        "ro-ralph-crew-canewiw",
		HookBead:  "",
		UpdatedAt: time.Now().Add(-161 * time.Hour).Format(time.RFC3339),
	}
	// Session NOT alive — human isn't logged in.

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].State != StateIdle {
		t.Errorf("crew with no hook + dead session should be StateIdle (logged off), got %s", agents[0].State)
	}
	if agents[0].NeedsAttention() {
		t.Error("logged-off crew should NOT need attention")
	}
}

// TestCheckAll_CrewLoggedOffWithStaleHook: if crew has hooked work that's
// gone stale past the stalled threshold, that IS worth flagging even with
// a dead session — the human owes work. Use stalled/gupp, not zombie.
func TestCheckAll_CrewLoggedOffWithStaleHook(t *testing.T) {
	mock := newMockHealthSource()
	mock.agents["gt-gastown-crew-human"] = &beads.Issue{
		ID:        "gt-gastown-crew-human",
		HookBead:  "gu-todo",
		UpdatedAt: time.Now().Add(-45 * time.Minute).Format(time.RFC3339), // >gupp
	}
	// Session dead.

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].State != StateGUPPViolation {
		t.Errorf("crew with stale hook should be StateGUPPViolation, got %s", agents[0].State)
	}
	if agents[0].State == StateZombie {
		t.Error("crew should never be flagged as StateZombie via dead session")
	}
}

// TestCheckAll_CrewSessionAliveRecentNoHook: crew that IS online (session
// alive) with no hook is just idle, not a problem.
func TestCheckAll_CrewSessionAliveNoHook(t *testing.T) {
	mock := newMockHealthSource()
	mock.agents["gt-gastown-crew-active"] = &beads.Issue{
		ID:        "gt-gastown-crew-active",
		HookBead:  "",
		UpdatedAt: time.Now().Add(-3 * time.Minute).Format(time.RFC3339),
	}
	mock.sessions["gt-crew-active"] = true

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].State != StateIdle {
		t.Errorf("active crew with no hook should be idle, got %s", agents[0].State)
	}
}

// TestCheckAll_PhantomRig: agent bead for a rig whose prefix isn't in
// routes.jsonl should surface as StatePhantom with a cleanup hint.
func TestCheckAll_PhantomRig(t *testing.T) {
	mock := newMockHealthSource()
	mock.knownRigs = map[string]bool{
		"gt-": true,
		"bd-": true,
		"hq-": true,
	}

	// cgs- is not in the known prefix set — rig was removed.
	mock.agents["cgs-codegenscheduler-polecat-chrome"] = &beads.Issue{
		ID:        "cgs-codegenscheduler-polecat-chrome",
		HookBead:  "",
		UpdatedAt: time.Now().Add(-120 * time.Hour).Format(time.RFC3339),
	}

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].State != StatePhantom {
		t.Errorf("agent with unregistered rig prefix should be StatePhantom, got %s", agents[0].State)
	}
	if !agents[0].NeedsAttention() {
		t.Error("phantom agents should need attention (cleanup)")
	}
	if !strings.Contains(agents[0].ActionHint, "cleanup") {
		t.Errorf("phantom hint should mention cleanup, got %q", agents[0].ActionHint)
	}
}

// TestCheckAll_PhantomRigWitness: phantom detection also applies to rig
// singletons (witness, refinery).
func TestCheckAll_PhantomRigWitness(t *testing.T) {
	mock := newMockHealthSource()
	mock.knownRigs = map[string]bool{"gt-": true}

	mock.agents["crd-removed-witness"] = &beads.Issue{
		ID:        "crd-removed-witness",
		HookBead:  "",
		UpdatedAt: time.Now().Add(-5 * time.Hour).Format(time.RFC3339),
	}

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].State != StatePhantom {
		t.Errorf("orphaned rig singleton should be StatePhantom, got %s", agents[0].State)
	}
}

// TestCheckAll_PhantomSkipsTownLevel: mayor/deacon/dog are town-level —
// they don't belong to any rig, so they can't be phantom.
func TestCheckAll_PhantomSkipsTownLevel(t *testing.T) {
	mock := newMockHealthSource()
	mock.knownRigs = map[string]bool{"gt-": true} // hq- not registered

	mock.agents["hq-mayor"] = &beads.Issue{
		ID:        "hq-mayor",
		HookBead:  "",
		UpdatedAt: time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
	}
	mock.sessions["hq-mayor"] = true

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].State == StatePhantom {
		t.Error("town-level agents (mayor) must never be flagged as phantom")
	}
}

// TestCheckAll_PhantomDisabledWhenPrefixesUnknown: if routes.jsonl is
// unavailable (empty prefix set), we can't tell phantom from legitimate,
// so we MUST NOT flag anything as phantom. This is the degrade-gracefully
// guard.
func TestCheckAll_PhantomDisabledWhenPrefixesUnknown(t *testing.T) {
	mock := newMockHealthSource()
	// mock.knownRigs left nil — simulates routes.jsonl missing.

	mock.agents["cgs-removed-polecat-chrome"] = &beads.Issue{
		ID:        "cgs-removed-polecat-chrome",
		HookBead:  "",
		UpdatedAt: time.Now().Add(-120 * time.Hour).Format(time.RFC3339),
	}

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].State == StatePhantom {
		t.Error("must not flag phantom when known-prefix set is empty/unknown")
	}
	// Should fall through to standard polecat logic: no hook + dead session → idle.
	if agents[0].State != StateIdle {
		t.Errorf("expected StateIdle (post-nuke fallback), got %s", agents[0].State)
	}
}

// TestCheckAll_PhantomPrefixErrorIgnored: if KnownRigPrefixes returns an
// error, we degrade gracefully and don't flag phantoms.
func TestCheckAll_PhantomPrefixErrorIgnored(t *testing.T) {
	mock := newMockHealthSource()
	mock.knownRigErr = fmt.Errorf("routes.jsonl unreadable")

	mock.agents["gt-gastown-polecat-ok"] = &beads.Issue{
		ID:        "gt-gastown-polecat-ok",
		HookBead:  "",
		UpdatedAt: time.Now().Add(-2 * time.Minute).Format(time.RFC3339),
	}
	mock.sessions["gt-ok"] = true

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll should not surface phantom-prefix errors: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].State == StatePhantom {
		t.Error("must not flag phantom when KnownRigPrefixes errored")
	}
}

// TestCheckAll_PhantomTakesPriority: when an agent is BOTH phantom AND
// would otherwise be zombie (dead session + hook), phantom wins because
// cleanup is the right action.
func TestCheckAll_PhantomTakesPriority(t *testing.T) {
	mock := newMockHealthSource()
	mock.knownRigs = map[string]bool{"gt-": true}

	// con- is unregistered, has hook, session dead — without phantom rule
	// this would be a zombie.
	mock.agents["con-removed-polecat-chrome"] = &beads.Issue{
		ID:        "con-removed-polecat-chrome",
		HookBead:  "gu-stale",
		UpdatedAt: time.Now().Add(-100 * time.Hour).Format(time.RFC3339),
	}

	detector := NewStuckDetectorWithSource(mock)
	agents, err := detector.CheckAll()
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].State != StatePhantom {
		t.Errorf("phantom should beat zombie classification, got %s", agents[0].State)
	}
}

// TestIsRigLevelAgentRole exercises the helper that gates phantom detection.
func TestIsRigLevelAgentRole(t *testing.T) {
	tests := []struct {
		role string
		want bool
	}{
		{constants.RolePolecat, true},
		{constants.RoleCrew, true},
		{constants.RoleWitness, true},
		{constants.RoleRefinery, true},
		{constants.RoleMayor, false},
		{constants.RoleDeacon, false},
		{"dog", false},
		{"unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			if got := isRigLevelAgentRole(tt.role); got != tt.want {
				t.Errorf("isRigLevelAgentRole(%q) = %v, want %v", tt.role, got, tt.want)
			}
		})
	}
}
