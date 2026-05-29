package ciwatcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFreezeRoundtrip(t *testing.T) {
	town := t.TempDir()

	frozen, err := IsFrozen(town, "alpha")
	if err != nil {
		t.Fatalf("IsFrozen on empty town: %v", err)
	}
	if frozen {
		t.Fatalf("expected not frozen on empty town")
	}

	ff := FreezeFile{
		Rig:       "alpha",
		Reason:    "broke-main-ci: gu-xuzc",
		BeadID:    "gu-xuzc",
		CommitSHA: "deadbeef",
		RunID:     "12345",
		RunURL:    "https://example.test/run/12345",
	}
	if err := WriteFreeze(town, ff); err != nil {
		t.Fatalf("WriteFreeze: %v", err)
	}

	frozen, err = IsFrozen(town, "alpha")
	if err != nil || !frozen {
		t.Fatalf("expected frozen=true err=nil, got frozen=%v err=%v", frozen, err)
	}

	got, err := ReadFreeze(town, "alpha")
	if err != nil {
		t.Fatalf("ReadFreeze: %v", err)
	}
	if got == nil {
		t.Fatalf("ReadFreeze returned nil after WriteFreeze")
	}
	if got.BeadID != "gu-xuzc" || got.Rig != "alpha" || got.RunID != "12345" {
		t.Errorf("freeze roundtrip mismatch: %+v", got)
	}
	if got.FrozenAt.IsZero() {
		t.Errorf("FrozenAt was not auto-populated")
	}

	// Different rig is not frozen.
	otherFrozen, err := IsFrozen(town, "beta")
	if err != nil {
		t.Fatalf("IsFrozen beta: %v", err)
	}
	if otherFrozen {
		t.Errorf("freeze on alpha should not affect beta")
	}

	if err := ClearFreeze(town, "alpha"); err != nil {
		t.Fatalf("ClearFreeze: %v", err)
	}
	frozen, _ = IsFrozen(town, "alpha")
	if frozen {
		t.Errorf("expected not frozen after Clear")
	}
}

func TestClearFreezeIdempotent(t *testing.T) {
	town := t.TempDir()
	if err := ClearFreeze(town, "missing"); err != nil {
		t.Errorf("ClearFreeze on missing should be nil, got %v", err)
	}
	// And again.
	if err := ClearFreeze(town, "missing"); err != nil {
		t.Errorf("ClearFreeze on missing (2nd call) should be nil, got %v", err)
	}
}

func TestWriteFreezeRequiresRig(t *testing.T) {
	town := t.TempDir()
	err := WriteFreeze(town, FreezeFile{Reason: "no rig"})
	if err == nil {
		t.Fatalf("expected error when Rig is empty")
	}
}

func TestReadFreezeMalformed(t *testing.T) {
	town := t.TempDir()
	dir := filepath.Join(town, ".runtime")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mq-frozen-alpha"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadFreeze(town, "alpha")
	if err == nil {
		t.Fatalf("expected parse error")
	}
	if !strings.Contains(err.Error(), "parse freeze file") {
		t.Errorf("error message lacks context: %v", err)
	}
}

func TestWriteFreezePreservesExplicitTimestamp(t *testing.T) {
	town := t.TempDir()
	ts := time.Date(2026, 5, 29, 6, 0, 0, 0, time.UTC)
	if err := WriteFreeze(town, FreezeFile{Rig: "alpha", FrozenAt: ts}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFreeze(town, "alpha")
	if err != nil || got == nil {
		t.Fatalf("ReadFreeze: %v", err)
	}
	if !got.FrozenAt.Equal(ts) {
		t.Errorf("FrozenAt overwritten: got %v want %v", got.FrozenAt, ts)
	}
}

func TestFreezePathLocation(t *testing.T) {
	got := FreezePath("/tmp/town", "alpha")
	want := "/tmp/town/.runtime/mq-frozen-alpha"
	if got != want {
		t.Errorf("FreezePath = %q, want %q", got, want)
	}
}
