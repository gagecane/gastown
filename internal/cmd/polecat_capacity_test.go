package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
	"github.com/steveyegge/gastown/internal/session"
)

func setupPolecatCapacityTestTown(t *testing.T, maxPolecats int) string {
	t.Helper()
	townRoot := t.TempDir()
	configureScheduler(t, townRoot, maxPolecats, 1)
	if err := config.SaveRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"), &config.RigsConfig{Version: config.CurrentRigsVersion}); err != nil {
		t.Fatalf("SaveRigsConfig: %v", err)
	}
	return townRoot
}

func setupPolecatCapacityRig(t *testing.T, maxPolecats int) string {
	t.Helper()
	townRoot := t.TempDir()
	configureScheduler(t, townRoot, maxPolecats, 1)
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", "polecats"), 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}
	if err := config.SaveRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"), &config.RigsConfig{
		Version: config.CurrentRigsVersion,
		Rigs: map[string]config.RigEntry{
			"gastown": {GitURL: "https://example.invalid/gastown.git"},
		},
	}); err != nil {
		t.Fatalf("SaveRigsConfig: %v", err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	return townRoot
}

func TestCapacitySnapshotCleansStaleReservations(t *testing.T) {
	townRoot := setupPolecatCapacityTestTown(t, 1)
	dir := polecatAdmissionDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir reservations: %v", err)
	}
	stale := polecatAdmissionReservation{
		ID:        "stale",
		PID:       99999999,
		Rig:       "gastown",
		Bead:      "gt-stale",
		Operation: "test",
		CreatedAt: time.Now().Add(-time.Hour),
	}
	data, err := json.Marshal(stale)
	if err != nil {
		t.Fatalf("marshal stale reservation: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stale.json"), data, 0644); err != nil {
		t.Fatalf("write stale reservation: %v", err)
	}

	snapshot, err := polecatCapacitySnapshotForTown(townRoot)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Reservations != 0 || snapshot.Free != 1 {
		t.Fatalf("snapshot after stale cleanup = %+v, want reservations=0 free=1", snapshot)
	}
	if _, err := os.Stat(filepath.Join(dir, "stale.json")); !os.IsNotExist(err) {
		t.Fatalf("stale reservation still exists: %v", err)
	}
}

func TestCapacitySnapshotRemovesStructurallyInvalidReservations(t *testing.T) {
	townRoot := setupPolecatCapacityTestTown(t, 1)
	dir := polecatAdmissionDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir reservations: %v", err)
	}
	path := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(path, []byte(`{}`), 0644); err != nil {
		t.Fatalf("write invalid reservation: %v", err)
	}

	snapshot, err := polecatCapacitySnapshotForTown(townRoot)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Reservations != 0 || snapshot.Free != 1 {
		t.Fatalf("snapshot after invalid cleanup = %+v, want reservations=0 free=1", snapshot)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid reservation still exists: %v", err)
	}
}

func TestCapacitySnapshotRemovesMismatchedReservationFile(t *testing.T) {
	townRoot := setupPolecatCapacityTestTown(t, 1)
	dir := polecatAdmissionDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir reservations: %v", err)
	}
	reservation := polecatAdmissionReservation{
		ID:        "other",
		PID:       os.Getpid(),
		Rig:       "gastown",
		Bead:      "gt-mismatch",
		Operation: "test",
		CreatedAt: time.Now(),
	}
	data, err := json.Marshal(reservation)
	if err != nil {
		t.Fatalf("marshal reservation: %v", err)
	}
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write mismatched reservation: %v", err)
	}

	snapshot, err := polecatCapacitySnapshotForTown(townRoot)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Reservations != 0 || snapshot.Free != 1 {
		t.Fatalf("snapshot after mismatch cleanup = %+v, want reservations=0 free=1", snapshot)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("mismatched reservation still exists: %v", err)
	}
}

