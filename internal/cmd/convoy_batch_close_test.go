package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCheckAndCloseCompletedConvoys_BatchesTracksQuery is the regression guard for
// gc-pai9b: the per-convoy `bd sql ... WHERE issue_id='<cv>' AND type='tracks'`
// fan-out (one fresh bd subprocess per open convoy) saturated the shared Dolt
// server under concurrent `gt done` completions and starved the dispatch loop.
//
// The fix routes checkAndCloseCompletedConvoys through getAllTrackedIssuesByConvoy
// (ONE `WHERE issue_id IN (...)` query). This test proves: with 3 open convoys,
// the tracks dependency lookup issues exactly ONE batched sql call (not 3 serial
// per-convoy ones), AND a fully-closed convoy still auto-closes correctly.
func TestCheckAndCloseCompletedConvoys_BatchesTracksQuery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping convoy shell-mock test on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"),
		[]byte(`{"prefix":"gt-","path":"gastown/mayor/rig"}`+"\n"), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	// Mock bd logs every sql invocation's query to sqlLog, and serves a batched
	// IN(...) tracks result. A per-convoy fan-out would produce 3 separate sql
	// lines each containing a single `issue_id = '...'`; the batched path produces
	// ONE line containing `issue_id IN (`.
	sqlLog := filepath.Join(binDir, "sql_invocations.log")
	script := `#!/bin/sh
i=0
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) eval "pos$i=\"$arg\""; i=$((i+1)) ;;
  esac
done
case "$pos0" in
  list)
    echo '[{"id":"hq-cv-a","title":"Convoy A","status":"open"},{"id":"hq-cv-b","title":"Convoy B","status":"open"},{"id":"hq-cv-c","title":"Convoy C","status":"open"}]'
    exit 0
    ;;
  sql)
    echo "$*" >> "` + sqlLog + `"
    case "$*" in
      *"IN ("*)
        # Batched tracks query for all convoys at once.
        echo '[{"issue_id":"hq-cv-a","depends_on_id":"gt-done1"},{"issue_id":"hq-cv-b","depends_on_id":"gt-open1"}]'
        ;;
      *)
        # Any per-convoy serial query (the regression) returns nothing —
        # if the code falls back to this, the assertions below catch it.
        echo '[]'
        ;;
    esac
    exit 0
    ;;
  show)
    # Issue details: gt-done1 closed, gt-open1 open. Convoy C has no tracked deps.
    case "$*" in
      *gt-done1*) echo '[{"id":"gt-done1","status":"closed","issue_type":"task","assignee":""}]' ;;
      *gt-open1*) echo '[{"id":"gt-open1","status":"open","issue_type":"task","assignee":""}]' ;;
      *) echo '[]' ;;
    esac
    exit 0
    ;;
  update|close)
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
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	closed, err := checkAndCloseCompletedConvoys(beadsDir, false)
	if err != nil {
		t.Fatalf("checkAndCloseCompletedConvoys() error: %v", err)
	}

	// Convoy A is fully closed (its one tracked issue is closed) → should close.
	// Convoy B has an open tracked issue → stays open. Convoy C has zero tracked
	// deps → must NOT auto-close (0/0 is "unresolved", not "complete").
	var closedA bool
	for _, c := range closed {
		if c.ID == "hq-cv-a" {
			closedA = true
		}
		if c.ID == "hq-cv-b" {
			t.Errorf("convoy B has an open tracked issue but was closed")
		}
		if c.ID == "hq-cv-c" {
			t.Errorf("convoy C has zero tracked deps but was auto-closed (0/0 must not close)")
		}
	}
	if !closedA {
		t.Errorf("convoy A is fully complete but was not closed; closed=%v", closed)
	}

	// The core regression assertion: the tracks lookup batched into ONE IN(...)
	// query, not a per-convoy serial fan-out.
	logBytes, _ := os.ReadFile(sqlLog)
	logStr := string(logBytes)
	sqlCalls := 0
	if logStr != "" {
		sqlCalls = len(strings.Split(strings.TrimSpace(logStr), "\n"))
	}
	if !strings.Contains(logStr, "IN (") {
		t.Errorf("expected a batched `issue_id IN (...)` tracks query; sql log:\n%s", logStr)
	}
	// With 3 convoys, the batched path issues 1 tracks sql call. Allow a small
	// margin for fallback detail queries, but a per-convoy fan-out (3+ separate
	// `issue_id = '...'` calls) is the failure we guard against.
	perConvoySerial := strings.Count(logStr, "issue_id = '")
	if perConvoySerial >= 3 {
		t.Errorf("detected per-convoy serial tracks fan-out (%d `issue_id = '...'` calls) — regression of gc-pai9b; sql log:\n%s", perConvoySerial, logStr)
	}
	if sqlCalls == 0 {
		t.Errorf("expected at least the batched sql call to be logged, got none")
	}
}

// TestCheckAndCloseCompletedConvoys_SkipsShipUnverified is the regression guard
// for gu-4cxuv: a convoy already marked convoy:ship-unverified (all tracked
// closed but ship-unverifiable per gu-j7u5) must be SKIPPED by the completion
// scan — not re-evaluated every tick. Re-evaluating 197 such convoys every tick
// clogged the dispatch budget and starved town-wide spawning. The skip must
// happen BEFORE the tracks query, so the labeled convoy never appears in the
// batched sql lookup.
func TestCheckAndCloseCompletedConvoys_SkipsShipUnverified(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping convoy shell-mock test on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"),
		[]byte(`{"prefix":"gt-","path":"gastown/mayor/rig"}`+"\n"), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	sqlLog := filepath.Join(binDir, "sql_invocations.log")
	// hq-cv-skip carries convoy:ship-unverified → must be skipped (never tracked-
	// queried, never closed). hq-cv-go is a normal completable convoy → closes.
	script := `#!/bin/sh
