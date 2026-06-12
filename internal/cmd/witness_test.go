package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/polecat"
)

// writeStaleWitnessHeartbeat writes a heartbeat file aged past the polecat
// stale threshold (but well inside the dead threshold) so the heartbeat-only
// verdict would be MAYBE_DEAD.
func writeStaleWitnessHeartbeat(t *testing.T, townRoot, session string) {
	t.Helper()
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-7 * time.Minute).UTC() // between Stale(3m) and Grace(10m)
	hb := polecat.SessionHeartbeat{
		Timestamp:     stale,
		LastKeepalive: stale,
		State:         polecat.HeartbeatWorking,
		Liveness:      polecat.LivenessSignalKeepalive,
	}
	data, err := json.Marshal(hb)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, session+".json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

// TestLivenessRowFor_PIDAliveSuppressesMaybeDead pins the gu-d5r8c fix: a stale
// heartbeat whose tmux pane PID is still alive must report ALIVE, not the
// MAYBE_DEAD false positive the heartbeat-only path would yield.
func TestLivenessRowFor_PIDAliveSuppressesMaybeDead(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-witness-live"
	writeStaleWitnessHeartbeat(t, townRoot, session)

	// Heartbeat-only (nil probe) is the pre-fix behavior: MAYBE_DEAD.
	bare := livenessRowFor(townRoot, session, "polecat", polecat.DefaultLivenessThresholds, nil)
	if bare.Verdict != string(polecat.LivenessMaybeDead) {
		t.Fatalf("heartbeat-only verdict = %q, want MAYBE_DEAD (test setup)", bare.Verdict)
	}

	// PID alive corroborates liveness → ALIVE.
	aliveProbe := polecat.PIDLivenessFunc(func(string) (bool, bool) { return true, true })
	row := livenessRowFor(townRoot, session, "polecat", polecat.DefaultLivenessThresholds, aliveProbe)
	if row.Verdict != string(polecat.LivenessAlive) {
		t.Errorf("PID-alive verdict = %q, want ALIVE (transient heartbeat lag tolerated)", row.Verdict)
	}
}

// TestLivenessRowFor_PIDGoneNotAlive pins that PID corroboration does not mask a
// genuinely dead session: a stale heartbeat with a provably-gone PID must never
// read ALIVE. (Per the liveness contract, a queried-and-gone PID short-circuits
// to DEAD, so corroboration tightens — not just loosens — the verdict.)
func TestLivenessRowFor_PIDGoneNotAlive(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-witness-gone"
	writeStaleWitnessHeartbeat(t, townRoot, session)

	goneProbe := polecat.PIDLivenessFunc(func(string) (bool, bool) { return false, true })
	row := livenessRowFor(townRoot, session, "polecat", polecat.DefaultLivenessThresholds, goneProbe)
	if row.Verdict == string(polecat.LivenessAlive) {
		t.Errorf("PID-gone verdict = ALIVE, want DEAD (dead PID must not read alive)")
	}
}

func TestWitnessRestartAgentFlag(t *testing.T) {
	flag := witnessRestartCmd.Flags().Lookup("agent")
	if flag == nil {
		t.Fatal("expected witness restart to define --agent flag")
	}
	if flag.DefValue != "" {
		t.Errorf("expected default agent override to be empty, got %q", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "overrides town default") {
		t.Errorf("expected --agent usage to mention overrides town default, got %q", flag.Usage)
	}
}

func TestWitnessStartAgentFlag(t *testing.T) {
	flag := witnessStartCmd.Flags().Lookup("agent")
	if flag == nil {
		t.Fatal("expected witness start to define --agent flag")
	}
	if flag.DefValue != "" {
		t.Errorf("expected default agent override to be empty, got %q", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "overrides town default") {
		t.Errorf("expected --agent usage to mention overrides town default, got %q", flag.Usage)
	}
}

func TestWitnessStartForegroundFlagHidden(t *testing.T) {
	flag := witnessStartCmd.Flags().Lookup("foreground")
	if flag == nil {
		t.Fatal("expected hidden compatibility --foreground flag")
	}
	if !flag.Hidden {
		t.Fatal("expected --foreground to be hidden")
	}
	if strings.Contains(witnessStartCmd.Long, "--foreground") {
		t.Fatalf("witness start help should not advertise --foreground:\n%s", witnessStartCmd.Long)
	}
}
