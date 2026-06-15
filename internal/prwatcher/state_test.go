package prwatcher

import (
	"strconv"
	"testing"
	"time"
)

func TestActionedRoundtrip(t *testing.T) {
	town := t.TempDir()

	a, err := LoadActioned(town, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !a.Fresh() {
		t.Errorf("missing ledger should be Fresh")
	}
	if a.Has("abc") {
		t.Errorf("empty ledger reports Has=true")
	}

	now := time.Date(2026, 6, 15, 6, 0, 0, 0, time.UTC)
	a.Mark("abc", now)
	a.Mark("def", now.Add(time.Minute))
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}

	a2, err := LoadActioned(town, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if a2.Fresh() {
		t.Errorf("ledger with backing file should not be Fresh")
	}
	if !a2.Has("abc") || !a2.Has("def") {
		t.Errorf("entries did not survive round-trip")
	}
	if a2.Len() != 2 {
		t.Errorf("Len after reload = %d, want 2", a2.Len())
	}
}

func TestActionedTrim(t *testing.T) {
	town := t.TempDir()
	a, err := LoadActioned(town, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 6, 15, 6, 0, 0, 0, time.UTC)
	total := ActionedCap + 50
	for i := 0; i < total; i++ {
		a.Mark("c-"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Second))
	}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	a2, err := LoadActioned(town, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if a2.Len() != ActionedCap {
		t.Errorf("trim: Len = %d, want %d", a2.Len(), ActionedCap)
	}
	if !a2.Has("c-" + strconv.Itoa(total-1)) {
		t.Errorf("most recent entry was trimmed")
	}
	if a2.Has("c-0") {
		t.Errorf("oldest entry survived trim")
	}
}

func TestLoadActionedRequiresRig(t *testing.T) {
	if _, err := LoadActioned(t.TempDir(), ""); err == nil {
		t.Errorf("expected error with empty rig")
	}
}

func TestActionedNilSafe(t *testing.T) {
	var a *Actioned
	if a.Has("anything") {
		t.Errorf("nil receiver Has should be false")
	}
	if a.Fresh() {
		t.Errorf("nil receiver Fresh should be false")
	}
	a.Mark("anything", time.Now())
	if err := a.Save(); err != nil {
		t.Errorf("nil receiver Save: %v", err)
	}
}
