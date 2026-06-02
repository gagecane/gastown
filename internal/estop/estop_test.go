package estop

import (
	"os"
	"path/filepath"
	"testing"
)

func TestActivateAndRead(t *testing.T) {
	townRoot := t.TempDir()

	if IsActive(townRoot) {
		t.Fatal("should not be active before activation")
	}

	if err := Activate(townRoot, TriggerManual, "test reason"); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	if !IsActive(townRoot) {
		t.Fatal("should be active after activation")
	}

	info := Read(townRoot)
	if info == nil {
		t.Fatal("Read returned nil")
	}
	if info.Trigger != TriggerManual {
		t.Errorf("trigger = %q, want %q", info.Trigger, TriggerManual)
	}
	if info.Reason != "test reason" {
		t.Errorf("reason = %q, want %q", info.Reason, "test reason")
	}
}

func TestDeactivate(t *testing.T) {
	townRoot := t.TempDir()

	if err := Activate(townRoot, TriggerManual, ""); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	if err := Deactivate(townRoot, false); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}

	if IsActive(townRoot) {
		t.Fatal("should not be active after deactivation")
	}
}

func TestDeactivateOnlyAutoSkipsManual(t *testing.T) {
	townRoot := t.TempDir()

	if err := Activate(townRoot, TriggerManual, "human triggered"); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	err := Deactivate(townRoot, true)
	if err == nil {
		t.Fatal("Deactivate(onlyAuto=true) should fail for manual E-stop")
	}

	if !IsActive(townRoot) {
		t.Fatal("manual E-stop should still be active")
	}
}

func TestDeactivateOnlyAutoRemovesAuto(t *testing.T) {
	townRoot := t.TempDir()

	if err := Activate(townRoot, TriggerAuto, "dolt-unreachable"); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	if err := Deactivate(townRoot, true); err != nil {
		t.Fatalf("Deactivate(onlyAuto=true): %v", err)
	}

	if IsActive(townRoot) {
		t.Fatal("auto E-stop should be removed")
	}
}

func TestFilePath(t *testing.T) {
	got := FilePath("/tmp/mytown")
	want := filepath.Join("/tmp/mytown", FileName)
	if got != want {
		t.Errorf("FilePath = %q, want %q", got, want)
	}
}

func TestReadNonExistent(t *testing.T) {
	info := Read(t.TempDir())
	if info != nil {
		t.Error("Read should return nil for non-existent file")
	}
}

func TestPerRigActivateAndRead(t *testing.T) {
	townRoot := t.TempDir()

	if IsRigActive(townRoot, "gastown") {
		t.Fatal("rig should not be active before activation")
	}

	if err := ActivateRig(townRoot, "gastown", TriggerManual, "closing laptop"); err != nil {
		t.Fatalf("ActivateRig: %v", err)
	}

	if !IsRigActive(townRoot, "gastown") {
		t.Fatal("gastown should be active after activation")
	}
	if IsRigActive(townRoot, "beads") {
		t.Fatal("beads should not be active")
	}
	// Town-wide should not be active
	if IsActive(townRoot) {
		t.Fatal("town-wide should not be active from per-rig activation")
	}

	info := ReadRig(townRoot, "gastown")
	if info == nil {
		t.Fatal("ReadRig returned nil")
	}
	if info.Reason != "closing laptop" {
		t.Errorf("reason = %q, want %q", info.Reason, "closing laptop")
	}
}

func TestIsAnyActive(t *testing.T) {
	townRoot := t.TempDir()

	if IsAnyActive(townRoot, "gastown") {
		t.Fatal("nothing should be active")
	}

	// Per-rig activation
	if err := ActivateRig(townRoot, "gastown", TriggerManual, ""); err != nil {
		t.Fatal(err)
	}
	if !IsAnyActive(townRoot, "gastown") {
		t.Fatal("gastown should be active via per-rig")
	}
	if IsAnyActive(townRoot, "beads") {
		t.Fatal("beads should not be affected by gastown per-rig")
	}

	// Clean up and test town-wide
	_ = DeactivateRig(townRoot, "gastown")
	if err := Activate(townRoot, TriggerManual, ""); err != nil {
		t.Fatal(err)
	}
	if !IsAnyActive(townRoot, "gastown") {
		t.Fatal("gastown should be active via town-wide")
	}
	if !IsAnyActive(townRoot, "beads") {
		t.Fatal("beads should be active via town-wide")
	}
}

