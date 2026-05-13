package polecat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoveryMarkerRoundTrip(t *testing.T) {
	town := t.TempDir()
	sess := "gt-rig-toast"

	if HasActiveRecoveryMarker(town, sess) {
		t.Fatalf("expected no marker initially")
	}

	if err := WriteRecoveryMarker(town, sess, "witness", "manual --no-verify push", 0); err != nil {
		t.Fatalf("WriteRecoveryMarker: %v", err)
	}

	if !HasActiveRecoveryMarker(town, sess) {
		t.Fatalf("expected active marker after write")
	}

	m := ReadRecoveryMarker(town, sess)
	if m == nil {
		t.Fatalf("ReadRecoveryMarker returned nil")
	}
	if m.SetBy != "witness" {
		t.Errorf("SetBy = %q, want witness", m.SetBy)
	}
	if m.Reason != "manual --no-verify push" {
		t.Errorf("Reason = %q", m.Reason)
	}
	if m.ExpiresAt.Sub(m.SetAt) != DefaultRecoveryMarkerTTL {
		t.Errorf("default TTL not applied: expires-set=%v", m.ExpiresAt.Sub(m.SetAt))
	}

	if err := ClearRecoveryMarker(town, sess); err != nil {
		t.Fatalf("ClearRecoveryMarker: %v", err)
	}
	if HasActiveRecoveryMarker(town, sess) {
		t.Fatalf("marker still active after clear")
	}
}

func TestRecoveryMarkerExpired(t *testing.T) {
	town := t.TempDir()
	sess := "gt-rig-nux"

	dir := filepath.Join(town, ".runtime", "recovery_markers")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	expired := RecoveryMarker{
		SetAt:     time.Now().Add(-2 * time.Hour).UTC(),
		ExpiresAt: time.Now().Add(-1 * time.Hour).UTC(),
		SetBy:     "mayor",
	}
	data, _ := json.Marshal(expired)
	if err := os.WriteFile(filepath.Join(dir, sess+".json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	if HasActiveRecoveryMarker(town, sess) {
		t.Fatalf("expired marker reported active")
	}
	if ReadRecoveryMarker(town, sess) == nil {
		t.Fatalf("expired marker should still be readable (just inactive)")
	}
}

func TestRecoveryMarkerCustomTTL(t *testing.T) {
	town := t.TempDir()
	sess := "gt-rig-furiosa"

	if err := WriteRecoveryMarker(town, sess, "mayor", "", 5*time.Minute); err != nil {
		t.Fatalf("WriteRecoveryMarker: %v", err)
	}
	m := ReadRecoveryMarker(town, sess)
	if m == nil {
		t.Fatal("nil marker")
	}
	if got := m.ExpiresAt.Sub(m.SetAt); got != 5*time.Minute {
		t.Errorf("custom TTL: expires-set=%v, want 5m", got)
	}
}

func TestRecoveryMarkerMalformedTreatedAsAbsent(t *testing.T) {
	town := t.TempDir()
	sess := "gt-rig-jasper"
	dir := filepath.Join(town, ".runtime", "recovery_markers")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sess+".json"), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if HasActiveRecoveryMarker(town, sess) {
		t.Fatalf("malformed marker should be treated as absent")
	}
	if ReadRecoveryMarker(town, sess) != nil {
		t.Fatalf("malformed marker should not parse")
	}
}

func TestClearRecoveryMarkerMissingIsNoop(t *testing.T) {
	town := t.TempDir()
	if err := ClearRecoveryMarker(town, "nope"); err != nil {
		t.Fatalf("clearing missing marker should be idempotent: %v", err)
	}
}
