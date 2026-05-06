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

// writeFakeTestTmux creates a shell script in dir named "tmux" that simulates
// "session not found" for has-session calls and fails on anything else.
func writeFakeTestTmux(t *testing.T, dir string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *has-session*) echo \"can't find session\" >&2; exit 1;;\n" +
		"  *) echo 'unexpected tmux command' >&2; exit 1;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0755); err != nil {
		t.Fatalf("writing fake tmux: %v", err)
	}
}

// writeFakeTestBD creates a shell script in dir named "bd" that outputs a
// polecat agent bead JSON. The descState parameter controls what appears in
// the description text (parsed by ParseAgentFields), while
// dbState controls the agent_state database column. updatedAt controls the
// bead's updated_at timestamp for time-bound testing.
func writeFakeTestBD(t *testing.T, dir, descState, dbState, hookBead, updatedAt string) string {
	t.Helper()
	desc := "agent_state: " + descState
	// JSON matches the structure that getAgentBeadInfo expects from bd show --json
	bdJSON := fmt.Sprintf(`[{"id":"gt-myr-polecat-mycat","issue_type":"agent","labels":["gt:agent"],"description":"%s","hook_bead":"%s","agent_state":"%s","updated_at":"%s"}]`,
		desc, hookBead, dbState, updatedAt)
	// Return agent bead JSON for "show", empty array for "list" (so
	// hasAssignedOpenWork doesn't false-positive on the agent bead).
	script := "#!/bin/sh\nif [ \"$1\" = \"list\" ]; then echo '[]'; exit 0; fi\necho '" + bdJSON + "'\n"
	path := filepath.Join(dir, "bd")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("writing fake bd: %v", err)
	}
	return path
}

// writeFakeBDWithHookBead creates a shell script in dir named "bd" that returns
// different JSON based on the bead ID: the agent bead in one state, and the hook
// bead (work bead) in a separate state. Used to test cases where the agent and hook
// beads have independent lifecycles (e.g., agent done/nuked while hook_bead open).
func writeFakeBDWithHookBead(t *testing.T, dir, agentState, hookBeadID, hookBeadStatus, updatedAt string) string {
	t.Helper()
	agentJSON := fmt.Sprintf(`[{"id":"gt-myr-polecat-mycat","issue_type":"agent","labels":["gt:agent"],"description":"agent_state: %s","hook_bead":"%s","agent_state":"%s","updated_at":"%s"}]`,
		agentState, hookBeadID, agentState, updatedAt)
	hookJSON := fmt.Sprintf(`[{"id":"%s","status":"%s"}]`, hookBeadID, hookBeadStatus)
	script := fmt.Sprintf("#!/bin/sh\n"+
		"if [ \"$1\" = \"list\" ]; then echo '[]'; exit 0; fi\n"+
		"case \"$2\" in\n"+
		"  gt-myr-polecat-mycat) echo '%s';;\n"+
		"  %s) echo '%s';;\n"+
		"  *) echo '[]'; exit 1;;\n"+
		"esac\n", agentJSON, hookBeadID, hookJSON)
	bdPath := filepath.Join(dir, "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("writing fake bd: %v", err)
	}
	return bdPath
}

// TestCheckPolecatHealth_SkipsSpawning verifies that checkPolecatHealth does NOT
// attempt to restart a polecat in agent_state=spawning when recently updated.
// This is the regression test for the double-spawn bug (issue #1752): the daemon
// heartbeat fires during the window between bead creation (hook_bead set atomically
// by gt sling) and the actual tmux session launch, causing a second Claude process.
func TestCheckPolecatHealth_SkipsSpawning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux and bd")
	}
	binDir := t.TempDir()
	writeFakeTestTmux(t, binDir)
	// Use a recent timestamp so the spawning guard's time-bound is satisfied
	recentTime := time.Now().UTC().Format(time.RFC3339)
	bdPath := writeFakeTestBD(t, binDir, "spawning", "spawning", "gt-xyz", recentTime)

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(&logBuf, "", 0),
		tmux:   tmux.NewTmux(),
		bdPath: bdPath,
	}

	d.checkPolecatHealth("myr", "mycat")

	got := logBuf.String()
	if !strings.Contains(got, "spawning") {
		t.Errorf("expected log to mention 'spawning', got: %q", got)
	}
	if strings.Contains(got, "CRASH DETECTED") {
		t.Errorf("spawning polecat must not trigger CRASH DETECTED, got: %q", got)
	}
}

