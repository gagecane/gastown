package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestShouldReapCircuitBreakerFile(t *testing.T) {
	now := time.Now()
	retention := 15 * time.Minute

	closed := []byte(`{"state":"closed","failures":0}`)
	open := []byte(`{"state":"open","failures":5,"tripped_at":"2026-06-04T00:00:00Z"}`)
	halfOpen := []byte(`{"state":"half-open","failures":3}`)
	empty := []byte(`{"failures":0}`)

	tests := []struct {
		name     string
		data     []byte
		ageMin   int // file mtime age in minutes
		wantReap bool
	}{
		{"closed + stale → reap", closed, 30, true},
		{"closed + fresh → keep", closed, 5, false},
		{"closed exactly at TTL → keep", closed, 15, false},
		{"closed just past TTL → reap", closed, 16, true},
		{"open + stale → keep (live signal)", open, 60, false},
		{"open + fresh → keep", open, 1, false},
		{"half-open + stale → keep (live signal)", halfOpen, 60, false},
		{"empty/unknown state + stale → keep (conservative)", empty, 60, false},
		{"corrupt json + stale → reap (beads regenerates)", []byte("not json"), 60, true},
		{"corrupt json + fresh → reap", []byte("{bad"), 1, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mtime := now.Add(-time.Duration(tc.ageMin) * time.Minute)
			got := shouldReapCircuitBreakerFile(tc.data, mtime, now, retention)
			if got != tc.wantReap {
				t.Errorf("shouldReapCircuitBreakerFile(%s, age=%dm) = %v, want %v",
					tc.name, tc.ageMin, got, tc.wantReap)
			}
		})
	}
}

func TestGCCircuitBreakerFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	retention := 15 * time.Minute

	// Helper: write a breaker file and set its mtime.
	write := func(name, content string, ageMin int) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(-time.Duration(ageMin) * time.Minute)
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatal(err)
		}
		return path
	}

	staleClosed := write("beads-dolt-circuit-127-0-0-1-3307-testdb_orphan.json", `{"state":"closed"}`, 30)
	freshClosed := write("beads-dolt-circuit-127-0-0-1-3307-hq.json", `{"state":"closed"}`, 2)
	staleOpen := write("beads-dolt-circuit-127-0-0-1-3307-tripped.json", `{"state":"open","tripped_at":"2026-06-04T00:00:00Z"}`, 30)
	corrupt := write("beads-dolt-circuit-127-0-0-1-3307-corrupt.json", `{bad json`, 30)

	// A non-matching file must be ignored entirely.
	other := filepath.Join(dir, "unrelated.json")
	if err := os.WriteFile(other, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}

	res := gcCircuitBreakerFiles(dir, now, retention)

	if res.Removed != 2 {
		t.Errorf("Removed = %d, want 2 (stale closed + corrupt)", res.Removed)
	}
	// Scanned counts only glob matches (4), not the unrelated file.
	if res.Scanned != 4 {
		t.Errorf("Scanned = %d, want 4", res.Scanned)
	}

	// Stale closed and corrupt must be gone.
	if _, err := os.Stat(staleClosed); !os.IsNotExist(err) {
		t.Error("stale closed file should have been removed")
	}
	if _, err := os.Stat(corrupt); !os.IsNotExist(err) {
		t.Error("corrupt file should have been removed")
	}
	// Fresh closed, stale open, and the unrelated file must survive.
	if _, err := os.Stat(freshClosed); err != nil {
		t.Error("fresh closed file must be preserved")
	}
	if _, err := os.Stat(staleOpen); err != nil {
		t.Error("stale OPEN file must be preserved (live signal)")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("non-matching file must be untouched")
	}
}

func TestGCCircuitBreakerFiles_MissingDirIsNoOp(t *testing.T) {
	res := gcCircuitBreakerFiles(filepath.Join(t.TempDir(), "does-not-exist"), time.Now(), time.Minute)
	if res.Scanned != 0 || res.Removed != 0 || res.Errors != 0 {
		t.Errorf("missing dir should be a no-op, got %+v", res)
	}
}

