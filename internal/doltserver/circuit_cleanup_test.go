package doltserver

import (
	"os"
	"path/filepath"
	"testing"
)

// writeCircuitFile creates a fake breaker state file and returns its path.
func writeCircuitFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	// Minimal closed-state payload matching beads' circuitState shape.
	const closed = `{"state":"closed","failures":0}`
	if err := os.WriteFile(path, []byte(closed), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

func TestCleanStaleCircuitBreakerFiles_RemovesTestPollution(t *testing.T) {
	dir := t.TempDir()

	// Test-pollution / legacy files — should be removed.
	pollution := []string{
		"beads-dolt-circuit-127-0-0-1-3307-testdb_abc123.json",
		"beads-dolt-circuit-127-0-0-1-3307-beads_t0001.json",
		"beads-dolt-circuit-127-0-0-1-3307-beads_pt99.json",
		"beads-dolt-circuit-127-0-0-1-3307-beads_vr42.json",
		"beads-dolt-circuit-127-0-0-1-3307-doctest_x.json",
		"beads-dolt-circuit-127-0-0-1-3307-doctortest_y.json",
		"beads-dolt-circuit-0.json",
	}
	for _, name := range pollution {
		writeCircuitFile(t, dir, name)
	}

	// Live-DB files — must be preserved.
	live := []string{
		"beads-dolt-circuit-127-0-0-1-3307-beads_global.json",
		"beads-dolt-circuit-127-0-0-1-3307-hq.json",
		"beads-dolt-circuit-127-0-0-1-3307-gastown_upstream.json",
	}
	for _, name := range live {
		writeCircuitFile(t, dir, name)
	}

	result, err := cleanStaleCircuitBreakerFilesIn(dir)
	if err != nil {
		t.Fatalf("cleanStaleCircuitBreakerFilesIn: %v", err)
	}

	if result.Removed != len(pollution) {
		t.Errorf("Removed = %d, want %d", result.Removed, len(pollution))
	}
	if result.Remaining != len(live) {
		t.Errorf("Remaining = %d, want %d", result.Remaining, len(live))
	}
	if result.BytesFreed <= 0 {
		t.Errorf("BytesFreed = %d, want > 0", result.BytesFreed)
	}

	// Pollution files gone.
	for _, name := range pollution {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("expected %s removed, stat err = %v", name, err)
		}
	}
	// Live files preserved.
	for _, name := range live {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s preserved, stat err = %v", name, err)
		}
	}
}

func TestCleanStaleCircuitBreakerFiles_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	result, err := cleanStaleCircuitBreakerFilesIn(dir)
	if err != nil {
		t.Fatalf("cleanStaleCircuitBreakerFilesIn: %v", err)
	}
	if result.Removed != 0 || result.Remaining != 0 {
		t.Errorf("got Removed=%d Remaining=%d, want 0/0", result.Removed, result.Remaining)
	}
}

func TestCleanStaleCircuitBreakerFiles_MissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	result, err := cleanStaleCircuitBreakerFilesIn(dir)
	if err != nil {
		t.Fatalf("expected nil error for missing dir, got %v", err)
	}
	if result.Removed != 0 {
		t.Errorf("Removed = %d, want 0", result.Removed)
	}
}

func TestCleanStaleCircuitBreakerFiles_IgnoresNonBreakerFiles(t *testing.T) {
	dir := t.TempDir()
	// A non-matching file must be left untouched even if it contains a
	// pollution-looking substring.
	other := filepath.Join(dir, "testdb_unrelated.json")
	if err := os.WriteFile(other, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeCircuitFile(t, dir, "beads-dolt-circuit-127-0-0-1-3307-testdb_z.json")

	result, err := cleanStaleCircuitBreakerFilesIn(dir)
	if err != nil {
		t.Fatalf("cleanStaleCircuitBreakerFilesIn: %v", err)
	}
	if result.Removed != 1 {
		t.Errorf("Removed = %d, want 1", result.Removed)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("non-breaker file should be preserved, stat err = %v", err)
	}
}

func TestIsTestPollutionCircuitFile(t *testing.T) {
	cases := []struct {
		base string
		want bool
	}{
		{"beads-dolt-circuit-127-0-0-1-3307-testdb_a.json", true},
		{"beads-dolt-circuit-127-0-0-1-3307-beads_t1.json", true},
		{"beads-dolt-circuit-0.json", true},
		{"beads-dolt-circuit-127-0-0-1-3307-beads_global.json", false},
		{"beads-dolt-circuit-127-0-0-1-3307-hq.json", false},
		{"beads-dolt-circuit-127-0-0-1-3307-gastown_upstream.json", false},
	}
	for _, c := range cases {
		if got := isTestPollutionCircuitFile(c.base); got != c.want {
			t.Errorf("isTestPollutionCircuitFile(%q) = %v, want %v", c.base, got, c.want)
		}
	}
}
