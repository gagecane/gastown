package polecat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSessionHeartbeat_IsV3 pins the v3-detection contract: any v3 field's
// presence implies the writer was v3-aware. v1/v2 readers ignore unknown
// fields, but v3 readers can use IsV3 to decide whether to consult
// LastKeepalive / KeepaliveOp / Liveness / ExpectedIdleUntil.
func TestSessionHeartbeat_IsV3(t *testing.T) {
	cases := []struct {
		name string
		hb   SessionHeartbeat
		want bool
	}{
		{"v1 only", SessionHeartbeat{Timestamp: time.Now()}, false},
		{"v2 with state", SessionHeartbeat{Timestamp: time.Now(), State: HeartbeatWorking}, false},
		{"v3 with last_keepalive", SessionHeartbeat{Timestamp: time.Now(), LastKeepalive: time.Now()}, true},
		{"v3 with keepalive_op", SessionHeartbeat{Timestamp: time.Now(), KeepaliveOp: "llm-call"}, true},
		{"v3 with liveness signal", SessionHeartbeat{Timestamp: time.Now(), Liveness: LivenessSignalKeepalive}, true},
		{"v3 with expected_idle_until", SessionHeartbeat{Timestamp: time.Now(), ExpectedIdleUntil: time.Now().Add(15 * time.Minute)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.hb.IsV3(); got != c.want {
				t.Errorf("IsV3() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestEffectiveLastKeepalive_Max pins the v3 freshness rule: the effective
// freshness is max(timestamp, last_keepalive). v1/v2 callers fall through
// to Timestamp because LastKeepalive is zero.
func TestEffectiveLastKeepalive_Max(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name string
		hb   SessionHeartbeat
		want time.Time
	}{
		{
			name: "v1: only timestamp",
			hb:   SessionHeartbeat{Timestamp: now},
			want: now,
		},
		{
			name: "v3: keepalive after timestamp",
			hb:   SessionHeartbeat{Timestamp: now.Add(-5 * time.Minute), LastKeepalive: now},
			want: now,
		},
		{
			name: "v3: timestamp after keepalive (rare — state-bearing touch supersedes)",
			hb:   SessionHeartbeat{Timestamp: now, LastKeepalive: now.Add(-5 * time.Minute)},
			want: now,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.hb.EffectiveLastKeepalive()
			if !got.Equal(tt.want) {
				t.Errorf("EffectiveLastKeepalive() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestKeepaliveWithOp_WritesV3Fields pins the cv-p3fem Phase 3 contract:
// Keepalive bumps both Timestamp and LastKeepalive, sets KeepaliveOp from
// the op argument, and stamps Liveness=keepalive on the file. v1/v2
// readers continue to see fresh Timestamp; v3 readers can distinguish a
// keepalive from a state-bearing touch.
func TestKeepaliveWithOp_WritesV3Fields(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-v3-fields"
	KeepaliveWithOp(townRoot, session, "llm-call")

	hb := ReadSessionHeartbeat(townRoot, session)
	if hb == nil {
		t.Fatal("expected heartbeat after KeepaliveWithOp")
	}
	if hb.LastKeepalive.IsZero() {
		t.Error("LastKeepalive must be set on v3 keepalive write")
	}
	if hb.KeepaliveOp != "llm-call" {
		t.Errorf("KeepaliveOp = %q, want %q", hb.KeepaliveOp, "llm-call")
	}
	if hb.Liveness != LivenessSignalKeepalive {
		t.Errorf("Liveness = %q, want %q", hb.Liveness, LivenessSignalKeepalive)
	}
	if !hb.IsV3() {
		t.Error("expected IsV3()=true after Keepalive")
	}
}

// TestTouchSessionHeartbeatWithState_LivenessSignal pins the v3 write
// classification: a state-bearing touch lands Liveness=alive (or =exiting
// when state==exiting). Plugin authors rely on this to distinguish a
// keepalive from a real touch.
func TestTouchSessionHeartbeatWithState_LivenessSignal(t *testing.T) {
	townRoot := t.TempDir()
	cases := []struct {
		state HeartbeatState
		want  LivenessSignal
	}{
		{HeartbeatWorking, LivenessSignalAlive},
		{HeartbeatIdle, LivenessSignalAlive},
		{HeartbeatStuck, LivenessSignalAlive},
		{HeartbeatExiting, LivenessSignalExiting},
	}
	for _, c := range cases {
		session := "gt-test-touch-" + string(c.state)
		TouchSessionHeartbeatWithState(townRoot, session, c.state, "ctx", "bead")
		hb := ReadSessionHeartbeat(townRoot, session)
		if hb == nil {
			t.Fatalf("expected heartbeat for state=%s", c.state)
		}
		if hb.Liveness != c.want {
			t.Errorf("state=%s: Liveness = %q, want %q", c.state, hb.Liveness, c.want)
		}
		if hb.LastKeepalive.IsZero() {
			t.Errorf("state=%s: LastKeepalive must be bumped on state-bearing touch", c.state)
		}
	}
}

// TestLiveness_NoFile_ReturnsUnknown pins the fail-open default: a missing
// heartbeat file MUST yield UNKNOWN, not MAYBE_DEAD or DEAD. Pre-rollout
// sessions look exactly like this; reaping them blindly would be a
// regression.
func TestLiveness_NoFile_ReturnsUnknown(t *testing.T) {
	townRoot := t.TempDir()
	rep := Liveness(townRoot, "gt-test-missing", DefaultLivenessThresholds)
	if rep.Verdict != LivenessUnknown {
		t.Errorf("verdict = %q, want %q", rep.Verdict, LivenessUnknown)
	}
	if rep.VerdictReason != ReasonNoHeartbeatFile {
		t.Errorf("reason = %q, want %q", rep.VerdictReason, ReasonNoHeartbeatFile)
	}
}

// TestLiveness_FreshHeartbeat_ReturnsAlive pins the happy path: a freshly
// touched heartbeat yields ALIVE.
func TestLiveness_FreshHeartbeat_ReturnsAlive(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-fresh"
	TouchSessionHeartbeat(townRoot, session)

	rep := Liveness(townRoot, session, DefaultLivenessThresholds)
	if rep.Verdict != LivenessAlive {
		t.Errorf("verdict = %q, want %q", rep.Verdict, LivenessAlive)
	}
	if rep.VerdictReason != ReasonKeepaliveFresh && rep.VerdictReason != ReasonHeartbeatFresh {
		t.Errorf("reason = %q, want fresh keepalive or heartbeat", rep.VerdictReason)
	}
}

// TestLiveness_GraceWindow_ReturnsMaybeDead pins the verdict mapping for
// stale-but-not-dead heartbeats (no PID probe).
func TestLiveness_GraceWindow_ReturnsMaybeDead(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-grace"
	writeStaleHeartbeat(t, townRoot, session, 7*time.Minute)

	rep := Liveness(townRoot, session, DefaultLivenessThresholds)
	if rep.Verdict != LivenessMaybeDead {
		t.Errorf("verdict = %q, want %q (in grace window without PID corroboration)",
			rep.Verdict, LivenessMaybeDead)
	}
}

// TestLiveness_PastDeadThreshold_NoCorroboration_StaysMaybeDead pins the
// gs-549 fix: past the dead threshold, without PID corroboration, the
// verdict tops out at MAYBE_DEAD. A single bad signal class (only
// heartbeat staleness) cannot escalate to DEAD.
func TestLiveness_PastDeadThreshold_NoCorroboration_StaysMaybeDead(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-past-dead-no-corrob"
	writeStaleHeartbeat(t, townRoot, session, 30*time.Minute)

	rep := Liveness(townRoot, session, DefaultLivenessThresholds)
	if rep.Verdict == LivenessDead {
		t.Errorf("verdict = DEAD, want != DEAD (single-signal escalation forbidden)")
	}
}

// TestLivenessWithPID_PIDGoneShortcircuits pins the PID fast-path
// (cv-p3fem open-question 3 recommendation): when the PID is provably
// gone, return DEAD with reason=pid_gone immediately, skipping further
// threshold logic. This is the unambiguous death signal.
func TestLivenessWithPID_PIDGoneShortcircuits(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-pid-gone"
	TouchSessionHeartbeat(townRoot, session) // fresh

	probe := func(name string) (bool, bool) { return false, true }
	rep := LivenessWithPID(townRoot, session, DefaultLivenessThresholds, probe)
	if rep.Verdict != LivenessDead {
		t.Errorf("verdict = %q, want DEAD (PID gone overrides fresh heartbeat)", rep.Verdict)
	}
	if rep.VerdictReason != ReasonPIDGone {
		t.Errorf("reason = %q, want %q", rep.VerdictReason, ReasonPIDGone)
	}
}

// TestLivenessWithPID_PIDAliveSuppressesDead pins the multi-signal
// corroboration: even past the dead threshold, a live PID keeps the
// verdict at MAYBE_DEAD. Combined with the dog plugin's per-agent gate
// (heartbeat AND PID must both fail), this closes gs-549 structurally.
func TestLivenessWithPID_PIDAliveSuppressesDead(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-pid-alive-stale"
	writeStaleHeartbeat(t, townRoot, session, 30*time.Minute)

	probe := func(name string) (bool, bool) { return true, true }
	rep := LivenessWithPID(townRoot, session, DefaultLivenessThresholds, probe)
	if rep.Verdict == LivenessDead {
		t.Errorf("verdict = DEAD, want != DEAD when PID is alive")
	}
}

// TestLiveness_RecoveryMarker_ShortcircuitsAlive pins the operator-override
// contract (gu-v5mk, cv-p3fem decision #9): a manual recovery marker
// short-circuits to ALIVE regardless of all other signals.
func TestLiveness_RecoveryMarker_ShortcircuitsAlive(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-recovery-override"
	writeStaleHeartbeat(t, townRoot, session, 1*time.Hour)

	if err := WriteRecoveryMarker(townRoot, session, "operator", "manual recovery", 0); err != nil {
		t.Fatal(err)
	}

	rep := Liveness(townRoot, session, DefaultLivenessThresholds)
	if rep.Verdict != LivenessAlive {
		t.Errorf("verdict = %q, want ALIVE (recovery marker short-circuits)", rep.Verdict)
	}
	if !rep.Recovered {
		t.Error("expected Recovered=true when recovery marker is active")
	}
	if rep.VerdictReason != ReasonRecoveryMarker {
		t.Errorf("reason = %q, want %q", rep.VerdictReason, ReasonRecoveryMarker)
	}
}

// TestLiveness_StateExiting_AliveWithinGrace pins the exit-tombstone
// suppression: an agent in the gt done flow (state=exiting) is treated as
// alive within the grace window even if its timestamp would otherwise
// call this stale. We don't extend past the grace window — a true crash
// mid-`gt done` shouldn't be invisible.
func TestLiveness_StateExiting_AliveWithinGrace(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-exiting"
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	hb := SessionHeartbeat{
		Timestamp: time.Now().Add(-7 * time.Minute).UTC(), // inside grace
		State:     HeartbeatExiting,
		Liveness:  LivenessSignalExiting,
	}
	data, err := json.Marshal(hb)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, session+".json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	rep := Liveness(townRoot, session, DefaultLivenessThresholds)
	if rep.Verdict != LivenessAlive {
		t.Errorf("verdict = %q, want ALIVE for state=exiting within grace", rep.Verdict)
	}
	if rep.VerdictReason != ReasonStateExiting {
		t.Errorf("reason = %q, want %q", rep.VerdictReason, ReasonStateExiting)
	}
}

// TestLiveness_ExpectedIdleUntil_FutureTimeAlive pins the cv-p3fem
// open-question 1 (gu-x9qc-approved) self-report behavior: an in-flight
// ExpectedIdleUntil keeps the verdict at ALIVE until the declared idle
// expires.
func TestLiveness_ExpectedIdleUntil_FutureTimeAlive(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-idle-future"
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	hb := SessionHeartbeat{
		Timestamp:         time.Now().Add(-7 * time.Minute).UTC(), // would be MAYBE_DEAD
		State:             HeartbeatIdle,
		ExpectedIdleUntil: time.Now().Add(5 * time.Minute).UTC(),
	}
	data, err := json.Marshal(hb)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, session+".json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	rep := Liveness(townRoot, session, DefaultLivenessThresholds)
	if rep.Verdict != LivenessAlive {
		t.Errorf("verdict = %q, want ALIVE during expected idle window", rep.Verdict)
	}
	if rep.VerdictReason != ReasonExpectedIdle {
		t.Errorf("reason = %q, want %q", rep.VerdictReason, ReasonExpectedIdle)
	}
}

// TestSetExpectedIdleUntil_CapsAtRigCap pins the security-mitigation #2
// behavior: ExpectedIdleUntil writes are silently capped at the per-rig
// cap. A wedged agent declaring +24h cannot suppress detection forever.
func TestSetExpectedIdleUntil_CapsAtRigCap(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-idle-cap"
	cap := 30 * time.Minute
	requested := time.Now().Add(24 * time.Hour)
	if err := SetExpectedIdleUntil(townRoot, session, requested, cap); err != nil {
		t.Fatal(err)
	}
	hb := ReadSessionHeartbeat(townRoot, session)
	if hb == nil {
		t.Fatal("expected heartbeat")
	}
	if hb.ExpectedIdleUntil.IsZero() {
		t.Fatal("ExpectedIdleUntil must be set")
	}
	maxAllowed := time.Now().Add(cap + time.Minute) // small grace
	if hb.ExpectedIdleUntil.After(maxAllowed) {
		t.Errorf("ExpectedIdleUntil %v exceeded cap %v", hb.ExpectedIdleUntil, cap)
	}
}

// TestSessionHeartbeat_V3RoundTripJSON pins the v3 JSON shape so plugin
// authors and the bash dog can lock in the field names. The test is
// intentionally explicit: schema drift is a P0 break.
func TestSessionHeartbeat_V3RoundTripJSON(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	hb := SessionHeartbeat{
		Timestamp:         now,
		State:             HeartbeatWorking,
		Context:           "ctx",
		Bead:              "gu-test",
		LastKeepalive:     now,
		KeepaliveOp:       "llm-call",
		Liveness:          LivenessSignalKeepalive,
		ExpectedIdleUntil: now.Add(10 * time.Minute),
	}
	data, err := json.Marshal(hb)
	if err != nil {
		t.Fatal(err)
	}
	var out SessionHeartbeat
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.KeepaliveOp != "llm-call" {
		t.Errorf("KeepaliveOp = %q, want llm-call", out.KeepaliveOp)
	}
	if out.Liveness != LivenessSignalKeepalive {
		t.Errorf("Liveness = %q, want %q", out.Liveness, LivenessSignalKeepalive)
	}
	if out.ExpectedIdleUntil.IsZero() {
		t.Error("ExpectedIdleUntil must round-trip")
	}
	// Verify the JSON keys that plugin authors will lock in.
	expectedKeys := []string{`"last_keepalive"`, `"keepalive_op"`, `"liveness"`, `"expected_idle_until"`}
	s := string(data)
	for _, k := range expectedKeys {
		if !contains(s, k) {
			t.Errorf("v3 JSON missing key %s; plugin contract break", k)
		}
	}
}

// TestIsSessionHeartbeatStale_PrefersLastKeepalive pins the v3 freshness
// rule for legacy callers: max(timestamp, last_keepalive) so a v3 file
// with a fresh keepalive but stale state-bearing timestamp is still
// treated as fresh by the IsSessionHeartbeatStale shim.
func TestIsSessionHeartbeatStale_PrefersLastKeepalive(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-prefer-keepalive"
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	hb := SessionHeartbeat{
		Timestamp:     time.Now().Add(-30 * time.Minute).UTC(), // very stale
		LastKeepalive: time.Now().UTC(),                        // fresh
		State:         HeartbeatWorking,
	}
	data, err := json.Marshal(hb)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, session+".json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	stale, exists := IsSessionHeartbeatStale(townRoot, session)
	if !exists {
		t.Fatal("expected exists=true")
	}
	if stale {
		t.Error("expected stale=false: last_keepalive is fresh")
	}
}

// writeStaleHeartbeat seeds a heartbeat at age=now-staleness with the
// minimum v2 shape so age tests exercise the heartbeat-only Liveness path.
func writeStaleHeartbeat(t *testing.T, townRoot, session string, staleness time.Duration) {
	t.Helper()
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	hb := SessionHeartbeat{
		Timestamp: time.Now().Add(-staleness).UTC(),
		State:     HeartbeatWorking,
	}
	data, err := json.Marshal(hb)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, session+".json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
