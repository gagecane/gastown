package witness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/session"
)

// writeRigAgentHeartbeat writes a heartbeat JSON file with a backdated
// timestamp so the staleness check sees it as `age` old. We bypass
// polecat.TouchSessionHeartbeat (which always stamps Now()) and write the
// SessionHeartbeat shape directly — that's what ReadSessionHeartbeat
// unmarshals.
func writeRigAgentHeartbeat(t *testing.T, townRoot, sessionName string, age time.Duration) {
	t.Helper()
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir heartbeats: %v", err)
	}
	hb := polecat.SessionHeartbeat{
		Timestamp: time.Now().UTC().Add(-age),
		State:     polecat.HeartbeatWorking,
	}
	data, err := json.Marshal(hb)
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}
	path := filepath.Join(dir, sessionName+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}
}

// TestDetectStaleRigAgentHeartbeats_DisabledByZero verifies that staleThreshold
// <= 0 short-circuits the scan entirely. This is the operator opt-out path
// (operational.witness.stale_rig_agent_heartbeat="0").
func TestDetectStaleRigAgentHeartbeats_DisabledByZero(t *testing.T) {
	installFakeTmuxNoServer(t)

	res := DetectStaleRigAgentHeartbeats(t.TempDir(), "testrig", nil, 0, "", 0, 0, nil)
	if res == nil {
		t.Fatalf("DetectStaleRigAgentHeartbeats returned nil")
	}
	if res.Checked != 0 {
		t.Errorf("Checked = %d, want 0 (scan disabled)", res.Checked)
	}
	if len(res.Stale) != 0 {
		t.Errorf("Stale = %d, want 0 (scan disabled)", len(res.Stale))
	}
}

// TestDetectStaleRigAgentHeartbeats_NoSessionNoEscalation verifies that when
// neither refinery nor witness sessions exist (fake-tmux baseline), the
// detector records "skip-no-session" without escalating. This is the common
// docked/parked-rig case — no agent should = no alarm.
func TestDetectStaleRigAgentHeartbeats_NoSessionNoEscalation(t *testing.T) {
	installFakeTmuxNoServer(t)

	townRoot := t.TempDir()
	res := DetectStaleRigAgentHeartbeats(townRoot, "testrig", nil, time.Hour, "", 0, 0, nil)
	if res.Checked != 2 {
		t.Fatalf("Checked = %d, want 2 (refinery+witness)", res.Checked)
	}
	for _, s := range res.Stale {
		if s.Action != "skip-no-session" {
			t.Errorf("%s Action = %q, want skip-no-session", s.AgentRole, s.Action)
		}
		if s.MailSent {
			t.Errorf("%s MailSent = true, want false (no escalation when session absent)", s.AgentRole)
		}
	}
}

// TestDetectStaleRigAgentHeartbeats_FreshSkips verifies that a heartbeat
// younger than the threshold is recorded as "skip-fresh" with no escalation.
func TestDetectStaleRigAgentHeartbeats_FreshSkips(t *testing.T) {
	installFakeTmuxNoServer(t)

	townRoot := t.TempDir()
	rigName := "testrig"
	prefix := session.PrefixFor(rigName)
	writeRigAgentHeartbeat(t, townRoot, session.RefinerySessionName(prefix), 30*time.Second)
	writeRigAgentHeartbeat(t, townRoot, session.WitnessSessionName(prefix), 30*time.Second)

	res := DetectStaleRigAgentHeartbeats(townRoot, rigName, nil, time.Hour, "", 0, 0, nil)
	if res.Checked != 2 {
		t.Fatalf("Checked = %d, want 2", res.Checked)
	}
	for _, s := range res.Stale {
		if s.Action != "skip-fresh" {
			t.Errorf("%s Action = %q, want skip-fresh", s.AgentRole, s.Action)
		}
		if s.MailSent {
			t.Errorf("%s MailSent = true, want false (fresh heartbeat shouldn't escalate)", s.AgentRole)
		}
	}
}

