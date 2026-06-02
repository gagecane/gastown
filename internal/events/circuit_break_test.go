package events

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLogAndReadCircuitBreaks_RoundTrip(t *testing.T) {
	root := t.TempDir()

	recs := []CircuitBreakRecord{
		{WorkBeadID: "gu-aaa", ContextID: "ctx-1", TargetRig: "rigA", LastFailure: "bead-not-found"},
		{WorkBeadID: "gu-aaa", ContextID: "ctx-2", TargetRig: "rigA", LastFailure: "container/epic"},
		{WorkBeadID: "gu-bbb", ContextID: "ctx-3"},
	}
	for _, r := range recs {
		if err := LogCircuitBreak(root, r); err != nil {
			t.Fatalf("LogCircuitBreak: %v", err)
		}
	}

	got, err := ReadCircuitBreaks(root, time.Hour)
	if err != nil {
		t.Fatalf("ReadCircuitBreaks: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 records, got %d", len(got))
	}
	// Timestamp should be auto-populated.
	for _, r := range got {
		if r.Timestamp == "" {
			t.Errorf("record missing auto-populated timestamp: %+v", r)
		}
	}
}

func TestReadCircuitBreaks_NoFile(t *testing.T) {
	got, err := ReadCircuitBreaks(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing file, got %+v", got)
	}
}

func TestReadCircuitBreaks_EmptyTownRoot(t *testing.T) {
	got, err := ReadCircuitBreaks("", time.Hour)
	if err != nil || got != nil {
		t.Errorf("empty townRoot should return (nil,nil); got (%+v,%v)", got, err)
	}
	if err := LogCircuitBreak("", CircuitBreakRecord{WorkBeadID: "x"}); err != nil {
		t.Errorf("empty townRoot LogCircuitBreak should be a no-op; got %v", err)
	}
}

func TestReadCircuitBreaks_PrunesOldRecords(t *testing.T) {
	root := t.TempDir()
	path := circuitBreakLogPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}

	old := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	recent := time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339)

	// Write directly with explicit timestamps.
	for _, r := range []CircuitBreakRecord{
		{Timestamp: old, WorkBeadID: "gu-old", ContextID: "ctx-old"},
		{Timestamp: recent, WorkBeadID: "gu-new", ContextID: "ctx-new"},
	} {
		if err := LogCircuitBreak(root, r); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ReadCircuitBreaks(root, time.Hour)
	if err != nil {
		t.Fatalf("ReadCircuitBreaks: %v", err)
	}
	if len(got) != 1 || got[0].WorkBeadID != "gu-new" {
		t.Fatalf("expected only the recent record, got %+v", got)
	}

	// The prune should have rewritten the file: a second read still sees 1.
	got2, err := ReadCircuitBreaks(root, time.Hour)
	if err != nil {
		t.Fatalf("ReadCircuitBreaks (2nd): %v", err)
	}
	if len(got2) != 1 {
		t.Errorf("expected pruned file to retain 1 record, got %d", len(got2))
	}
}

func TestReadCircuitBreaks_RetentionZeroDisablesPrune(t *testing.T) {
	root := t.TempDir()
	old := time.Now().UTC().Add(-99 * time.Hour).Format(time.RFC3339)
	if err := LogCircuitBreak(root, CircuitBreakRecord{Timestamp: old, WorkBeadID: "gu-old"}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCircuitBreaks(root, 0)
	if err != nil {
		t.Fatalf("ReadCircuitBreaks: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("retention<=0 should return all records without pruning, got %d", len(got))
	}
}

func TestReadCircuitBreaks_SkipsCorruptLines(t *testing.T) {
	root := t.TempDir()
	path := circuitBreakLogPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	content := `{"ts":"` + time.Now().UTC().Format(time.RFC3339) + `","work_bead_id":"gu-ok","context_id":"c1"}
this is not json
{"ts":"` + time.Now().UTC().Format(time.RFC3339) + `","work_bead_id":"gu-ok2","context_id":"c2"}
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCircuitBreaks(root, time.Hour)
	if err != nil {
		t.Fatalf("ReadCircuitBreaks: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 valid records (corrupt line skipped), got %d", len(got))
	}
}
