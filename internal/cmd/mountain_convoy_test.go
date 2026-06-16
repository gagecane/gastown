package cmd

import (
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// gu-cax95: gt mountain accepts a convoy ID directly (cross-rig case where the
// tracked beads live in separate rig databases and cannot be bd-linked to an
// HQ epic). It applies the mountain label and launches the existing convoy in
// one step — without re-staging from non-existent bd parent-child links.

// A staged convoy passed to `gt mountain` gets the mountain label, transitions
// to open, and dispatches Wave 1.
func TestRunMountain_ConvoyInput_StagedReady(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	td := newTestDAG(t).
		Convoy("hq-cv-xrig", "Cross-rig convoy").WithStatus("staged_ready").
		Task("casw-1", "Webapp task", withRig("casw")).TrackedBy("hq-cv-xrig").
		Task("cacr-1", "CDK task", withRig("cacr")).TrackedBy("hq-cv-xrig")

	_, logPath := td.Setup(t)

	// Stub dispatch so we don't spawn a real `gt sling`.
	var mu sync.Mutex
	var dispatched []string
	orig := dispatchTaskDirect
	dispatchTaskDirect = func(townRoot, beadID, rig string) error {
		mu.Lock()
		dispatched = append(dispatched, beadID)
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { dispatchTaskDirect = orig })

	defer func() { mountainForce = false }()

	if err := runMountain(mountainCmd, []string{"hq-cv-xrig"}); err != nil {
		t.Fatalf("runMountain on convoy: %v", err)
	}

	logContent := readLog(t, logPath)

	// Should have labeled the convoy as a mountain.
	if !strings.Contains(logContent, "CMD:update hq-cv-xrig --add-label=mountain") {
		t.Errorf("expected mountain label add, got log:\n%s", logContent)
	}

	// Should have launched (transition to open).
	if !strings.Contains(logContent, "CMD:update hq-cv-xrig --status=open") {
		t.Errorf("expected status=open transition, got log:\n%s", logContent)
	}

	// Should NOT have tried to re-stage from epic parent-child links.
	if strings.Contains(logContent, "list --parent") {
		t.Errorf("convoy input should not re-stage via list --parent, got log:\n%s", logContent)
	}

	// Both tracked tasks should have been dispatched in Wave 1 (independent).
	mu.Lock()
	got := append([]string{}, dispatched...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("expected 2 dispatched tasks, got %d: %v", len(got), got)
	}
}

// An already-open convoy passed to `gt mountain` gets the mountain label but is
// not re-launched (no second status=open transition, no dispatch).
func TestRunMountain_ConvoyInput_AlreadyOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	td := newTestDAG(t).
		Convoy("hq-cv-open", "Open convoy").WithStatus("open").
		Task("casw-2", "Webapp task", withRig("casw")).TrackedBy("hq-cv-open")

	_, logPath := td.Setup(t)

	var dispatchCalled bool
	orig := dispatchTaskDirect
	dispatchTaskDirect = func(townRoot, beadID, rig string) error {
		dispatchCalled = true
		return nil
	}
	t.Cleanup(func() { dispatchTaskDirect = orig })

	if err := runMountain(mountainCmd, []string{"hq-cv-open"}); err != nil {
		t.Fatalf("runMountain on open convoy: %v", err)
	}

	logContent := readLog(t, logPath)

	// Should still label it.
	if !strings.Contains(logContent, "CMD:update hq-cv-open --add-label=mountain") {
		t.Errorf("expected mountain label add, got log:\n%s", logContent)
	}

	// Should NOT re-launch an already-open convoy.
	if strings.Contains(logContent, "--status=open") {
		t.Errorf("already-open convoy should not be re-launched, got log:\n%s", logContent)
	}
	if dispatchCalled {
		t.Errorf("already-open convoy should not dispatch a new wave")
	}
}

// A closed convoy is rejected.
func TestRunMountain_ConvoyInput_Closed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	td := newTestDAG(t).
		Convoy("hq-cv-done", "Closed convoy").WithStatus("closed")

	td.Setup(t)

	err := runMountain(mountainCmd, []string{"hq-cv-done"})
	if err == nil {
		t.Fatal("expected error for closed convoy, got nil")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Errorf("error should mention closed, got: %v", err)
	}
}

// gu-4513b: an ad-hoc `gt mountain <epic>` must build a fan-in capstone
// validation bead — blocked by every leg — so the convoy rolls up to a combined
// deliverable instead of degrading to the bare auto-close AND gate. runMountain
// assembles its own DAG/waves and previously never reached appendValidationWave
// (only runConvoyStage did), so the epic-promotion path produced no capstone.
func TestRunMountain_EpicInput_AppendsValidationCapstone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	td := newTestDAG(t).
		Epic("gt-epic-x", "Mountain Epic").
		Task("gt-a", "Task A", withRig("gastown")).ParentOf("gt-epic-x").
		Task("gt-b", "Task B", withRig("gastown")).ParentOf("gt-epic-x").BlockedBy("gt-a")

	_, logPath := td.Setup(t)

	// Stub dispatch so we don't spawn a real `gt sling`.
	var mu sync.Mutex
	var dispatched []string
	orig := dispatchTaskDirect
	dispatchTaskDirect = func(townRoot, beadID, rig string) error {
		mu.Lock()
		dispatched = append(dispatched, beadID)
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { dispatchTaskDirect = orig })

	defer func() { mountainForce = false }()

	if err := runMountain(mountainCmd, []string{"gt-epic-x"}); err != nil {
		t.Fatalf("runMountain on epic: %v", err)
	}

	logContent := readLog(t, logPath)

	// A capstone validation bead must be created with the mol-validate-prd formula.
	if !strings.Contains(logContent, "mol-validate-prd") {
		t.Errorf("expected validation bead created with mol-validate-prd, got log:\n%s", logContent)
	}

	// Each slingable leg must block the validation bead.
	for _, leg := range []string{"gt-a", "gt-b"} {
		if !strings.Contains(logContent, "dep add "+leg+" ") || !strings.Contains(logContent, "--type=blocks") {
			t.Errorf("expected blocking dep from leg %s to the validation bead, got log:\n%s", leg, logContent)
		}
	}

	// Wave 1 dispatches only the unblocked leg (gt-a); the capstone is in a later
	// wave and must NOT be dispatched immediately.
	mu.Lock()
	got := append([]string{}, dispatched...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "gt-a" {
		t.Fatalf("expected Wave 1 to dispatch only gt-a, got %v", got)
	}
}

// readLog reads the bd stub log file.
func readLog(t *testing.T, logPath string) string {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd.log: %v", err)
	}
	return string(b)
}