// TestCheckPolecatHealth_DetectsCrashedPolecat verifies that checkPolecatHealth
// does detect a crash for a polecat in agent_state=working with a dead session.
// This ensures the spawning guard in issue #1752 does not accidentally suppress
// legitimate crash detection for polecats that were running normally.
func TestCheckPolecatHealth_DetectsCrashedPolecat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux and bd")
	}
	binDir := t.TempDir()
	writeFakeTestTmux(t, binDir)
	recentTime := time.Now().UTC().Format(time.RFC3339)
	bdPath := writeFakeTestBD(t, binDir, "working", "working", "gt-xyz", recentTime)

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(&logBuf, "", 0),
		tmux:   tmux.NewTmux(),
		bdPath: bdPath,
	}

	d.checkPolecatHealth("myr", "mycat")

	got := logBuf.String()
	if !strings.Contains(got, "CRASH DETECTED") {
		t.Errorf("expected CRASH DETECTED for working polecat with dead session, got: %q", got)
	}
}

// TestCheckPolecatHealth_SpawningGuardExpires verifies that the spawning guard
// has a time-bound: polecats stuck in agent_state=spawning for more than 5 minutes
// are treated as crashed (gt sling may have failed during spawn).
func TestCheckPolecatHealth_SpawningGuardExpires(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux and bd")
	}
	binDir := t.TempDir()
	writeFakeTestTmux(t, binDir)
	// Use a timestamp >5 minutes ago to expire the spawning guard
	oldTime := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	bdPath := writeFakeTestBD(t, binDir, "spawning", "spawning", "gt-xyz", oldTime)

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(&logBuf, "", 0),
		tmux:   tmux.NewTmux(),
		bdPath: bdPath,
	}

	d.checkPolecatHealth("myr", "mycat")

	got := logBuf.String()
	if !strings.Contains(got, "Spawning guard expired") {
		t.Errorf("expected spawning guard to expire for old timestamp, got: %q", got)
	}
	if !strings.Contains(got, "CRASH DETECTED") {
		t.Errorf("expected CRASH DETECTED after spawning guard expires, got: %q", got)
	}
}

// TestCheckPolecatHealth_DescriptionStateOverridesLegacyDBColumn verifies that
// daemon lifecycle reads the description's agent_state first. bd >= 0.62.0 no
// longer has a supported structured agent_state writer, so the description is
// Gastown's active contract and the DB column is legacy fallback only.
func TestCheckPolecatHealth_DescriptionStateOverridesLegacyDBColumn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux and bd")
	}
	binDir := t.TempDir()
	writeFakeTestTmux(t, binDir)
	recentTime := time.Now().UTC().Format(time.RFC3339)
	// Description says "spawning" (current Gastown contract) while the legacy
	// structured column still says "working".
	bdPath := writeFakeTestBD(t, binDir, "spawning", "working", "gt-xyz", recentTime)

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(&logBuf, "", 0),
		tmux:   tmux.NewTmux(),
		bdPath: bdPath,
	}

	d.checkPolecatHealth("myr", "mycat")

	got := logBuf.String()
	if !strings.Contains(got, "spawning") {
		t.Errorf("expected log to mention description-backed spawning state, got: %q", got)
	}
	if strings.Contains(got, "CRASH DETECTED") {
		t.Errorf("daemon should honor description state 'spawning' and skip crash detection, got: %q", got)
	}
}

// TestCheckPolecatHealth_SkipsClosedHookBead verifies that checkPolecatHealth
// does NOT fire CRASHED_POLECAT when the hook_bead is already closed.
// This is the regression test for the false-positive spam bug (issue hq-1o7):
// when a polecat completes work normally, the hook_bead gets closed but the
// stale reference remains on the agent bead, causing repeated false alerts.
func TestCheckPolecatHealth_SkipsClosedHookBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux and bd")
	}
	binDir := t.TempDir()
	writeFakeTestTmux(t, binDir)
	recentTime := time.Now().UTC().Format(time.RFC3339)
	bdPath := writeFakeBDWithHookBead(t, binDir, "working", "fe-xyz", "closed", recentTime)

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(&logBuf, "", 0),
		tmux:   tmux.NewTmux(),
		bdPath: bdPath,
	}

	d.checkPolecatHealth("myr", "mycat")

	got := logBuf.String()
	if !strings.Contains(got, "hook_bead fe-xyz is already closed") {
		t.Errorf("expected log about closed hook_bead, got: %q", got)
	}
	if strings.Contains(got, "CRASH DETECTED") {
		t.Errorf("closed hook_bead must not trigger CRASH DETECTED, got: %q", got)
	}
}

