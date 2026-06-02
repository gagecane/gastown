package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/session"
)

func setupSlingTestRegistry(t *testing.T) {
	t.Helper()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("bd", "beads")
	reg.Register("mp", "my-project")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

// TestNudgeRefinerySessionName verifies that nudgeRefinery constructs the
// correct tmux session name ({prefix}-refinery) and passes the message.
func TestNudgeRefinerySessionName(t *testing.T) {
	setupSlingTestRegistry(t)
	logPath := filepath.Join(t.TempDir(), "nudge.log")
	t.Setenv("GT_TEST_NUDGE_LOG", logPath)

	tests := []struct {
		name        string
		rigName     string
		message     string
		wantSession string
	}{
		{
			name:        "simple rig name",
			rigName:     "gastown",
			message:     "MERGE_READY received - check inbox for pending work",
			wantSession: "gt-refinery",
		},
		{
			name:        "hyphenated rig name",
			rigName:     "my-project",
			message:     "MERGE_READY received - check inbox for pending work",
			wantSession: "mp-refinery",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Truncate log for each subtest
			if err := os.WriteFile(logPath, nil, 0644); err != nil {
				t.Fatalf("truncate log: %v", err)
			}

			nudgeRefinery(tt.rigName, tt.message)

			logBytes, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read log: %v", err)
			}
			logContent := string(logBytes)

			// Verify session name
			wantPrefix := "nudge:" + tt.wantSession + ":"
			if !strings.Contains(logContent, wantPrefix) {
				t.Errorf("nudgeRefinery(%q) session = got log %q, want prefix %q",
					tt.rigName, logContent, wantPrefix)
			}

			// Verify message is passed through
			if !strings.Contains(logContent, tt.message) {
				t.Errorf("nudgeRefinery() message not found in log: got %q, want %q",
					logContent, tt.message)
			}
		})
	}
}

// TestWakeRigAgentsDoesNotNudgeRefinery verifies that wakeRigAgents only
// nudges the witness, not the refinery. The refinery should only be nudged
// when an MR is actually created (via nudgeRefinery), not at polecat dispatch time.
func TestWakeRigAgentsDoesNotNudgeRefinery(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "nudge.log")
	t.Setenv("GT_TEST_NUDGE_LOG", logPath)

	// wakeRigAgents calls exec.Command("gt", "rig", "boot", ...) and tmux.NudgeSession.
	// The boot command and witness nudge will fail silently (no real rig/tmux).
	// We only care that nudgeRefinery is NOT called (no log entries).
	wakeRigAgents("testrig")

	// Check that no refinery nudge was logged
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		// File doesn't exist = no nudges logged = correct
		return
	}
	if strings.Contains(string(logBytes), "refinery") {
		t.Errorf("wakeRigAgents() should not nudge refinery, but log contains: %s", string(logBytes))
	}
}

// TestNudgeRefineryNoOpWithoutLog verifies that nudgeRefinery doesn't panic
// or error when called without the test log env var and without a real tmux session.
// The tmux NudgeSession call should fail silently.
func TestNudgeRefineryNoOpWithoutLog(t *testing.T) {
	// Ensure test log is NOT set so we exercise the real tmux path
	t.Setenv("GT_TEST_NUDGE_LOG", "")

	// Should not panic even though no tmux session exists
	nudgeRefinery("nonexistent-rig", "test message")
}

