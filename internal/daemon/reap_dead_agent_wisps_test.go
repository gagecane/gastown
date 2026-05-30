package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

// --- Test fixtures --------------------------------------------------------
//
// These mirror the helpers in reap_dead_polecat_wisps_test.go, but the
// candidate beads carry an updated_at timestamp because the agent reaper uses
// that field as its staleness proxy (witness/refinery don't write heartbeat
// files).

// writeFakeBDListWithUpdates installs a fake bd binary that responds to
// `bd list --status=<status> --json --limit=0` with the JSON payload supplied
// per status. bd update calls are recorded to bd-update.log.
func writeFakeBDListWithUpdates(t *testing.T, dir string, statusJSON map[string]string) string {
	t.Helper()
	var cases strings.Builder
	for status, payload := range statusJSON {
		escaped := strings.ReplaceAll(payload, "'", "'\\''")
		fmt.Fprintf(&cases, "    *--status=%s*) echo '%s';;\n", status, escaped)
	}
	cases.WriteString("    *) echo '[]';;\n")

	script := "#!/bin/sh\n" +
		"echo \"$@\" >> \"" + filepath.Join(dir, "bd.log") + "\"\n" +
		"if [ \"$1\" = \"list\" ]; then\n" +
		"  case \"$*\" in\n" +
		cases.String() +
		"  esac\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"update\" ]; then\n" +
		"  echo \"$@\" >> \"" + filepath.Join(dir, "bd-update.log") + "\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo \"unexpected bd command: $*\" >&2\n" +
		"exit 1\n"

	bdPath := filepath.Join(dir, "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("writing fake bd: %v", err)
	}
	return bdPath
}

// agentBeadJSON renders a single bead JSON entry for the fake bd output. age
// is how far back updated_at should be relative to now (positive = past).
func agentBeadJSON(id, assignee, status string, age time.Duration) string {
	updated := time.Now().UTC().Add(-age).Format(time.RFC3339Nano)
	return fmt.Sprintf(`{"id":%q,"assignee":%q,"status":%q,"updated_at":%q}`, id, assignee, status, updated)
}

// newAgentReapDaemon constructs a Daemon wired up for the agent-reaper tests:
// tempdir townroot, fake bd on PATH, registered prefix for "myrig" -> "myr".
func newAgentReapDaemon(t *testing.T, logBuf *strings.Builder, bdPath string) (*Daemon, string) {
	t.Helper()
	townRoot := t.TempDir()

	old := session.DefaultRegistry()
	reg := session.NewPrefixRegistry()
	reg.Register("myr", "myrig")
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })

	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(logBuf, "", 0),
		tmux:   tmux.NewTmuxWithSocket(""),
		bdPath: bdPath,
	}
	// Create the rig directory so bd's cwd exists. The reaper runs bd from
	// rigBDWorkingDir(townRoot, rigName) and exec.Command fails fast if the
	// directory is missing.
	if err := os.MkdirAll(filepath.Join(townRoot, "myrig"), 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}
	return d, townRoot
}

// --- Tests ----------------------------------------------------------------