// TestCheckPolecatHealth_NotifiesWitnessOnCrash verifies that when a polecat
// crash is detected, the daemon sends a notification to the witness via
// `gt mail send` with a CRASHED_POLECAT subject. Restart is deferred to the
// stuck-agent-dog plugin for context-aware recovery.
func TestCheckPolecatHealth_NotifiesWitnessOnCrash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux and bd")
	}
	binDir := t.TempDir()
	writeFakeTestTmux(t, binDir)
	recentTime := time.Now().UTC().Format(time.RFC3339)
	bdPath := writeFakeTestBD(t, binDir, "working", "working", "gt-xyz", recentTime)

	// Create a fake gt script that logs invocations to a file
	gtLog := filepath.Join(t.TempDir(), "gt-invocations.log")
	fakeGt := filepath.Join(binDir, "gt")
	gtScript := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\n", gtLog)
	if err := os.WriteFile(fakeGt, []byte(gtScript), 0755); err != nil {
		t.Fatalf("writing fake gt: %v", err)
	}

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	townRoot := t.TempDir()
	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(&logBuf, "", 0),
		tmux:   tmux.NewTmux(),
		bdPath: bdPath,
		gtPath: fakeGt,
	}

	d.checkPolecatHealth("myr", "mycat")

	got := logBuf.String()
	if !strings.Contains(got, "CRASH DETECTED") {
		t.Fatalf("expected CRASH DETECTED, got: %q", got)
	}

	// Verify gt mail send was called with CRASHED_POLECAT subject
	logData, err := os.ReadFile(gtLog)
	if err != nil {
		t.Fatalf("reading gt invocation log: %v", err)
	}
	invocations := string(logData)
	if !strings.Contains(invocations, "mail send") {
		t.Errorf("expected gt mail send invocation, got: %q", invocations)
	}
	if !strings.Contains(invocations, "CRASHED_POLECAT") {
		t.Errorf("expected CRASHED_POLECAT in mail subject, got: %q", invocations)
	}
	if !strings.Contains(invocations, "myr/witness") {
		t.Errorf("expected witness address myr/witness, got: %q", invocations)
	}
}

// TestCheckPolecatHealth_SkipsDonePolecat verifies that checkPolecatHealth does
// NOT fire CRASH DETECTED when a polecat has agent_state=done (completed normally)
// even if its hook_bead is still open. This is the race-window regression test for
// bug #2795 part 2: between gt done setting agent_state=done and the hook_bead
// being closed, the daemon heartbeat fires on the dead session + open hook_bead
// combination, causing repeated false CRASHED_POLECAT alerts to the witness.
func TestCheckPolecatHealth_SkipsDonePolecat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux and bd")
	}
	binDir := t.TempDir()
	writeFakeTestTmux(t, binDir)
	recentTime := time.Now().UTC().Format(time.RFC3339)
	bdPath := writeFakeBDWithHookBead(t, binDir, "done", "gt-xyz", "open", recentTime)

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(&logBuf, "", 0),
		tmux:   tmux.NewTmux(),
		bdPath: bdPath,
	}

	d.checkPolecatHealth("myr", "mycat")

	got := logBuf.String()
	if !strings.Contains(got, "Skipping crash detection") {
		t.Errorf("expected skip log message, got: %q", got)
	}
	if !strings.Contains(got, "agent_state=done") {
		t.Errorf("expected agent_state=done in skip log, got: %q", got)
	}
	if strings.Contains(got, "CRASH DETECTED") {
		t.Errorf("done polecat with open hook_bead must not trigger CRASH DETECTED, got: %q", got)
	}
}