func TestCapacitySnapshotKeepsOldLiveReservation(t *testing.T) {
	townRoot := setupPolecatCapacityTestTown(t, 1)
	dir := polecatAdmissionDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir reservations: %v", err)
	}
	reservation := polecatAdmissionReservation{
		ID:        "live",
		PID:       os.Getpid(),
		Rig:       "gastown",
		Bead:      "gt-live",
		Operation: "test",
		CreatedAt: time.Now().Add(-time.Hour),
	}
	data, err := json.Marshal(reservation)
	if err != nil {
		t.Fatalf("marshal reservation: %v", err)
	}
	path := filepath.Join(dir, "live.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write live reservation: %v", err)
	}

	snapshot, err := polecatCapacitySnapshotForTown(townRoot)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Reservations != 1 || snapshot.Free != 0 {
		t.Fatalf("snapshot with old live reservation = %+v, want reservations=1 free=0", snapshot)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("live reservation should remain: %v", err)
	}
}

// TestCapacitySnapshotReapsYoungDeadReservation pins the gu-3jizl fix: a
// dead-PID reservation is reaped immediately, with no age grace period. A
// dispatcher that died ungracefully (crash/kill) before its deferred
// Release() ran leaves a reservation file behind; holding it for any grace
// window leaks admission capacity. Previously a 30-minute TTL gate kept these
// dead-owner reservations counted as occupied capacity town-wide, eroding
// usable capacity across rigs.
func TestCapacitySnapshotReapsYoungDeadReservation(t *testing.T) {
	townRoot := setupPolecatCapacityTestTown(t, 1)
	dir := polecatAdmissionDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir reservations: %v", err)
	}
	deadYoung := polecatAdmissionReservation{
		ID:        "dead-young",
		PID:       99999999,
		Rig:       "gastown",
		Bead:      "gt-dead-young",
		Operation: "test",
		CreatedAt: time.Now(),
	}
	data, err := json.Marshal(deadYoung)
	if err != nil {
		t.Fatalf("marshal dead-young reservation: %v", err)
	}
	path := filepath.Join(dir, "dead-young.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write dead-young reservation: %v", err)
	}

	snapshot, err := polecatCapacitySnapshotForTown(townRoot)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Reservations != 0 || snapshot.Free != 1 {
		t.Fatalf("snapshot after young dead-PID cleanup = %+v, want reservations=0 free=1", snapshot)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("young dead-PID reservation still exists: %v", err)
	}
}

func TestAcquirePolecatAdmissionUsesConfiguredCap(t *testing.T) {
	townRoot := setupPolecatCapacityTestTown(t, 1)

	first, snapshot, err := acquirePolecatAdmission(townRoot, "gastown", "gt-one", "test")
	if err != nil {
		t.Fatalf("first admission: %v", err)
	}
	defer first.Release()
	if snapshot.Max != 1 || snapshot.Reservations != 1 || snapshot.Free != 0 {
		t.Fatalf("snapshot after first admission = %+v, want max=1 reservations=1 free=0", snapshot)
	}

	second, deniedSnapshot, err := acquirePolecatAdmission(townRoot, "gastown", "gt-two", "test")
	if second != nil {
		defer second.Release()
	}
	var admissionErr *polecatCapacityAdmissionError
	if !errors.As(err, &admissionErr) {
		t.Fatalf("second admission error = %v, want polecatCapacityAdmissionError", err)
	}
	if deniedSnapshot.Max != 1 || deniedSnapshot.Reservations != 1 || deniedSnapshot.Free != 0 {
		t.Fatalf("denied snapshot = %+v, want max=1 reservations=1 free=0", deniedSnapshot)
	}
	if !strings.Contains(err.Error(), "scheduler.max_polecats") {
		t.Fatalf("denial error %q should mention scheduler.max_polecats", err.Error())
	}

	first.Release()
	third, snapshot, err := acquirePolecatAdmission(townRoot, "gastown", "gt-three", "test")
	if err != nil {
		t.Fatalf("third admission after release: %v", err)
	}
	defer third.Release()
	if snapshot.Max != 1 || snapshot.Reservations != 1 || snapshot.Free != 0 {
		t.Fatalf("snapshot after third admission = %+v, want max=1 reservations=1 free=0", snapshot)
	}
}