func TestIsSlingConfigError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"not initialized", fmt.Errorf("database not initialized"), true},
		{"no such table", fmt.Errorf("no such table: issues"), true},
		{"table not found", fmt.Errorf("table not found: issues"), true},
		{"issue_prefix missing", fmt.Errorf("issue_prefix not configured"), true},
		{"no database", fmt.Errorf("no database found"), true},
		{"database not found", fmt.Errorf("database not found"), true},
		{"connection refused", fmt.Errorf("connection refused"), true},
		{"circuit breaker", fmt.Errorf("Dolt circuit breaker is open: server appears down"), true},
		{"server appears down", fmt.Errorf("server appears down"), true},
		{"server down", fmt.Errorf("server down"), true},
		{"server not running", fmt.Errorf("Dolt server is not running"), true},
		{"server may not be running", fmt.Errorf("Dolt server may not be running"), true},
		{"transient error", fmt.Errorf("optimistic lock failed"), false},
		{"generic error", fmt.Errorf("something else"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSlingConfigError(tt.err); got != tt.want {
				t.Errorf("isSlingConfigError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestIsAgentBead verifies that agent state beads (polecat/witness/refinery/
// mayor/dog) are correctly identified by the gt:agent label or the legacy
// issue_type == "agent" field, so that scheduleBead and getReadySlingContexts
// refuse to dispatch them as work (gu-7gm).
// TestIsParentOfOpenChildren verifies the gu-fs88 dispatch gate for beads
// that parent unclosed children. The helper is the dispatch-time mirror of
// patrol_helpers.checkHasOpenChildren but routes through an injectable
// variable so unit tests do not need a real bd subprocess.
//
// Key contract: errors from hasOpenChildrenFn must NOT block dispatch —
// a transient bd failure permanently stalling the fleet is worse than
// occasionally letting an epic through. The error is logged and the helper
// returns false.
func TestIsParentOfOpenChildren(t *testing.T) {
	tests := []struct {
		name    string
		stubOut bool
		stubErr error
		want    bool
	}{
		{"no children -> not a parent", false, nil, false},
		{"has open children -> parent container", true, nil, true},
		{"bd error -> treat as no open children (don't stall dispatch)", false, fmt.Errorf("bd boom"), false},
		{"bd error with true flag still returns false (error trumps)", true, fmt.Errorf("bd boom"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev := hasOpenChildrenFn
			hasOpenChildrenFn = func(id string) (bool, error) {
				if id != "gt-test" {
					t.Fatalf("unexpected bead id: %q", id)
				}
				return tt.stubOut, tt.stubErr
			}
			t.Cleanup(func() { hasOpenChildrenFn = prev })

			if got := isParentOfOpenChildren("gt-test"); got != tt.want {
				t.Errorf("isParentOfOpenChildren() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestVerifyBeadIDMatch verifies the exact-ID guard (gu-yphj) that prevents
// bd's partial-ID resolver from silently routing dispatch to the wrong bead
// when a requested full ID is a strict prefix of another bead's ID.
//
// Scenario: "gt-74f" exists (OPEN) and "gt-74fjf" exists (CLOSED). bd show
// "gt-74f" can resolve to "gt-74fjf" via substring matching. Without this
// guard, sling would silently reject with "work already completed" referring
// to the wrong bead.
func TestVerifyBeadIDMatch(t *testing.T) {
	tests := []struct {
		name        string
		requested   string
		resolved    string
		wantErr     bool
		wantErrSubs []string // substrings expected in the error
	}{
		{
			name:      "exact match passes",
			requested: "gt-74f",
			resolved:  "gt-74f",
			wantErr:   false,
		},
		{
			name:      "different prefixed ID fails (prefix collision)",
			requested: "gt-74f",
			resolved:  "gt-74fjf",
			wantErr:   true,
			wantErrSubs: []string{
				"gt-74f",
				"gt-74fjf",
				"prefix collision",
			},
		},
		{
			name:      "different-prefix mismatch also fails",
			requested: "bd-abc",
			resolved:  "gt-abc",
			wantErr:   true,
		},
		{
			name:      "bare hash without prefix is permitted to resolve loosely",
			requested: "74f",
			resolved:  "gt-74fjf",
			wantErr:   false, // Partial IDs have no prefix -> loose resolution allowed
		},
		{
			name:      "empty resolved ID (older bd or partial JSON) is permissive",
			requested: "gt-74f",
			resolved:  "",
			wantErr:   false,
		},
		{
			name:      "empty requested ID (no prefix) skips check",
			requested: "",
			resolved:  "gt-anything",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyBeadIDMatch(tt.requested, tt.resolved)
			if (err != nil) != tt.wantErr {
				t.Fatalf("verifyBeadIDMatch(%q, %q) err = %v, wantErr %v",
					tt.requested, tt.resolved, err, tt.wantErr)
			}
			if err == nil {
				return
			}
			msg := err.Error()
			for _, sub := range tt.wantErrSubs {
				if !strings.Contains(msg, sub) {
					t.Errorf("verifyBeadIDMatch(%q, %q) err %q missing substring %q",
						tt.requested, tt.resolved, msg, sub)
				}
			}
		})
	}
}

// TestVerifyBeadExistsPrefixCollision reproduces gu-yphj: when bd's resolver
// returns a different bead than requested (due to prefix collision on partial
// ID fallback), verifyBeadExists must reject the result rather than silently
// accepting the misrouted bead. This closes the gt-side gap that let sling
// dispatch to the wrong (closed) bead.
func TestVerifyBeadExistsPrefixCollision(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bd stub uses POSIX shell; skipping on Windows")
	}
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Stub bd that returns a bead with a DIFFERENT id than requested,
	// simulating the prefix-collision substring match.
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
set -e
cmd="$1"
shift || true
if [ "$cmd" = "--allow-stale" ]; then
  cmd="$1"
  shift || true
fi
case "$cmd" in
  show)
    # Always return gt-74fjf regardless of requested id (mimics bd's
    # partial-resolver falling through to the longer substring match).
    echo '[{"id":"gt-74fjf","title":"Old closed bead","status":"closed","assignee":""}]'
    ;;
  version)
    echo "bd 0.1.0"
    ;;
esac
exit 0
`
	_ = writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Caller asks for gt-74f, bd resolves to gt-74fjf — must be rejected.
	err = verifyBeadExists("gt-74f")
	if err == nil {
		t.Fatal("verifyBeadExists(\"gt-74f\") = nil, want error (bd returned different id)")
	}
	msg := err.Error()
	for _, sub := range []string{"gt-74f", "gt-74fjf"} {
		if !strings.Contains(msg, sub) {
			t.Errorf("verifyBeadExists error %q missing substring %q", msg, sub)
		}
	}
}

// TestGetBeadInfoPrefixCollision mirrors the verifyBeadExists test for
// getBeadInfo, which is the path sling uses before checking status. Without
// this guard a prefix collision would surface as "bead X is closed (work
// already completed)" even though X is actually open.
func TestGetBeadInfoPrefixCollision(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bd stub uses POSIX shell; skipping on Windows")
	}
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
set -e
cmd="$1"
shift || true
if [ "$cmd" = "--allow-stale" ]; then
  cmd="$1"
  shift || true
fi
case "$cmd" in
  show)
    echo '[{"id":"gt-74fjf","title":"Old closed bead","status":"closed","assignee":""}]'
    ;;
  version)
    echo "bd 0.1.0"
    ;;
esac
exit 0
`
	_ = writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	info, err := getBeadInfo("gt-74f")
	if err == nil {
		t.Fatalf("getBeadInfo(\"gt-74f\") = %+v, want error", info)
	}
	if !strings.Contains(err.Error(), "gt-74f") || !strings.Contains(err.Error(), "gt-74fjf") {
		t.Errorf("getBeadInfo error %q does not mention both requested and resolved ids", err.Error())
	}
}

// TestGetBeadInfoExactMatchSucceeds verifies the guard does not break the
// happy path: when bd returns the exact id we asked for, getBeadInfo passes
// through unchanged.
func TestGetBeadInfoExactMatchSucceeds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bd stub uses POSIX shell; skipping on Windows")
	}
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
set -e
cmd="$1"
shift || true
if [ "$cmd" = "--allow-stale" ]; then
  cmd="$1"
  shift || true
fi
case "$cmd" in
  show)
    echo '[{"id":"gt-74f","title":"Live bead","status":"open","assignee":""}]'
    ;;
  version)
    echo "bd 0.1.0"
    ;;
esac
exit 0
`
	_ = writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	info, err := getBeadInfo("gt-74f")
	if err != nil {
		t.Fatalf("getBeadInfo(\"gt-74f\") unexpected err: %v", err)
	}
	if info.ID != "gt-74f" {
		t.Errorf("getBeadInfo returned id = %q, want %q", info.ID, "gt-74f")
	}
	if info.Status != "open" {
		t.Errorf("getBeadInfo returned status = %q, want %q", info.Status, "open")
	}
}

// TestEnsureFormulaRequiredVars_GatesCommands verifies that gates_commands is
// included in the required defaults for mol-polecat-work so that rigs using
// modern gate config always have the placeholder defined.
func TestEnsureFormulaRequiredVars_GatesCommands(t *testing.T) {
	vars := ensureFormulaRequiredVars("mol-polecat-work", []string{"issue=gt-abc"})
	found := false
	for _, v := range vars {
		if v == "gates_commands=" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ensureFormulaRequiredVars(mol-polecat-work) missing gates_commands= default; got %v", vars)
	}
}

// TestLoadRigCommandVars_GatesCommands verifies that loadRigCommandVars generates
// gates_commands from merge_queue.gates, filtering to pre-merge phase only and
// sorting by gate name.
func TestLoadRigCommandVars_GatesCommands(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrip"
	rigDir := filepath.Join(townRoot, rigName)

	// Create settings/config.json with modern gates config (no legacy *_command fields).
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}
	settings := &config.RigSettings{
		MergeQueue: &config.MergeQueueConfig{
			Gates: map[string]*config.GateConfig{
				"lint":      {Cmd: "golangci-lint run", Phase: "pre-merge"},
				"vet":       {Cmd: "go vet ./..."},                         // empty phase = pre-merge
				"build":     {Cmd: "go build ./...", Phase: "post-squash"}, // must be excluded
				"unit-test": {Cmd: "go test ./...", Phase: "pre-merge"},
			},
		},
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	vars := loadRigCommandVars(townRoot, rigName)

	// Find gates_commands var
	var gatesCommands string
	for _, v := range vars {
		if k, val, ok := strings.Cut(v, "="); ok && k == "gates_commands" {
			gatesCommands = val
			break
		}
	}

	if gatesCommands == "" {
		t.Fatalf("loadRigCommandVars did not produce gates_commands; got vars: %v", vars)
	}

	// Verify post-squash gate is excluded
	if strings.Contains(gatesCommands, "go build ./...") {
		t.Errorf("gates_commands should not include post-squash gate 'build', got:\n%s", gatesCommands)
	}

	// Verify pre-merge gates are included
	for _, cmd := range []string{"golangci-lint run", "go vet ./...", "go test ./..."} {
		if !strings.Contains(gatesCommands, cmd) {
			t.Errorf("gates_commands missing command %q, got:\n%s", cmd, gatesCommands)
		}
	}

	// Verify sorted order: lint < unit-test < vet alphabetically
	lintIdx := strings.Index(gatesCommands, "# Gate: lint")
	unitTestIdx := strings.Index(gatesCommands, "# Gate: unit-test")
	vetIdx := strings.Index(gatesCommands, "# Gate: vet")
	if lintIdx < 0 || unitTestIdx < 0 || vetIdx < 0 {
		t.Fatalf("missing gate headers in gates_commands:\n%s", gatesCommands)
	}
	if !(lintIdx < unitTestIdx && unitTestIdx < vetIdx) {
		t.Errorf("gates_commands not sorted: lint=%d unit-test=%d vet=%d in:\n%s",
			lintIdx, unitTestIdx, vetIdx, gatesCommands)
	}
}

// TestLoadRigCommandVars_NoGates verifies that gates_commands is absent when no
// gates are configured, so the formula falls through to legacy *_command fields.
func TestLoadRigCommandVars_NoGates(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrip"
	rigDir := filepath.Join(townRoot, rigName)

	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}
	settings := &config.RigSettings{
		MergeQueue: &config.MergeQueueConfig{
			TestCommand:  "go test ./...",
			BuildCommand: "go build ./...",
		},
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	vars := loadRigCommandVars(townRoot, rigName)

	for _, v := range vars {
		if k, _, ok := strings.Cut(v, "="); ok && k == "gates_commands" {
			t.Errorf("gates_commands should be absent when no gates configured; got %q", v)
		}
	}

	// Verify legacy commands are present
	hasTest := false
	for _, v := range vars {
		if v == "test_command=go test ./..." {
			hasTest = true
		}
	}
	if !hasTest {
		t.Errorf("expected test_command in vars; got %v", vars)
	}
}

// TestLoadRigCommandVars_AllPostSquash verifies that gates_commands is absent
// when all configured gates are post-squash (none run during pre-verify).
func TestLoadRigCommandVars_AllPostSquash(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrip"
	rigDir := filepath.Join(townRoot, rigName)

	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}
	settings := &config.RigSettings{
		MergeQueue: &config.MergeQueueConfig{
			Gates: map[string]*config.GateConfig{
				"build": {Cmd: "go build ./...", Phase: "post-squash"},
			},
		},
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	vars := loadRigCommandVars(townRoot, rigName)

	for _, v := range vars {
		if k, _, ok := strings.Cut(v, "="); ok && k == "gates_commands" {
			t.Errorf("gates_commands should be absent when all gates are post-squash; got %q", v)
		}
	}
}

// TestIsPolecatOwnedBeadInfo verifies the gu-gal8 dispatch gate: beads whose
// owner address matches "<rig>/polecats/<name>" must be rejected from polecat
// dispatch. The owner field is parsed exactly — no substring or prefix match
// — so adjacent shapes (witness/refinery sublevels, deeper paths, plain user
// emails) must not trigger the filter.
func TestHookBeadWithRetryFailsFastOnBdStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix shell script bd stub")
	}
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)

	binDir := t.TempDir()
	countPath := filepath.Join(binDir, "count")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--allow-stale" ]; then
  echo "Error: unknown flag: --allow-stale" >&2
  exit 0
fi
count=0
if [ -f %[1]q ]; then count=$(cat %[1]q); fi
count=$((count + 1))
printf '%%s' "$count" > %[1]q
echo "Dolt circuit breaker is open: server appears down" >&2
exit 1
`, countPath)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")

	err := hookBeadWithRetry("gt-work", "gastown/polecats/rust", t.TempDir())
	if err == nil {
		t.Fatal("hookBeadWithRetry error = nil, want fail-fast error")
	}
	if !strings.Contains(err.Error(), "Dolt circuit breaker is open") {
		t.Fatalf("error missing bd stderr: %v", err)
	}
	if !strings.Contains(err.Error(), "Safe next action") {
		t.Fatalf("error missing reconciliation guidance: %v", err)
	}
	countBytes, readErr := os.ReadFile(countPath)
	if readErr != nil {
		t.Fatalf("read count: %v", readErr)
	}
	if got := strings.TrimSpace(string(countBytes)); got != "1" {
		t.Fatalf("bd update invoked %s times, want 1", got)
	}
}