func TestPerRigDeactivate(t *testing.T) {
	townRoot := t.TempDir()
	if err := ActivateRig(townRoot, "gastown", TriggerManual, ""); err != nil {
		t.Fatal(err)
	}
	if err := DeactivateRig(townRoot, "gastown"); err != nil {
		t.Fatal(err)
	}
	if IsRigActive(townRoot, "gastown") {
		t.Fatal("gastown should not be active after deactivation")
	}
}

func TestActivateCapturesTriggeredBy(t *testing.T) {
	townRoot := t.TempDir()

	t.Setenv("USER", "testuser")
	t.Setenv("GT_ROLE", "gastown/polecats/raider")
	t.Setenv("GT_SESSION", "")

	if err := Activate(townRoot, TriggerManual, "freezing"); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	info := Read(townRoot)
	if info == nil {
		t.Fatal("Read returned nil")
	}
	want := "testuser (gastown/polecats/raider)"
	if info.TriggeredBy != want {
		t.Errorf("TriggeredBy = %q, want %q", info.TriggeredBy, want)
	}
	if info.Reason != "freezing" {
		t.Errorf("Reason = %q, want %q", info.Reason, "freezing")
	}
}

func TestActivateRigCapturesTriggeredBy(t *testing.T) {
	townRoot := t.TempDir()

	t.Setenv("USER", "rigops")
	t.Setenv("GT_ROLE", "")
	t.Setenv("GT_SESSION", "")

	if err := ActivateRig(townRoot, "gastown", TriggerManual, ""); err != nil {
		t.Fatalf("ActivateRig: %v", err)
	}

	info := ReadRig(townRoot, "gastown")
	if info == nil {
		t.Fatal("ReadRig returned nil")
	}
	if info.TriggeredBy != "rigops" {
		t.Errorf("TriggeredBy = %q, want %q", info.TriggeredBy, "rigops")
	}
}

func TestParseLegacyThreeFieldFile(t *testing.T) {
	// A pre-attribution ESTOP file: trigger\ttimestamp\treason
	townRoot := t.TempDir()
	content := "manual\t2026-06-02T06:28:09Z\tstale freeze\n"
	if err := os.WriteFile(FilePath(townRoot), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	info := Read(townRoot)
	if info == nil {
		t.Fatal("Read returned nil")
	}
	if info.Trigger != TriggerManual {
		t.Errorf("Trigger = %q, want %q", info.Trigger, TriggerManual)
	}
	if info.Reason != "stale freeze" {
		t.Errorf("Reason = %q, want %q", info.Reason, "stale freeze")
	}
	if info.TriggeredBy != "" {
		t.Errorf("TriggeredBy = %q, want empty for legacy file", info.TriggeredBy)
	}
}

func TestCurrentActor(t *testing.T) {
	// Cases all set USER explicitly so the os/user fallback (which would
	// resolve the real test runner's username) is never exercised here.
	cases := []struct {
		name    string
		user    string
		role    string
		session string
		want    string
	}{
		{"user and role", "alice", "gastown/mayor", "", "alice (gastown/mayor)"},
		{"user only", "bob", "", "", "bob"},
		{"session fallback when role unset", "carol", "", "sess-123", "carol (sess-123)"},
		{"role wins over session", "dave", "gastown/refinery", "sess-9", "dave (gastown/refinery)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("USER", tc.user)
			t.Setenv("GT_ROLE", tc.role)
			t.Setenv("GT_SESSION", tc.session)
			if got := CurrentActor(); got != tc.want {
				t.Errorf("CurrentActor() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseBareFile(t *testing.T) {
	townRoot := t.TempDir()
	// Simulate a bare touch (no content)
	if err := os.WriteFile(FilePath(townRoot), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	info := Read(townRoot)
	if info == nil {
		t.Fatal("Read should handle empty file")
	}
	if info.Trigger != TriggerManual {
		t.Errorf("bare file trigger = %q, want %q", info.Trigger, TriggerManual)
	}
}

func TestDeactivateNonExistent(t *testing.T) {
	townRoot := t.TempDir()
	// Should not error on non-existent file
	if err := Deactivate(townRoot, false); err != nil {
		t.Fatalf("Deactivate non-existent: %v", err)
	}
}