func TestAcquirePolecatAdmissionDisabledWhenSchedulerCapNonPositive(t *testing.T) {
	for _, maxPolecats := range []int{-1, 0} {
		t.Run("max", func(t *testing.T) {
			townRoot := t.TempDir()
			configureScheduler(t, townRoot, maxPolecats, 1)

			handle, snapshot, err := acquirePolecatAdmission(townRoot, "gastown", "gt-one", "test")
			if err != nil {
				t.Fatalf("admission with max=%d: %v", maxPolecats, err)
			}
			defer handle.Release()
			if !handle.disabled {
				t.Fatalf("admission handle should be disabled for max=%d", maxPolecats)
			}
			if snapshot.Max != maxPolecats {
				t.Fatalf("snapshot max = %d, want %d", snapshot.Max, maxPolecats)
			}
			if _, err := os.Stat(polecatAdmissionDir(townRoot)); !os.IsNotExist(err) {
				t.Fatalf("reservation dir exists for disabled admission: %v", err)
			}
		})
	}
}

func TestConcurrentPolecatAdmissionReservationsDoNotExceedCap(t *testing.T) {
	townRoot := setupPolecatCapacityTestTown(t, 1)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var handles []*polecatAdmissionHandle
	successes := 0
	denials := 0

	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			handle, _, err := acquirePolecatAdmission(townRoot, "gastown", "gt-race", "test")
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
				handles = append(handles, handle)
				return
			}
			var admissionErr *polecatCapacityAdmissionError
			if errors.As(err, &admissionErr) || strings.Contains(err.Error(), "admission is busy") {
				denials++
				return
			}
			t.Errorf("unexpected admission error: %v", err)
		}()
	}
	close(start)
	wg.Wait()
	for _, handle := range handles {
		handle.Release()
	}

	if successes != 1 {
		t.Fatalf("successful admissions = %d, want 1", successes)
	}
	if denials != 5 {
		t.Fatalf("denied admissions = %d, want 5", denials)
	}
}

// TestPolecatAdmissionEnforcesPerRigCapUnderLock is the gu-uxwob regression
// guard. The per-rig cap is now enforced UNDER the admission flock inside
// acquirePolecatAdmission, counting the same unit as the town cap: working +
// recovery_blocked + that rig's already-written reservations (gu-ccycc).
//
// Deterministic by construction: the admission flock (TryLock) serializes
// concurrent callers, so a live concurrent sling that "claimed the last slot"
// is observable, by the time we hold the lock, as its on-disk rig-attributed
// reservation. We simulate that prior sling by pre-writing one reservation for
// the rig, then assert a fresh admission to the SAME rig is denied by the
// per-rig cap (cap=1, already 1 reservation), while the town still has room —
// and that a DIFFERENT rig with headroom is still admitted.
//
// Before the fix acquirePolecatAdmission guarded only the town cap, so this
// second same-rig admission would succeed and overshoot the per-rig cap; the
// per-rig check lived only in the lock-free SpawnPolecatForSling reads.
func TestPolecatAdmissionEnforcesPerRigCapUnderLock(t *testing.T) {
	townRoot := setupPolecatCapacityTestTown(t, 10) // town cap high (headroom)
	one := 1
	writeRigCap(t, townRoot, "capped", &one)

	// Simulate a concurrent sling that already claimed capped's only slot:
	// write its rig-attributed admission reservation on disk.
	dir := polecatAdmissionDir(townRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir reservations: %v", err)
	}
	prior := polecatAdmissionReservation{
		ID:        "prior-sling",
		PID:       os.Getpid(), // live PID so cleanup keeps it
		Rig:       "capped",
		Bead:      "gt-prior",
		Operation: "test",
		CreatedAt: time.Now(),
	}
	data, err := json.Marshal(prior)
	if err != nil {
		t.Fatalf("marshal prior reservation: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prior-sling.json"), data, 0o644); err != nil {
		t.Fatalf("write prior reservation: %v", err)
	}

	// Same rig, already at its per-rig cap via the prior reservation → denied.
	handle, snapshot, err := acquirePolecatAdmission(townRoot, "capped", "gt-second", "test")
	if err == nil {
		handle.Release()
		t.Fatalf("admission to capped rig succeeded, want per-rig cap denial (snapshot %+v)", snapshot)
	}
	var admissionErr *polecatCapacityAdmissionError
	if !errors.As(err, &admissionErr) {
		t.Fatalf("error = %v, want *polecatCapacityAdmissionError", err)
	}
	if !strings.Contains(err.Error(), "per-rig cap") {
		t.Fatalf("denial reason = %q, want per-rig cap message", err.Error())
	}

	// A different, uncapped rig still has town headroom and must be admitted —
	// proving the denial is the PER-RIG gate, not the town cap.
	otherHandle, _, err := acquirePolecatAdmission(townRoot, "roomy", "gt-other", "test")
	if err != nil {
		t.Fatalf("admission to uncapped roomy rig failed: %v (town cap=10 has room)", err)
	}
	otherHandle.Release()
}

