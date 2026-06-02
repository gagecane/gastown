package channelevents

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeEventWithAge creates a .event file in the given channel dir and sets its
// modification time to now-age.
func writeEventWithAge(t *testing.T, townRoot, channel, name string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(townRoot, "events", channel)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(`{"type":"TEST"}`), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	mod := time.Now().Add(-age)
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
	return path
}

func TestGCOlderThan_PrunesStaleKeepsFresh(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	stale := writeEventWithAge(t, townRoot, "witness", "1-1-1.event", 10*24*time.Hour)
	fresh := writeEventWithAge(t, townRoot, "witness", "2-2-2.event", 1*time.Hour)
	staleMayor := writeEventWithAge(t, townRoot, "mayor", "3-3-3.event", 8*24*time.Hour)

	result, err := GCOlderThan(townRoot, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("GCOlderThan: %v", err)
	}

	if result.Channels != 2 {
		t.Errorf("Channels = %d, want 2", result.Channels)
	}
	if result.Removed != 2 {
		t.Errorf("Removed = %d, want 2", result.Removed)
	}
	if result.Errors != 0 {
		t.Errorf("Errors = %d, want 0", result.Errors)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale event should be removed")
	}
	if _, err := os.Stat(staleMayor); !os.IsNotExist(err) {
		t.Errorf("stale mayor event should be removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh event should be retained: %v", err)
	}
}

func TestGCOlderThan_MissingRoot(t *testing.T) {
	t.Parallel()
	result, err := GCOlderThan(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatalf("expected nil error for missing events root, got %v", err)
	}
	if result.Removed != 0 || result.Channels != 0 {
		t.Errorf("expected zero counts, got %+v", result)
	}
}

func TestGCOlderThan_IgnoresNonEventFiles(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	// A stale non-.event file must not be touched.
	dir := filepath.Join(townRoot, "events", "witness")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("keep me"), 0644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(other, old, old); err != nil {
		t.Fatal(err)
	}

	result, err := GCOlderThan(townRoot, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("GCOlderThan: %v", err)
	}
	if result.Removed != 0 {
		t.Errorf("Removed = %d, want 0 (non-.event file must be ignored)", result.Removed)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("non-.event file should be retained: %v", err)
	}
}

func TestGCOlderThan_EmptyChannelDir(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "events", "merge-queue"), 0755); err != nil {
		t.Fatal(err)
	}

	result, err := GCOlderThan(townRoot, time.Hour)
	if err != nil {
		t.Fatalf("GCOlderThan: %v", err)
	}
	if result.Channels != 1 || result.Removed != 0 || result.Errors != 0 {
		t.Errorf("unexpected result for empty channel dir: %+v", result)
	}
}
