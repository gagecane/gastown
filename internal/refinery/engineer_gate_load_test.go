package refinery

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/rig"
)

// newGateLoadEngineer builds a minimal Engineer suitable for exercising the
// load-aware gate deferral logic in isolation.
func newGateLoadEngineer(t *testing.T) *Engineer {
	t.Helper()
	r := &rig.Rig{Name: "test-rig", Path: t.TempDir()}
	e := NewEngineer(r)
	e.workDir = t.TempDir()
	e.output = io.Discard
	return e
}

func TestWaitForGateLoad_DisabledIsNoOp(t *testing.T) {
	e := newGateLoadEngineer(t)
	e.config.GateMaxLoadPerCore = 0 // disabled
	var samples int32
	e.loadSampler = func() float64 {
		atomic.AddInt32(&samples, 1)
		return 100.0 // would be "busy" if anyone checked
	}

	start := time.Now()
	e.waitForGateLoad(context.Background(), GatePhasePreMerge)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("disabled deferral should return immediately, took %v", elapsed)
	}
	if got := atomic.LoadInt32(&samples); got != 0 {
		t.Fatalf("disabled deferral must not sample load, sampled %d times", got)
	}
}

func TestWaitForGateLoad_QuietHostProceedsImmediately(t *testing.T) {
	e := newGateLoadEngineer(t)
	e.config.GateMaxLoadPerCore = 2.0
	e.loadSampler = func() float64 { return 0.5 } // below threshold

	start := time.Now()
	e.waitForGateLoad(context.Background(), GatePhasePreMerge)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("quiet host should proceed immediately, took %v", elapsed)
	}
}

func TestWaitForGateLoad_UnknownMetricProceedsImmediately(t *testing.T) {
	e := newGateLoadEngineer(t)
	e.config.GateMaxLoadPerCore = 2.0
	// Load source unavailable (e.g. Windows) reports 0; we must never block
	// on an unknown metric.
	e.loadSampler = func() float64 { return 0 }

	start := time.Now()
	e.waitForGateLoad(context.Background(), GatePhasePreMerge)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("unknown metric should proceed immediately, took %v", elapsed)
	}
}

