package cmd

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Re-stage existing convoy tests (gt-csl.3.6)
// ---------------------------------------------------------------------------

// IT-13: Re-stage existing staged convoy updates in place (no duplicate).
//
// 1. Set up a DAG with a convoy that has status "staged_ready" and tracks 2 tasks.
// 2. Call updateStagedConvoy (the re-stage path).
// 3. Verify: bd.log shows `bd update <convoy-id>` was called (not `bd create`).
// 4. Verify: no duplicate convoy was created.
// 5. Verify: original convoy ID preserved.
func TestRestageConvoy_UpdatesInPlace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	// Build a DAG with a convoy already in "staged_ready" status,
	// tracking two tasks.
	testDAG := newTestDAG(t).
		Convoy("hq-cv-test1", "Staged Convoy").WithStatus("staged_ready").
		Task("gt-x1", "Task X1", withRig("gastown")).TrackedBy("hq-cv-test1").
		Task("gt-x2", "Task X2", withRig("gastown")).TrackedBy("hq-cv-test1").BlockedBy("gt-x1")

	_, logPath := testDAG.Setup(t)

	// Build the ConvoyDAG directly (as runConvoyStage would after collectBeads).
	convoyDAG := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-x1": {ID: "gt-x1", Title: "Task X1", Type: "task", Status: "open", Rig: "gastown",
			Blocks: []string{"gt-x2"}},
		"gt-x2": {ID: "gt-x2", Title: "Task X2", Type: "task", Status: "open", Rig: "gastown",
			BlockedBy: []string{"gt-x1"}},
	}}

	waves, _, err := computeWaves(convoyDAG)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	// Call updateStagedConvoy — the re-stage path.
	err = updateStagedConvoy("hq-cv-test1", convoyDAG, waves, "staged_ready", "")
	if err != nil {
		t.Fatalf("updateStagedConvoy: %v", err)
	}

	// Read bd.log to inspect which commands were run.
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd.log: %v", err)
	}
	logContent := string(logBytes)

	// Verify: bd update was called with --status=staged_ready.
	if !strings.Contains(logContent, "update hq-cv-test1") {
		t.Errorf("bd.log should contain 'update hq-cv-test1', got:\n%s", logContent)
	}
	if !strings.Contains(logContent, "--status=staged_ready") {
		t.Errorf("bd.log should contain '--status=staged_ready', got:\n%s", logContent)
	}

	// Verify: NO bd create was called.
	lines := strings.Split(logContent, "\n")
	for _, line := range lines {
		if strings.Contains(line, "CMD:create") {
			t.Errorf("bd create should NOT be called during re-stage, but found: %s", line)
		}
	}

	// Verify: NO bd dep add was called (tracking deps already exist).
	for _, line := range lines {
		if strings.Contains(line, "dep add") {
			t.Errorf("bd dep add should NOT be called during re-stage (deps already exist), but found: %s", line)
		}
	}

	// Verify: description update was called.
	foundDescUpdate := false
	for _, line := range lines {
		if strings.Contains(line, "update hq-cv-test1") && strings.Contains(line, "--description=") {
			foundDescUpdate = true
		}
	}
	if !foundDescUpdate {
		t.Errorf("bd.log should contain description update for hq-cv-test1, got:\n%s", logContent)
	}
}

// IT-13b: Re-stage detection logic correctly identifies already-staged convoys.
// Verifies the re-stage flag is set when input convoy has "staged_" prefix status.
func TestRestageConvoy_DetectionLogic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	testDAG := newTestDAG(t).
		Convoy("hq-cv-det", "Detection Convoy").WithStatus("staged_ready").
		Task("gt-d1", "Detection Task 1", withRig("gastown")).TrackedBy("hq-cv-det").
		Task("gt-d2", "Detection Task 2", withRig("gastown")).TrackedBy("hq-cv-det")

	testDAG.Setup(t)

	// Step 2: Resolve bead type via bdShow.
	result, err := bdShow("hq-cv-det")
	if err != nil {
		t.Fatalf("bdShow: %v", err)
	}

	// Verify it's a convoy.
	if result.IssueType != "convoy" {
		t.Fatalf("expected convoy type, got %q", result.IssueType)
	}

	// Verify status is "staged_ready".
	if result.Status != "staged_ready" {
		t.Fatalf("expected status 'staged_ready', got %q", result.Status)
	}

	// Verify the detection logic: status starts with "staged_".
	if !strings.HasPrefix(result.Status, "staged_") {
		t.Errorf("expected status to start with 'staged_', got %q", result.Status)
	}

	// Verify resolveInputKind classifies as convoy.
	beadTypes := map[string]string{"hq-cv-det": result.IssueType}
	input, err := resolveInputKind(beadTypes)
	if err != nil {
		t.Fatalf("resolveInputKind: %v", err)
	}
	if input.Kind != StageInputConvoy {
		t.Errorf("expected StageInputConvoy, got %v", input.Kind)
	}
}

// IT-13c: Re-stage with different status updates correctly.
// Verifies updateStagedConvoy can set staged_warnings status.
func TestRestageConvoy_UpdatesStatusToWarnings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	testDAG := newTestDAG(t).
		Convoy("hq-cv-warn", "Warn Convoy").WithStatus("staged_ready").
		Task("gt-w1", "Warn Task 1", withRig("gastown")).TrackedBy("hq-cv-warn").
		Task("bd-w2", "Warn Task 2", withRig("beads")).TrackedBy("hq-cv-warn")

	_, logPath := testDAG.Setup(t)

	// Build a ConvoyDAG with cross-rig tasks.
	convoyDAG := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-w1": {ID: "gt-w1", Title: "Warn Task 1", Type: "task", Status: "open", Rig: "gastown"},
		"bd-w2": {ID: "bd-w2", Title: "Warn Task 2", Type: "task", Status: "open", Rig: "beads"},
	}}

	waves, _, err := computeWaves(convoyDAG)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	// Call updateStagedConvoy with staged_warnings status.
	err = updateStagedConvoy("hq-cv-warn", convoyDAG, waves, "staged_warnings", "")
	if err != nil {
		t.Fatalf("updateStagedConvoy: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd.log: %v", err)
	}
	logContent := string(logBytes)

	// Status should be updated to staged_warnings.
	if !strings.Contains(logContent, "--status=staged_warnings") {
		t.Errorf("re-stage with warnings should set --status=staged_warnings, got:\n%s", logContent)
	}

	// No create command should be in the log.
	for _, line := range strings.Split(logContent, "\n") {
		if strings.Contains(line, "CMD:create") {
			t.Errorf("should NOT call 'bd create', found: %s", line)
		}
	}
}