// TestCheckPolecatHealth_SkipsNukedPolecat verifies that checkPolecatHealth does
// NOT fire CRASH DETECTED when a polecat has been nuked (agent_state=nuked) even
// if its hook_bead (work bead) is still open. This is the regression test for
// bug #2795: `gt polecat nuke --force` sets agent_state=nuked on the agent bead
// but leaves the work bead open, causing repeated false RECOVERY_NEEDED alerts
// on every heartbeat cycle.
func TestCheckPolecatHealth_SkipsNukedPolecat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux and bd")
	}
	binDir := t.TempDir()
	writeFakeTestTmux(t, binDir)
	recentTime := time.Now().UTC().Format(time.RFC3339)
	bdPath := writeFakeBDWithHookBead(t, binDir, "nuked", "gt-xyz", "open", recentTime)

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(&logBuf, "", 0),
		tmux:   tmux.NewTmux(),
		bdPath: bdPath,
	}

	d.checkPolecatHealth("myr", "mycat")

	got := logBuf.String()
	if !strings.Contains(got, "Skipping crash detection") {
		t.Errorf("expected skip log message, got: %q", got)
	}
	if !strings.Contains(got, "agent_state=nuked") {
		t.Errorf("expected agent_state=nuked in skip log, got: %q", got)
	}
	if strings.Contains(got, "CRASH DETECTED") {
		t.Errorf("nuked polecat must not trigger CRASH DETECTED, got: %q", got)
	}
}

// writeFakeTmuxWithAgent creates a shell script that simulates a live tmux session
// with an agent process running. has-session succeeds, display-message returns the
// given paneCommand (e.g., "claude" or "codex") so IsAgentRunning returns true.
func writeFakeTmuxWithAgent(t *testing.T, dir, paneCommand string) {
	t.Helper()
	// Use $* glob matching (not $1) because tmux.run() prepends -u (and
	// optionally -L <socket>) before the subcommand.
	script := fmt.Sprintf("#!/bin/sh\n"+
		"case \"$*\" in\n"+
		"  *has-session*) exit 0;;\n"+
		"  *display-message*) echo '%s';;\n"+
		"  *kill-session*) exit 0;;\n"+
		"  *) exit 1;;\n"+
		"esac\n", paneCommand)
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0755); err != nil {
		t.Fatalf("writing fake tmux: %v", err)
	}
}

// writeFakeTmuxIdleSession creates a shell script that simulates a live tmux session
// with NO agent process running (idle shell). has-session succeeds, display-message
// returns "bash" so IsAgentRunning returns false.
func writeFakeTmuxIdleSession(t *testing.T, dir string) {
	t.Helper()
	writeFakeTmuxWithAgent(t, dir, "bash")
}

// writeFakeBDLookupFail creates a "bd" script that fails on "show" (simulating a
// bead infrastructure error) but returns configurable output for "list" queries.
// When hasWork is true, "list" returns an open work bead assigned to "myr/polecats/mycat".
func writeFakeBDLookupFail(t *testing.T, dir string, hasWork bool) string {
	t.Helper()
	listOut := `[]`
	if hasWork {
		listOut = `[{"id":"wh-test-1","status":"open","assignee":"myr/polecats/mycat"}]`
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"list\" ]; then echo '" + listOut + "'; exit 0; fi\n" +
		"# show fails — simulate bead infrastructure degradation\n" +
		"exit 1\n"
	path := filepath.Join(dir, "bd")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("writing fake bd: %v", err)
	}
	return path
}

// TestReapIdlePolecat_SkipsWhenBeadLookupFailsButHasWork verifies that reapIdlePolecat
// does NOT kill a polecat when the agent bead lookup fails but hasAssignedOpenWork
// confirms the polecat has an open work bead assigned. This is the regression test
// for the working-bead-lookup-failed kill bug (GH#3342 followup).
func TestReapIdlePolecat_SkipsWhenBeadLookupFailsButHasWork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux and bd")
	}
	old := session.DefaultRegistry()
	reg := session.NewPrefixRegistry()
	reg.Register("myr", "myr")
	session.SetDefaultRegistry(reg)
	defer session.SetDefaultRegistry(old)

	binDir := t.TempDir()
	writeFakeTmuxIdleSession(t, binDir)
	bdPath := writeFakeBDLookupFail(t, binDir, true /* hasWork */)

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	townRoot := t.TempDir()
	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(&logBuf, "", 0),
		tmux:   tmux.NewTmuxWithSocket(""),
		bdPath: bdPath,
	}

	hbPath := filepath.Join(townRoot, ".runtime", "heartbeats", "myr-mycat.json")
	_ = os.MkdirAll(filepath.Dir(hbPath), 0755)
	staleHB := polecat.SessionHeartbeat{
		Timestamp: time.Now().UTC().Add(-60 * time.Minute),
		State:     polecat.HeartbeatWorking,
	}
	data, _ := json.Marshal(staleHB)
	_ = os.WriteFile(hbPath, data, 0644)

	d.reapIdlePolecat("myr", "mycat", 15*time.Minute)

	if strings.Contains(logBuf.String(), "Reaping idle polecat") {
		t.Errorf("must NOT reap polecat with open assigned work when agent bead lookup fails, got: %q", logBuf.String())
	}
}

