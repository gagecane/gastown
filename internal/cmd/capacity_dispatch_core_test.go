package cmd

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

// writeSchedulerState writes a scheduler-state.json under town/.runtime so
// dispatchScheduledWork's capacity.LoadState sees the desired Paused state.
func writeSchedulerState(t *testing.T, town string, st *capacity.SchedulerState) {
	t.Helper()
	if err := capacity.SaveState(town, st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
}

// writeTownSchedulerConfig writes town settings with the given max_polecats so
// dispatchScheduledWork's "direct dispatch / disabled" gate (maxPolecats <= 0)
// can be exercised without a live Dolt server.
func writeTownSchedulerConfig(t *testing.T, town string, maxPolecats int) {
	t.Helper()
	settings := config.NewTownSettings()
	settings.Scheduler = &capacity.SchedulerConfig{MaxPolecats: &maxPolecats}
	path := config.TownSettingsPath(town)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}
	if err := config.SaveTownSettings(path, settings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
}

// TestDispatchScheduledWork_Paused covers the early-return guard: a paused
// scheduler must place nothing and report no error, regardless of queued work.
// This is the operator "stop the world" lever; a regression here would let the
// scheduler keep spawning polecats after a `gt scheduler pause` (gu-t9kdv: the
// 0%-coverage dispatch entrypoint guards exactly this class of spawn bug).
func TestDispatchScheduledWork_Paused(t *testing.T) {
	town := t.TempDir()
	writeSchedulerState(t, town, &capacity.SchedulerState{Paused: true, PausedBy: "operator"})

	dispatched, err := dispatchScheduledWork(town, "test", 0, false)
	if err != nil {
		t.Fatalf("paused dispatch returned error: %v", err)
	}
	if dispatched != 0 {
		t.Fatalf("paused dispatch placed %d beads, want 0", dispatched)
	}
}

// TestDispatchScheduledWork_DirectDispatchDisabled covers the maxPolecats <= 0
// gate: with the scheduler in direct-dispatch/disabled mode the loop must no-op
// (0 dispatched, no error) before it ever queries ready work or spawns.
func TestDispatchScheduledWork_DirectDispatchDisabled(t *testing.T) {
	town := t.TempDir()
	writeSchedulerState(t, town, &capacity.SchedulerState{})
	writeTownSchedulerConfig(t, town, -1) // direct dispatch

	dispatched, err := dispatchScheduledWork(town, "test", 0, false)
	if err != nil {
		t.Fatalf("disabled dispatch returned error: %v", err)
	}
	if dispatched != 0 {
		t.Fatalf("disabled dispatch placed %d beads, want 0", dispatched)
	}
}

// TestDispatchScheduledWork_LockHeld covers the contention guard: when another
// dispatcher already holds scheduler-dispatch.lock, this call must return
// (0, nil) immediately rather than blocking or double-dispatching. Holding the
// flock from the test simulates the daemon's periodic tick owning the lock.
func TestDispatchScheduledWork_LockHeld(t *testing.T) {
	town := t.TempDir()
	writeSchedulerState(t, town, &capacity.SchedulerState{})

	runtimeDir := filepath.Join(town, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatalf("mkdir runtime: %v", err)
	}
	held := flock.New(filepath.Join(runtimeDir, "scheduler-dispatch.lock"))
	locked, err := held.TryLock()
	if err != nil || !locked {
		t.Fatalf("test could not pre-acquire dispatch lock: locked=%v err=%v", locked, err)
	}
	t.Cleanup(func() { _ = held.Unlock() })

	dispatched, err := dispatchScheduledWork(town, "test", 0, false)
	if err != nil {
		t.Fatalf("lock-contended dispatch returned error: %v", err)
	}
	if dispatched != 0 {
		t.Fatalf("lock-contended dispatch placed %d beads, want 0", dispatched)
	}
}

// TestValidatePendingBeadForDispatch_EmptyRig covers the fast-path: a bead with
// no target rig has no cross-rig prefix to check and is always accepted.
func TestValidatePendingBeadForDispatch_EmptyRig(t *testing.T) {
	town := t.TempDir()
	b := capacity.PendingBead{WorkBeadID: "gu-abc", TargetRig: ""}
	if err := validatePendingBeadForDispatch(town, b, false); err != nil {
		t.Fatalf("empty-rig bead must validate, got %v", err)
	}
}

// TestValidatePendingBeadForDispatch_PrefixMismatch covers the cross-rig prefix
// guard (gt-el4): a bead whose ID prefix does not match the target rig's
// registered prefix must be refused with ErrCrossRigPrefix, so the polecat is
// never launched into a rig DB that cannot resolve the bead. escalate=false so
// the test never shells out to `gt escalate`.
func TestValidatePendingBeadForDispatch_PrefixMismatch(t *testing.T) {
	town := t.TempDir()
	writeRigsConfig(t, town, "walletui", "wui")

	b := capacity.PendingBead{WorkBeadID: "hq-uejt", TargetRig: "walletui"}
	err := validatePendingBeadForDispatch(town, b, false)
	if !errors.Is(err, capacity.ErrCrossRigPrefix) {
		t.Fatalf("prefix mismatch must return ErrCrossRigPrefix, got %v", err)
	}
}

// TestValidatePendingBeadForDispatch_PrefixMatch covers the accept branch: a
// bead whose prefix matches the rig's registered prefix passes validation.
func TestValidatePendingBeadForDispatch_PrefixMatch(t *testing.T) {
	town := t.TempDir()
	writeRigsConfig(t, town, "walletui", "wui")

	b := capacity.PendingBead{WorkBeadID: "wui-1234", TargetRig: "walletui"}
	if err := validatePendingBeadForDispatch(town, b, false); err != nil {
		t.Fatalf("matching-prefix bead must validate, got %v", err)
	}
}

// writeRigsConfig writes mayor/rigs.json registering one rig with a beads prefix
// so rigBeadsPrefix resolves a non-empty prefix for the cross-rig guard.
func writeRigsConfig(t *testing.T, town, rigName, prefix string) {
	t.Helper()
	rc := config.RigsConfig{
		Version: config.CurrentRigsVersion,
		Rigs: map[string]config.RigEntry{
			rigName: {BeadsConfig: &config.BeadsConfig{Repo: "local", Prefix: prefix}},
		},
	}
	path := filepath.Join(town, "mayor", "rigs.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	data, err := json.Marshal(rc)
	if err != nil {
		t.Fatalf("marshal rigs config: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write rigs config: %v", err)
	}
}

// TestRecordDispatchFailure_NilContext covers the guard that a PendingBead with
// no parsed context is a no-op (nothing to increment / persist).
func TestRecordDispatchFailure_NilContext(t *testing.T) {
	town := t.TempDir()
	// Context nil → must return before touching the (nil) beads handle.
	recordDispatchFailure(town, nil, capacity.PendingBead{ID: "ctx-1"}, errors.New("boom"))
}

// TestRecordDispatchFailure_AlreadyDispatched covers the gu-cqmw guard: an
// "already hooked/in_progress" error means a live agent is working the bead —
// not a true dispatch failure — so the failure counter must NOT increment
// (which would spuriously circuit-break an actively-worked bead). A non-nil
// beads handle would only be touched on the increment path; reaching it here
// would mean the guard regressed.
func TestRecordDispatchFailure_AlreadyDispatched(t *testing.T) {
	town := t.TempDir()
	ctx := &capacity.SlingContextFields{WorkBeadID: "gu-abc", DispatchFailures: 0}
	b := capacity.PendingBead{ID: "ctx-1", WorkBeadID: "gu-abc", Context: ctx}

	recordDispatchFailure(town, nil, b, errors.New("already hooked to polecat fury"))

	if ctx.DispatchFailures != 0 {
		t.Fatalf("already-dispatched error must not increment counter, got %d", ctx.DispatchFailures)
	}
}

// TestLogCircuitBreak_EmptyTownRoot covers the guard that an empty town root is
// a no-op (no log file written, no panic).
func TestLogCircuitBreak_EmptyTownRoot(t *testing.T) {
	logCircuitBreak("", "gu-work", "ctx-1", "walletui", "spawn failed")
}

// TestLogCircuitBreak_WritesRecord covers the happy path: a circuit-break record
// is appended to the town circuit-break log with the work/context/rig fields the
// circuit_break_dog patrol keys on (gu-ixo67).
func TestLogCircuitBreak_WritesRecord(t *testing.T) {
	town := t.TempDir()
	logCircuitBreak(town, "gu-work", "ctx-1", "walletui", "spawn failed 3x")

	logPath := filepath.Join(town, events.CircuitBreakLogFile)
	f, err := os.Open(logPath) //nolint:gosec // G304: test-constructed path
	if err != nil {
		t.Fatalf("circuit-break log not written: %v", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatal("circuit-break log is empty")
	}
	var rec events.CircuitBreakRecord
	if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
		t.Fatalf("unmarshal circuit-break record: %v", err)
	}
	if rec.WorkBeadID != "gu-work" || rec.ContextID != "ctx-1" || rec.TargetRig != "walletui" {
		t.Fatalf("unexpected record: %+v", rec)
	}
	if rec.LastFailure != "spawn failed 3x" {
		t.Fatalf("LastFailure = %q, want %q", rec.LastFailure, "spawn failed 3x")
	}
}

// TestIsDaemonDispatch covers the GT_DAEMON env gate that decides whether the
// dispatch loop is bounded by the daemon budget (gu-t6jqq).
func TestIsDaemonDispatch(t *testing.T) {
	t.Setenv("GT_DAEMON", "1")
	if !isDaemonDispatch() {
		t.Fatal("GT_DAEMON=1 must report daemon dispatch")
	}
	t.Setenv("GT_DAEMON", "0")
	if isDaemonDispatch() {
		t.Fatal("GT_DAEMON=0 must not report daemon dispatch")
	}
	os.Unsetenv("GT_DAEMON")
	if isDaemonDispatch() {
		t.Fatal("unset GT_DAEMON must not report daemon dispatch")
	}
}

// TestResolveMaintenanceBudget covers the GT_DISPATCH_MAINT_BUDGET override:
// the default applies when unset/garbage, a parseable duration overrides it, and
// a non-positive value disables the cap (returned verbatim).
func TestResolveMaintenanceBudget(t *testing.T) {
	os.Unsetenv("GT_DISPATCH_MAINT_BUDGET")
	if got := resolveMaintenanceBudget(); got != maintenanceBudget {
		t.Fatalf("unset = %v, want default %v", got, maintenanceBudget)
	}

	t.Setenv("GT_DISPATCH_MAINT_BUDGET", "45s")
	if got := resolveMaintenanceBudget().Seconds(); got != 45 {
		t.Fatalf("override = %vs, want 45s", got)
	}

	t.Setenv("GT_DISPATCH_MAINT_BUDGET", "garbage")
	if got := resolveMaintenanceBudget(); got != maintenanceBudget {
		t.Fatalf("garbage = %v, want default %v", got, maintenanceBudget)
	}

	t.Setenv("GT_DISPATCH_MAINT_BUDGET", "0s")
	if got := resolveMaintenanceBudget(); got != 0 {
		t.Fatalf("zero = %v, want 0 (cap disabled)", got)
	}
}

// TestDispatchPhase covers the timing wrapper: it must invoke the wrapped pass
// exactly once. The fast path (under the slow threshold) takes the silent branch
// unless GT_HEARTBEAT_PROFILE is set; either way the function runs the closure.
func TestDispatchPhase(t *testing.T) {
	os.Unsetenv("GT_HEARTBEAT_PROFILE")
	calls := 0
	dispatchPhase("test-pass", func() { calls++ })
	if calls != 1 {
		t.Fatalf("dispatchPhase ran closure %d times, want 1", calls)
	}

	// Profile mode takes the per-pass logging branch; the closure still runs once.
	t.Setenv("GT_HEARTBEAT_PROFILE", "1")
	calls = 0
	dispatchPhase("profiled-pass", func() { calls++ })
	if calls != 1 {
		t.Fatalf("profiled dispatchPhase ran closure %d times, want 1", calls)
	}
}

// TestBeadsForContext_HQFallback covers the fallback branch: a nil/empty-rig
// context resolves to the HQ beads dir.
func TestBeadsForContext_HQFallback(t *testing.T) {
	town := t.TempDir()
	// nil fields → HQ fallback, must not panic and must return a usable handle.
	if got := beadsForContext(town, nil); got == nil {
		t.Fatal("beadsForContext(nil) returned nil")
	}
	// empty TargetRig → HQ fallback.
	if got := beadsForContext(town, &capacity.SlingContextFields{}); got == nil {
		t.Fatal("beadsForContext(empty rig) returned nil")
	}
}

// TestBeadsForPendingContext covers both branches: an explicit ContextBeadsDir
// is used directly, otherwise it falls back to beadsForContext on the parsed
// context fields.
func TestBeadsForPendingContext(t *testing.T) {
	town := t.TempDir()

	// Explicit beads dir branch.
	withDir := capacity.PendingBead{
		ID:              "ctx-1",
		ContextBeadsDir: filepath.Join(town, "walletui", ".beads"),
		ContextWorkDir:  filepath.Join(town, "walletui"),
	}
	if got := beadsForPendingContext(town, withDir); got == nil {
		t.Fatal("beadsForPendingContext(explicit dir) returned nil")
	}

	// Fallback branch (no ContextBeadsDir → uses Context fields / HQ).
	fallback := capacity.PendingBead{ID: "ctx-2", Context: &capacity.SlingContextFields{}}
	if got := beadsForPendingContext(town, fallback); got == nil {
		t.Fatal("beadsForPendingContext(fallback) returned nil")
	}
}

// TestBeadsForContextRecord covers the record-form constructor.
func TestBeadsForContextRecord(t *testing.T) {
	town := t.TempDir()
	rec := slingContextRecord{
		workDir:  filepath.Join(town, "walletui"),
		beadsDir: filepath.Join(town, "walletui", ".beads"),
	}
	if got := beadsForContextRecord(rec); got == nil {
		t.Fatal("beadsForContextRecord returned nil")
	}
}