func TestWaitForGateLoad_BusyThenQuietProceeds(t *testing.T) {
	e := newGateLoadEngineer(t)
	e.config.GateMaxLoadPerCore = 2.0
	e.config.GateLoadWaitTimeout = 30 * time.Second // generous bound; we expect to quiet first
	e.gateLoadRecheck = 10 * time.Millisecond       // fast recheck to keep the test quick

	// First sample is busy (entry check), then the ticker re-samples and finds
	// the host quiet.
	var calls int32
	e.loadSampler = func() float64 {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return 10.0 // entry check: busy
		}
		return 0.5 // subsequent re-samples: quiet
	}

	// Drive with a short recheck by shrinking the wait via a fast deadline is
	// not what we want here; instead verify it does eventually return and that
	// it re-sampled at least twice. Use a bounded timeout so a regression that
	// never quiets can't hang the suite forever.
	done := make(chan struct{})
	go func() {
		e.waitForGateLoad(context.Background(), GatePhasePreMerge)
		close(done)
	}()

	select {
	case <-done:
		if got := atomic.LoadInt32(&calls); got < 2 {
			t.Fatalf("expected at least 2 load samples (entry + recheck), got %d", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitForGateLoad did not return after host quieted")
	}
}

func TestWaitForGateLoad_TimeoutProceedsAnyway(t *testing.T) {
	e := newGateLoadEngineer(t)
	e.config.GateMaxLoadPerCore = 2.0
	e.config.GateLoadWaitTimeout = 50 * time.Millisecond // tiny bound
	e.loadSampler = func() float64 { return 100.0 }      // permanently busy

	start := time.Now()
	e.waitForGateLoad(context.Background(), GatePhasePreMerge)
	elapsed := time.Since(start)
	if elapsed < 40*time.Millisecond {
		t.Fatalf("should have waited ~timeout before proceeding, only waited %v", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("a permanently busy host must not wedge the queue; waited %v", elapsed)
	}
}

func TestWaitForGateLoad_ContextCancelReturnsPromptly(t *testing.T) {
	e := newGateLoadEngineer(t)
	e.config.GateMaxLoadPerCore = 2.0
	e.config.GateLoadWaitTimeout = time.Hour // long bound; cancellation must win
	e.loadSampler = func() float64 { return 100.0 }

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	e.waitForGateLoad(ctx, GatePhasePreMerge)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("context cancel should unblock the wait promptly, took %v", elapsed)
	}
}

func TestEngineer_LoadConfig_GateLoad(t *testing.T) {
	tmpDir := t.TempDir()
	config := map[string]interface{}{
		"merge_queue": map[string]interface{}{
			"gate_max_load_per_core": 1.5,
			"gate_load_wait_timeout": "3m",
		},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngineer(&rig.Rig{Name: "test-rig", Path: tmpDir})
	if err := e.LoadConfig(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.config.GateMaxLoadPerCore != 1.5 {
		t.Errorf("expected gate_max_load_per_core 1.5, got %v", e.config.GateMaxLoadPerCore)
	}
	if e.config.GateLoadWaitTimeout != 3*time.Minute {
		t.Errorf("expected gate_load_wait_timeout 3m, got %v", e.config.GateLoadWaitTimeout)
	}
}

func TestEngineer_LoadConfig_GateLoadDefaultsDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	config := map[string]interface{}{
		"merge_queue": map[string]interface{}{"enabled": true},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngineer(&rig.Rig{Name: "test-rig", Path: tmpDir})
	if err := e.LoadConfig(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.config.GateMaxLoadPerCore != 0 {
		t.Errorf("expected load-aware deferral disabled by default (0), got %v", e.config.GateMaxLoadPerCore)
	}
}

func TestEngineer_LoadConfig_GateLoadInvalid(t *testing.T) {
	tests := []struct {
		name string
		mq   map[string]interface{}
	}{
		{"negative threshold", map[string]interface{}{"gate_max_load_per_core": -1.0}},
		{"bad timeout", map[string]interface{}{"gate_load_wait_timeout": "not-a-duration"}},
		{"non-positive timeout", map[string]interface{}{"gate_load_wait_timeout": "0s"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			config := map[string]interface{}{"merge_queue": tt.mq}
			data, _ := json.MarshalIndent(config, "", "  ")
			if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
				t.Fatal(err)
			}
			e := NewEngineer(&rig.Rig{Name: "test-rig", Path: tmpDir})
			if err := e.LoadConfig(); err == nil {
				t.Errorf("expected error for %s", tt.name)
			}
		})
	}
}

func TestGateLoadWaitTimeout_DefaultsWhenUnset(t *testing.T) {
	e := newGateLoadEngineer(t)
	e.config.GateLoadWaitTimeout = 0
	if got := e.gateLoadWaitTimeout(); got != DefaultGateLoadWaitTimeout {
		t.Fatalf("expected default %v, got %v", DefaultGateLoadWaitTimeout, got)
	}
	e.config.GateLoadWaitTimeout = 90 * time.Second
	if got := e.gateLoadWaitTimeout(); got != 90*time.Second {
		t.Fatalf("expected configured 90s, got %v", got)
	}
}

// TestRunGatesForPhase_RunsLoadDeferral verifies the deferral hook is wired
// into the gate runner: a busy host with a tiny timeout still proceeds and the
// gate executes.
func TestRunGatesForPhase_RunsLoadDeferral(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell commands")
	}
	e := newGateLoadEngineer(t)
	e.config.GateMaxLoadPerCore = 2.0
	e.config.GateLoadWaitTimeout = 50 * time.Millisecond
	var sampled int32
	e.loadSampler = func() float64 {
		atomic.AddInt32(&sampled, 1)
		return 100.0 // busy, will hit the timeout
	}
	e.config.Gates = map[string]*GateConfig{
		"pre-test": {Cmd: "true", Phase: GatePhasePreMerge},
	}

	result := e.runGatesForPhase(context.Background(), GatePhasePreMerge)
	if !result.Success {
		t.Fatalf("gate should pass after load wait, got: %s", result.Error)
	}
	if atomic.LoadInt32(&sampled) == 0 {
		t.Fatal("expected runGatesForPhase to invoke load deferral")
	}
}
