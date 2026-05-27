package testpathmap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixedClock returns a clock function pinned to t.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// newTestStore returns a Store rooted in a fresh temp town. The
// returned cleanup is just t.TempDir's automatic cleanup.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	town := t.TempDir()
	s, err := New(town)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestNew_RejectsEmptyTownRoot(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("New(\"\") should return an error")
	}
	if _, err := New("   "); err == nil {
		t.Fatal("New(whitespace) should return an error")
	}
}

func TestPath_PointsAtTownRoot(t *testing.T) {
	town := "/tmp/example"
	s, err := New(town)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := filepath.Join(town, ".gt", "test-path-rig-map.json")
	if got := s.Path(); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestLookup_EmptyStoreReturnsMiss(t *testing.T) {
	s := newTestStore(t)
	rig, ok, err := s.Lookup("internal/foo/foo_test.go")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if ok || rig != "" {
		t.Errorf("Lookup on empty store = (%q, %v), want (\"\", false)", rig, ok)
	}
}

func TestRecord_ThenLookup_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	if err := s.Record("internal/foo/foo_test.go", "casc_crud"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	rig, ok, err := s.Lookup("internal/foo/foo_test.go")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !ok {
		t.Fatal("Lookup should hit after Record")
	}
	if rig != "casc_crud" {
		t.Errorf("Lookup target = %q, want casc_crud", rig)
	}
}

func TestRecord_SameRigIncrementsCount(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 3; i++ {
		if err := s.Record("a/b_test.go", "rigX"); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	entry, ok := snap["a/b_test.go"]
	if !ok {
		t.Fatal("expected entry to be present")
	}
	if entry.TargetRig != "rigX" {
		t.Errorf("TargetRig = %q, want rigX", entry.TargetRig)
	}
	if entry.CorrectionCount != 3 {
		t.Errorf("CorrectionCount = %d, want 3", entry.CorrectionCount)
	}
}

func TestRecord_DifferentRigOverwritesAndResetsCount(t *testing.T) {
	s := newTestStore(t)
	if err := s.Record("a/b_test.go", "rigA"); err != nil {
		t.Fatalf("Record A: %v", err)
	}
	if err := s.Record("a/b_test.go", "rigA"); err != nil {
		t.Fatalf("Record A again: %v", err)
	}
	if err := s.Record("a/b_test.go", "rigB"); err != nil {
		t.Fatalf("Record B: %v", err)
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	entry := snap["a/b_test.go"]
	if entry.TargetRig != "rigB" {
		t.Errorf("TargetRig = %q, want rigB (most recent wins)", entry.TargetRig)
	}
	if entry.CorrectionCount != 1 {
		t.Errorf("CorrectionCount after rig change = %d, want 1 (reset)", entry.CorrectionCount)
	}
}

func TestRecord_DropsEmptyInputs(t *testing.T) {
	s := newTestStore(t)
	if err := s.Record("", "rigA"); err != nil {
		t.Errorf("Record(\"\", _) returned error: %v", err)
	}
	if err := s.Record("path", ""); err != nil {
		t.Errorf("Record(_, \"\") returned error: %v", err)
	}
	if err := s.Record("   ", "  "); err != nil {
		t.Errorf("Record on whitespace returned error: %v", err)
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 0 {
		t.Errorf("Snapshot has %d entries, want 0 (empty inputs dropped)", len(snap))
	}
}

func TestLookup_ExpiredEntryReturnsMiss(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t).WithTTL(7 * 24 * time.Hour).withClock(fixedClock(now))

	if err := s.Record("a/b_test.go", "rigA"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Advance past TTL; lookup should miss.
	future := s.withClock(fixedClock(now.Add(8 * 24 * time.Hour)))
	rig, ok, err := future.Lookup("a/b_test.go")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if ok || rig != "" {
		t.Errorf("expired Lookup = (%q, %v), want miss", rig, ok)
	}
}

func TestLookup_FreshEntryWithinTTLHits(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t).WithTTL(7 * 24 * time.Hour).withClock(fixedClock(now))

	if err := s.Record("a/b_test.go", "rigA"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Advance within TTL; lookup should still hit.
	future := s.withClock(fixedClock(now.Add(6 * 24 * time.Hour)))
	rig, ok, err := future.Lookup("a/b_test.go")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !ok || rig != "rigA" {
		t.Errorf("fresh Lookup = (%q, %v), want (rigA, true)", rig, ok)
	}
}

func TestCompact_RemovesExpiredEntries(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t).WithTTL(7 * 24 * time.Hour).withClock(fixedClock(now))

	if err := s.Record("old/a_test.go", "rigA"); err != nil {
		t.Fatalf("Record old: %v", err)
	}

	// Move forward and add a fresh entry.
	future := s.withClock(fixedClock(now.Add(10 * 24 * time.Hour)))
	if err := future.Record("new/b_test.go", "rigB"); err != nil {
		t.Fatalf("Record new: %v", err)
	}

	// Record() compacts on every write — the old entry should be gone.
	snap, err := future.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if _, present := snap["old/a_test.go"]; present {
		t.Error("expected old/a_test.go to be compacted out")
	}
	if _, present := snap["new/b_test.go"]; !present {
		t.Error("expected new/b_test.go to be present")
	}
}

func TestCompact_ExplicitCallReturnsCount(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t).WithTTL(7 * 24 * time.Hour).withClock(fixedClock(now))

	if err := s.Record("old/a_test.go", "rigA"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := s.Record("old/c_test.go", "rigC"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Compact at TTL boundary should be a no-op.
	noOp := s.withClock(fixedClock(now.Add(time.Hour)))
	if removed, err := noOp.Compact(); err != nil || removed != 0 {
		t.Errorf("Compact at fresh = (%d, %v), want (0, nil)", removed, err)
	}

	// Compact past TTL should remove both.
	future := s.withClock(fixedClock(now.Add(30 * 24 * time.Hour)))
	removed, err := future.Compact()
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if removed != 2 {
		t.Errorf("Compact removed = %d, want 2", removed)
	}

	snap, err := future.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 0 {
		t.Errorf("Snapshot has %d entries after compact, want 0", len(snap))
	}
}

func TestRead_TolerantOfMissingFile(t *testing.T) {
	s := newTestStore(t)
	// Without ever writing, Snapshot/Lookup should not error.
	if _, err := s.Snapshot(); err != nil {
		t.Errorf("Snapshot on never-written store: %v", err)
	}
	if _, _, err := s.Lookup("a"); err != nil {
		t.Errorf("Lookup on never-written store: %v", err)
	}
}

func TestRead_TolerantOfMalformedFile(t *testing.T) {
	s := newTestStore(t)
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(s.path, []byte("not json"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Lookup tolerates malformed file.
	if _, _, err := s.Lookup("a"); err != nil {
		t.Errorf("Lookup on malformed file: %v", err)
	}

	// Record overwrites cleanly.
	if err := s.Record("a", "rigA"); err != nil {
		t.Fatalf("Record after malformed: %v", err)
	}
	rig, ok, err := s.Lookup("a")
	if err != nil {
		t.Fatalf("Lookup after recovery: %v", err)
	}
	if !ok || rig != "rigA" {
		t.Errorf("Lookup after recovery = (%q, %v), want (rigA, true)", rig, ok)
	}
}

func TestRead_TolerantOfUnknownVersion(t *testing.T) {
	s := newTestStore(t)
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Pretend a future version of the schema wrote this file.
	future := struct {
		Version int            `json:"version"`
		Entries map[string]any `json:"entries"`
	}{
		Version: 999,
		Entries: map[string]any{"a": map[string]any{"unknown_field": 42}},
	}
	data, err := json.Marshal(future)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(s.path, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// We must not parse the foreign data — Lookup misses cleanly.
	if rig, ok, err := s.Lookup("a"); err != nil || ok || rig != "" {
		t.Errorf("Lookup on future-version file = (%q, %v, %v), want miss", rig, ok, err)
	}

	// Record then succeeds and writes our schema.
	if err := s.Record("b", "rigB"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var d document
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("Unmarshal after Record: %v", err)
	}
	if d.Version != SchemaVersion {
		t.Errorf("on-disk Version after Record = %d, want %d", d.Version, SchemaVersion)
	}
	if _, ok := d.Entries["b"]; !ok {
		t.Errorf("on-disk Entries missing recorded key, got %v", d.Entries)
	}
}

func TestSnapshot_FiltersExpired(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t).WithTTL(7 * 24 * time.Hour).withClock(fixedClock(now))

	if err := s.Record("fresh", "rigF"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Manually inject an old entry by reading, mutating, and writing
	// through the same code path the production store uses.
	doc, err := s.readLocked()
	if err != nil {
		t.Fatalf("readLocked: %v", err)
	}
	doc.Entries["stale"] = Entry{
		TargetRig:       "rigStale",
		LastCorrectedAt: now.Add(-30 * 24 * time.Hour),
		CorrectionCount: 1,
	}
	if err := s.writeLocked(doc); err != nil {
		t.Fatalf("writeLocked: %v", err)
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if _, ok := snap["stale"]; ok {
		t.Error("Snapshot should filter out stale entry")
	}
	if _, ok := snap["fresh"]; !ok {
		t.Error("Snapshot should include fresh entry")
	}
}

func TestRecord_AcrossInstances(t *testing.T) {
	town := t.TempDir()
	s1, err := New(town)
	if err != nil {
		t.Fatalf("New s1: %v", err)
	}
	s2, err := New(town)
	if err != nil {
		t.Fatalf("New s2: %v", err)
	}

	if err := s1.Record("path", "rigA"); err != nil {
		t.Fatalf("s1.Record: %v", err)
	}
	rig, ok, err := s2.Lookup("path")
	if err != nil {
		t.Fatalf("s2.Lookup: %v", err)
	}
	if !ok || rig != "rigA" {
		t.Errorf("s2 sees s1's write = (%q, %v), want (rigA, true)", rig, ok)
	}
}

func TestRecord_DefaultTTLMatchesConst(t *testing.T) {
	s := newTestStore(t)
	if s.ttl != DefaultTTL {
		t.Errorf("default ttl = %v, want %v", s.ttl, DefaultTTL)
	}
}

func TestWithTTL_DoesNotMutateOriginal(t *testing.T) {
	s := newTestStore(t)
	original := s.ttl
	custom := s.WithTTL(time.Hour)
	if s.ttl != original {
		t.Errorf("WithTTL mutated original ttl: got %v, want %v", s.ttl, original)
	}
	if custom.ttl != time.Hour {
		t.Errorf("WithTTL custom ttl = %v, want %v", custom.ttl, time.Hour)
	}
}
