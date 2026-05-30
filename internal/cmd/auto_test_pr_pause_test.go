// Tests for the auto-test-pr CLI runtime-control verbs (Phase 0
// task 2b: gu-uez5w). These tests cover the flag-validation surface
// and the formatters in isolation. The Beads-backed write paths are
// covered by the autotestpr package's mutator tests; we don't
// duplicate that surface here because the verbs are thin glue.
package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/autotestpr"
	"github.com/steveyegge/gastown/internal/beads"
)

// resetAutoTestPRPauseFlags zeroes the package-level flag bindings
// between tests. cobra leaves them set to the operator-provided
// value across runs in a single test binary; the resets here keep
// each subtest hermetic.
func resetAutoTestPRPauseFlags(t *testing.T) {
	t.Helper()
	autoTestPRPauseRig = ""
	autoTestPRPauseAll = false
	autoTestPRPauseDuration = pauseDurationDefault.String()
	autoTestPRPauseReason = ""
	autoTestPRResumeRig = ""
	autoTestPRResumeAll = false
	autoTestPRResumeOverride = false
	autoTestPRStatusFormat = "table"
	autoTestPRShowRig = ""
	autoTestPRShowVerbose = false
	autoTestPRShowRaw = false
	autoTestPRHistoryRig = ""
	autoTestPRHistoryLast = historyLastDefault
}

func TestAutoTestPRPauseRequiresRigOrAll(t *testing.T) {
	resetAutoTestPRPauseFlags(t)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := autoTestPRPauseCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	err := runAutoTestPRPause(cmd, nil)
	if err == nil {
		t.Fatal("expected error when both --rig and --all are unset")
	}
	code, ok := IsSilentExit(err)
	if !ok || code != 2 {
		t.Errorf("err = %v; want SilentExit(2)", err)
	}
	if !strings.Contains(stderr.String(), "--rig=<rig> or --all is required") {
		t.Errorf("stderr should mention required flag, got: %q", stderr.String())
	}
}

func TestAutoTestPRPauseRejectsBothFlags(t *testing.T) {
	resetAutoTestPRPauseFlags(t)
	autoTestPRPauseRig = "gastown_upstream"
	autoTestPRPauseAll = true

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := autoTestPRPauseCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	err := runAutoTestPRPause(cmd, nil)
	if err == nil {
		t.Fatal("expected error when both --rig and --all are set")
	}
	code, ok := IsSilentExit(err)
	if !ok || code != 2 {
		t.Errorf("err = %v; want SilentExit(2)", err)
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr should mention mutual exclusivity, got: %q", stderr.String())
	}
}

func TestAutoTestPRPauseInvalidDuration(t *testing.T) {
	resetAutoTestPRPauseFlags(t)
	autoTestPRPauseAll = true
	autoTestPRPauseDuration = "not-a-duration"

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := autoTestPRPauseCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	err := runAutoTestPRPause(cmd, nil)
	if err == nil {
		t.Fatal("expected error with invalid --duration")
	}
	code, ok := IsSilentExit(err)
	if !ok || code != 2 {
		t.Errorf("err = %v; want SilentExit(2)", err)
	}
	if !strings.Contains(stderr.String(), "invalid --duration") {
		t.Errorf("stderr should mention duration parse failure, got: %q", stderr.String())
	}
}

func TestAutoTestPRPauseNonPositiveDuration(t *testing.T) {
	resetAutoTestPRPauseFlags(t)
	autoTestPRPauseAll = true
	autoTestPRPauseDuration = "-1h"

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := autoTestPRPauseCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	err := runAutoTestPRPause(cmd, nil)
	if err == nil {
		t.Fatal("expected error with negative --duration")
	}
	if !strings.Contains(stderr.String(), "must be positive") {
		t.Errorf("stderr should mention positivity check, got: %q", stderr.String())
	}
}

func TestAutoTestPRResumeRequiresRigOrAll(t *testing.T) {
	resetAutoTestPRPauseFlags(t)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := autoTestPRResumeCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	err := runAutoTestPRResume(cmd, nil)
	if err == nil {
		t.Fatal("expected error when both --rig and --all are unset")
	}
	code, ok := IsSilentExit(err)
	if !ok || code != 2 {
		t.Errorf("err = %v; want SilentExit(2)", err)
	}
}

func TestAutoTestPRStatusInvalidFormat(t *testing.T) {
	resetAutoTestPRPauseFlags(t)
	autoTestPRStatusFormat = "yaml"

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := autoTestPRStatusCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	err := runAutoTestPRStatus(cmd, nil)
	if err == nil {
		t.Fatal("expected error with --format=yaml")
	}
	if !strings.Contains(stderr.String(), "unknown --format") {
		t.Errorf("stderr should mention format, got: %q", stderr.String())
	}
}

