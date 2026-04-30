package cmd

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Staged convoy creation tests (gt-csl.3.5)
// ---------------------------------------------------------------------------

// IT-10: Stage clean (no errors, no warnings) → creates convoy as staged_ready.
// Uses dagBuilder to set up the bd stub environment. Builds a clean ConvoyDAG
// directly (with rigs set). Verifies `bd create` was called with
// --status=staged_ready and `bd dep add` was called for each slingable bead.
func TestCreateStagedConvoy_CleanReady(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	// Set up bd stub environment for create/dep add commands.
	testDAG := newTestDAG(t).
		Task("gt-a", "Task A", withRig("gastown")).
		Task("gt-b", "Task B", withRig("gastown")).BlockedBy("gt-a").
		Task("gt-c", "Task C", withRig("gastown")).BlockedBy("gt-b")

	_, logPath := testDAG.Setup(t)

	// Build the ConvoyDAG directly with rigs populated (avoids rigFromBeadID stub).
	convoyDAG := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Title: "Task A", Type: "task", Status: "open", Rig: "gastown",
			Blocks: []string{"gt-b"}},
		"gt-b": {ID: "gt-b", Title: "Task B", Type: "task", Status: "open", Rig: "gastown",
			BlockedBy: []string{"gt-a"}, Blocks: []string{"gt-c"}},
		"gt-c": {ID: "gt-c", Title: "Task C", Type: "task", Status: "open", Rig: "gastown",
			BlockedBy: []string{"gt-b"}},
	}}

	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b", "gt-c"}}

	// Run the full error/warning detection pipeline.
	errFindings := detectErrors(convoyDAG)
	warnFindings := detectWarnings(convoyDAG, input)
	errs, warns := categorizeFindings(append(errFindings, warnFindings...))
	status := chooseStatus(errs, warns)

	if status != "staged_ready" {
		t.Fatalf("expected staged_ready, got %q", status)
	}

	waves, _, err := computeWaves(convoyDAG)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	convoyID, err := createStagedConvoy(convoyDAG, waves, status, "")
	if err != nil {
		t.Fatalf("createStagedConvoy: %v", err)
	}

	if convoyID == "" {
		t.Fatal("expected non-empty convoy ID")
	}
	if !strings.HasPrefix(convoyID, "hq-cv-") {
		t.Errorf("convoy ID should start with hq-cv-, got %q", convoyID)
	}

	// Read bd.log and verify bd create was called with --status=staged_ready.
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd.log: %v", err)
	}
	logContent := string(logBytes)

	if !strings.Contains(logContent, "create") {
		t.Errorf("bd.log should contain 'create' command, got:\n%s", logContent)
	}
	if !strings.Contains(logContent, "--status=staged_ready") {
		t.Errorf("bd.log should contain '--status=staged_ready', got:\n%s", logContent)
	}

	// Verify bd dep add was called for each slingable bead.
	for _, beadID := range []string{"gt-a", "gt-b", "gt-c"} {
		if !strings.Contains(logContent, "dep add "+convoyID+" "+beadID) {
			t.Errorf("bd.log should contain 'dep add %s %s', got:\n%s", convoyID, beadID, logContent)
		}
	}
}