func TestApplyAgentFieldsToCapacitySnapshotSeparatesPendingMR(t *testing.T) {
	tests := []struct {
		name   string
		fields *beads.AgentFields
		want   polecatCapacitySnapshot
	}{
		{
			name:   "active mr is pending capacity",
			fields: &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: "clean", ActiveMR: "gt-mr-open"},
			want:   polecatCapacitySnapshot{PendingMR: 1},
		},
		{
			name:   "push failed remains recovery blocked",
			fields: &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: "clean", ActiveMR: "gt-mr-open", PushFailed: true},
			want:   polecatCapacitySnapshot{RecoveryBlocked: 1},
		},
		{
			name:   "clean idle is reusable",
			fields: &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: "clean"},
			want:   polecatCapacitySnapshot{ReusableIdle: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := polecatCapacitySnapshot{}
			applyAgentFieldsToCapacitySnapshot(&snapshot, "gastown", "synth", tt.fields, nil, false)
			if snapshot.Working != tt.want.Working || snapshot.RecoveryBlocked != tt.want.RecoveryBlocked || snapshot.ReusableIdle != tt.want.ReusableIdle || snapshot.PendingMR != tt.want.PendingMR {
				t.Fatalf("snapshot = %+v, want %+v", snapshot, tt.want)
			}
		})
	}
}

