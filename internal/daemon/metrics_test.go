package daemon

import (
	"context"
	"testing"
)

func TestNewDaemonMetrics(t *testing.T) {
	dm, err := newDaemonMetrics()
	if err != nil {
		t.Fatalf("newDaemonMetrics() error: %v", err)
	}
	if dm == nil {
		t.Fatal("expected non-nil *daemonMetrics")
	}
}

func TestDaemonMetrics_NilReceiver(t *testing.T) {
	var dm *daemonMetrics
	ctx := context.Background()

	// All methods must be nil-safe — no panic expected.
	dm.recordHeartbeat(ctx)
	dm.recordRestart(ctx, "deacon")
	dm.updateDoltHealth(5, 100, 2.5, 1024, true)
	dm.updateHookedBeads(map[string]int64{"hq": 1}, map[string]int64{"hq": 0})
}

func TestDaemonMetrics_RecordHeartbeat(t *testing.T) {
	dm, err := newDaemonMetrics()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	dm.recordHeartbeat(ctx)
	dm.recordHeartbeat(ctx)
}

func TestDaemonMetrics_RecordRestart(t *testing.T) {
	dm, err := newDaemonMetrics()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	for _, agentType := range []string{"deacon", "witness", "refinery", "polecat"} {
		dm.recordRestart(ctx, agentType)
	}
}

func TestDaemonMetrics_UpdateDoltHealth_Healthy(t *testing.T) {
	dm, err := newDaemonMetrics()
	if err != nil {
		t.Fatal(err)
	}

	dm.updateDoltHealth(5, 100, 2.5, 1048576, true)

	dm.doltMu.RLock()
	defer dm.doltMu.RUnlock()

	if dm.doltConnections != 5 {
		t.Errorf("doltConnections = %d, want 5", dm.doltConnections)
	}
	if dm.doltMaxConnections != 100 {
		t.Errorf("doltMaxConnections = %d, want 100", dm.doltMaxConnections)
	}
	if dm.doltLatencyMs != 2.5 {
		t.Errorf("doltLatencyMs = %f, want 2.5", dm.doltLatencyMs)
	}
	if dm.doltDiskBytes != 1048576 {
		t.Errorf("doltDiskBytes = %d, want 1048576", dm.doltDiskBytes)
	}
	if dm.doltHealthy != 1 {
		t.Errorf("doltHealthy = %d, want 1", dm.doltHealthy)
	}
}

func TestDaemonMetrics_UpdateDoltHealth_Unhealthy(t *testing.T) {
	dm, err := newDaemonMetrics()
	if err != nil {
		t.Fatal(err)
	}

	dm.updateDoltHealth(0, 0, 0, 0, false)

	dm.doltMu.RLock()
	defer dm.doltMu.RUnlock()

	if dm.doltHealthy != 0 {
		t.Errorf("doltHealthy = %d, want 0 (unhealthy)", dm.doltHealthy)
	}
}

func TestDaemonMetrics_UpdateDoltHealth_Idempotent(t *testing.T) {
	dm, err := newDaemonMetrics()
	if err != nil {
		t.Fatal(err)
	}

	dm.updateDoltHealth(10, 200, 5.0, 2048, true)
	dm.updateDoltHealth(3, 200, 1.0, 2048, false)

	dm.doltMu.RLock()
	defer dm.doltMu.RUnlock()

	if dm.doltConnections != 3 {
		t.Errorf("doltConnections = %d, want 3 (last write wins)", dm.doltConnections)
	}
	if dm.doltHealthy != 0 {
		t.Errorf("doltHealthy = %d, want 0 (unhealthy from last write)", dm.doltHealthy)
	}
}

func TestDaemonMetrics_UpdateHookedBeads_NilReceiver(t *testing.T) {
	var dm *daemonMetrics
	// Must be nil-safe per the struct contract.
	dm.updateHookedBeads(map[string]int64{"hq": 5}, map[string]int64{"hq": 1})
}

