package witness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/wisp"
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

// TestDetectStaleRigAgentHeartbeats_ParkedRigSkips verifies the gu-qwe7q/gu-eke9u
// fix: a PARKED rig whose agents are intentionally stopped and whose heartbeats
// are frozen must NOT escalate. The frozen heartbeat is the expected result of
// the park, not a wedge — escalating it produced a HIGH false positive that
// re-fired every cycle. Both candidates are recorded as "skip-parked" with no
// escalation, even though both heartbeats are well past the threshold and the
// session is reported alive.
func TestDetectStaleRigAgentHeartbeats_ParkedRigSkips(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"
	prefix := session.PrefixFor(rigName)
	refSession := session.RefinerySessionName(prefix)
	witSession := session.WitnessSessionName(prefix)

	// Sessions reported alive AND heartbeats stale: without the parked guard this
	// is the gu-rh0g escalate-everything path. The park must override it.
	installFakeTmuxAlive(t, refSession, witSession)
	writeRigAgentHeartbeat(t, townRoot, refSession, 6*time.Hour)
	writeRigAgentHeartbeat(t, townRoot, witSession, 6*time.Hour)

	// Mark the rig parked via the wisp layer (the fast path "gt rig park" uses).
	if err := wisp.NewConfig(townRoot, rigName).Set(rig.WispStatusKey, rig.WispStatusParked); err != nil {
		t.Fatalf("set parked wisp status: %v", err)
	}

	res := DetectStaleRigAgentHeartbeats(townRoot, rigName, nil, time.Hour, "", 0, 0, nil)
	if res.Checked != 2 {
		t.Fatalf("Checked = %d, want 2 (refinery+witness)", res.Checked)
	}
	for _, s := range res.Stale {
		if s.Action != "skip-parked" {
			t.Errorf("%s Action = %q, want skip-parked", s.AgentRole, s.Action)
		}
		if s.MailSent {
			t.Errorf("%s MailSent = true, want false (parked rig must not escalate)", s.AgentRole)
		}
	}
}