// TestApplyAgentFieldsToCapacitySnapshot_WorkBeadIsCanonical is the gu-c94lq
// regression guard. The working classification must read the canonical
// work-bead signal (hasHookedWorkBead) — a status=hooked bead assigned to the
// polecat — NOT the deprecated agent-bead hook_bead field. hq-l6mm5 made the
// work bead authoritative and turned hook_bead writes into a no-op, so a
// polecat that picks up work by REUSING an idle slot is hooked via the work
// bead but has an EMPTY hook_bead. Reading hook_bead under-counted those
// polecats; reading the work-bead signal counts them correctly. Conversely a
// stale non-empty hook_bead on a polecat with NO hooked work bead (the
// live-evidence closed-bead case) must NOT be counted.
func TestApplyAgentFieldsToCapacitySnapshot_WorkBeadIsCanonical(t *testing.T) {
	rig := "gastown"
	name := "reuse"
	// Live session so a "working" classification lands in Working (not
	// RecoveryBlocked).
	live := map[string]bool{
		session.PolecatSessionName(session.PrefixFor(rig), name): true,
	}

	// THE BUG: reuse-hooked polecat. Work bead status=hooked + assignee set, but
	// hook_bead is EMPTY (reuse path never writes it). Old code read hook_bead=""
	// and skipped it; the work-bead signal must count it as Working.
	reuseFields := &beads.AgentFields{AgentState: string(beads.AgentStateIdle), HookBead: ""}
	reuse := polecatCapacitySnapshot{}
	applyAgentFieldsToCapacitySnapshot(&reuse, rig, name, reuseFields, live, true)
	if reuse.Working != 1 {
		t.Fatalf("reuse-hooked polecat (work bead hooked, hook_bead empty) must count as Working, got %+v", reuse)
	}

	// Spawn-hooked polecat: both the work bead AND a stale hook_bead are set. Must
	// STILL be Working — no regression for the fresh-spawn path.
	spawnFields := &beads.AgentFields{AgentState: "working", HookBead: "gu-work"}
	spawn := polecatCapacitySnapshot{}
	applyAgentFieldsToCapacitySnapshot(&spawn, rig, name, spawnFields, live, true)
	if spawn.Working != 1 {
		t.Fatalf("spawn-hooked polecat (both signals set) must count as Working, got %+v", spawn)
	}

	// THE LIVE-EVIDENCE CASE: closed work bead (no hooked bead → hasHookedWorkBead
	// false) but a STALE non-empty hook_bead lingers on the agent bead. The old
	// code counted this as Working off hook_bead; the work-bead signal must NOT.
	// With a clean cleanup status it falls through to ReusableIdle.
	staleFields := &beads.AgentFields{AgentState: string(beads.AgentStateIdle), HookBead: "gu-closed", CleanupStatus: "clean"}
	stale := polecatCapacitySnapshot{}
	applyAgentFieldsToCapacitySnapshot(&stale, rig, name, staleFields, live, false)
	if stale.Working != 0 {
		t.Fatalf("stale hook_bead with no hooked work bead must NOT count as Working, got %+v", stale)
	}
	if stale.ReusableIdle != 1 {
		t.Fatalf("stale hook_bead + clean cleanup + no hooked work bead must be ReusableIdle, got %+v", stale)
	}
}

// TestApplyAgentFieldsToCapacitySnapshot_NilFieldsDeadSessionIsReusable pins
// down the gu-o086 alignment: a polecat directory with NO agent bead and a
// dead tmux session is a fresh/orphan warm-pool slot, not a recovery
// candidate. Reclaim's isReclaimCandidate (internal/daemon/daemon.go) treats
// this exact case as "idle reusable warm-pool slot — preserve". The capacity
// classifier must agree, otherwise the recovery_blocked counter inflates and
// dispatch starves while reclaim refuses to churn the slot.
//
// Production observation pre-fix: 34/50 polecat directories without agent
// beads inflated recovery_blocked by 27 (the rest were correctly counted via
// other branches), making `gt scheduler status` report capacity exhaustion
// while `gt polecat list --all` correctly classified the same polecats as
// SAFE_TO_NUKE / counts_toward_capacity=False.
func TestApplyAgentFieldsToCapacitySnapshot_NilFieldsDeadSessionIsReusable(t *testing.T) {
	snapshot := polecatCapacitySnapshot{}
	// fields=nil, tmuxClient=nil => running=false => orphan warm-pool slot.
	applyAgentFieldsToCapacitySnapshot(&snapshot, "gastown", "orphan", nil, nil, false)
	if snapshot.ReusableIdle != 1 {
		t.Fatalf("expected ReusableIdle=1 for nil-fields dead-session warm-pool slot (gu-o086), snapshot=%+v", snapshot)
	}
	if snapshot.RecoveryBlocked != 0 {
		t.Fatalf("nil-fields dead-session must not be counted as RecoveryBlocked (gu-o086), snapshot=%+v", snapshot)
	}
	if snapshot.Working != 0 {
		t.Fatalf("nil-fields dead-session must not be counted as Working, snapshot=%+v", snapshot)
	}
}