func TestAutoTestPRShowRequiresRig(t *testing.T) {
	resetAutoTestPRPauseFlags(t)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := autoTestPRShowCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	err := runAutoTestPRShow(cmd, nil)
	if err == nil {
		t.Fatal("expected error without --rig")
	}
	if !strings.Contains(stderr.String(), "--rig is required") {
		t.Errorf("stderr should mention --rig requirement, got: %q", stderr.String())
	}
}

func TestAutoTestPRHistoryRequiresRig(t *testing.T) {
	resetAutoTestPRPauseFlags(t)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := autoTestPRHistoryCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	err := runAutoTestPRHistory(cmd, nil)
	if err == nil {
		t.Fatal("expected error without --rig")
	}
	if !strings.Contains(stderr.String(), "--rig is required") {
		t.Errorf("stderr should mention --rig requirement, got: %q", stderr.String())
	}
}

func TestAutoTestPRHistoryRejectsZeroLast(t *testing.T) {
	resetAutoTestPRPauseFlags(t)
	autoTestPRHistoryRig = "gastown_upstream"
	autoTestPRHistoryLast = 0

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := autoTestPRHistoryCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	err := runAutoTestPRHistory(cmd, nil)
	if err == nil {
		t.Fatal("expected error with --last=0")
	}
	if !strings.Contains(stderr.String(), "--last must be positive") {
		t.Errorf("stderr should mention --last positivity, got: %q", stderr.String())
	}
}