// TestReapIdlePolecat_ReapsWhenBeadLookupFailsAndNoWork verifies that reapIdlePolecat
// DOES kill a polecat when the agent bead lookup fails, no work is assigned, and the
// agent process is not running. Ensures the hasAssignedOpenWork guard doesn't over-protect.
func TestReapIdlePolecat_ReapsWhenBeadLookupFailsAndNoWork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux and bd")
	}
	old := session.DefaultRegistry()
	reg := session.NewPrefixRegistry()
	reg.Register("myr", "myr")
	session.SetDefaultRegistry(reg)
	defer session.SetDefaultRegistry(old)

	binDir := t.TempDir()
	writeFakeTmuxIdleSession(t, binDir)
	bdPath := writeFakeBDLookupFail(t, binDir, false /* no work */)

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	townRoot := t.TempDir()
	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(&logBuf, "", 0),
		tmux:   tmux.NewTmuxWithSocket(""),
		bdPath: bdPath,
	}

	hbPath := filepath.Join(townRoot, ".runtime", "heartbeats", "myr-mycat.json")
	_ = os.MkdirAll(filepath.Dir(hbPath), 0755)
	staleHB := polecat.SessionHeartbeat{
		Timestamp: time.Now().UTC().Add(-45 * time.Minute), // 3x the 15m timeout
		State:     polecat.HeartbeatWorking,
	}
	data, _ := json.Marshal(staleHB)
	_ = os.WriteFile(hbPath, data, 0644)

	d.reapIdlePolecat("myr", "mycat", 15*time.Minute)

	if !strings.Contains(logBuf.String(), "Reaping idle polecat") {
		t.Errorf("expected idle polecat with no work and failed bead lookup to be reaped, got: %q", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "working-bead-lookup-failed") {
		t.Errorf("expected working-bead-lookup-failed reason, got: %q", logBuf.String())
	}
}

// TestReapIdlePolecat_SkipsActiveAgent verifies that reapIdlePolecat does NOT kill
// a polecat whose hook_bead is missing but whose agent process is still running.
// This is the regression test for GH#3342: a failed gt sling rollback can clear
// the hook while the agent is actively working, causing the daemon to incorrectly
// reap the session.
func TestReapIdlePolecat_SkipsActiveAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux and bd")
	}
	// Register "myr" prefix so session name resolves to "myr-mycat"
	old := session.DefaultRegistry()
	reg := session.NewPrefixRegistry()
	reg.Register("myr", "myr")
	session.SetDefaultRegistry(reg)
	defer session.SetDefaultRegistry(old)

	binDir := t.TempDir()
	// Fake tmux: session alive, agent (codex) running in pane
	writeFakeTmuxWithAgent(t, binDir, "codex")
	// Fake bd: agent bead exists but hook_bead is empty (cleared by failed sling)
	recentTime := time.Now().UTC().Format(time.RFC3339)
	bdPath := writeFakeTestBD(t, binDir, "working", "working", "", recentTime)

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	townRoot := t.TempDir()
	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(&logBuf, "", 0),
		tmux:   tmux.NewTmux(),
		bdPath: bdPath,
	}

	// Write a stale heartbeat (working state, 20 minutes old) so the reaper considers it
	polecat.TouchSessionHeartbeatWithState(townRoot, "myr-mycat", polecat.HeartbeatWorking, "", "")
	// Backdate the heartbeat to make it stale
	hbPath := filepath.Join(townRoot, "heartbeats", "myr-mycat.json")
	staleHB := polecat.SessionHeartbeat{
		Timestamp: time.Now().UTC().Add(-20 * time.Minute),
		State:     polecat.HeartbeatWorking,
	}
	data, _ := json.Marshal(staleHB)
	_ = os.WriteFile(hbPath, data, 0644)

	d.reapIdlePolecat("myr", "mycat", 15*time.Minute)

	got := logBuf.String()
	if strings.Contains(got, "Reaping idle polecat") {
		t.Errorf("must NOT reap polecat with active agent process (GH#3342), got: %q", got)
	}
}