// TestDetectStaleRigAgentHeartbeats_StaleEscalates verifies that a heartbeat
// older than the threshold drives Action="escalated" — the gu-rh0g signature
// (process up but agent loop frozen, or process dead with daemon supervisor
// missing the restart). MailSent is false here because router=nil; the
// router-wired path is covered by integration tests.
func TestDetectStaleRigAgentHeartbeats_StaleEscalates(t *testing.T) {
	installFakeTmuxNoServer(t)

	townRoot := t.TempDir()
	rigName := "testrig"
	prefix := session.PrefixFor(rigName)
	writeRigAgentHeartbeat(t, townRoot, session.RefinerySessionName(prefix), 2*time.Hour)
	// Witness fresh — only refinery should escalate. This isolates the per-agent
	// branch logic so a regression that escalates everything (or nothing) shows.
	writeRigAgentHeartbeat(t, townRoot, session.WitnessSessionName(prefix), 30*time.Second)

	res := DetectStaleRigAgentHeartbeats(townRoot, rigName, nil, time.Hour, "", 0, 0, nil)
	if res.Checked != 2 {
		t.Fatalf("Checked = %d, want 2", res.Checked)
	}

	var refinery, witness *StaleRigAgentResult
	for i := range res.Stale {
		s := &res.Stale[i]
		switch s.AgentRole {
		case "refinery":
			refinery = s
		case "witness":
			witness = s
		}
	}
	if refinery == nil || witness == nil {
		t.Fatalf("missing per-agent result: refinery=%v witness=%v", refinery, witness)
	}
	if refinery.Action != "escalated" {
		t.Errorf("refinery Action = %q, want escalated", refinery.Action)
	}
	if refinery.HeartbeatAge < time.Hour {
		t.Errorf("refinery HeartbeatAge = %s, want >= 1h", refinery.HeartbeatAge)
	}
	if witness.Action != "skip-fresh" {
		t.Errorf("witness Action = %q, want skip-fresh", witness.Action)
	}
}

// TestDetectStaleRigAgentHeartbeats_SelfSkip verifies that the scanning
// agent's own session is never escalated, even when its heartbeat is stale
// past the threshold. This is the gu-vqmmp self-amplifying-flood guard: an
// idle witness whose own session heartbeat aged out (it blocks in
// await-signal between cycles) must NOT escalate itself. The other agent
// (refinery here) still escalates normally.
func TestDetectStaleRigAgentHeartbeats_SelfSkip(t *testing.T) {
	installFakeTmuxNoServer(t)

	townRoot := t.TempDir()
	rigName := "testrig"
	prefix := session.PrefixFor(rigName)
	// Both stale past threshold; witness is the scanning agent ("self").
	writeRigAgentHeartbeat(t, townRoot, session.RefinerySessionName(prefix), 2*time.Hour)
	witnessSession := session.WitnessSessionName(prefix)
	writeRigAgentHeartbeat(t, townRoot, witnessSession, 2*time.Hour)

	res := DetectStaleRigAgentHeartbeats(townRoot, rigName, nil, time.Hour, witnessSession, 0, 0, nil)
	if res.Checked != 2 {
		t.Fatalf("Checked = %d, want 2", res.Checked)
	}

	var refinery, witness *StaleRigAgentResult
	for i := range res.Stale {
		s := &res.Stale[i]
		switch s.AgentRole {
		case "refinery":
			refinery = s
		case "witness":
			witness = s
		}
	}
	if refinery == nil || witness == nil {
		t.Fatalf("missing per-agent result: refinery=%v witness=%v", refinery, witness)
	}
	// The scanning agent (witness) must be skipped despite its stale heartbeat.
	if witness.Action != "skip-self" {
		t.Errorf("witness (self) Action = %q, want skip-self", witness.Action)
	}
	if witness.MailSent {
		t.Errorf("witness (self) MailSent = true, want false (never escalate self)")
	}
	// The other agent (refinery) still escalates normally.
	if refinery.Action != "escalated" {
		t.Errorf("refinery Action = %q, want escalated", refinery.Action)
	}
}