// TestApplyAgentFieldsToCapacitySnapshot_LiveSessionSetLookup is the regression
// guard for gu-el5bx: the capacity snapshot used to shell `tmux has-session`
// once per polecat (~51 serial tmux subprocesses ≈ 40s at high session count).
// The fix enumerates sessions ONCE into a membership set and the per-polecat
// liveness check is now an in-memory lookup. This pins the set-driven behavior:
// a polecat whose session name is in the set counts as running (Working when it
// has no bead), and one absent from the set is treated as dead (ReusableIdle).
func TestApplyAgentFieldsToCapacitySnapshot_LiveSessionSetLookup(t *testing.T) {
	rig := "gastown"
	aliveName := "alive"
	deadName := "dead"
	live := map[string]bool{
		session.PolecatSessionName(session.PrefixFor(rig), aliveName): true,
	}

	// In-set + nil fields => live session, missing bead => Working (gu-o086).
	alive := polecatCapacitySnapshot{}
	applyAgentFieldsToCapacitySnapshot(&alive, rig, aliveName, nil, live, false)
	if alive.Working != 1 || alive.ReusableIdle != 0 {
		t.Fatalf("in-set polecat must count as Working, got %+v", alive)
	}

	// Not-in-set + nil fields => dead session, missing bead => ReusableIdle.
	dead := polecatCapacitySnapshot{}
	applyAgentFieldsToCapacitySnapshot(&dead, rig, deadName, nil, live, false)
	if dead.ReusableIdle != 1 || dead.Working != 0 {
		t.Fatalf("not-in-set polecat must count as ReusableIdle, got %+v", dead)
	}
}

// TestPerRigOccupancyCountsDeadSessionHookedPolecat is the gu-ccycc regression
// guard. A polecat that holds a hook but whose tmux session has died must
// occupy a per-rig slot exactly as it occupies a town-wide slot. The town cap
// classifies such a polecat as RecoveryBlocked (occupied); the per-rig cap must
// agree. Before the fix the per-rig counter enumerated LIVE tmux sessions first
// and filtered by hook, so a dead-session-hooked polecat was invisible to the
// per-rig cap while still consuming a town slot — the dispatch-wedge in
// gu-ccycc (per-rig says "free", town says "full").
//
// This exercises the shared classifier applyAgentFieldsToCapacitySnapshot,
// which is the single source both the town snapshot and the per-rig sub-
// snapshot fold through (so they cannot drift). The end-to-end town-snapshot
// path is covered by the integration suite (requires Dolt).
func TestPerRigOccupancyCountsDeadSessionHookedPolecat(t *testing.T) {
	rig := "gastown"
	// Live session set is empty => every polecat's session is "dead".
	deadSessions := map[string]bool{}

	// Hooked + dead session: the exact gu-ccycc case. "Hooked" is now the
	// canonical work-bead signal (hasHookedWorkBead=true), NOT the deprecated
	// agent-bead hook_bead field (gu-c94lq).
	hookedDead := &beads.AgentFields{AgentState: "working"}

	// The per-rig sub-snapshot (built the same way polecatCapacitySnapshotFor-
	// TownNoCleanup builds it) must count this polecat as occupied.
	rigSnapshot := polecatCapacitySnapshot{}
	applyAgentFieldsToCapacitySnapshot(&rigSnapshot, rig, "stalled", hookedDead, deadSessions, true)
	if rigSnapshot.RecoveryBlocked != 1 {
		t.Fatalf("hooked dead-session polecat must classify RecoveryBlocked, got %+v", rigSnapshot)
	}
	if rigSnapshot.occupied() != 1 {
		t.Fatalf("hooked dead-session polecat must occupy a per-rig slot, occupied=%d (snapshot %+v)",
			rigSnapshot.occupied(), rigSnapshot)
	}

	// And the town snapshot, folding the SAME polecat through the SAME
	// classifier, must reach the identical occupied count — the two gates agree.
	townSnapshot := polecatCapacitySnapshot{}
	applyAgentFieldsToCapacitySnapshot(&townSnapshot, rig, "stalled", hookedDead, deadSessions, true)
	if townSnapshot.occupied() != rigSnapshot.occupied() {
		t.Fatalf("per-rig occupied=%d disagrees with town occupied=%d for the same polecat",
			rigSnapshot.occupied(), townSnapshot.occupied())
	}
}