// TestReapDeadAgentWisps_ReapsDeadWitnessWisp is the happy path: a wisp is
// hooked to <rig>/witness, the witness tmux session is gone, and the bead's
// updated_at is older than the timeout. Reaper resets to status=open with
// cleared assignee.
func TestReapDeadAgentWisps_ReapsDeadWitnessWisp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	binDir := t.TempDir()
	writeFakeTmuxDeadSession(t, binDir)
	bdPath := writeFakeBDListWithUpdates(t, binDir, map[string]string{
		"hooked":      "[" + agentBeadJSON("gu-stuck-w", "myrig/witness", "hooked", 3*time.Hour) + "]",
		"in_progress": `[]`,
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d, _ := newAgentReapDaemon(t, &logBuf, bdPath)

	d.reapRigDeadAgentWisps("myrig", agentReapConfig{fallbackTimeout: time.Hour})

	data, err := os.ReadFile(filepath.Join(binDir, "bd-update.log"))
	if err != nil {
		bdLog, _ := os.ReadFile(filepath.Join(binDir, "bd.log"))
		t.Fatalf("no bd update recorded: %v\nbd.log:\n%s\nlogger:\n%s", err, bdLog, logBuf.String())
	}
	updates := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(updates) != 1 {
		t.Fatalf("expected 1 bd update, got %d: %v", len(updates), updates)
	}
	want := "update gu-stuck-w --status=open --assignee="
	if !strings.Contains(updates[0], want) {
		t.Errorf("bd update missing expected args\n want contains: %s\n got: %s", want, updates[0])
	}
	if strings.Contains(updates[0], "--rig=") {
		t.Errorf("bd update should not pass --rig= (bd has no such flag); got: %s", updates[0])
	}

	got := logBuf.String()
	if !strings.Contains(got, "reap-dead-agent-wisps: reset gu-stuck-w") {
		t.Errorf("missing reap log line, got: %q", got)
	}
	if !strings.Contains(got, "role=witness") {
		t.Errorf("log should name the role, got: %q", got)
	}
	if !strings.Contains(got, "prev_status=hooked") {
		t.Errorf("log should record previous status, got: %q", got)
	}
}

// TestReapDeadAgentWisps_ReapsDeadRefineryWisp confirms refinery is also
// covered, not only witness. The role string should reflect refinery.
func TestReapDeadAgentWisps_ReapsDeadRefineryWisp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	binDir := t.TempDir()
	writeFakeTmuxDeadSession(t, binDir)
	bdPath := writeFakeBDListWithUpdates(t, binDir, map[string]string{
		"hooked":      `[]`,
		"in_progress": "[" + agentBeadJSON("gu-stuck-r", "myrig/refinery", "in_progress", 3*time.Hour) + "]",
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d, _ := newAgentReapDaemon(t, &logBuf, bdPath)

	d.reapRigDeadAgentWisps("myrig", agentReapConfig{fallbackTimeout: time.Hour})

	data, err := os.ReadFile(filepath.Join(binDir, "bd-update.log"))
	if err != nil {
		t.Fatalf("expected gu-stuck-r reset, got no update log: %v", err)
	}
	if !strings.Contains(string(data), "gu-stuck-r") {
		t.Errorf("expected gu-stuck-r update, got: %s", data)
	}
	if !strings.Contains(logBuf.String(), "role=refinery") {
		t.Errorf("log should name refinery role, got: %q", logBuf.String())
	}
}

// TestReapDeadAgentWisps_SkipsLiveSession verifies a wisp with a live tmux
// session is left alone, even if the bead is stale. A stale bead alone is
// not proof of death — the role might just be running slowly.
func TestReapDeadAgentWisps_SkipsLiveSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	binDir := t.TempDir()
	writeFakeTmuxLiveSession(t, binDir)
	bdPath := writeFakeBDListWithUpdates(t, binDir, map[string]string{
		"hooked":      "[" + agentBeadJSON("gu-alive", "myrig/witness", "hooked", 3*time.Hour) + "]",
		"in_progress": `[]`,
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d, _ := newAgentReapDaemon(t, &logBuf, bdPath)

	d.reapRigDeadAgentWisps("myrig", agentReapConfig{fallbackTimeout: time.Hour})

	if _, err := os.Stat(filepath.Join(binDir, "bd-update.log")); err == nil {
		data, _ := os.ReadFile(filepath.Join(binDir, "bd-update.log"))
		t.Errorf("bd update was called despite live session: %s", data)
	}
}

// TestReapDeadAgentWisps_SkipsFreshBead verifies a wisp on a dead session is
// not reaped if updated_at is younger than the timeout. The grace period
// absorbs witnesses that briefly die during a tmux server restart but come
// back fast.
func TestReapDeadAgentWisps_SkipsFreshBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	binDir := t.TempDir()
	writeFakeTmuxDeadSession(t, binDir)
	bdPath := writeFakeBDListWithUpdates(t, binDir, map[string]string{
		"hooked":      "[" + agentBeadJSON("gu-fresh", "myrig/witness", "hooked", 10*time.Minute) + "]",
		"in_progress": `[]`,
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d, _ := newAgentReapDaemon(t, &logBuf, bdPath)

	d.reapRigDeadAgentWisps("myrig", agentReapConfig{fallbackTimeout: time.Hour})

	if _, err := os.Stat(filepath.Join(binDir, "bd-update.log")); err == nil {
		data, _ := os.ReadFile(filepath.Join(binDir, "bd-update.log"))
		t.Errorf("bd update was called despite fresh updated_at: %s", data)
	}
}

// TestReapDeadAgentWisps_SkipsZeroUpdatedAt verifies we don't reap when bd
// returns a missing/zero updated_at. Without a known timestamp we can't prove
// the wisp is stale, so we defer to manual cleanup or the next list cycle.
func TestReapDeadAgentWisps_SkipsZeroUpdatedAt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	binDir := t.TempDir()
	writeFakeTmuxDeadSession(t, binDir)
	// Hand-craft JSON without updated_at so json.Unmarshal leaves it zero.
	bdPath := writeFakeBDListWithUpdates(t, binDir, map[string]string{
		"hooked":      `[{"id":"gu-noupdate","assignee":"myrig/witness","status":"hooked"}]`,
		"in_progress": `[]`,
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d, _ := newAgentReapDaemon(t, &logBuf, bdPath)

	d.reapRigDeadAgentWisps("myrig", agentReapConfig{fallbackTimeout: time.Hour})

	if _, err := os.Stat(filepath.Join(binDir, "bd-update.log")); err == nil {
		data, _ := os.ReadFile(filepath.Join(binDir, "bd-update.log"))
		t.Errorf("bd update was called despite missing updated_at: %s", data)
	}
}

// TestReapDeadAgentWisps_SkipsNonAgentAssignees verifies the agent reaper does
// NOT touch wisps assigned to polecats, crew, mayor, or other rigs. Polecat
// reaping has its own dedicated reaper with stricter requirements (heartbeat
// file present, polecats/<name>/ dir present), and double-reaping would be
// confusing in operator logs.
func TestReapDeadAgentWisps_SkipsNonAgentAssignees(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	binDir := t.TempDir()
	writeFakeTmuxDeadSession(t, binDir)
	bdPath := writeFakeBDListWithUpdates(t, binDir, map[string]string{
		"hooked": "[" + strings.Join([]string{
			agentBeadJSON("gu-pc", "myrig/polecats/shiny", "hooked", 3*time.Hour),
			agentBeadJSON("gu-cr", "myrig/crew/max", "hooked", 3*time.Hour),
			agentBeadJSON("gu-mayor", "mayor", "hooked", 3*time.Hour),
			agentBeadJSON("gu-other", "otherrig/witness", "hooked", 3*time.Hour),
			agentBeadJSON("gu-ok-w", "myrig/witness", "hooked", 3*time.Hour),
		}, ",") + "]",
		"in_progress": `[]`,
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d, _ := newAgentReapDaemon(t, &logBuf, bdPath)

	d.reapRigDeadAgentWisps("myrig", agentReapConfig{fallbackTimeout: time.Hour})

	data, err := os.ReadFile(filepath.Join(binDir, "bd-update.log"))
	if err != nil {
		t.Fatalf("expected gu-ok-w reset, got no update log: %v", err)
	}
	updates := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(updates) != 1 {
		t.Fatalf("expected exactly 1 update (gu-ok-w), got %d: %v", len(updates), updates)
	}
	if !strings.Contains(updates[0], "gu-ok-w") {
		t.Errorf("expected gu-ok-w to be updated, got: %s", updates[0])
	}
	for _, blocked := range []string{"gu-pc", "gu-cr", "gu-mayor", "gu-other"} {
		if strings.Contains(updates[0], blocked) {
			t.Errorf("unexpected update for non-agent assignee %s: %s", blocked, updates[0])
		}
	}
}

// --- Heartbeat-first reaper tests (cv-p3fem Phase 1) ---------------------
//
// These tests pin the new behaviour gating gu-rh0g closure:
//   - When a heartbeat file exists, its timestamp drives the reap, not
//     bead.UpdatedAt. The per-role threshold (witness=15m, refinery=30m)
//     applies. A fresh heartbeat protects against false reaps even if
//     bead.UpdatedAt is hours old.
//   - When no heartbeat file exists, the reaper falls back to
//     bead.UpdatedAt + DeadAgentReapTimeout (the legacy gu-s009 path).
//
// The fixture writes a real heartbeat file under <townRoot>/.runtime/
// heartbeats/<sessionName>.json so we exercise the production read path
// (polecat.ReadSessionHeartbeat), not a mock.

// writeHeartbeatFile installs a heartbeat at the canonical path with the
// timestamp `time.Now().Add(-age)`. Used by the heartbeat-first tests.
func writeHeartbeatFile(t *testing.T, townRoot, sessionName string, age time.Duration) {
	t.Helper()
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir heartbeats: %v", err)
	}
	hb := polecat.SessionHeartbeat{
		Timestamp: time.Now().Add(-age).UTC(),
		State:     polecat.HeartbeatWorking,
	}
	data, err := json.Marshal(hb)
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionName+".json"), data, 0644); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}
}

// TestReapDeadAgentWisps_Heartbeat_StaleWitnessReaped pins the happy path of
// the new heartbeat-first contract: a heartbeat older than WitnessReapTimeout
// drives the reap, even though bead.UpdatedAt is a fresher 5m (which would
// have prevented reap under the legacy 2h fallback).
func TestReapDeadAgentWisps_Heartbeat_StaleWitnessReaped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	binDir := t.TempDir()
	writeFakeTmuxDeadSession(t, binDir)
	bdPath := writeFakeBDListWithUpdates(t, binDir, map[string]string{
		// updated_at is only 5m old — far younger than the legacy 2h fallback,
		// so a reap here proves we used the heartbeat path.
		"hooked":      "[" + agentBeadJSON("gu-stale-hb-w", "myrig/witness", "hooked", 5*time.Minute) + "]",
		"in_progress": `[]`,
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d, townRoot := newAgentReapDaemon(t, &logBuf, bdPath)
	// Heartbeat is 20m old → past witness threshold of 15m.
	writeHeartbeatFile(t, townRoot, session.WitnessSessionName(session.PrefixFor("myrig")), 20*time.Minute)

	d.reapRigDeadAgentWisps("myrig", agentReapConfig{
		fallbackTimeout: 2 * time.Hour,    // would NOT match updated_at=5m
		witnessTimeout:  15 * time.Minute, // would match heartbeat=20m
		refineryTimeout: 30 * time.Minute,
	})

	data, err := os.ReadFile(filepath.Join(binDir, "bd-update.log"))
	if err != nil {
		t.Fatalf("expected reap of gu-stale-hb-w via heartbeat path; no update log: %v", err)
	}
	if !strings.Contains(string(data), "gu-stale-hb-w") {
		t.Errorf("expected gu-stale-hb-w reset, got: %s", data)
	}
	got := logBuf.String()
	if !strings.Contains(got, "source=heartbeat") {
		t.Errorf("log line should record source=heartbeat; got: %q", got)
	}
}

// TestReapDeadAgentWisps_Heartbeat_FreshHeartbeatProtectsAgainstStaleUpdatedAt
// is the symmetric guard: a fresh heartbeat must SHIELD a wisp from reap even
// when bead.UpdatedAt is hours old. Otherwise a long-running cycle that
// hasn't touched bd recently (but is still alive and bumping its heartbeat
// per gt command) would be mistakenly reaped under the old updated_at proxy.
func TestReapDeadAgentWisps_Heartbeat_FreshHeartbeatProtectsAgainstStaleUpdatedAt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	binDir := t.TempDir()
	writeFakeTmuxDeadSession(t, binDir)
	bdPath := writeFakeBDListWithUpdates(t, binDir, map[string]string{
		// updated_at is 3h old — would trigger the legacy 2h fallback.
		"hooked":      "[" + agentBeadJSON("gu-fresh-hb-w", "myrig/witness", "hooked", 3*time.Hour) + "]",
		"in_progress": `[]`,
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d, townRoot := newAgentReapDaemon(t, &logBuf, bdPath)
	// Heartbeat is fresh (1m old) → well within witness threshold.
	writeHeartbeatFile(t, townRoot, session.WitnessSessionName(session.PrefixFor("myrig")), 1*time.Minute)

	d.reapRigDeadAgentWisps("myrig", agentReapConfig{
		fallbackTimeout: 2 * time.Hour,
		witnessTimeout:  15 * time.Minute,
		refineryTimeout: 30 * time.Minute,
	})

	if data, err := os.ReadFile(filepath.Join(binDir, "bd-update.log")); err == nil {
		t.Errorf("fresh heartbeat should have prevented reap; got update: %s", data)
	}
}

// TestReapDeadAgentWisps_Heartbeat_RefineryUsesPerRoleThreshold confirms
// refinery uses RefineryReapTimeout (30m), not WitnessReapTimeout. A 20m-old
// heartbeat is past witness's 15m bar but UNDER refinery's 30m bar, so the
// refinery wisp must NOT be reaped while a witness wisp WOULD be.
func TestReapDeadAgentWisps_Heartbeat_RefineryUsesPerRoleThreshold(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	binDir := t.TempDir()
	writeFakeTmuxDeadSession(t, binDir)
	bdPath := writeFakeBDListWithUpdates(t, binDir, map[string]string{
		"hooked":      "[" + agentBeadJSON("gu-r-20m", "myrig/refinery", "hooked", 5*time.Minute) + "]",
		"in_progress": `[]`,
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d, townRoot := newAgentReapDaemon(t, &logBuf, bdPath)
	// Refinery heartbeat 20m old: past witness (15m) but NOT past refinery (30m).
	writeHeartbeatFile(t, townRoot, session.RefinerySessionName(session.PrefixFor("myrig")), 20*time.Minute)

	d.reapRigDeadAgentWisps("myrig", agentReapConfig{
		fallbackTimeout: 2 * time.Hour,
		witnessTimeout:  15 * time.Minute,
		refineryTimeout: 30 * time.Minute,
	})

	if data, err := os.ReadFile(filepath.Join(binDir, "bd-update.log")); err == nil {
		t.Errorf("20m-old refinery heartbeat is fresh under refinery's 30m threshold; should not reap. Got: %s", data)
	}
}

// TestReapDeadAgentWisps_Heartbeat_MissingHeartbeatFallsBackToUpdatedAt is
// the rollout-window guarantee: a session that has never touched a heartbeat
// file (because cv-p3fem hasn't been deployed there yet) is still reaped
// against the legacy fallback timeout. Without this we'd lose coverage of
// pre-rollout sessions and gu-s009 would regress.
func TestReapDeadAgentWisps_Heartbeat_MissingHeartbeatFallsBackToUpdatedAt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	binDir := t.TempDir()
	writeFakeTmuxDeadSession(t, binDir)
	bdPath := writeFakeBDListWithUpdates(t, binDir, map[string]string{
		"hooked":      "[" + agentBeadJSON("gu-old-w", "myrig/witness", "hooked", 3*time.Hour) + "]",
		"in_progress": `[]`,
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d, _ := newAgentReapDaemon(t, &logBuf, bdPath)
	// NOTE: deliberately do NOT write a heartbeat file.

	d.reapRigDeadAgentWisps("myrig", agentReapConfig{
		fallbackTimeout: 1 * time.Hour, // 3h updated_at > 1h fallback → reap
		witnessTimeout:  15 * time.Minute,
		refineryTimeout: 30 * time.Minute,
	})

	data, err := os.ReadFile(filepath.Join(binDir, "bd-update.log"))
	if err != nil {
		t.Fatalf("missing heartbeat should fall back to updated_at and reap; no update log: %v", err)
	}
	if !strings.Contains(string(data), "gu-old-w") {
		t.Errorf("expected gu-old-w reset via fallback, got: %s", data)
	}
	got := logBuf.String()
	if !strings.Contains(got, "source=updated_at") {
		t.Errorf("log line should record source=updated_at for fallback path; got: %q", got)
	}
}

// TestReapDeadAgentWisps_Heartbeat_FreshHeartbeatNoReapEvenWithMissingUpdatedAt
// guards a subtle case: heartbeat present, but bd returned a zero updated_at.
// The legacy code used updated_at as the only signal and would refuse to reap
// (which is fine). The new code must use the heartbeat instead — and if the
// heartbeat is fresh, must not reap. This pins that we do NOT regress to a
// "no updated_at + heartbeat present → never reap" hole.
func TestReapDeadAgentWisps_Heartbeat_FreshHeartbeatNoReapEvenWithMissingUpdatedAt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	binDir := t.TempDir()
	writeFakeTmuxDeadSession(t, binDir)
	bdPath := writeFakeBDListWithUpdates(t, binDir, map[string]string{
		"hooked":      `[{"id":"gu-noup-w","assignee":"myrig/witness","status":"hooked"}]`,
		"in_progress": `[]`,
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d, townRoot := newAgentReapDaemon(t, &logBuf, bdPath)
	writeHeartbeatFile(t, townRoot, session.WitnessSessionName(session.PrefixFor("myrig")), 30*time.Second)

	d.reapRigDeadAgentWisps("myrig", agentReapConfig{
		fallbackTimeout: 1 * time.Hour,
		witnessTimeout:  15 * time.Minute,
		refineryTimeout: 30 * time.Minute,
	})

	if data, err := os.ReadFile(filepath.Join(binDir, "bd-update.log")); err == nil {
		t.Errorf("fresh heartbeat should override missing updated_at and prevent reap; got: %s", data)
	}
}