func TestDaemonMetrics_UpdateHookedBeads_Snapshot(t *testing.T) {
	dm, err := newDaemonMetrics()
	if err != nil {
		t.Fatal(err)
	}

	dm.updateHookedBeads(
		map[string]int64{"hq": 10, "rig-a": 2},
		map[string]int64{"hq": 3, "rig-a": 0},
	)

	dm.hookedMu.RLock()
	defer dm.hookedMu.RUnlock()

	if dm.hookedByDB["hq"] != 10 {
		t.Errorf("hookedByDB[hq] = %d, want 10", dm.hookedByDB["hq"])
	}
	if dm.hookedByDB["rig-a"] != 2 {
		t.Errorf("hookedByDB[rig-a] = %d, want 2", dm.hookedByDB["rig-a"])
	}
	if dm.hookedDeadByDB["hq"] != 3 {
		t.Errorf("hookedDeadByDB[hq] = %d, want 3", dm.hookedDeadByDB["hq"])
	}
	if dm.hookedDeadByDB["rig-a"] != 0 {
		t.Errorf("hookedDeadByDB[rig-a] = %d, want 0", dm.hookedDeadByDB["rig-a"])
	}
}

func TestDaemonMetrics_UpdateHookedBeads_ReplacesSnapshot(t *testing.T) {
	// Second update must fully replace the first so a rig that disappears
	// between scans stops emitting stale series (see gu-hhqk AC#5 design note).
	dm, err := newDaemonMetrics()
	if err != nil {
		t.Fatal(err)
	}

	dm.updateHookedBeads(
		map[string]int64{"hq": 10, "rig-a": 2},
		map[string]int64{"hq": 3, "rig-a": 1},
	)
	dm.updateHookedBeads(
		map[string]int64{"hq": 4}, // rig-a vanished
		map[string]int64{"hq": 0},
	)

	dm.hookedMu.RLock()
	defer dm.hookedMu.RUnlock()

	if _, ok := dm.hookedByDB["rig-a"]; ok {
		t.Error("rig-a should be absent from total snapshot after replacement")
	}
	if _, ok := dm.hookedDeadByDB["rig-a"]; ok {
		t.Error("rig-a should be absent from dead-letter snapshot after replacement")
	}
	if dm.hookedByDB["hq"] != 4 {
		t.Errorf("hookedByDB[hq] = %d, want 4", dm.hookedByDB["hq"])
	}
	if dm.hookedDeadByDB["hq"] != 0 {
		t.Errorf("hookedDeadByDB[hq] = %d, want 0", dm.hookedDeadByDB["hq"])
	}
}

func TestDaemonMetrics_UpdateHookedBeads_DefensiveCopy(t *testing.T) {
	// Caller's map must not be aliased — mutations after update must not
	// affect the stored snapshot.
	dm, err := newDaemonMetrics()
	if err != nil {
		t.Fatal(err)
	}

	totals := map[string]int64{"hq": 10}
	deadLetter := map[string]int64{"hq": 3}

	dm.updateHookedBeads(totals, deadLetter)

	// Mutate the caller's maps after the snapshot call.
	totals["hq"] = 999
	deadLetter["hq"] = 888
	totals["injected"] = 1

	dm.hookedMu.RLock()
	defer dm.hookedMu.RUnlock()

	if dm.hookedByDB["hq"] != 10 {
		t.Errorf("hookedByDB[hq] = %d, want 10 (defensive copy violated)", dm.hookedByDB["hq"])
	}
	if _, ok := dm.hookedByDB["injected"]; ok {
		t.Error("injected key leaked into snapshot (defensive copy violated)")
	}
	if dm.hookedDeadByDB["hq"] != 3 {
		t.Errorf("hookedDeadByDB[hq] = %d, want 3 (defensive copy violated)", dm.hookedDeadByDB["hq"])
	}
}

func TestDaemonMetrics_UpdateHookedBeads_NilMaps(t *testing.T) {
	// Passing nil maps is equivalent to zeroing out — confirms we accept
	// both nil (scanner with no Dolt server) and empty (no rigs found).
	dm, err := newDaemonMetrics()
	if err != nil {
		t.Fatal(err)
	}

	dm.updateHookedBeads(map[string]int64{"hq": 5}, map[string]int64{"hq": 1})
	dm.updateHookedBeads(nil, nil)

	dm.hookedMu.RLock()
	defer dm.hookedMu.RUnlock()

	if len(dm.hookedByDB) != 0 {
		t.Errorf("hookedByDB should be empty after nil update, got %d entries", len(dm.hookedByDB))
	}
	if len(dm.hookedDeadByDB) != 0 {
		t.Errorf("hookedDeadByDB should be empty after nil update, got %d entries", len(dm.hookedDeadByDB))
	}
}