// TestCapacityFanoutConcurrency covers the semaphore bound for the per-rig
// agent-bead fan-out (gu-el5bx). Defaults to 4; GT_CAPACITY_FANOUT overrides
// with a positive int; junk/zero/negative fall back to the default so a
// fat-fingered env can never serialize (1 is allowed) or, worse, set an
// unbounded/invalid width.
func TestCapacityFanoutConcurrency(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want int
	}{
		{"default when unset", "", 4},
		{"override 6", "6", 6},
		{"override 1 (serial allowed)", "1", 1},
		{"zero falls back", "0", 4},
		{"negative falls back", "-3", 4},
		{"junk falls back", "lots", 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env == "" {
				os.Unsetenv("GT_CAPACITY_FANOUT")
			} else {
				t.Setenv("GT_CAPACITY_FANOUT", tt.env)
			}
			if got := capacityFanoutConcurrency(); got != tt.want {
				t.Errorf("capacityFanoutConcurrency() with env=%q = %d, want %d", tt.env, got, tt.want)
			}
		})
	}
}

// TestLiveSessionSet_NilClient guards the no-tmux-server / nil-client path:
// liveSessionSet must return a usable empty set (not nil-panic), so every
// per-polecat lookup reports "not running" — matching the prior behavior when
// HasSession errored.
func TestLiveSessionSet_NilClient(t *testing.T) {
	set := liveSessionSet(nil)
	if set == nil {
		t.Fatal("liveSessionSet(nil) must return a non-nil empty set")
	}
	if set["anything"] {
		t.Fatalf("empty set must report no membership, got true")
	}
}

func TestPrintDryRunPlanUsesCapacitySnapshot(t *testing.T) {
	out := captureStdout(t, func() {
		printDryRunPlan(capacity.DispatchPlan{
			ToDispatch: []capacity.PendingBead{{ID: "ctx-1", WorkBeadID: "gt-one", TargetRig: "gastown"}},
			Skipped:    2,
			Reason:     "capacity",
		}, polecatCapacitySnapshot{
			Max:             2,
			Working:         1,
			RecoveryBlocked: 1,
			Reservations:    0,
			ReusableIdle:    3,
			PendingMR:       2,
			Free:            0,
		}, 5)
	})
	for _, want := range []string{"0 free of 2", "working: 1", "recovery_blocked: 1", "reusable_idle: 3", "pending_mr: 2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output %q missing %q", out, want)
		}
	}
}

func TestResolveTargetRigPassesHeldAdmissionToSpawn(t *testing.T) {
	townRoot := setupPolecatCapacityRig(t, 1)
	oldSpawn := spawnPolecatForSling
	t.Cleanup(func() { spawnPolecatForSling = oldSpawn })
	called := false
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		called = true
		if rigName != "gastown" {
			t.Fatalf("rigName = %q, want gastown", rigName)
		}
		if !opts.SkipAdmission {
			t.Fatal("spawn should skip admission when caller already holds reservation")
		}
		if opts.TownRoot != townRoot {
			t.Fatalf("TownRoot = %q, want %q", opts.TownRoot, townRoot)
		}
		return &SpawnedPolecatInfo{
			RigName:     "gastown",
			PolecatName: "toast",
			ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "toast", "gastown"),
			SessionName: "gt-gastown-polecat-toast",
		}, nil
	}

	resolved, err := resolveTarget("gastown", ResolveTargetOptions{
		TownRoot:             townRoot,
		SkipPolecatAdmission: true,
		NoBoot:               true,
	})
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if !called {
		t.Fatal("spawnPolecatForSling was not called")
	}
	if resolved.Agent != "gastown/polecats/toast" {
		t.Fatalf("resolved agent = %q, want gastown/polecats/toast", resolved.Agent)
	}
}

