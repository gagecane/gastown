package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeDrainMockBd writes a mock `bd` into binDir.
//
//   - `list` echoes listJSON (the convoy:ship-unverified backlog).
//   - `sql` serves the batched tracks edges (one tracked bead per convoy).
//   - `show` is the batched detail call (`bd show id1 id2 ... --json`): it walks
//     the per-bead `showBeads` map and emits a JSON array of every bead whose ID
//     appears in the argument list, mirroring the real batch shape.
//   - `update` is logged to updateLog so tests can assert label clears.
//
// trackEdges maps convoyID→trackedBeadID; showBeads maps beadID→its detail JSON
// object (without the surrounding brackets).
func writeDrainMockBd(t *testing.T, binDir, listJSON string, trackEdges, showBeads map[string]string, updateLog string) {
	t.Helper()

	var edges []string
	for cv, bead := range trackEdges {
		edges = append(edges, `{"issue_id":"`+cv+`","target":"`+bead+`"}`)
	}
	edgesJSON := "[" + strings.Join(edges, ",") + "]"

	// Build the show dispatcher: for each known bead, if its ID is in "$*", append
	// its object. JSON fragments are single-quoted (they contain double quotes, so
	// they cannot live inside a double-quoted shell assignment); the accumulator
	// and separator are double-quoted, giving `out="$out$sep"'<json>'`.
	var showLogic strings.Builder
	showLogic.WriteString("    out=\"\"\n    sep=\"\"\n")
	for bead, obj := range showBeads {
		showLogic.WriteString(`    case "$*" in *` + bead + `*) out="$out$sep"'` + obj + `'; sep="," ;; esac` + "\n")
	}
	showLogic.WriteString(`    echo "[$out]"` + "\n")

	script := `#!/bin/sh
case "$1" in
  list)
    echo '` + listJSON + `'
    exit 0
    ;;
  sql)
    case "$*" in
      *"IN ("*) echo '` + edgesJSON + `' ;;
      *) echo '[]' ;;
    esac
    exit 0
    ;;
  show)
` + showLogic.String() + `    exit 0
    ;;
  update)
    echo "$*" >> "` + updateLog + `"
    exit 0
    ;;
  close)
    exit 0
    ;;
  *)
    echo '[]'
    exit 0
    ;;
esac
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
}

// TestDrainShipUnverifiedConvoys_Classifies is the core regression guard for
// gu-yosez: the drain must list convoys CARRYING convoy:ship-unverified (the
// inverse of the scan's skip), re-verify each, and classify it correctly —
// landed→close, reopened→re-enter, in-flight→park, false-close→escalate.
func TestDrainShipUnverifiedConvoys_Classifies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping convoy shell-mock test on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	// No routes.jsonl + no rig worktree → lookupCitingCommit returns verified=false
	// for every bead (fail-open). To exercise the FALSE-CLOSE path we need
	// verified=true,cited=false, which requires a real git repo. Instead we drive
	// classification through the cheaper label/desc signals that don't need git:
	//   - landed: a citing-commit lookup that fails-open is "shipped" → closes only
	//     if there is positive evidence. With no git, evaluateTrackedBeadShipped
	//     fails open (returns "") → treated as shipped → convoy closes.
	//   - in-flight: awaiting_refinery_merge label → stays parked.
	//   - reopened: tracked bead status=open → allClosed false → reopened.
	// The genuine false-close path (needs git) is covered separately below.

	listJSON := `[` +
		`{"id":"hq-cv-landed","title":"Landed","status":"open","labels":["gt:convoy","convoy:ship-unverified"]},` +
		`{"id":"hq-cv-flight","title":"In flight","status":"open","labels":["gt:convoy","convoy:ship-unverified"]},` +
		`{"id":"hq-cv-reopen","title":"Reopened","status":"open","labels":["gt:convoy","convoy:ship-unverified"]}` +
		`]`
	trackEdges := map[string]string{
		"hq-cv-landed": "gt-landed",
		"hq-cv-flight": "gt-flight",
		"hq-cv-reopen": "gt-reopen",
	}
	showBeads := map[string]string{
		"gt-landed": `{"id":"gt-landed","status":"closed","issue_type":"task","assignee":""}`,
		"gt-flight": `{"id":"gt-flight","status":"closed","issue_type":"task","assignee":"","labels":["awaiting_refinery_merge"]}`,
		"gt-reopen": `{"id":"gt-reopen","status":"open","issue_type":"task","assignee":""}`,
	}
	updateLog := filepath.Join(binDir, "update.log")
	writeDrainMockBd(t, binDir, listJSON, trackEdges, showBeads, updateLog)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Stub escalate so we observe the false-close path without spawning gt.
	var escalated []string
	orig := escalateConvoyFalseCloseFn
	escalateConvoyFalseCloseFn = func(townBeads, convoyID, title string, beadIDs []string) {
		escalated = append(escalated, convoyID)
	}
	defer func() { escalateConvoyFalseCloseFn = orig }()

	res, err := drainShipUnverifiedConvoys(beadsDir, false)
	if err != nil {
		t.Fatalf("drainShipUnverifiedConvoys() error: %v", err)
	}

	// Landed: closed.
	if len(res.Closed) != 1 || res.Closed[0].ID != "hq-cv-landed" {
		t.Errorf("expected hq-cv-landed closed; got Closed=%v", res.Closed)
	}
	// In-flight: parked, no escalation.
	if res.StillInFlight != 1 {
		t.Errorf("expected 1 still-in-flight (hq-cv-flight); got %d", res.StillInFlight)
	}
	// Reopened: re-entered scan.
	if len(res.Reopened) != 1 || res.Reopened[0] != "hq-cv-reopen" {
		t.Errorf("expected hq-cv-reopen reopened; got Reopened=%v", res.Reopened)
	}
	if len(escalated) != 0 {
		t.Errorf("expected no escalations in label/desc-driven test; got %v", escalated)
	}

	// Every drained convoy must have had its label cleared first (remove-label),
	// proving the drain re-evaluates rather than leaving the graveyard label in place.
	logBytes, _ := os.ReadFile(updateLog)
	logStr := string(logBytes)
	for _, id := range []string{"hq-cv-landed", "hq-cv-flight", "hq-cv-reopen"} {
		if !strings.Contains(logStr, "remove-label="+convoyShipUnverifiedLabel) || !strings.Contains(logStr, id) {
			t.Errorf("expected label clear for %s; update log:\n%s", id, logStr)
		}
	}
}

// TestDrainShipUnverifiedConvoys_FalseClose verifies the genuine false-close path:
// a closed tracked bead with NO citing commit on origin/main and no in-flight
// label is escalated to mayor. This requires a real git repo so the citation
// lookup runs (verified=true) and finds nothing (cited=false).
func TestDrainShipUnverifiedConvoys_FalseClose(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping convoy shell-mock test on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// A real git repo at mayor/rig (the fallback worktree for unrouted beads) with
	// an origin/main that does NOT cite the bead → verified=true, cited=false.
	// resolveRigWorktreePath joins its townBeads arg with mayor/rig; the drain is
	// invoked with beadsDir below, so the worktree lives under beadsDir.
	rigPath := filepath.Join(beadsDir, "mayor", "rig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}
	gitRun(t, rigPath, "init", "-q")
	gitRun(t, rigPath, "config", "user.email", "t@t")
	gitRun(t, rigPath, "config", "user.name", "t")
	gitRun(t, rigPath, "commit", "-q", "--allow-empty", "-m", "unrelated work")
	// Create origin/main ref pointing at HEAD so HasCommitCitingRef has a ref to scan.
	gitRun(t, rigPath, "update-ref", "refs/remotes/origin/main", "HEAD")

	listJSON := `[{"id":"hq-cv-false","title":"False close","status":"open","labels":["gt:convoy","convoy:ship-unverified"]}]`
	trackEdges := map[string]string{"hq-cv-false": "gt-false"}
	showBeads := map[string]string{"gt-false": `{"id":"gt-false","status":"closed","issue_type":"task","assignee":""}`}
	updateLog := filepath.Join(binDir, "update.log")
	writeDrainMockBd(t, binDir, listJSON, trackEdges, showBeads, updateLog)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var escalated []string
	var escalatedBeads []string
	orig := escalateConvoyFalseCloseFn
	escalateConvoyFalseCloseFn = func(townBeads, convoyID, title string, beadIDs []string) {
		escalated = append(escalated, convoyID)
		escalatedBeads = append(escalatedBeads, beadIDs...)
	}
	defer func() { escalateConvoyFalseCloseFn = orig }()

	res, err := drainShipUnverifiedConvoys(beadsDir, false)
	if err != nil {
		t.Fatalf("drainShipUnverifiedConvoys() error: %v", err)
	}

	if len(escalated) != 1 || escalated[0] != "hq-cv-false" {
		t.Fatalf("expected hq-cv-false escalated; got %v", escalated)
	}
	if len(escalatedBeads) != 1 || escalatedBeads[0] != "gt-false" {
		t.Errorf("expected escalation evidence [gt-false]; got %v", escalatedBeads)
	}
	if len(res.Closed) != 0 {
		t.Errorf("false-close convoy must NOT close; got Closed=%v", res.Closed)
	}
	if len(res.Escalated) != 1 {
		t.Errorf("expected res.Escalated to record 1; got %v", res.Escalated)
	}
}

// TestDrainShipUnverifiedConvoys_DryRun proves dry-run takes no mutating action:
// no label clears, no closes, no escalations — only the planned verdict.
func TestDrainShipUnverifiedConvoys_DryRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping convoy shell-mock test on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	listJSON := `[{"id":"hq-cv-landed","title":"Landed","status":"open","labels":["gt:convoy","convoy:ship-unverified"]}]`
	trackEdges := map[string]string{"hq-cv-landed": "gt-landed"}
	showBeads := map[string]string{"gt-landed": `{"id":"gt-landed","status":"closed","issue_type":"task","assignee":""}`}
	updateLog := filepath.Join(binDir, "update.log")
	writeDrainMockBd(t, binDir, listJSON, trackEdges, showBeads, updateLog)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var escalated []string
	orig := escalateConvoyFalseCloseFn
	escalateConvoyFalseCloseFn = func(townBeads, convoyID, title string, beadIDs []string) {
		escalated = append(escalated, convoyID)
	}
	defer func() { escalateConvoyFalseCloseFn = orig }()

	res, err := drainShipUnverifiedConvoys(beadsDir, true)
	if err != nil {
		t.Fatalf("drainShipUnverifiedConvoys(dryRun) error: %v", err)
	}
	if len(res.Closed) != 1 {
		t.Errorf("dry-run should still REPORT the close candidate; got %v", res.Closed)
	}
	if len(escalated) != 0 {
		t.Errorf("dry-run must not escalate; got %v", escalated)
	}
	// No mutating update (label clear) should have been written in dry-run.
	if logBytes, _ := os.ReadFile(updateLog); len(logBytes) != 0 {
		t.Errorf("dry-run must not write any bd update; update log:\n%s", logBytes)
	}
}

// TestDrainShipUnverifiedConvoys_DryRunReopened guards the dry-run classification
// gap: a convoy whose tracked bead has REOPENED has no unshipped (closed) beads,
// so a naive dry-run would mis-report it as "would close". It must report
// "reopened" instead — matching what the live path actually does.
func TestDrainShipUnverifiedConvoys_DryRunReopened(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping convoy shell-mock test on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	listJSON := `[{"id":"hq-cv-reopen","title":"Reopened","status":"open","labels":["gt:convoy","convoy:ship-unverified"]}]`
	trackEdges := map[string]string{"hq-cv-reopen": "gt-reopen"}
	showBeads := map[string]string{"gt-reopen": `{"id":"gt-reopen","status":"open","issue_type":"task","assignee":""}`}
	updateLog := filepath.Join(binDir, "update.log")
	writeDrainMockBd(t, binDir, listJSON, trackEdges, showBeads, updateLog)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	res, err := drainShipUnverifiedConvoys(beadsDir, true)
	if err != nil {
		t.Fatalf("drainShipUnverifiedConvoys(dryRun) error: %v", err)
	}
	if len(res.Closed) != 0 {
		t.Errorf("reopened convoy must NOT be reported as a close candidate; got Closed=%v", res.Closed)
	}
	if len(res.Reopened) != 1 || res.Reopened[0] != "hq-cv-reopen" {
		t.Errorf("expected hq-cv-reopen reported reopened; got Reopened=%v", res.Reopened)
	}
}

// TestDrainShipUnverifiedConvoys_OnlyLabeled proves the drain lists ONLY convoys
// carrying convoy:ship-unverified — it passes the label to listConvoyIssues, the
// inverse of the completion/stranded scans which skip it.
func TestDrainShipUnverifiedConvoys_OnlyLabeled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping convoy shell-mock test on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	listLog := filepath.Join(binDir, "list.log")
	script := `#!/bin/sh
case "$1" in
  list) echo "$*" >> "` + listLog + `"; echo '[]'; exit 0 ;;
  *) echo '[]'; exit 0 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := drainShipUnverifiedConvoys(beadsDir, false); err != nil {
		t.Fatalf("drainShipUnverifiedConvoys() error: %v", err)
	}

	logBytes, _ := os.ReadFile(listLog)
	if !strings.Contains(string(logBytes), "--label="+convoyShipUnverifiedLabel) {
		t.Errorf("drain must query convoys carrying %s (inverse of the scan skip); list log:\n%s",
			convoyShipUnverifiedLabel, logBytes)
	}
}
