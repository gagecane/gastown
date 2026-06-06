package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeConvoyListMock writes a mock `bd` that reports `n` open, unlabeled
// convoys (hq-cv-0..n-1), logs every tracks sql query to sqlLog, and reports
// each convoy's single tracked dep as closed (so each is a completion
// candidate). update/close are no-ops.
func writeConvoyListMock(t *testing.T, binDir, sqlLog string, n int) string {
	t.Helper()
	var items strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			items.WriteString(",")
		}
		fmt.Fprintf(&items, `{"id":"hq-cv-%d","title":"Convoy %d","status":"open","labels":["gt:convoy"]}`, i, i)
	}
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
    echo '[` + items.String() + `]'
    exit 0
    ;;
  sql)
    echo "$*" >> "` + sqlLog + `"
    # Batched IN(...) tracks query: every convoy tracks one closed dep.
    case "$*" in
      *"IN ("*)
        printf '['
        first=1
        for c in $(echo "$*" | grep -o "hq-cv-[0-9]*"); do
          if [ $first -eq 0 ]; then printf ','; fi
          printf '{"issue_id":"%s","depends_on_issue_id":"gt-done-%s"}' "$c" "$c"
          first=0
        done
        printf ']'
        ;;
      *) echo '[]' ;;
    esac
    exit 0
    ;;
  show)
    # Every tracked dep reports closed.
    case "$*" in
      *gt-done-*) echo '[{"id":"gt-done","status":"closed","issue_type":"task","assignee":""}]' ;;
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
	return bdPath
}

func setupConvoyTownBeads(t *testing.T) (binDir, beadsDir, sqlLog string) {
	t.Helper()
	binDir = t.TempDir()
	townRoot := t.TempDir()
	beadsDir = filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"),
		[]byte(`{"prefix":"gt-","path":"gastown/mayor/rig"}`+"\n"), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	sqlLog = filepath.Join(binDir, "sql_invocations.log")
	return binDir, beadsDir, sqlLog
}

// TestCheckAndCloseCompletedConvoys_CapsPerInvocation is the regression guard
// for gu-c76op (FIX a, cap): an unbounded sweep over a large convoy backlog
// could exceed the dispatch budget and SIGKILL before labeling anything,
// starving town-wide spawning. The sweep must process at most
// completionSweepMaxConvoys convoys per invocation and defer the rest to the
// next tick (which makes forward progress, since this tick's processed convoys
// get labeled / closed and are filtered next time).
func TestCheckAndCloseCompletedConvoys_CapsPerInvocation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping convoy shell-mock test on Windows")
	}

	binDir, beadsDir, sqlLog := setupConvoyTownBeads(t)
	overCap := completionSweepMaxConvoys + 17
	writeConvoyListMock(t, binDir, sqlLog, overCap)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	closed, err := checkAndCloseCompletedConvoys(beadsDir, false)
	if err != nil {
		t.Fatalf("checkAndCloseCompletedConvoys() error: %v", err)
	}

	// At most the cap should have been closed this invocation.
	if len(closed) > completionSweepMaxConvoys {
		t.Errorf("closed %d convoys, exceeds cap %d — gu-c76op cap not enforced", len(closed), completionSweepMaxConvoys)
	}

	// The over-cap convoys must NOT appear in the batched tracks sql query: the
	// cap is applied before the heavy bd work, so the deferred convoys cost
	// nothing this tick.
	logStr, _ := os.ReadFile(sqlLog)
	for i := completionSweepMaxConvoys; i < overCap; i++ {
		id := fmt.Sprintf("hq-cv-%d", i)
		if strings.Contains(string(logStr), id) {
			t.Errorf("over-cap convoy %s appeared in a tracks sql query — cap must precede the batch lookup; sql log:\n%s", id, logStr)
		}
	}
}

// TestCheckAndCloseCompletedConvoys_TimeBox is the regression guard for gu-c76op
// (FIX a, time-box): even under the cap, slow per-convoy ship-verification must
// not let the sweep run unbounded. With the clock advanced past the time-box on
// the first convoy, the loop exits immediately and closes nothing this tick.
func TestCheckAndCloseCompletedConvoys_TimeBox(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping convoy shell-mock test on Windows")
	}

	binDir, beadsDir, sqlLog := setupConvoyTownBeads(t)
	writeConvoyListMock(t, binDir, sqlLog, 5)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Drive the clock so the deadline (start + timeBox) is already in the past by
	// the time the per-convoy loop checks it: first call (deadline base) returns
	// t0, subsequent calls return t0 + 2*timeBox.
	base := time.Unix(1_900_000_000, 0)
	var calls int
	orig := timeNowForCompletionSweep
	timeNowForCompletionSweep = func() time.Time {
		calls++
		if calls == 1 {
			return base
		}
		return base.Add(2 * completionSweepTimeBox)
	}
	defer func() { timeNowForCompletionSweep = orig }()

	closed, err := checkAndCloseCompletedConvoys(beadsDir, false)
	if err != nil {
		t.Fatalf("checkAndCloseCompletedConvoys() error: %v", err)
	}
	if len(closed) != 0 {
		t.Errorf("time-box should have stopped the loop before closing any convoy, but closed %d: %v", len(closed), closed)
	}
}
