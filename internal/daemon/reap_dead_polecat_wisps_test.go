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
// These helpers mirror the patterns in polecat_health_test.go, adapted for
// the rig-scoped list + update flow that reapRigDeadPolecatWisps drives.

// writeFakeBDListOnly creates a "bd" script that responds to
// `bd list --rig=<rig> --status=<status> --json --limit=0` with the JSON
// payload supplied via statusJSON (keyed by status), and records every
// invocation (args on separate lines) to <dir>/bd.log. Any bd update call
// writes its args to <dir>/bd-update.log as a single line.
//
// This lets the test assert (a) that in_progress and hooked are both queried
// and (b) exactly which beads the reaper tried to reset.
func writeFakeBDListOnly(t *testing.T, dir string, statusJSON map[string]string) string {
	t.Helper()
	var cases strings.Builder
	for status, payload := range statusJSON {
		// Escape single quotes inside the JSON payload so the shell case arm
		// stays syntactically valid. No payload in these tests includes a
		// single quote, but be safe.
		escaped := strings.ReplaceAll(payload, "'", "'\\''")
		fmt.Fprintf(&cases, "    *--status=%s*) echo '%s';;\n", status, escaped)
	}
	// Default: empty JSON array so an unexpected status string doesn't crash
	// json.Unmarshal but is still visible in the log.
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

// writeFakeTmuxDeadSession creates a "tmux" script where has-session always
// fails (session not found). This simulates a crashed polecat.
func writeFakeTmuxDeadSession(t *testing.T, dir string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *has-session*) echo \"can't find session\" >&2; exit 1;;\n" +
		"  *) echo 'unexpected tmux' >&2; exit 1;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0755); err != nil {
		t.Fatalf("writing fake tmux: %v", err)
	}
}

// writeFakeTmuxLiveSession creates a "tmux" script where has-session succeeds.
// Simulates a still-running polecat so the reaper should leave its beads alone.
func writeFakeTmuxLiveSession(t *testing.T, dir string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *has-session*) exit 0;;\n" +
		"  *) echo 'unexpected tmux' >&2; exit 1;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0755); err != nil {
		t.Fatalf("writing fake tmux: %v", err)
	}
}

// writeHeartbeatAge writes a heartbeat file for sessionName with a timestamp
// `age` in the past. age > 0 means the heartbeat is that old.
func writeHeartbeatAge(t *testing.T, townRoot, sessionName string, age time.Duration) {
	t.Helper()
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir heartbeats: %v", err)
	}
	hb := polecat.SessionHeartbeat{
		Timestamp: time.Now().UTC().Add(-age),
		State:     polecat.HeartbeatWorking,
	}
	data, _ := json.Marshal(hb)
	if err := os.WriteFile(filepath.Join(dir, sessionName+".json"), data, 0644); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}
}

// newReapTestDaemon constructs a Daemon wired up for the reaper tests:
// a tempdir TownRoot with a rig directory, a fake bd/tmux on PATH, and a
// logger that writes to logBuf.
func newReapTestDaemon(t *testing.T, logBuf *strings.Builder, bdPath string) (*Daemon, string) {
	t.Helper()
	townRoot := t.TempDir()

	// Register "myr" prefix for rig "myrig" so PolecatSessionName resolves
	// to "myr-<name>". The test fixtures all use rig name "myrig" and write
	// heartbeats under the "myr-" prefix.
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
	return d, townRoot
}

// makePolecatDir creates <townRoot>/<rig>/polecats/<name>/ so the reaper can
// distinguish "crashed session" (dir present) from "orphaned" (dir missing).
func makePolecatDir(t *testing.T, townRoot, rig, name string) {
	t.Helper()
	dir := filepath.Join(townRoot, rig, "polecats", name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir polecat: %v", err)
	}
}

// --- Tests ----------------------------------------------------------------

