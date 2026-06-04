//go:build integration

package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/testutil"
	"github.com/steveyegge/gastown/internal/workspace"
)

// TestQuerySessionEvents_FindsEventsFromAllLocations verifies that querySessionEvents
// finds session.ended events from both town-level and rig-level beads databases.
//
// Bug: Events created by rig-level agents (polecats, witness, etc.) are stored in
// the rig's .beads database. Events created by town-level agents (mayor, deacon)
// are stored in the town's .beads database. querySessionEvents must query ALL
// beads locations to find all events.
//
// This test:
// 1. Creates a town with a rig
// 2. Creates session.ended events in both town and rig beads
// 3. Verifies querySessionEvents finds events from both locations
func TestQuerySessionEvents_FindsEventsFromAllLocations(t *testing.T) {
	// Route all gt/bd subprocesses (and the in-process querySessionEvents read)
	// at an isolated, throwaway Dolt container via GT_DOLT_PORT instead of the
	// host workspace's production server. This both isolates the test data and
	// avoids the parent-daemon-interaction hang that previously forced this
	// test to be skipped inside a Gas Town workspace. (The historical
	// bd-0.47.2 auto-flush bug that also blocked it is fixed in bd >= 1.0.)
	testutil.RequireDoltContainer(t)

	// Skip if gt and bd are not installed
	if _, err := exec.LookPath("gt"); err != nil {
		t.Skip("gt not installed, skipping integration test")
	}
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping integration test")
	}

	// Create a temporary directory structure
	tmpDir := t.TempDir()
	townRoot := filepath.Join(tmpDir, "test-town")

	// Create town directory
	if err := os.MkdirAll(townRoot, 0755); err != nil {
		t.Fatalf("creating town directory: %v", err)
	}

	// Initialize a git repo (required for gt install)
	gitInitCmd := exec.Command("git", "init")
	gitInitCmd.Dir = townRoot
	if out, err := gitInitCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	// Use gt install to set up the town
	// Clear GT environment variables to isolate test from parent workspace
	gtInstallCmd := exec.Command("gt", "install")
	gtInstallCmd.Dir = townRoot
	gtInstallCmd.Env = testutil.CleanGTEnv()
	if out, err := gtInstallCmd.CombinedOutput(); err != nil {
		t.Fatalf("gt install: %v\n%s", err, out)
	}

	// Create a bare repo to use as the rig source
	bareRepo := filepath.Join(tmpDir, "bare-repo.git")
	bareInitCmd := exec.Command("git", "init", "--bare", bareRepo)
	if out, err := bareInitCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	// Create a temporary clone to add initial content (bare repos need content)
	tempClone := filepath.Join(tmpDir, "temp-clone")
	cloneCmd := exec.Command("git", "clone", bareRepo, tempClone)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone bare: %v\n%s", err, out)
	}

	// Add initial commit to bare repo
	initFileCmd := exec.Command("bash", "-c", "echo 'test' > README.md && git add . && git commit -m 'init'")
	initFileCmd.Dir = tempClone
	if out, err := initFileCmd.CombinedOutput(); err != nil {
		t.Fatalf("initial commit: %v\n%s", err, out)
	}
	pushCmd := exec.Command("git", "push", "origin", "main")
	pushCmd.Dir = tempClone
	// Try main first, fall back to master
	if _, err := pushCmd.CombinedOutput(); err != nil {
		pushCmd2 := exec.Command("git", "push", "origin", "master")
		pushCmd2.Dir = tempClone
		if out, err := pushCmd2.CombinedOutput(); err != nil {
			t.Fatalf("git push: %v\n%s", err, out)
		}
	}

	// Add rig using gt rig add. The CLI requires a remote-style URL for local
	// repos, so pass the bare repo as a file:// URL (bareRepo is absolute).
	rigAddCmd := exec.Command("gt", "rig", "add", "testrig", "file://"+bareRepo, "--prefix=tr")
	rigAddCmd.Dir = townRoot
	rigAddCmd.Env = testutil.CleanGTEnv()
	if out, err := rigAddCmd.CombinedOutput(); err != nil {
		t.Fatalf("gt rig add: %v\n%s", err, out)
	}

	// Find the rig path
	rigPath := filepath.Join(townRoot, "testrig")

	// Verify rig has its own .beads
	rigBeadsPath := filepath.Join(rigPath, ".beads")
	if _, err := os.Stat(rigBeadsPath); os.IsNotExist(err) {
		t.Fatalf("rig .beads not created at %s", rigBeadsPath)
	}

	// Create a session.ended event in TOWN beads (simulating mayor/deacon)
	townEventPayload := `{"cost_usd":1.50,"session_id":"hq-mayor","role":"mayor","ended_at":"2026-01-12T10:00:00Z"}`
	townEventCmd := exec.Command("bd", "create",
		"--type=event",
		"--title=Town session ended",
		"--event-category=session.ended",
		"--event-payload="+townEventPayload,
		"--json",
	)
	townEventCmd.Dir = townRoot
	townEventCmd.Env = testutil.CleanGTEnv()
	townOut, err := townEventCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("creating town event: %v\n%s", err, townOut)
	}
	t.Logf("Created town event: %s", string(townOut))

	// Create a session.ended event in RIG beads (simulating polecat)
	rigEventPayload := `{"cost_usd":2.50,"session_id":"gt-testrig-toast","role":"polecat","rig":"testrig","worker":"toast","ended_at":"2026-01-12T11:00:00Z"}`
	rigEventCmd := exec.Command("bd", "create",
		"--type=event",
		"--title=Rig session ended",
		"--event-category=session.ended",
		"--event-payload="+rigEventPayload,
		"--json",
	)
	rigEventCmd.Dir = rigPath
	rigEventCmd.Env = testutil.CleanGTEnv()
	rigOut, err := rigEventCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("creating rig event: %v\n%s", err, rigOut)
	}
	t.Logf("Created rig event: %s", string(rigOut))

	// Verify events are in separate databases by querying each directly.
	// Capture stdout only: bd writes diagnostic warnings (e.g. .beads dir
	// permissions) to stderr, which would otherwise corrupt the JSON parse.
	townListCmd := exec.Command("bd", "list", "--type=event", "--all", "--json")
	townListCmd.Dir = townRoot
	townListCmd.Env = testutil.CleanGTEnv()
	townListOut, err := townListCmd.Output()
	if err != nil {
		t.Fatalf("listing town events: %v", err)
	}

	rigListCmd := exec.Command("bd", "list", "--type=event", "--all", "--json")
	rigListCmd.Dir = rigPath
	rigListCmd.Env = testutil.CleanGTEnv()
	rigListOut, err := rigListCmd.Output()
	if err != nil {
		t.Fatalf("listing rig events: %v", err)
	}

	var townEvents, rigEvents []struct{ ID string }
	if err := json.Unmarshal(townListOut, &townEvents); err != nil {
		t.Fatalf("parsing town events JSON: %v\n%s", err, townListOut)
	}
	if err := json.Unmarshal(rigListOut, &rigEvents); err != nil {
		t.Fatalf("parsing rig events JSON: %v\n%s", err, rigListOut)
	}

	t.Logf("Town beads has %d events", len(townEvents))
	t.Logf("Rig beads has %d events", len(rigEvents))

	// Both should have events (they're in separate DBs)
	if len(townEvents) == 0 {
		t.Error("Expected town beads to have events")
	}
	if len(rigEvents) == 0 {
		t.Error("Expected rig beads to have events")
	}

	// Save current directory and change to town root for query
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting current directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restoring directory: %v", err)
		}
	}()

	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("changing to town root: %v", err)
	}

	// Verify workspace discovery works
	foundTownRoot, wsErr := workspace.FindFromCwdOrError()
	if wsErr != nil {
		t.Fatalf("workspace.FindFromCwdOrError failed: %v", wsErr)
	}
	normalizePath := func(path string) string {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return filepath.Clean(path)
		}
		return resolved
	}
	if normalizePath(foundTownRoot) != normalizePath(townRoot) {
		t.Errorf("workspace.FindFromCwdOrError returned %s, expected %s", foundTownRoot, townRoot)
	}

	// Call querySessionEvents - this should find events from ALL locations
	entries := querySessionEvents()

	t.Logf("querySessionEvents returned %d entries", len(entries))

	// We created 2 session.ended events (one town, one rig)
	// The fix should find BOTH
	if len(entries) < 2 {
		t.Errorf("querySessionEvents found %d entries, expected at least 2 (one from town, one from rig)", len(entries))
		t.Log("This indicates the bug: querySessionEvents only queries town-level beads, missing rig-level events")
	}

	// Verify we found both the mayor and polecat sessions
	var foundMayor, foundPolecat bool
	for _, e := range entries {
		t.Logf("  Entry: session=%s role=%s cost=$%.2f", e.SessionID, e.Role, e.CostUSD)
		if e.Role == "mayor" {
			foundMayor = true
		}
		if e.Role == "polecat" {
			foundPolecat = true
		}
	}

	if !foundMayor {
		t.Error("Missing mayor session from town beads")
	}
	if !foundPolecat {
		t.Error("Missing polecat session from rig beads")
	}
}