i=0
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) eval "pos$i=\"$arg\""; i=$((i+1)) ;;
  esac
done
case "$pos0" in
  list)
    echo '[{"id":"hq-cv-skip","title":"Skip me","status":"open","labels":["gt:convoy","convoy:ship-unverified"]},{"id":"hq-cv-go","title":"Close me","status":"open","labels":["gt:convoy"]}]'
    exit 0
    ;;
  sql)
    echo "$*" >> "` + sqlLog + `"
    case "$*" in
      *"IN ("*) echo '[{"issue_id":"hq-cv-go","depends_on_id":"gt-done1"}]' ;;
      *) echo '[]' ;;
    esac
    exit 0
    ;;
  show)
    case "$*" in
      *gt-done1*) echo '[{"id":"gt-done1","status":"closed","issue_type":"task","assignee":""}]' ;;
      *) echo '[]' ;;
    esac
    exit 0
    ;;
  update|close) exit 0 ;;
  *) echo '[]'; exit 0 ;;
esac
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	closed, err := checkAndCloseCompletedConvoys(beadsDir, false)
	if err != nil {
		t.Fatalf("checkAndCloseCompletedConvoys() error: %v", err)
	}

	// The labeled convoy must NOT be closed; the normal one must close.
	for _, c := range closed {
		if c.ID == "hq-cv-skip" {
			t.Errorf("convoy hq-cv-skip carries %s but was processed/closed — gu-4cxuv skip failed", convoyShipUnverifiedLabel)
		}
	}
	var closedGo bool
	for _, c := range closed {
		if c.ID == "hq-cv-go" {
			closedGo = true
		}
	}
	if !closedGo {
		t.Errorf("hq-cv-go (completable, unlabeled) should have closed; closed=%v", closed)
	}

	// The skipped convoy must never appear in the tracks sql lookup — the skip
	// happens before the batch query, so its ID must be absent from the sql log.
	logBytes, _ := os.ReadFile(sqlLog)
	if strings.Contains(string(logBytes), "hq-cv-skip") {
		t.Errorf("skipped convoy hq-cv-skip appeared in a tracks sql query — skip must precede the batch lookup; sql log:\n%s", logBytes)
	}
}