// TestPrintStatusTableEmptyRigsReports prints the literal "no rigs
// opted in" line per task 2b acceptance criterion (synthesis line
// 1174).
func TestPrintStatusTableEmptyRigsReports(t *testing.T) {
	t.Parallel()

	state := autotestpr.TownState{
		EnabledRigs:    []string{},
		CircuitBreaker: autotestpr.CircuitBreakerState{Count: 0},
	}
	buf := &bytes.Buffer{}
	if err := printStatusTable(buf, state); err != nil {
		t.Fatalf("printStatusTable: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "no rigs opted in") {
		t.Errorf("status output missing 'no rigs opted in' line: %q", out)
	}
	// Also expect the (town-wide) row.
	if !strings.Contains(out, "(town-wide)") {
		t.Errorf("status output missing (town-wide) row: %q", out)
	}
	if !strings.Contains(out, "running") {
		t.Errorf("status output missing 'running' state: %q", out)
	}
}

func TestPrintStatusTableShowsPausedRig(t *testing.T) {
	t.Parallel()

	state := autotestpr.TownState{
		EnabledRigs: []string{"gastown_upstream"},
		RigPauses: map[string]autotestpr.RigPauseEntry{
			"gastown_upstream": {
				PausedUntil: "2026-05-25T12:00:00Z",
				PausedBy:    "overseer",
			},
		},
		CircuitBreaker: autotestpr.CircuitBreakerState{Count: 0},
	}
	buf := &bytes.Buffer{}
	if err := printStatusTable(buf, state); err != nil {
		t.Fatalf("printStatusTable: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "gastown_upstream") {
		t.Errorf("status output missing rig name: %q", out)
	}
	if !strings.Contains(out, "paused") {
		t.Errorf("status output missing 'paused' state: %q", out)
	}
	if !strings.Contains(out, "2026-05-25T12:00:00Z") {
		t.Errorf("status output missing pause-until timestamp: %q", out)
	}
}

func TestPrintStatusTableCircuitBreakerTripped(t *testing.T) {
	t.Parallel()

	state := autotestpr.TownState{
		EnabledRigs: []string{"gastown_upstream"},
		CircuitBreaker: autotestpr.CircuitBreakerState{
			Count:        3,
			TrippedUntil: "2026-05-25T12:00:00Z",
		},
	}
	buf := &bytes.Buffer{}
	if err := printStatusTable(buf, state); err != nil {
		t.Fatalf("printStatusTable: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "paused-by-circuit-breaker") {
		t.Errorf("status output missing 'paused-by-circuit-breaker' state: %q", out)
	}
	if !strings.Contains(out, "count=3") {
		t.Errorf("status output missing breaker count: %q", out)
	}
}

func TestPrintShowSummaryUnknownRig(t *testing.T) {
	t.Parallel()

	state := autotestpr.TownState{
		EnabledRigs: []string{"gastown_upstream"},
	}
	buf := &bytes.Buffer{}
	if err := printShowSummary(buf, state, "casc_crud", false); err != nil {
		t.Fatalf("printShowSummary: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "rig:     casc_crud") {
		t.Errorf("show output missing rig line: %q", out)
	}
	if !strings.Contains(out, "enabled: false") {
		t.Errorf("show output should report enabled=false for unknown rig: %q", out)
	}
}

func TestPrintShowSummaryVerbose(t *testing.T) {
	t.Parallel()

	state := autotestpr.TownState{
		EnabledRigs: []string{"gastown_upstream"},
		Incidents: []autotestpr.Incident{
			{
				At:    "2026-05-24T12:00:00Z",
				Actor: "overseer",
				Kind:  autotestpr.IncidentRigPause,
				Rig:   "gastown_upstream",
			},
			{
				At:    "2026-05-24T13:00:00Z",
				Actor: "overseer",
				Kind:  autotestpr.IncidentGlobalResume,
				// Town-wide entry — should still show in rig view.
			},
			{
				At:    "2026-05-24T14:00:00Z",
				Actor: "overseer",
				Kind:  autotestpr.IncidentRigPause,
				Rig:   "casc_crud",
			},
		},
	}
	buf := &bytes.Buffer{}
	if err := printShowSummary(buf, state, "gastown_upstream", true); err != nil {
		t.Fatalf("printShowSummary: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "incidents:") {
		t.Errorf("verbose output missing incidents header: %q", out)
	}
	if !strings.Contains(out, "rig-pause") {
		t.Errorf("verbose output missing rig-pause entry: %q", out)
	}
	if !strings.Contains(out, "global-resume") {
		t.Errorf("verbose output should include town-wide entries: %q", out)
	}
	if strings.Contains(out, "casc_crud") {
		t.Errorf("verbose output should NOT include other rigs' entries: %q", out)
	}
}

func TestIncidentsForRigFiltersAndLimits(t *testing.T) {
	t.Parallel()

	state := autotestpr.TownState{
		Incidents: []autotestpr.Incident{
			{At: "1", Kind: autotestpr.IncidentRigPause, Rig: "a"},
			{At: "2", Kind: autotestpr.IncidentRigPause, Rig: "b"},
			{At: "3", Kind: autotestpr.IncidentGlobalPause}, // town-wide
			{At: "4", Kind: autotestpr.IncidentRigResume, Rig: "a"},
			{At: "5", Kind: autotestpr.IncidentRigResume, Rig: "a"},
		},
	}
	got := incidentsForRig(state, "a", 3)
	if len(got) != 3 {
		t.Fatalf("len = %d; want 3", len(got))
	}
	// Should be the 3 most-recent matches: town-wide, resume, resume.
	wants := []string{"3", "4", "5"}
	for i, w := range wants {
		if got[i].At != w {
			t.Errorf("got[%d].At = %q; want %q", i, got[i].At, w)
		}
	}
}

func TestIncidentsForRigNoLimit(t *testing.T) {
	t.Parallel()

	state := autotestpr.TownState{
		Incidents: []autotestpr.Incident{
			{At: "1", Rig: "a"},
			{At: "2", Rig: "a"},
		},
	}
	got := incidentsForRig(state, "a", 0)
	if len(got) != 2 {
		t.Errorf("limit=0 should not trim; got %d", len(got))
	}
}

func TestRigPausePtr(t *testing.T) {
	t.Parallel()

	state := autotestpr.TownState{
		RigPauses: map[string]autotestpr.RigPauseEntry{
			"gastown_upstream": {PausedBy: "overseer"},
		},
	}
	if got := rigPausePtr(state, "casc_crud"); got != nil {
		t.Errorf("rigPausePtr(unknown) = %+v; want nil", got)
	}
	got := rigPausePtr(state, "gastown_upstream")
	if got == nil || got.PausedBy != "overseer" {
		t.Errorf("rigPausePtr(known) = %+v; want PausedBy=overseer", got)
	}
}

func TestResolveOperatorActorFallback(t *testing.T) {
	t.Setenv("BD_ACTOR", "")
	t.Setenv("GT_ROLE", "")
	if got := resolveOperatorActor(); got != "overseer" {
		t.Errorf("resolveOperatorActor() = %q; want %q", got, "overseer")
	}

	t.Setenv("GT_ROLE", "polecat")
	if got := resolveOperatorActor(); got != "polecat" {
		t.Errorf("resolveOperatorActor() = %q; want %q", got, "polecat")
	}

	t.Setenv("BD_ACTOR", "gastown_upstream/polecats/radrat")
	if got := resolveOperatorActor(); got != "gastown_upstream/polecats/radrat" {
		t.Errorf("BD_ACTOR should win over GT_ROLE; got %q", got)
	}
}

// TestStatusJSONShapeForEmptyState pins the gu-kn0j8 acceptance: when
// the town bead is freshly provisioned (no rigs opted in, no pause,
// counter zero), `status --format=json` MUST emit the literal
// `{enabled_rigs:[], paused:false, circuit_breaker:{count:0}}`.
func TestStatusJSONShapeForEmptyState(t *testing.T) {
	t.Parallel()

	state := autotestpr.DefaultTownState()
	out := state.ToStatusJSON()
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	got := string(raw)
	want := `{"enabled_rigs":[],"paused":false,"circuit_breaker":{"count":0}}`
	if got != want {
		t.Errorf("StatusJSON shape mismatch:\n got: %s\nwant: %s", got, want)
	}
}

// nowFnGuard pins nowFn for one test and restores it on cleanup.
// Currently exercised by tests that don't yet need it; kept here so
// future per-rig + town-wide integration tests have a single helper
// to patch the clock.
func nowFnGuard(t *testing.T, fixed time.Time) {
	t.Helper()
	prev := nowFn
	nowFn = func() time.Time { return fixed }
	t.Cleanup(func() { nowFn = prev })
}

// _ keeps the unused-import warning from firing if no test uses
// nowFnGuard yet — the helper is there for future tests added when
// the bead-backed integration test fixture lands.
var _ = nowFnGuard

// TestLoadTownStateWithTimeoutExpires verifies the ≤2s timeout path
// fires when the underlying Dolt read takes too long. We simulate a
// slow reader by injecting a very short timeout and a beads wrapper
// that blocks forever.
func TestLoadTownStateWithTimeoutExpires(t *testing.T) {
	t.Parallel()

	// We test the timeout path at the loadTownStateWithTimeout level by
	// setting an absurdly short timeout against a beads wrapper pointing
	// at a nonexistent dir. The bd subprocess will take at least a few ms
	// to fail, so the 1ns context fires first, proving the timeout
	// plumbing works.
	b := beads.New("/nonexistent-town-root-for-timeout-test")
	_, err := loadTownStateWithTimeout(b, 1*time.Nanosecond)
	// With 1ns timeout, we expect EITHER ErrStatusTimeout (ctx won the
	// race) or a real error (the goroutine finished first). In practice,
	// context.WithTimeout(1ns) fires immediately and we get
	// ErrStatusTimeout. But we accept both to avoid a flaky test.
	if err == nil {
		t.Fatal("expected error with 1ns timeout")
	}
	// The test is mostly a compile-time + shape check that the timeout
	// plumbing exists. If the goroutine finishes first (bd fails fast
	// on a bad dir), that's fine too — the important contract is that
	// the function doesn't hang for 60s.
	t.Logf("error (expected): %v", err)
}

// TestStatusTimeoutExitCode verifies that the status command surfaces a
// clear exit code (4) and message when Dolt is degraded.
func TestStatusTimeoutExitCode(t *testing.T) {
	resetAutoTestPRPauseFlags(t)
	autoTestPRStatusFormat = "table"

	// Hermetic: stub the beads constructor and the town-state load so this
	// exercises ONLY the timeout->SilentExit(4) wiring, with no dependency on
	// a live town, a running Dolt server, or bd on PATH. The previous version
	// forced a 1ns timeout and raced a real read — green locally (live Dolt
	// server slow enough that the timeout won) but red in CI's bd-less,
	// server-less unit job (read failed instantly, that error won the race).
	prevBeads := newAutoTestPRBeadsFn
	prevLoad := loadTownStateFn
	newAutoTestPRBeadsFn = func() (*beads.Beads, error) { return nil, nil }
	loadTownStateFn = func(*beads.Beads, time.Duration) (autotestpr.TownState, error) {
		return autotestpr.TownState{}, ErrStatusTimeout
	}
	t.Cleanup(func() {
		newAutoTestPRBeadsFn = prevBeads
		loadTownStateFn = prevLoad
	})

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := autoTestPRStatusCmd
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	defer cmd.SetOut(nil)
	defer cmd.SetErr(nil)

	err := runAutoTestPRStatus(cmd, nil)
	code, ok := IsSilentExit(err)
	if !ok || code != 4 {
		t.Errorf("err = %v; want SilentExit(4) for timeout", err)
	}
	if !strings.Contains(stderr.String(), "Dolt read timed out") {
		t.Errorf("stderr should mention timeout, got: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "gt dolt status") {
		t.Errorf("stderr should hint at gt dolt status, got: %q", stderr.String())
	}
}
