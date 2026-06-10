package daemon

import (
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// --- Interval tests ---

func TestAgentHeartbeatInterval_Default(t *testing.T) {
	if got := agentHeartbeatInterval(nil); got != defaultAgentHeartbeatInterval {
		t.Errorf("expected default %v, got %v", defaultAgentHeartbeatInterval, got)
	}
}

func TestAgentHeartbeatInterval_Custom(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			AgentHeartbeat: &AgentHeartbeatConfig{Enabled: true, IntervalStr: "2m"},
		},
	}
	if got := agentHeartbeatInterval(cfg); got != 2*time.Minute {
		t.Errorf("expected 2m, got %v", got)
	}
}

func TestAgentHeartbeatInterval_Invalid(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			AgentHeartbeat: &AgentHeartbeatConfig{Enabled: true, IntervalStr: "nonsense"},
		},
	}
	if got := agentHeartbeatInterval(cfg); got != defaultAgentHeartbeatInterval {
		t.Errorf("expected default for invalid interval, got %v", got)
	}
}

// --- IsPatrolEnabled tests (agent_heartbeat is DEFAULT-ON) ---

func TestIsPatrolEnabled_AgentHeartbeat_NilConfigDefaultsOn(t *testing.T) {
	if !IsPatrolEnabled(nil, "agent_heartbeat") {
		t.Error("agent_heartbeat should default ON with nil config")
	}
}

func TestIsPatrolEnabled_AgentHeartbeat_EmptyPatrolsDefaultsOn(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	if !IsPatrolEnabled(cfg, "agent_heartbeat") {
		t.Error("agent_heartbeat should default ON when not explicitly configured")
	}
}

func TestIsPatrolEnabled_AgentHeartbeat_ExplicitlyDisabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			AgentHeartbeat: &AgentHeartbeatConfig{Enabled: false},
		},
	}
	if IsPatrolEnabled(cfg, "agent_heartbeat") {
		t.Error("agent_heartbeat should be disabled when explicitly set false")
	}
}

func TestIsPatrolEnabled_AgentHeartbeat_ExplicitlyEnabled(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			AgentHeartbeat: &AgentHeartbeatConfig{Enabled: true},
		},
	}
	if !IsPatrolEnabled(cfg, "agent_heartbeat") {
		t.Error("agent_heartbeat should be enabled when explicitly set true")
	}
}

// --- Lifecycle defaults ---

func TestEnsureLifecycleDefaults_PopulatesAgentHeartbeat(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	if !EnsureLifecycleDefaults(cfg) {
		t.Fatal("expected EnsureLifecycleDefaults to report a change")
	}
	if cfg.Patrols.AgentHeartbeat == nil {
		t.Fatal("expected AgentHeartbeat to be populated")
	}
	if !cfg.Patrols.AgentHeartbeat.Enabled {
		t.Error("default AgentHeartbeat should be Enabled")
	}
	if cfg.Patrols.AgentHeartbeat.IntervalStr == "" {
		t.Error("default AgentHeartbeat should set an interval")
	}
}

func TestDefaultLifecycleConfig_IncludesAgentHeartbeat(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	if cfg.Patrols == nil || cfg.Patrols.AgentHeartbeat == nil {
		t.Fatal("DefaultLifecycleConfig must include AgentHeartbeat")
	}
	if !cfg.Patrols.AgentHeartbeat.Enabled {
		t.Error("default AgentHeartbeat should be Enabled")
	}
	if cfg.Patrols.AgentHeartbeat.IntervalStr == "" {
		t.Error("default AgentHeartbeat should set an interval")
	}
}

// EnsureLifecycleDefaults must NOT overwrite an explicit operator opt-out.
// Without this guarantee, an operator who set `agent_heartbeat: {enabled:
// false}` would have it silently re-enabled on the next config refresh.
func TestEnsureLifecycleDefaults_AgentHeartbeat_PreservesExplicitDisable(t *testing.T) {
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{
		AgentHeartbeat: &AgentHeartbeatConfig{Enabled: false},
	}}
	_ = EnsureLifecycleDefaults(cfg)
	if cfg.Patrols.AgentHeartbeat.Enabled {
		t.Error("explicit AgentHeartbeat:disabled must survive EnsureLifecycleDefaults")
	}
}

// --- daemonMRProber: idle-empty-mq prober behavior ---

// fakeMRLister is a minimal stand-in for a per-rig beads handle used by
// daemonMRProber tests.
type fakeMRLister struct {
	issues []*beads.Issue
	err    error
}

func (f *fakeMRLister) ListMergeRequests(_ beads.ListOptions) ([]*beads.Issue, error) {
	return f.issues, f.err
}

// TestDaemonMRProber_UnknownRigReturnsNonEmpty defends the conservative
// fallback: a rig the prober cannot resolve must NOT cause the witness
// detector to suppress a stale-heartbeat escalation. Returning a positive
// count keeps the gs-ecdg suppression off for unknown rigs so we err toward
// surfacing the alarm.
func TestDaemonMRProber_UnknownRigReturnsNonEmpty(t *testing.T) {
	p := daemonMRProber{resolve: map[string]daemonMRLister{}}
	got, err := p.PendingMergeRequestCount("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got <= 0 {
		t.Errorf("unknown rig must return non-empty count to keep alarms loud, got %d", got)
	}
}

// TestDaemonMRProber_EmptyQueueReturnsZero verifies the happy idle-rig path:
// a rig whose merge queue has no actionable MRs returns 0 so the witness
// detector's gs-ecdg branch can suppress a false STALE_RIG_AGENT for a
// healthily-idle refinery.
func TestDaemonMRProber_EmptyQueueReturnsZero(t *testing.T) {
	p := daemonMRProber{resolve: map[string]daemonMRLister{
		"alpha": &fakeMRLister{issues: nil},
	}}
	got, err := p.PendingMergeRequestCount("alpha")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("empty queue must return 0, got %d", got)
	}
}

// TestDaemonMRProber_BlockedMRsAreNotActionable mirrors cmd.pendingMRsForRig:
// MRs marked blocked are not counted because the refinery would not have
// dispatched them either. A queue of only-blocked MRs is operationally
// equivalent to empty for this prober.
func TestDaemonMRProber_BlockedMRsAreNotActionable(t *testing.T) {
	blocked := &beads.Issue{
		ID:             "alpha-mr-1",
		Status:         "open",
		BlockedByCount: 1,
	}
	p := daemonMRProber{resolve: map[string]daemonMRLister{
		"alpha": &fakeMRLister{issues: []*beads.Issue{blocked}},
	}}
	got, err := p.PendingMergeRequestCount("alpha")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("only blocked MRs should count as 0 actionable, got %d", got)
	}
}

// TestDaemonMRProber_FiltersByRig verifies the rig-scoping filter: an MR
// targeting rig "beta" must NOT show up in rig "alpha"'s actionable count.
// Without rig scoping, alpha's idle refinery would be falsely flagged as
// non-empty by beta's queue, defeating the gs-ecdg suppression.
func TestDaemonMRProber_FiltersByRig(t *testing.T) {
	betaMR := &beads.Issue{
		ID:          "beta-mr-1",
		Status:      "open",
		Description: "rig: beta\n",
	}
	p := daemonMRProber{resolve: map[string]daemonMRLister{
		"alpha": &fakeMRLister{issues: []*beads.Issue{betaMR}},
	}}
	got, err := p.PendingMergeRequestCount("alpha")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("MR targeting another rig must not count, got %d", got)
	}
}