// TestReapDeadPolecatWisps_SkipsMissingRig verifies the reaper is a no-op
// for a rig that has no polecats/ directory at all (e.g., witness-only rig).
func TestReapDeadPolecatWisps_SkipsMissingRig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	binDir := t.TempDir()
	writeFakeTmuxDeadSession(t, binDir)
	// bd list response should never be consulted; return an error-causing payload
	// so we catch a regression if the rig-skip is removed.
	bdPath := writeFakeBDListOnly(t, binDir, map[string]string{
		"hooked":      `[{"id":"gu-bad","assignee":"myrig/polecats/shiny","status":"hooked"}]`,
		"in_progress": `[]`,
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d, _ := newReapTestDaemon(t, &logBuf, bdPath)

	// Do NOT create <townRoot>/myrig/polecats/ — rig is missing.
	d.reapRigDeadPolecatWisps("myrig", time.Hour)

	// bd list must NOT have been called.
	if _, err := os.Stat(filepath.Join(binDir, "bd.log")); err == nil {
		data, _ := os.ReadFile(filepath.Join(binDir, "bd.log"))
		t.Errorf("bd list was invoked despite missing polecats dir: %s", data)
	}
	if strings.Contains(logBuf.String(), "reap-dead-polecat-wisps") {
		t.Errorf("no reap log expected for missing rig, got: %q", logBuf.String())
	}
}

// TestReapDeadPolecatWisps_ReapsDeadPolecatBead verifies the core happy path:
// bead assigned to a polecat whose session is dead, whose directory still
// exists, and whose heartbeat is older than the timeout → reset to open.
func TestReapDeadPolecatWisps_ReapsDeadPolecatBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	binDir := t.TempDir()
	writeFakeTmuxDeadSession(t, binDir)
	bdPath := writeFakeBDListOnly(t, binDir, map[string]string{
		"hooked":      `[{"id":"gu-stuck","assignee":"myrig/polecats/shiny","status":"hooked"}]`,
		"in_progress": `[]`,
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d, townRoot := newReapTestDaemon(t, &logBuf, bdPath)
	makePolecatDir(t, townRoot, "myrig", "shiny")

	// Heartbeat is 90 minutes old, well past the 1h threshold.
	writeHeartbeatAge(t, townRoot, "myr-shiny", 90*time.Minute)

	d.reapRigDeadPolecatWisps("myrig", time.Hour)

	// Verify bd update was called exactly once for gu-stuck with status=open
	// and cleared assignee.
	data, err := os.ReadFile(filepath.Join(binDir, "bd-update.log"))
	if err != nil {
		bdLog, _ := os.ReadFile(filepath.Join(binDir, "bd.log"))
		t.Fatalf("no bd update recorded: %v\nbd.log:\n%s\nlogger:\n%s", err, bdLog, logBuf.String())
	}
	updates := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(updates) != 1 {
		t.Fatalf("expected 1 bd update, got %d: %v", len(updates), updates)
	}
	// bd has no --rig flag — the daemon must run bd from the rig directory
	// instead. We assert on the args (no --rig=) and trust that the daemon
	// sets cmd.Dir correctly (the build of the candidate list above also
	// depends on it).
	want := "update gu-stuck --status=open --assignee="
	if !strings.Contains(updates[0], want) {
		t.Errorf("bd update missing expected args\n want contains: %s\n got: %s", want, updates[0])
	}
	if strings.Contains(updates[0], "--rig=") {
		t.Errorf("bd update should not pass --rig= (bd has no such flag); got: %s", updates[0])
	}

	// Verify INFO log line carries the context needed for operators.
	got := logBuf.String()
	if !strings.Contains(got, "reap-dead-polecat-wisps: reset gu-stuck") {
		t.Errorf("missing reap log line, got: %q", got)
	}
	if !strings.Contains(got, "polecat=shiny") {
		t.Errorf("log should name the polecat, got: %q", got)
	}
	if !strings.Contains(got, "prev_status=hooked") {
		t.Errorf("log should record previous status, got: %q", got)
	}
}

// TestReapDeadPolecatWisps_SkipsAlivePolecat verifies a bead assigned to a
// polecat with a live tmux session is left alone, even if the heartbeat is
// stale. A stale heartbeat alone is not proof of death.
func TestReapDeadPolecatWisps_SkipsAlivePolecat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	binDir := t.TempDir()
	writeFakeTmuxLiveSession(t, binDir)
	bdPath := writeFakeBDListOnly(t, binDir, map[string]string{
		"hooked":      `[]`,
		"in_progress": `[{"id":"gu-alive","assignee":"myrig/polecats/shiny","status":"in_progress"}]`,
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d, townRoot := newReapTestDaemon(t, &logBuf, bdPath)
	makePolecatDir(t, townRoot, "myrig", "shiny")
	writeHeartbeatAge(t, townRoot, "myr-shiny", 90*time.Minute) // stale

	d.reapRigDeadPolecatWisps("myrig", time.Hour)

	if _, err := os.Stat(filepath.Join(binDir, "bd-update.log")); err == nil {
		data, _ := os.ReadFile(filepath.Join(binDir, "bd-update.log"))
		t.Errorf("bd update was called despite live session: %s", data)
	}
}

// TestReapDeadPolecatWisps_SkipsFreshHeartbeat verifies a dead polecat whose
// heartbeat is *younger* than the timeout is not reaped yet. This is the
// grace period that absorbs transient outages.
func TestReapDeadPolecatWisps_SkipsFreshHeartbeat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	binDir := t.TempDir()
	writeFakeTmuxDeadSession(t, binDir)
	bdPath := writeFakeBDListOnly(t, binDir, map[string]string{
		"hooked":      `[{"id":"gu-fresh","assignee":"myrig/polecats/shiny","status":"hooked"}]`,
		"in_progress": `[]`,
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d, townRoot := newReapTestDaemon(t, &logBuf, bdPath)
	makePolecatDir(t, townRoot, "myrig", "shiny")

	// Heartbeat 10 minutes old, well inside the 1h timeout.
	writeHeartbeatAge(t, townRoot, "myr-shiny", 10*time.Minute)

	d.reapRigDeadPolecatWisps("myrig", time.Hour)

	if _, err := os.Stat(filepath.Join(binDir, "bd-update.log")); err == nil {
		data, _ := os.ReadFile(filepath.Join(binDir, "bd-update.log"))
		t.Errorf("bd update was called despite fresh heartbeat: %s", data)
	}
}

// TestReapDeadPolecatWisps_SkipsMissingHeartbeat verifies a dead polecat with
// no heartbeat file is NOT reaped. Missing heartbeat is ambiguous (could be a
// fresh install, could be a rename race) — we defer to the witness orphan scan.
func TestReapDeadPolecatWisps_SkipsMissingHeartbeat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	binDir := t.TempDir()
	writeFakeTmuxDeadSession(t, binDir)
	bdPath := writeFakeBDListOnly(t, binDir, map[string]string{
		"hooked":      `[{"id":"gu-noheartbeat","assignee":"myrig/polecats/shiny","status":"hooked"}]`,
		"in_progress": `[]`,
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d, townRoot := newReapTestDaemon(t, &logBuf, bdPath)
	makePolecatDir(t, townRoot, "myrig", "shiny")
	// Deliberately no heartbeat file.

	d.reapRigDeadPolecatWisps("myrig", time.Hour)

	if _, err := os.Stat(filepath.Join(binDir, "bd-update.log")); err == nil {
		data, _ := os.ReadFile(filepath.Join(binDir, "bd-update.log"))
		t.Errorf("bd update was called despite missing heartbeat: %s", data)
	}
}

// TestReapDeadPolecatWisps_SkipsDeletedPolecatDir verifies that when a
// polecat's directory is gone we do NOT reap — the witness DetectOrphanedBeads
// owns that case. This keeps the two code paths from double-resetting beads.
func TestReapDeadPolecatWisps_SkipsDeletedPolecatDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	binDir := t.TempDir()
	writeFakeTmuxDeadSession(t, binDir)
	bdPath := writeFakeBDListOnly(t, binDir, map[string]string{
		"hooked":      `[{"id":"gu-orphan","assignee":"myrig/polecats/ghost","status":"hooked"}]`,
		"in_progress": `[]`,
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d, townRoot := newReapTestDaemon(t, &logBuf, bdPath)
	// Create the polecats/ dir (so the rig passes the first gate) but NOT
	// the "ghost" polecat's subdir.
	if err := os.MkdirAll(filepath.Join(townRoot, "myrig", "polecats"), 0755); err != nil {
		t.Fatalf("mkdir polecats: %v", err)
	}
	writeHeartbeatAge(t, townRoot, "myr-ghost", 90*time.Minute)

	d.reapRigDeadPolecatWisps("myrig", time.Hour)

	if _, err := os.Stat(filepath.Join(binDir, "bd-update.log")); err == nil {
		data, _ := os.ReadFile(filepath.Join(binDir, "bd-update.log"))
		t.Errorf("bd update was called despite missing polecat dir: %s", data)
	}
}

// TestReapDeadPolecatWisps_SkipsMalformedAssignee verifies we do NOT crash or
// mis-reap on assignees that don't fit the rig/polecats/<name> shape.
func TestReapDeadPolecatWisps_SkipsMalformedAssignee(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	binDir := t.TempDir()
	writeFakeTmuxDeadSession(t, binDir)
	// Mixed: one bead with a nested-path assignee (malformed), one with the
	// wrong rig prefix, and one properly-shaped bead that should be reaped.
	bdPath := writeFakeBDListOnly(t, binDir, map[string]string{
		"hooked": `[
			{"id":"gu-nested","assignee":"myrig/polecats/nested/path","status":"hooked"},
			{"id":"gu-wrongrig","assignee":"otherrig/polecats/shiny","status":"hooked"},
			{"id":"gu-ok","assignee":"myrig/polecats/shiny","status":"hooked"}
		]`,
		"in_progress": `[]`,
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	var logBuf strings.Builder
	d, townRoot := newReapTestDaemon(t, &logBuf, bdPath)
	makePolecatDir(t, townRoot, "myrig", "shiny")
	writeHeartbeatAge(t, townRoot, "myr-shiny", 90*time.Minute)

	d.reapRigDeadPolecatWisps("myrig", time.Hour)

	data, err := os.ReadFile(filepath.Join(binDir, "bd-update.log"))
	if err != nil {
		t.Fatalf("expected gu-ok to be reset, got no update log: %v", err)
	}
	updates := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(updates) != 1 {
		t.Fatalf("expected exactly 1 update (gu-ok), got %d: %v", len(updates), updates)
	}
	if !strings.Contains(updates[0], "gu-ok") {
		t.Errorf("expected gu-ok to be updated, got: %s", updates[0])
	}
	if strings.Contains(updates[0], "gu-nested") || strings.Contains(updates[0], "gu-wrongrig") {
		t.Errorf("unexpected update for malformed/wrong-rig bead: %s", updates[0])
	}
}