// TestReapIdlePolecat_ReapsIdleNoHook verifies that reapIdlePolecat DOES kill
// a polecat whose hook_bead is missing AND whose agent process is NOT running
// (idle shell). This ensures the GH#3342 fix doesn't prevent legitimate reaping.
func TestReapIdlePolecat_ReapsIdleNoHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux and bd")
	}
	// Register "myr" prefix so session name resolves to "myr-mycat"
	old := session.DefaultRegistry()
	reg := session.NewPrefixRegistry()
	reg.Register("myr", "myr")
	session.SetDefaultRegistry(reg)
	defer session.SetDefaultRegistry(old)

	binDir := t.TempDir()
	// Fake tmux: session alive, but only a shell running (no agent)
	writeFakeTmuxIdleSession(t, binDir)
	// Fake bd: agent bead exists but hook_bead is empty
	recentTime := time.Now().UTC().Format(time.RFC3339)
	bdPath := writeFakeTestBD(t, binDir, "working", "working", "", recentTime)

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	townRoot := t.TempDir()
	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(&logBuf, "", 0),
		tmux:   tmux.NewTmuxWithSocket(""),
		bdPath: bdPath,
	}

	// Write a stale heartbeat (working state, 20 minutes old) so the reaper considers it
	polecat.TouchSessionHeartbeatWithState(townRoot, "myr-mycat", polecat.HeartbeatWorking, "", "")
	// Backdate the heartbeat to make it stale
	hbPath := filepath.Join(townRoot, ".runtime", "heartbeats", "myr-mycat.json")
	staleHB := polecat.SessionHeartbeat{
		Timestamp: time.Now().UTC().Add(-20 * time.Minute),
		State:     polecat.HeartbeatWorking,
	}
	data, _ := json.Marshal(staleHB)
	_ = os.WriteFile(hbPath, data, 0644)

	d.reapIdlePolecat("myr", "mycat", 15*time.Minute)

	got := logBuf.String()
	if !strings.Contains(got, "Reaping idle polecat") {
		t.Errorf("expected idle polecat with no agent to be reaped, got: %q", got)
	}
	if !strings.Contains(got, "working-no-hook") {
		t.Errorf("expected working-no-hook reason, got: %q", got)
	}
}

// TestCheckPolecatHealth_RecordsDeathForIdlePolecat verifies that
// checkPolecatHealth records session death and emits a session_death event for
// an idle polecat (no hook_bead) with a dead tmux session. This is the
// regression test for gu-but4: previously, the function returned early for
// idle polecats, so mass-death detection (kernel OOM, tmux server restart)
// had blind spots for exactly the failure mode operators most need to detect.
// The witness notification path is skipped because there's no work to recover —
// the Witness's orphan patrol handles cleanup.
func TestCheckPolecatHealth_RecordsDeathForIdlePolecat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux and bd")
	}
	binDir := t.TempDir()
	writeFakeTestTmux(t, binDir)
	recentTime := time.Now().UTC().Format(time.RFC3339)
	// Idle polecat: agent_state=working but hook_bead is empty.
	bdPath := writeFakeTestBD(t, binDir, "working", "working", "", recentTime)

	// Fake gt script that logs invocations — we'll use it to verify that the
	// witness is NOT notified for idle polecat deaths (no work to recover).
	gtLog := filepath.Join(t.TempDir(), "gt-invocations.log")
	fakeGt := filepath.Join(binDir, "gt")
	gtScript := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\n", gtLog)
	if err := os.WriteFile(fakeGt, []byte(gtScript), 0755); err != nil {
		t.Fatalf("writing fake gt: %v", err)
	}

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(&logBuf, "", 0),
		tmux:   tmux.NewTmux(),
		bdPath: bdPath,
		gtPath: fakeGt,
	}

	d.checkPolecatHealth("myr", "mycat")

	got := logBuf.String()
	if !strings.Contains(got, "CRASH DETECTED (idle)") {
		t.Errorf("expected idle-crash log for idle polecat with dead session, got: %q", got)
	}

	// Verify the death was recorded for mass-death aggregation.
	d.deathsMu.Lock()
	numDeaths := len(d.recentDeaths)
	d.deathsMu.Unlock()
	if numDeaths != 1 {
		t.Errorf("expected 1 recorded session death, got %d", numDeaths)
	}

	// Verify witness was NOT notified — idle polecats don't need restart.
	if data, err := os.ReadFile(gtLog); err == nil {
		if strings.Contains(string(data), "CRASHED_POLECAT") {
			t.Errorf("idle polecat death must not send CRASHED_POLECAT alert, got: %q", string(data))
		}
	}
}