func TestStandaloneFormulaRigTargetAcquiresSingleAdmission(t *testing.T) {
	townRoot := setupPolecatCapacityRig(t, 1)
	oldAcquire := acquirePolecatAdmissionFn
	oldSpawn := spawnPolecatForSling
	oldFind := findHookedFormulaSingletonFn
	oldDryRun, oldNoBoot := slingDryRun, slingNoBoot
	t.Cleanup(func() {
		acquirePolecatAdmissionFn = oldAcquire
		spawnPolecatForSling = oldSpawn
		findHookedFormulaSingletonFn = oldFind
		slingDryRun, slingNoBoot = oldDryRun, oldNoBoot
	})
	slingDryRun = false
	slingNoBoot = true
	admissions := 0
	acquirePolecatAdmissionFn = func(townRootArg, rigName, beadID, operation string) (*polecatAdmissionHandle, polecatCapacitySnapshot, error) {
		admissions++
		if townRootArg != townRoot || rigName != "gastown" || beadID != "test-formula" || operation != "formula" {
			t.Fatalf("admission args = (%q,%q,%q,%q)", townRootArg, rigName, beadID, operation)
		}
		return &polecatAdmissionHandle{disabled: true}, polecatCapacitySnapshot{Max: 1, Free: 0}, nil
	}
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		if !opts.SkipAdmission {
			t.Fatal("formula rig spawn should use caller-held admission")
		}
		return &SpawnedPolecatInfo{
			RigName:     "gastown",
			PolecatName: "toast",
			ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "toast", "gastown"),
			SessionName: "gt-gastown-polecat-toast",
		}, nil
	}
	findHookedFormulaSingletonFn = func(workDir, targetAgent, formulaName string) (*beads.Issue, error) {
		return &beads.Issue{ID: "gt-wisp-existing"}, nil
	}

	if err := runSlingFormula(context.Background(), []string{"test-formula", "gastown"}); err != nil {
		t.Fatalf("runSlingFormula: %v", err)
	}
	if admissions != 1 {
		t.Fatalf("admissions = %d, want 1", admissions)
	}
}

func TestStandaloneFormulaExistingPolecatNoopDoesNotRequireCapacity(t *testing.T) {
	townRoot := setupPolecatCapacityRig(t, 1)
	oldAcquire := acquirePolecatAdmissionFn
	oldResolve := resolveTargetAgentFn
	oldFind := findHookedFormulaSingletonFn
	oldDryRun := slingDryRun
	t.Cleanup(func() {
		acquirePolecatAdmissionFn = oldAcquire
		resolveTargetAgentFn = oldResolve
		findHookedFormulaSingletonFn = oldFind
		slingDryRun = oldDryRun
	})
	slingDryRun = false
	acquirePolecatAdmissionFn = func(townRootArg, rigName, beadID, operation string) (*polecatAdmissionHandle, polecatCapacitySnapshot, error) {
		t.Fatalf("no-op existing formula should not acquire capacity, got (%q,%q,%q,%q)", townRootArg, rigName, beadID, operation)
		return nil, polecatCapacitySnapshot{}, nil
	}
	resolveTargetAgentFn = func(target string) (string, string, string, error) {
		if target != "gastown/polecats/toast" {
			t.Fatalf("target = %q, want gastown/polecats/toast", target)
		}
		return "gastown/polecats/toast", "%1", filepath.Join(townRoot, "gastown", "polecats", "toast", "gastown"), nil
	}
	findHookedFormulaSingletonFn = func(workDir, targetAgent, formulaName string) (*beads.Issue, error) {
		return &beads.Issue{ID: "gt-wisp-existing"}, nil
	}

	if err := runSlingFormula(context.Background(), []string{"test-formula", "gastown/polecats/toast"}); err != nil {
		t.Fatalf("runSlingFormula: %v", err)
	}
}
