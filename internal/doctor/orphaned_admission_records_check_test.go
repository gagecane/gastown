package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withStubbedAdmissionLiveness replaces the PID liveness probe with a set of
// live PIDs and restores the original after the test. Any PID not in the set
// is treated as dead.
func withStubbedAdmissionLiveness(t *testing.T, livePIDs ...int) {
	t.Helper()
	live := make(map[int]bool, len(livePIDs))
	for _, p := range livePIDs {
		live[p] = true
	}
	orig := admissionRecordPIDAlive
	admissionRecordPIDAlive = func(pid int) bool { return live[pid] }
	t.Cleanup(func() { admissionRecordPIDAlive = orig })
}

// writeAdmissionRecord writes a reservation JSON file into the admission dir.
func writeAdmissionRecord(t *testing.T, townRoot, id string, pid int, bead string, createdAt time.Time) string {
	t.Helper()
	dir := filepath.Join(townRoot, ".runtime", "polecat-admission")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir admission dir: %v", err)
	}
	rec := admissionReservationFile{ID: id, PID: pid, Bead: bead, CreatedAt: createdAt}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	path := filepath.Join(dir, id+".json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write record: %v", err)
	}
	return path
}

func TestOrphanedAdmissionRecordsCheck_NoDir(t *testing.T) {
	tmpDir := t.TempDir()
	check := NewOrphanedAdmissionRecordsCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir})
	if result.Status != StatusOK {
		t.Errorf("Status = %v, want OK; msg=%q", result.Status, result.Message)
	}
}

func TestOrphanedAdmissionRecordsCheck_AllAlive(t *testing.T) {
	tmpDir := t.TempDir()
	withStubbedAdmissionLiveness(t, 1001, 1002)
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	withStubbedNow(t, now)
	writeAdmissionRecord(t, tmpDir, "1001-1", 1001, "gu-aaa", now.Add(-time.Minute))
	writeAdmissionRecord(t, tmpDir, "1002-2", 1002, "gu-bbb", now.Add(-time.Minute))

	check := NewOrphanedAdmissionRecordsCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir})
	if result.Status != StatusOK {
		t.Errorf("Status = %v, want OK; msg=%q", result.Status, result.Message)
	}
}

func TestOrphanedAdmissionRecordsCheck_DeadPID_Errors(t *testing.T) {
	tmpDir := t.TempDir()
	withStubbedAdmissionLiveness(t, 1001) // 9999 is dead
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	withStubbedNow(t, now)
	writeAdmissionRecord(t, tmpDir, "1001-1", 1001, "gu-aaa", now.Add(-time.Minute))
	writeAdmissionRecord(t, tmpDir, "9999-2", 9999, "gu-dead", now.Add(-30*time.Minute))

	check := NewOrphanedAdmissionRecordsCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir})

	if result.Status != StatusError {
		t.Fatalf("Status = %v, want Error; msg=%q", result.Status, result.Message)
	}
	if len(check.orphans) != 1 {
		t.Fatalf("orphans = %d, want 1", len(check.orphans))
	}
	joined := strings.Join(result.Details, "\n")
	if !strings.Contains(joined, "9999") || !strings.Contains(joined, "gu-dead") {
		t.Errorf("details should name the dead PID and bead, got: %s", joined)
	}
	if !strings.Contains(joined, "30m0s") {
		t.Errorf("details should include the record age, got: %s", joined)
	}
	if result.FixHint == "" {
		t.Error("error result should include a fix hint")
	}
}

func TestOrphanedAdmissionRecordsCheck_Fix_ReapsDeadOnly(t *testing.T) {
	tmpDir := t.TempDir()
	withStubbedAdmissionLiveness(t, 1001)
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	withStubbedNow(t, now)
	alivePath := writeAdmissionRecord(t, tmpDir, "1001-1", 1001, "gu-aaa", now.Add(-time.Minute))
	deadPath := writeAdmissionRecord(t, tmpDir, "9999-2", 9999, "gu-dead", now.Add(-time.Minute))

	check := NewOrphanedAdmissionRecordsCheck()
	if result := check.Run(&CheckContext{TownRoot: tmpDir}); result.Status != StatusError {
		t.Fatalf("pre-fix Status = %v, want Error", result.Status)
	}
	if err := check.Fix(&CheckContext{TownRoot: tmpDir}); err != nil {
		t.Fatalf("Fix: %v", err)
	}
	if _, err := os.Stat(deadPath); !os.IsNotExist(err) {
		t.Errorf("dead record should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(alivePath); err != nil {
		t.Errorf("alive record must NOT be removed, stat err=%v", err)
	}

	// Re-run should now be clean.
	if result := check.Run(&CheckContext{TownRoot: tmpDir}); result.Status != StatusOK {
		t.Errorf("post-fix Status = %v, want OK; msg=%q", result.Status, result.Message)
	}
}

func TestOrphanedAdmissionRecordsCheck_Fix_RechecksLiveness(t *testing.T) {
	tmpDir := t.TempDir()
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	withStubbedNow(t, now)
	path := writeAdmissionRecord(t, tmpDir, "9999-1", 9999, "gu-dead", now.Add(-time.Minute))

	check := NewOrphanedAdmissionRecordsCheck()
	// Run sees the PID as dead → flagged as orphan.
	withStubbedAdmissionLiveness(t) // nothing alive
	if result := check.Run(&CheckContext{TownRoot: tmpDir}); result.Status != StatusError {
		t.Fatalf("pre-fix Status = %v, want Error", result.Status)
	}
	// Between Run and Fix the PID becomes alive (e.g., PID reuse) — Fix must
	// NOT reap it.
	withStubbedAdmissionLiveness(t, 9999)
	if err := check.Fix(&CheckContext{TownRoot: tmpDir}); err != nil {
		t.Fatalf("Fix: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("record whose PID came alive must NOT be removed, stat err=%v", err)
	}
}

func TestOrphanedAdmissionRecordsCheck_IgnoresMalformedAndNonJSON(t *testing.T) {
	tmpDir := t.TempDir()
	withStubbedAdmissionLiveness(t) // all dead
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	withStubbedNow(t, now)
	dir := filepath.Join(tmpDir, ".runtime", "polecat-admission")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	// Malformed JSON .json file — should be skipped, not reported.
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	// Non-.json file — should be skipped.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0644); err != nil {
		t.Fatal(err)
	}
	// Record with PID 0 — invalid, not an orphan.
	writeAdmissionRecord(t, tmpDir, "0-0", 0, "gu-zero", now)

	check := NewOrphanedAdmissionRecordsCheck()
	result := check.Run(&CheckContext{TownRoot: tmpDir})
	if result.Status != StatusOK {
		t.Errorf("Status = %v, want OK; msg=%q", result.Status, result.Message)
	}
}

func TestOrphanedAdmissionRecordsCheck_Properties(t *testing.T) {
	check := NewOrphanedAdmissionRecordsCheck()
	if check.Name() != "orphaned-admission-records" {
		t.Errorf("Name() = %q", check.Name())
	}
	if check.Category() != CategoryCleanup {
		t.Errorf("Category() = %q, want %q", check.Category(), CategoryCleanup)
	}
	if !check.CanFix() {
		t.Error("CanFix() should be true — orphaned records can be reaped")
	}
}