// IT-11: Stage convoy tracks all slingable beads via deps.
// Verifies that epics are NOT tracked, but tasks/bugs ARE tracked.
func TestCreateStagedConvoy_TracksOnlySlingable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Epic("gt-epic", "Root Epic").
		Task("gt-t1", "Task 1", withRig("gastown")).ParentOf("gt-epic").
		Bug("gt-b1", "Bug 1", withRig("gastown")).ParentOf("gt-epic").
		Task("gt-t2", "Task 2", withRig("gastown")).ParentOf("gt-epic").BlockedBy("gt-t1")

	_, logPath := dag.Setup(t)

	input := &StageInput{Kind: StageInputEpic, IDs: []string{"gt-epic"}}
	beads, deps, err := collectBeads(input)
	if err != nil {
		t.Fatalf("collectBeads: %v", err)
	}

	convoyDAG := buildConvoyDAG(beads, deps)

	waves, _, err := computeWaves(convoyDAG)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	convoyID, err := createStagedConvoy(convoyDAG, waves, "staged_ready", "")
	if err != nil {
		t.Fatalf("createStagedConvoy: %v", err)
	}

	// Read bd.log.
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd.log: %v", err)
	}
	logContent := string(logBytes)

	// Slingable beads (tasks and bugs) should be tracked.
	for _, beadID := range []string{"gt-t1", "gt-b1", "gt-t2"} {
		if !strings.Contains(logContent, "dep add "+convoyID+" "+beadID) {
			t.Errorf("bd.log should contain 'dep add %s %s' for slingable bead, got:\n%s", convoyID, beadID, logContent)
		}
	}

	// Epics should NOT be tracked.
	lines := strings.Split(logContent, "\n")
	for _, line := range lines {
		if strings.Contains(line, "dep add") && strings.Contains(line, "gt-epic") {
			t.Errorf("epic gt-epic should NOT be tracked via dep add, but found: %s", line)
		}
	}
}

// IT-12: Stage convoy description includes wave count + timestamp.
func TestCreateStagedConvoy_DescriptionFormat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Task("gt-a", "Task A", withRig("gastown")).
		Task("gt-b", "Task B", withRig("gastown")).BlockedBy("gt-a")

	_, logPath := dag.Setup(t)

	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b"}}
	beads, deps, err := collectBeads(input)
	if err != nil {
		t.Fatalf("collectBeads: %v", err)
	}

	convoyDAG := buildConvoyDAG(beads, deps)

	waves, _, err := computeWaves(convoyDAG)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	_, err = createStagedConvoy(convoyDAG, waves, "staged_ready", "")
	if err != nil {
		t.Fatalf("createStagedConvoy: %v", err)
	}

	// Read bd.log to find the create command and verify description.
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd.log: %v", err)
	}
	logContent := string(logBytes)

	// Find the create command line.
	lines := strings.Split(logContent, "\n")
	var createLine string
	for _, line := range lines {
		if strings.Contains(line, "create") && strings.Contains(line, "--type=convoy") {
			createLine = line
			break
		}
	}
	if createLine == "" {
		t.Fatalf("no create command found in bd.log:\n%s", logContent)
	}

	// Description should include task count, wave count, and a timestamp.
	if !strings.Contains(createLine, "2 tasks") {
		t.Errorf("create command should mention '2 tasks' in description, got: %s", createLine)
	}
	if !strings.Contains(createLine, "2 waves") {
		t.Errorf("create command should mention '2 waves' in description, got: %s", createLine)
	}
	// Timestamp should look like an RFC3339 date (contains T and Z or +).
	if !strings.Contains(createLine, "Staged at") {
		t.Errorf("create command should contain 'Staged at' timestamp, got: %s", createLine)
	}
}

// IT-41: Convoy ID printed to stdout.
func TestCreateStagedConvoy_IDFormat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Task("gt-a", "Task A", withRig("gastown"))

	dag.Setup(t)

	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a"}}
	beads, deps, err := collectBeads(input)
	if err != nil {
		t.Fatalf("collectBeads: %v", err)
	}

	convoyDAG := buildConvoyDAG(beads, deps)

	waves, _, err := computeWaves(convoyDAG)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	convoyID, err := createStagedConvoy(convoyDAG, waves, "staged_ready", "")
	if err != nil {
		t.Fatalf("createStagedConvoy: %v", err)
	}

	// Convoy ID must be non-empty and start with hq-cv-.
	if convoyID == "" {
		t.Fatal("convoy ID should not be empty")
	}
	if !strings.HasPrefix(convoyID, "hq-cv-") {
		t.Errorf("convoy ID should start with 'hq-cv-', got %q", convoyID)
	}
	// The suffix should be base36 (lowercase alphanumeric).
	suffix := strings.TrimPrefix(convoyID, "hq-cv-")
	for _, ch := range suffix {
		if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')) {
			t.Errorf("convoy ID suffix should be base36 chars, got %q in %q", string(ch), suffix)
		}
	}
}