// TestCheckPolecatHealth_MassDeathFiresForIdlePolecats verifies that
// simultaneous deaths of many idle polecats trigger the mass-death aggregator.
// This is the scenario from gu-but4 evidence: 11 idle polecat sessions died
// simultaneously at 2026-04-30 21:43 UTC (daemon OOM kill), but because they
// were all idle (empty hook_bead), zero deaths were recorded and the operator
// had to manually discover the mass death. After this fix, the mass-death
// event MUST fire.
func TestCheckPolecatHealth_MassDeathFiresForIdlePolecats(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux and bd")
	}

	// Seed the default prefix registry so each rig in this test produces a
	// distinct tmux session name, matching production (where mayor/rigs.json
	// assigns every rig a unique beads prefix). Without this, the registry is
	// empty and PolecatSessionName falls back to DefaultPrefix="gt" for every
	// rig — which collapses the three "obsidian" polecats below into a single
	// session name (gt-obsidian). The alarm dedup added in a965e84f (gu-50qv)
	// then correctly treats re-detections of that one session as a single
	// zombie, producing only 9 CRASH DETECTED lines instead of 11 and failing
	// the test. See gu-w3ye for the regression analysis.
	old := session.DefaultRegistry()
	reg := session.NewPrefixRegistry()
	reg.Register("cc", "casc_crud")
	reg.Register("ce", "casc_e2e")
	reg.Register("cws", "codegen_ws")
	reg.Register("gt", "gastown_upstream")
	reg.Register("ralph", "ralph")
	session.SetDefaultRegistry(reg)
	defer session.SetDefaultRegistry(old)

	binDir := t.TempDir()
	writeFakeTestTmux(t, binDir)
	recentTime := time.Now().UTC().Format(time.RFC3339)
	bdPath := writeFakeTestBD(t, binDir, "working", "working", "", recentTime)

	// Fake gt script (required by notifyWitnessOfCrashedPolecat path, though
	// we do not expect it to be called for idle polecats).
	fakeGt := filepath.Join(binDir, "gt")
	if err := os.WriteFile(fakeGt, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("writing fake gt: %v", err)
	}

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(&logBuf, "", 0),
		tmux:   tmux.NewTmux(),
		bdPath: bdPath,
		gtPath: fakeGt,
	}

	// Simulate 11 simultaneous idle polecat deaths (the gu-but4 evidence count).
	// Each (rig, name) pair above maps to a unique session name because the
	// registry assigns each rig a distinct prefix. Three rigs contain a polecat
	// named "obsidian"; they become cc-obsidian, cws-obsidian, ralph-obsidian —
	// the real production session names we'd see during a simultaneous death.
	polecats := []struct{ rig, name string }{
		{"casc_crud", "obsidian"},
		{"casc_e2e", "furiosa"},
		{"codegen_ws", "obsidian"},
		{"gastown_upstream", "chrome"},
		{"gastown_upstream", "dust"},
		{"gastown_upstream", "fury"},
		{"gastown_upstream", "guzzle"},
		{"gastown_upstream", "rust"},
		{"gastown_upstream", "shiny"},
		{"gastown_upstream", "thunder"},
		{"ralph", "obsidian"},
	}
	for _, p := range polecats {
		d.checkPolecatHealth(p.rig, p.name)
	}

	got := logBuf.String()
	// Each death must be logged
	idleCrashes := strings.Count(got, "CRASH DETECTED (idle)")
	if idleCrashes != len(polecats) {
		t.Errorf("expected %d idle-crash logs, got %d. Log: %q",
			len(polecats), idleCrashes, got)
	}
	// Mass-death aggregator must fire (threshold=3, window=30s — well within bounds)
	if !strings.Contains(got, "MASS DEATH DETECTED") {
		t.Errorf("expected MASS DEATH DETECTED log after %d idle polecat deaths, got: %q",
			len(polecats), got)
	}
}