// writeRigAgentHeartbeatV3 writes a v3 heartbeat with explicit agent-reported
// state so the staleness detector can carry it into the escalation (gu-8ni5o).
func writeRigAgentHeartbeatV3(t *testing.T, townRoot, sessionName string, age time.Duration, state polecat.HeartbeatState, op, ctx, bead string) {
	t.Helper()
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir heartbeats: %v", err)
	}
	hb := polecat.SessionHeartbeat{
		Timestamp:   time.Now().UTC().Add(-age),
		State:       state,
		KeepaliveOp: op,
		Context:     ctx,
		Bead:        bead,
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

// TestDetectStaleRigAgentHeartbeats_CarriesAgentState verifies the detector
// surfaces the agent's last-reported state on the result so the escalation can
// include idle-vs-mid-op triage context without a manual pane capture (gu-8ni5o).
func TestDetectStaleRigAgentHeartbeats_CarriesAgentState(t *testing.T) {
	installFakeTmuxNoServer(t)

	townRoot := t.TempDir()
	rigName := "testrig"
	prefix := session.PrefixFor(rigName)
	writeRigAgentHeartbeatV3(t, townRoot, session.RefinerySessionName(prefix), 2*time.Hour,
		polecat.HeartbeatWorking, "go-test", "running gate", "gu-bead1")
	writeRigAgentHeartbeat(t, townRoot, session.WitnessSessionName(prefix), 30*time.Second)

	res := DetectStaleRigAgentHeartbeats(townRoot, rigName, nil, time.Hour, "", 0, 0, nil)

	var refinery *StaleRigAgentResult
	for i := range res.Stale {
		if res.Stale[i].AgentRole == "refinery" {
			refinery = &res.Stale[i]
		}
	}
	if refinery == nil {
		t.Fatal("missing refinery result")
	}
	if refinery.LastState != polecat.HeartbeatWorking {
		t.Errorf("LastState = %q, want working", refinery.LastState)
	}
	if refinery.LastKeepaliveOp != "go-test" {
		t.Errorf("LastKeepaliveOp = %q, want go-test", refinery.LastKeepaliveOp)
	}
	if refinery.LastBead != "gu-bead1" {
		t.Errorf("LastBead = %q, want gu-bead1", refinery.LastBead)
	}
}

// TestDetectStaleRigAgentHeartbeats_IdleWitnessCleanCycleSuppressed verifies the
// gu-eke9u fix: an ALIVE witness whose stale heartbeat last self-reported a
// clean-cycle idle-ready state (state=idle) is recorded "skip-idle-clean-cycle"
// and does NOT escalate. This is the discrete-cycle idle-ready witness parked at
// the prompt between deacon nudges — its heartbeat age tracks last-nudge-time,
// not health. The expected-idle-window variant is exercised too.
func TestDetectStaleRigAgentHeartbeats_IdleWitnessCleanCycleSuppressed(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"
	prefix := session.PrefixFor(rigName)
	witSession := session.WitnessSessionName(prefix)

	installFakeTmuxAlive(t, witSession)

	// Witness heartbeat stale past threshold but last state idle; session alive.
	writeRigAgentHeartbeatV3(t, townRoot, witSession, 6*time.Hour,
		polecat.HeartbeatIdle, "", "standing ready", "")

	res := DetectStaleRigAgentHeartbeats(townRoot, rigName, nil, time.Hour, "", 0, 0, nil)

	witness := findStaleResult(res, "witness")
	if witness == nil {
		t.Fatalf("missing witness result")
	}
	if !witness.SessionAlive {
		t.Fatalf("witness SessionAlive = false, want true (fake tmux should report it alive)")
	}
	if witness.Action != "skip-idle-clean-cycle" {
		t.Errorf("witness Action = %q, want skip-idle-clean-cycle", witness.Action)
	}
	if witness.MailSent {
		t.Errorf("witness MailSent = true, want false (idle clean-cycle must not escalate)")
	}
}

// TestDetectStaleRigAgentHeartbeats_IdleWitnessGateSafetyCarveouts verifies the
// gu-eke9u gate does NOT regress the gu-rh0g real-wedge signal:
//   - working + alive + stale STILL escalates (real mid-op freeze)
//   - idle + DEAD session STILL escalates (died right after reporting idle)
//   - exiting + alive + stale STILL escalates (conservative: possible stuck-in-done)
func TestDetectStaleRigAgentHeartbeats_IdleWitnessGateSafetyCarveouts(t *testing.T) {
	rigName := "testrig"
	prefix := session.PrefixFor(rigName)
	witSession := session.WitnessSessionName(prefix)

	t.Run("working alive stale escalates", func(t *testing.T) {
		townRoot := t.TempDir()
		installFakeTmuxAlive(t, witSession)
		writeRigAgentHeartbeatV3(t, townRoot, witSession, 6*time.Hour,
			polecat.HeartbeatWorking, "patrol-scan", "mid cycle", "")
		res := DetectStaleRigAgentHeartbeats(townRoot, rigName, nil, time.Hour, "", 0, 0, nil)
		witness := findStaleResult(res, "witness")
		if witness == nil {
			t.Fatalf("missing witness result")
		}
		if witness.Action != "escalated" {
			t.Errorf("witness Action = %q, want escalated (working mid-op must still escalate)", witness.Action)
		}
	})

	t.Run("idle dead escalates", func(t *testing.T) {
		townRoot := t.TempDir()
		// Server down → session reported dead even though last state was idle.
		installFakeTmuxNoServer(t)
		writeRigAgentHeartbeatV3(t, townRoot, witSession, 6*time.Hour,
			polecat.HeartbeatIdle, "", "standing ready", "")
		res := DetectStaleRigAgentHeartbeats(townRoot, rigName, nil, time.Hour, "", 0, 0, nil)
		witness := findStaleResult(res, "witness")
		if witness == nil {
			t.Fatalf("missing witness result")
		}
		if witness.SessionAlive {
			t.Fatalf("witness SessionAlive = true, want false (server down)")
		}
		if witness.Action != "escalated" {
			t.Errorf("witness Action = %q, want escalated (dead session must escalate regardless of last state)", witness.Action)
		}
	})

	t.Run("exiting alive stale escalates", func(t *testing.T) {
		townRoot := t.TempDir()
		installFakeTmuxAlive(t, witSession)
		writeRigAgentHeartbeatV3(t, townRoot, witSession, 6*time.Hour,
			polecat.HeartbeatExiting, "", "done", "")
		res := DetectStaleRigAgentHeartbeats(townRoot, rigName, nil, time.Hour, "", 0, 0, nil)
		witness := findStaleResult(res, "witness")
		if witness == nil {
			t.Fatalf("missing witness result")
		}
		if witness.Action != "escalated" {
			t.Errorf("witness Action = %q, want escalated (exiting is conservatively excluded from the idle gate)", witness.Action)
		}
	})
}

func TestStaleAgentDisposition(t *testing.T) {
	cases := []struct {
		name string
		item StaleRigAgentResult
		want string // substring expected in the disposition
	}{
		{"idle is false positive", StaleRigAgentResult{LastState: polecat.HeartbeatIdle}, "FALSE POSITIVE"},
		{"exiting is false positive", StaleRigAgentResult{LastState: polecat.HeartbeatExiting}, "FALSE POSITIVE"},
		{"working is real wedge", StaleRigAgentResult{LastState: polecat.HeartbeatWorking}, "REAL WEDGE"},
		{"stuck is real wedge", StaleRigAgentResult{LastState: polecat.HeartbeatStuck}, "REAL WEDGE"},
		{"no state is unknown", StaleRigAgentResult{}, "UNKNOWN"},
		{"future idle-until is false positive", StaleRigAgentResult{
			LastState:         polecat.HeartbeatWorking,
			ExpectedIdleUntil: time.Now().UTC().Add(time.Hour),
		}, "FALSE POSITIVE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := staleAgentDisposition(tc.item)
			if !contains(got, tc.want) {
				t.Errorf("disposition = %q, want substring %q", got, tc.want)
			}
		})
	}
}

// TestStaleAgentTriageContext_EmptyForV1 verifies a v1 heartbeat (no state)
// yields no triage block, so the mail omits it rather than printing blanks.
func TestStaleAgentTriageContext_EmptyForV1(t *testing.T) {
	if got := staleAgentTriageContext(StaleRigAgentResult{}); got != "" {
		t.Errorf("expected empty triage context for v1 heartbeat, got:\n%s", got)
	}
	got := staleAgentTriageContext(StaleRigAgentResult{LastState: polecat.HeartbeatWorking})
	if !contains(got, "REAL WEDGE") || !contains(got, "state:") {
		t.Errorf("expected triage block with state+disposition, got:\n%s", got)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
