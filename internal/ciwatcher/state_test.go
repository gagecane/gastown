package ciwatcher

import (
	"strconv"
	"testing"
	"time"
)

func TestSeenRunsRoundtrip(t *testing.T) {
	town := t.TempDir()

	s, err := LoadSeenRuns(town, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if s.Has("123") {
		t.Errorf("empty ledger reports Has=true")
	}
	if s.Len() != 0 {
		t.Errorf("empty ledger Len=%d", s.Len())
	}

	now := time.Date(2026, 5, 29, 6, 0, 0, 0, time.UTC)
	s.Mark("123", now)
	s.Mark("456", now.Add(time.Minute))
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	s2, err := LoadSeenRuns(town, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !s2.Has("123") || !s2.Has("456") {
		t.Errorf("entries did not survive round-trip: %v", s2.cache)
	}
	if s2.Len() != 2 {
		t.Errorf("Len after reload = %d, want 2", s2.Len())
	}
}

func TestSeenRunsTrim(t *testing.T) {
	town := t.TempDir()
	s, err := LoadSeenRuns(town, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 5, 29, 6, 0, 0, 0, time.UTC)
	// Insert SeenRunsCap+50 entries.
	total := SeenRunsCap + 50
	for i := 0; i < total; i++ {
		s.Mark("run-"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Second))
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s2, err := LoadSeenRuns(town, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if s2.Len() != SeenRunsCap {
		t.Errorf("trim: Len = %d, want %d", s2.Len(), SeenRunsCap)
	}
	// Most recent entry must be retained.
	if !s2.Has("run-" + strconv.Itoa(total-1)) {
		t.Errorf("most recent entry was trimmed")
	}
	// Oldest entry must be dropped.
	if s2.Has("run-0") {
		t.Errorf("oldest entry survived trim")
	}
}

func TestSeenRunsLoadMissing(t *testing.T) {
	town := t.TempDir()
	s, err := LoadSeenRuns(town, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 0 {
		t.Errorf("missing file should yield empty ledger, got Len=%d", s.Len())
	}
}

func TestLoadSeenRunsRequiresRig(t *testing.T) {
	town := t.TempDir()
	if _, err := LoadSeenRuns(town, ""); err == nil {
		t.Errorf("expected error with empty rig")
	}
}

func TestSeenRunsNilSafe(t *testing.T) {
	var s *SeenRuns
	if s.Has("anything") {
		t.Errorf("nil receiver Has should be false")
	}
	s.Mark("anything", time.Now())
	if err := s.Save(); err != nil {
		t.Errorf("nil receiver Save: %v", err)
	}
}