// TestCheckPolecatHealth_PoolReadyDoesNotFireMassDeath verifies the gu-vy4l
// regression: pool-init creates N polecats with agent_state=idle and dead
// sessions (intentional — the session spawns only when work is dispatched).
// Previously, every heartbeat cycle classified all pool-ready polecats as
// crashed, and once the pool reached massDeathThreshold (3), a false
// MASS DEATH DETECTED event fired every cycle. After the fix, a dead session
// for an agent_state=idle polecat with no hook_bead is a normal resting state:
// no crash log, no recorded death, no mass-death event.
func TestCheckPolecatHealth_PoolReadyDoesNotFireMassDeath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux and bd")
	}
	binDir := t.TempDir()
	writeFakeTestTmux(t, binDir)
	recentTime := time.Now().UTC().Format(time.RFC3339)
	// Pool-ready polecat: agent_state=idle, hook_bead empty, session dead.
	bdPath := writeFakeTestBD(t, binDir, "idle", "idle", "", recentTime)

	fakeGt := filepath.Join(binDir, "gt")
	if err := os.WriteFile(fakeGt, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("writing fake gt: %v", err)
	}

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(&logBuf, "", 0),
		tmux:   tmux.NewTmux(),
		bdPath: bdPath,
		gtPath: fakeGt,
	}

	// Simulate a 4-polecat pool (default size from pool-init).
	polecats := []struct{ rig, name string }{
		{"gastown_upstream", "chrome"},
		{"gastown_upstream", "dust"},
		{"gastown_upstream", "rust"},
		{"gastown_upstream", "shiny"},
	}
	// Exercise multiple heartbeat cycles to show the aggregator stays quiet.
	for cycle := 0; cycle < 3; cycle++ {
		for _, p := range polecats {
			d.checkPolecatHealth(p.rig, p.name)
		}
	}

	got := logBuf.String()
	if strings.Contains(got, "CRASH DETECTED") {
		t.Errorf("pool-ready polecats must not log CRASH DETECTED, got: %q", got)
	}
	if strings.Contains(got, "MASS DEATH DETECTED") {
		t.Errorf("pool-ready polecats must not trigger MASS DEATH DETECTED, got: %q", got)
	}

	d.deathsMu.Lock()
	numDeaths := len(d.recentDeaths)
	d.deathsMu.Unlock()
	if numDeaths != 0 {
		t.Errorf("pool-ready polecats must not record session deaths, got %d", numDeaths)
	}
}

// TestCheckPolecatHealth_SkipsExpectedRestingIdlePolecat verifies that
// checkPolecatHealth does NOT record a death or emit a session_death event
// for an idle polecat in an expected resting agent_state (done/nuked/idle)
// with a dead session. These are intentional shutdowns or pool-ready rest,
// not crashes, and must not pollute the mass-death aggregator.
//
// This guards gu-but4's fix against over-firing, and also covers gu-vy4l:
// pool-ready polecats (agent_state=idle, session intentionally dead after
// pool-init or reapIdlePolecats) were previously misclassified as crashes,
// causing a false MASS DEATH DETECTED event on every heartbeat cycle once
// pool size reached massDeathThreshold.
func TestCheckPolecatHealth_SkipsExpectedRestingIdlePolecat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux and bd")
	}
	for _, state := range []string{"done", "nuked", "idle"} {
		state := state
		t.Run("state="+state, func(t *testing.T) {
			binDir := t.TempDir()
			writeFakeTestTmux(t, binDir)
			recentTime := time.Now().UTC().Format(time.RFC3339)
			bdPath := writeFakeTestBD(t, binDir, state, state, "", recentTime)

			t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

			var logBuf strings.Builder
			d := &Daemon{
				config: &Config{TownRoot: t.TempDir()},
				logger: log.New(&logBuf, "", 0),
				tmux:   tmux.NewTmux(),
				bdPath: bdPath,
			}

			d.checkPolecatHealth("myr", "mycat")

			got := logBuf.String()
			if strings.Contains(got, "CRASH DETECTED") {
				t.Errorf("expected-resting (%s) idle polecat must not log CRASH DETECTED, got: %q",
					state, got)
			}

			d.deathsMu.Lock()
			numDeaths := len(d.recentDeaths)
			d.deathsMu.Unlock()
			if numDeaths != 0 {
				t.Errorf("expected-resting (%s) idle polecat must not record death, got %d deaths",
					state, numDeaths)
			}
		})
	}
}
