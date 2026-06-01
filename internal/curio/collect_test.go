package curio

import (
	"os"
	"path/filepath"
	"testing"
)

func writeReservation(t *testing.T, dir, id string, pid int, rig string) {
	t.Helper()
	content := `{"id":"` + id + `","pid":` + itoa(pid) + `,"rig":"` + rig + `"}`
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func TestCollectAdmissions_MissingDirIsEmpty(t *testing.T) {
	recs, err := CollectAdmissions(t.TempDir())
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected 0 records, got %d", len(recs))
	}
}

func TestCollectAdmissions_DeadAndLivePIDs(t *testing.T) {
	townRoot := t.TempDir()
	admDir := filepath.Join(townRoot, ".runtime", "polecat-admission")
	if err := os.MkdirAll(admDir, 0755); err != nil {
		t.Fatal(err)
	}
	// PID 1 (init) is always alive; a huge PID is reliably dead.
	writeReservation(t, admDir, "live", os.Getpid(), "rigA")
	writeReservation(t, admDir, "dead", 2147480000, "rigB")

	recs, err := CollectAdmissions(townRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	byID := map[string]AdmissionRecord{}
	for _, r := range recs {
		byID[r.ID] = r
	}
	if !byID["live"].OwnerAlive {
		t.Error("current process PID should be alive")
	}
	if byID["dead"].OwnerAlive {
		t.Error("huge PID should be dead")
	}

	// End to end: the dead reservation must produce a candidate.
	in := Input{Window: Window{ID: "w"}, Admissions: recs}
	cands := Evaluate(DefaultRules(), in)
	if len(cands) != 1 || cands[0].RuleID != "dead_owner_admission" || cands[0].Target != "dead" {
		t.Errorf("expected 1 dead_owner_admission candidate for 'dead', got %+v", cands)
	}
}

func TestCollectAdmissions_SkipsCorruptAndNonJSON(t *testing.T) {
	townRoot := t.TempDir()
	admDir := filepath.Join(townRoot, ".runtime", "polecat-admission")
	if err := os.MkdirAll(admDir, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(admDir, "corrupt.json"), []byte("{not json"), 0644)
	_ = os.WriteFile(filepath.Join(admDir, "notes.txt"), []byte("ignore me"), 0644)
	writeReservation(t, admDir, "good", os.Getpid(), "rigA")

	recs, err := CollectAdmissions(townRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].ID != "good" {
		t.Errorf("expected only the good record, got %+v", recs)
	}
}

func TestCollectInput_OnlyAdmissionsLive(t *testing.T) {
	in, err := CollectInput(t.TempDir(), "win-1")
	if err != nil {
		t.Fatal(err)
	}
	if in.Window.ID != "win-1" {
		t.Errorf("window id not set: %q", in.Window.ID)
	}
	// Phase 1: other collectors are staged, so their slices stay empty.
	if len(in.Beads) != 0 || len(in.LogLines) != 0 || len(in.EventCounts) != 0 {
		t.Error("non-admission slices should be empty in Phase 1")
	}
}
