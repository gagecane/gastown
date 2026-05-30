// liveness_test.go — cv-p3fem Phase 3 verdict reader tests.
//
// Coverage:
//   - PID fast-path (gone process → DEAD without consulting heartbeat).
//   - Recovery marker override (operator wins).
//   - keepalive_fresh vs heartbeat_fresh reason disambiguation.
//   - MAYBE_DEAD grace window vs DEAD past dead threshold.
//   - expected_idle_until self-report path (and its TTL ceiling).
//   - JSON shape stability (plugin contract).
//   - Mass-kill corroboration: multiple stale heartbeats with live PIDs
//     must NOT each individually be DEAD (the gs-549 trap).

package polecat

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// liveTestSetup returns a fresh tmp town root, a session name, and the path
// to its heartbeat file. Caller is responsible for writing the file.
func liveTestSetup(t *testing.T) (string, string) {
	t.Helper()
	town := t.TempDir()
	if err := os.MkdirAll(heartbeatsDir(town), 0755); err != nil {
		t.Fatalf("mkdir heartbeats: %v", err)
	}
	return town, "test-sess"
}

func writeHB(t *testing.T, town, name string, hb SessionHeartbeat) {
	t.Helper()
	data, err := json.Marshal(hb)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(heartbeatFile(town, name), data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestLiveness_NoFile_Unknown(t *testing.T) {
	town, name := liveTestSetup(t)
	r := Liveness(town, name, LivenessOptions{})
	if r.Verdict != LivenessUnknown {
		t.Fatalf("verdict = %v, want UNKNOWN", r.VerdictString)
	}
	if r.VerdictReason != ReasonNoHeartbeatFile {
		t.Fatalf("reason = %s, want %s", r.VerdictReason, ReasonNoHeartbeatFile)
	}
}

func TestLiveness_Fresh_Alive(t *testing.T) {
	town, name := liveTestSetup(t)
	now := time.Now().UTC()
	writeHB(t, town, name, SessionHeartbeat{
		Timestamp:     now.Add(-30 * time.Second),
		LastKeepalive: now.Add(-5 * time.Second),
		State:         HeartbeatWorking,
		KeepaliveOp:   "llm-call",
	})
	r := Liveness(town, name, LivenessOptions{})
	if r.Verdict != LivenessAlive {
		t.Fatalf("verdict = %s, want ALIVE", r.VerdictString)
	}
	if r.VerdictReason != ReasonKeepaliveFresh {
		t.Fatalf("reason = %s, want %s (keepalive newer than timestamp)",
			r.VerdictReason, ReasonKeepaliveFresh)
	}
}

func TestLiveness_FreshTimestampNoKeepalive_HeartbeatFresh(t *testing.T) {
	town, name := liveTestSetup(t)
	now := time.Now().UTC()
	writeHB(t, town, name, SessionHeartbeat{
		Timestamp: now.Add(-10 * time.Second),
		State:     HeartbeatWorking,
	})
	r := Liveness(town, name, LivenessOptions{})
	if r.Verdict != LivenessAlive {
		t.Fatalf("verdict = %s, want ALIVE", r.VerdictString)
	}
	if r.VerdictReason != ReasonHeartbeatFresh {
		t.Fatalf("reason = %s, want %s", r.VerdictReason, ReasonHeartbeatFresh)
	}
}

func TestLiveness_StaleButInsideGrace_MaybeDead(t *testing.T) {
	town, name := liveTestSetup(t)
	now := time.Now().UTC()
	// 5m old: past 3m stale, inside 20m dead.
	writeHB(t, town, name, SessionHeartbeat{
		Timestamp:     now.Add(-5 * time.Minute),
		LastKeepalive: now.Add(-5 * time.Minute),
		State:         HeartbeatWorking,
	})
	r := Liveness(town, name, LivenessOptions{})
	if r.Verdict != LivenessMaybeDead {
		t.Fatalf("verdict = %s, want MAYBE_DEAD", r.VerdictString)
	}
	if r.VerdictReason != ReasonInsideGraceWindow {
		t.Fatalf("reason = %s, want %s", r.VerdictReason, ReasonInsideGraceWindow)
	}
}

func TestLiveness_PastDead(t *testing.T) {
	town, name := liveTestSetup(t)
	now := time.Now().UTC()
	writeHB(t, town, name, SessionHeartbeat{
		Timestamp:     now.Add(-30 * time.Minute),
		LastKeepalive: now.Add(-30 * time.Minute),
		State:         HeartbeatWorking,
	})
	r := Liveness(town, name, LivenessOptions{})
	if r.Verdict != LivenessDead {
		t.Fatalf("verdict = %s, want DEAD", r.VerdictString)
	}
	if r.VerdictReason != ReasonPastDeadThreshold {
		t.Fatalf("reason = %s, want %s", r.VerdictReason, ReasonPastDeadThreshold)
	}
}

func TestLiveness_PIDDead_FastPath(t *testing.T) {
	town, name := liveTestSetup(t)
	now := time.Now().UTC()
	// Heartbeat is FRESH — would normally return ALIVE. PID being gone
	// short-circuits to DEAD regardless.
	writeHB(t, town, name, SessionHeartbeat{
		Timestamp:     now,
		LastKeepalive: now,
		State:         HeartbeatWorking,
	})
	// PID 1 is always alive; pick a never-existed sentinel — using a very
	// high PID that isn't in /proc.
	r := Liveness(town, name, LivenessOptions{PID: 999999})
	if r.Verdict != LivenessDead {
		t.Fatalf("verdict = %s, want DEAD (PID gone fast-path)", r.VerdictString)
	}
	if r.VerdictReason != ReasonPIDDead {
		t.Fatalf("reason = %s, want %s", r.VerdictReason, ReasonPIDDead)
	}
}

func TestLiveness_PIDAlive_StaleHeartbeat_MaybeDead(t *testing.T) {
	// gs-549 corroboration test: if the PID is alive but the heartbeat is
	// stale, the verdict must NOT be DEAD. MAYBE_DEAD is the
	// operator-actionable verdict; auto-action requires more.
	town, name := liveTestSetup(t)
	now := time.Now().UTC()
	writeHB(t, town, name, SessionHeartbeat{
		Timestamp:     now.Add(-5 * time.Minute),
		LastKeepalive: now.Add(-5 * time.Minute),
		State:         HeartbeatWorking,
	})
	// Use os.Getpid() — guaranteed alive (it's us).
	r := Liveness(town, name, LivenessOptions{PID: os.Getpid()})
	if r.Verdict != LivenessMaybeDead {
		t.Fatalf("verdict = %s, want MAYBE_DEAD (live PID + stale heartbeat)",
			r.VerdictString)
	}
}

func TestLiveness_RecoveryMarker_OverridesEverything(t *testing.T) {
	town, name := liveTestSetup(t)
	now := time.Now().UTC()
	// Heartbeat would be DEAD without override.
	writeHB(t, town, name, SessionHeartbeat{
		Timestamp:     now.Add(-1 * time.Hour),
		LastKeepalive: now.Add(-1 * time.Hour),
		State:         HeartbeatWorking,
	})
	if err := WriteRecoveryMarker(town, name, "operator", "manual cleanup", time.Hour); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	t.Cleanup(func() { _ = ClearRecoveryMarker(town, name) })

	r := Liveness(town, name, LivenessOptions{PID: 999999})
	if r.Verdict != LivenessAlive {
		t.Fatalf("verdict = %s, want ALIVE (recovery marker overrides)", r.VerdictString)
	}
	if r.VerdictReason != ReasonRecoveryMarker {
		t.Fatalf("reason = %s, want %s", r.VerdictReason, ReasonRecoveryMarker)
	}
}

func TestLiveness_ExpectedIdleUntil_Future_Alive(t *testing.T) {
	town, name := liveTestSetup(t)
	now := time.Now().UTC()
	// Heartbeat 8m old → past stale (3m), inside grace (10m). Without
	// expected_idle_until this would be MAYBE_DEAD; with a future
	// expected_idle_until it should be ALIVE for reason=expected_idle.
	writeHB(t, town, name, SessionHeartbeat{
		Timestamp:         now.Add(-8 * time.Minute),
		LastKeepalive:     now.Add(-8 * time.Minute),
		State:             HeartbeatWorking,
		ExpectedIdleUntil: now.Add(10 * time.Minute),
	})
	r := Liveness(town, name, LivenessOptions{})
	if r.Verdict != LivenessAlive {
		t.Fatalf("verdict = %s, want ALIVE", r.VerdictString)
	}
	if r.VerdictReason != ReasonExpectedIdle {
		t.Fatalf("reason = %s, want %s", r.VerdictReason, ReasonExpectedIdle)
	}
}

func TestLiveness_ExpectedIdleUntil_PastDeadCeiling_DEAD(t *testing.T) {
	// TTL-bounded self-report: past the dead ceiling, expected_idle_until
	// is ignored. A wedged agent that lies about idleness for 24h still
	// gets reaped after dead_seconds.
	town, name := liveTestSetup(t)
	now := time.Now().UTC()
	writeHB(t, town, name, SessionHeartbeat{
		Timestamp:         now.Add(-30 * time.Minute),
		LastKeepalive:     now.Add(-30 * time.Minute),
		State:             HeartbeatIdle,
		ExpectedIdleUntil: now.Add(24 * time.Hour),
	})
	r := Liveness(town, name, LivenessOptions{})
	if r.Verdict != LivenessDead {
		t.Fatalf("verdict = %s, want DEAD (past dead ceiling)", r.VerdictString)
	}
	if r.VerdictReason != ReasonPastDeadThreshold {
		t.Fatalf("reason = %s, want %s", r.VerdictReason, ReasonPastDeadThreshold)
	}
}

// TestLiveness_JSONContract pins the stable plugin contract: the field
// names plugin authors lock in cannot drift across versions. If this test
// fails after a rename, the rename is a breaking contract change — file a
// gu-leg- bead and decide intentionally.
func TestLiveness_JSONContract(t *testing.T) {
	town, name := liveTestSetup(t)
	now := time.Now().UTC()
	writeHB(t, town, name, SessionHeartbeat{
		Timestamp:     now.Add(-12 * time.Second),
		LastKeepalive: now.Add(-8 * time.Second),
		State:         HeartbeatWorking,
		KeepaliveOp:   "llm-call",
		Bead:          "gu-leg-xtwu2",
	})
	r := Liveness(town, name, LivenessOptions{})

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	required := []string{
		"session", "verdict", "verdict_reason",
		"age_seconds", "last_keepalive_age_seconds",
		"state", "keepalive_op", "bead", "thresholds",
	}
	for _, k := range required {
		if _, ok := got[k]; !ok {
			t.Errorf("missing required JSON field %q", k)
		}
	}
	thr, ok := got["thresholds"].(map[string]any)
	if !ok {
		t.Fatalf("thresholds not an object: %v", got["thresholds"])
	}
	for _, k := range []string{"stale_seconds", "grace_seconds", "dead_seconds"} {
		if _, ok := thr[k]; !ok {
			t.Errorf("thresholds missing required field %q", k)
		}
	}
}

// TestLiveness_PerRoleThresholdsRespected exercises the design-doc
// per-role thresholds (witness 5/15/30, refinery 10/30/60).
func TestLiveness_PerRoleThresholdsRespected(t *testing.T) {
	town, name := liveTestSetup(t)
	now := time.Now().UTC()
	// 12m stale: under polecat default dead (20m), but we'll pin per-role
	// thresholds to the witness values (15m grace boundary). Verdict
	// should be MAYBE_DEAD with witness thresholds, ALIVE with refinery.
	writeHB(t, town, name, SessionHeartbeat{
		Timestamp:     now.Add(-12 * time.Minute),
		LastKeepalive: now.Add(-12 * time.Minute),
		State:         HeartbeatWorking,
	})
	witnessOpts := LivenessOptions{Stale: 5 * time.Minute, Grace: 15 * time.Minute, Dead: 30 * time.Minute}
	w := Liveness(town, name, witnessOpts)
	if w.Verdict != LivenessMaybeDead {
		t.Errorf("witness verdict = %s, want MAYBE_DEAD", w.VerdictString)
	}
	refineryOpts := LivenessOptions{Stale: 15 * time.Minute, Grace: 30 * time.Minute, Dead: 60 * time.Minute}
	r := Liveness(town, name, refineryOpts)
	if r.Verdict != LivenessAlive {
		t.Errorf("refinery verdict = %s, want ALIVE (under stale)", r.VerdictString)
	}
}
