package daemon

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// writeDaemonConfig writes a settings/config.json under townRoot with the given
// operational.daemon JSON body so loadOperationalConfig picks up the knobs.
func writeDaemonConfig(t *testing.T, townRoot, daemonJSON string) {
	t.Helper()
	dir := filepath.Join(townRoot, "settings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}
	body := `{"operational":{"daemon":` + daemonJSON + `}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

func newGateDaemon(t *testing.T, daemonJSON string) *Daemon {
	t.Helper()
	townRoot := t.TempDir()
	writeDaemonConfig(t, townRoot, daemonJSON)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(io.Discard, "", 0),
		ctx:    ctx,
	}
}

// The per-heartbeat cap admits at most N new spawns; the rest defer. Memory
// budget disabled so the test isolates the cap. Stagger disabled for speed.
func TestAdmitSpawn_PerHeartbeatCap(t *testing.T) {
	d := newGateDaemon(t, `{"spawn_max_per_heartbeat":3,"spawn_stagger":"0s","pressure_mem_budget_fraction":0}`)

	admitted := 0
	for i := 0; i < 10; i++ {
		if d.admitSpawn("witness", "rig") {
			admitted++
		}
	}
	if admitted != 3 {
		t.Fatalf("admitted = %d, want 3 (per-heartbeat cap)", admitted)
	}

	// After reset, the budget refreshes for the next heartbeat.
	d.resetSpawnGate()
	if !d.admitSpawn("witness", "rig") {
		t.Fatal("expected admission after resetSpawnGate, got deferral")
	}
}

// A zero cap disables the per-heartbeat limit entirely.
func TestAdmitSpawn_CapDisabled(t *testing.T) {
	d := newGateDaemon(t, `{"spawn_max_per_heartbeat":0,"spawn_stagger":"0s","pressure_mem_budget_fraction":0}`)
	for i := 0; i < 20; i++ {
		if !d.admitSpawn("refinery", "rig") {
			t.Fatalf("admission %d deferred with cap disabled", i)
		}
	}
}

// Stagger spaces successive admitted spawns apart in wall-clock time.
func TestAdmitSpawn_Staggers(t *testing.T) {
	d := newGateDaemon(t, `{"spawn_max_per_heartbeat":0,"spawn_stagger":"40ms","pressure_mem_budget_fraction":0}`)

	start := time.Now()
	// First admission's slot is "now" (no wait). Three more each add 40ms.
	for i := 0; i < 4; i++ {
		if !d.admitSpawn("witness", "rig") {
			t.Fatalf("admission %d unexpectedly deferred", i)
		}
	}
	elapsed := time.Since(start)
	// 4 spawns => slots at 0, 40, 80, 120ms => total wait ~120ms.
	if elapsed < 110*time.Millisecond {
		t.Fatalf("elapsed = %v, want >= ~120ms from staggering", elapsed)
	}
}

// resetSpawnGate clears the per-heartbeat counter.
func TestResetSpawnGate(t *testing.T) {
	d := newGateDaemon(t, `{"spawn_max_per_heartbeat":1,"spawn_stagger":"0s","pressure_mem_budget_fraction":0}`)
	if !d.admitSpawn("witness", "rig") {
		t.Fatal("first admission should succeed")
	}
	if d.admitSpawn("witness", "rig") {
		t.Fatal("second admission should defer (cap=1)")
	}
	d.resetSpawnGate()
	if !d.admitSpawn("witness", "rig") {
		t.Fatal("admission after reset should succeed")
	}
}

// The cap holds under concurrent admission, mirroring the rig worker pool
// (10 goroutines) fanning out witness/refinery starts on one heartbeat.
func TestAdmitSpawn_ConcurrentCap(t *testing.T) {
	d := newGateDaemon(t, `{"spawn_max_per_heartbeat":4,"spawn_stagger":"0s","pressure_mem_budget_fraction":0}`)

	var admitted int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if d.admitSpawn("witness", "rig") {
				atomic.AddInt64(&admitted, 1)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&admitted); got != 4 {
		t.Fatalf("concurrent admitted = %d, want 4 (cap holds under contention)", got)
	}
}

// A memory budget requiring nearly all RAM free forces a deferral; a negligible
// budget admits. This exercises the default-on OOM safety net in checkPressure.
func TestAdmitSpawn_MemoryBudget(t *testing.T) {
	if totalMemoryGB() <= 0 || availableMemoryGB() <= 0 {
		t.Skip("memory metrics unavailable on this platform")
	}

	// Require 99.9% of total RAM free — effectively always defers.
	deny := newGateDaemon(t, `{"spawn_max_per_heartbeat":0,"spawn_stagger":"0s","pressure_mem_budget_fraction":0.999}`)
	if deny.admitSpawn("witness", "rig") {
		t.Fatal("expected deferral under an unsatisfiable memory budget")
	}

	// Require 0.1% free — effectively always admits.
	allow := newGateDaemon(t, `{"spawn_max_per_heartbeat":0,"spawn_stagger":"0s","pressure_mem_budget_fraction":0.001}`)
	if !allow.admitSpawn("witness", "rig") {
		t.Fatal("expected admission under a trivially-satisfiable memory budget")
	}
}