func TestCircuitBreakerGCInterval(t *testing.T) {
	if got := circuitBreakerGCInterval(nil); got != defaultCircuitBreakerGCInterval {
		t.Errorf("default = %v, want %v", got, defaultCircuitBreakerGCInterval)
	}

	config := &DaemonPatrolConfig{Patrols: &PatrolsConfig{
		CircuitBreakerGC: &CircuitBreakerGCConfig{Enabled: true, IntervalStr: "10m"},
	}}
	if got := circuitBreakerGCInterval(config); got != 10*time.Minute {
		t.Errorf("configured = %v, want 10m", got)
	}

	config.Patrols.CircuitBreakerGC.IntervalStr = "nonsense"
	if got := circuitBreakerGCInterval(config); got != defaultCircuitBreakerGCInterval {
		t.Errorf("invalid should fall back to default, got %v", got)
	}
}

func TestCircuitBreakerGCRetention(t *testing.T) {
	if got := circuitBreakerGCRetention(nil); got != defaultCircuitBreakerGCRetention {
		t.Errorf("default = %v, want %v", got, defaultCircuitBreakerGCRetention)
	}

	config := &DaemonPatrolConfig{Patrols: &PatrolsConfig{
		CircuitBreakerGC: &CircuitBreakerGCConfig{Enabled: true, RetentionStr: "1h"},
	}}
	if got := circuitBreakerGCRetention(config); got != time.Hour {
		t.Errorf("configured = %v, want 1h", got)
	}

	config.Patrols.CircuitBreakerGC.RetentionStr = "bad"
	if got := circuitBreakerGCRetention(config); got != defaultCircuitBreakerGCRetention {
		t.Errorf("invalid should fall back to default, got %v", got)
	}
}

// TestCircuitBreakerGCDefaultEnabled proves a town whose daemon.json predates
// this patrol still gets it: IsPatrolEnabled returns true when the stanza is
// absent, and respects an explicit disable.
func TestCircuitBreakerGCDefaultEnabled(t *testing.T) {
	if !IsPatrolEnabled(nil, "circuit_breaker_gc") {
		t.Error("should default to enabled when config is nil")
	}
	if !IsPatrolEnabled(&DaemonPatrolConfig{Patrols: &PatrolsConfig{}}, "circuit_breaker_gc") {
		t.Error("should default to enabled when stanza is missing")
	}
	disabled := &DaemonPatrolConfig{Patrols: &PatrolsConfig{
		CircuitBreakerGC: &CircuitBreakerGCConfig{Enabled: false},
	}}
	if IsPatrolEnabled(disabled, "circuit_breaker_gc") {
		t.Error("should respect explicit disable")
	}
}

// TestEnsureLifecycleDefaultsAddsCircuitBreakerGC verifies the patrol is
// populated into a config created before circuit_breaker_gc existed.
func TestEnsureLifecycleDefaultsAddsCircuitBreakerGC(t *testing.T) {
	config := &DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Patrols: &PatrolsConfig{},
	}
	if !EnsureLifecycleDefaults(config) {
		t.Fatal("expected changed=true when adding new patrol defaults")
	}
	if config.Patrols.CircuitBreakerGC == nil {
		t.Fatal("CircuitBreakerGC should be populated after EnsureLifecycleDefaults")
	}
	if !config.Patrols.CircuitBreakerGC.Enabled {
		t.Error("CircuitBreakerGC should be enabled by default")
	}
	if config.Patrols.CircuitBreakerGC.IntervalStr != "5m" {
		t.Errorf("default interval = %q, want 5m", config.Patrols.CircuitBreakerGC.IntervalStr)
	}
	if config.Patrols.CircuitBreakerGC.RetentionStr != "15m" {
		t.Errorf("default retention = %q, want 15m", config.Patrols.CircuitBreakerGC.RetentionStr)
	}
}
