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

// TestFindStrandedConvoys_SingleSlingContextScan asserts that one full
// findStrandedConvoys() invocation issues exactly one
// `bd list --label=gt:sling-context` subprocess regardless of how many
// convoys are open.
//
// This is the production correctness check for the gu-c6ua hoist: if
// future refactoring re-introduces a per-convoy sling-context lookup,
// this test catches it immediately. Pre-hoist this would have spawned
// O(convoys × rigs) calls — for our 27-convoy / 19-rig town that was
// 513 calls per scan, taking 4-5 minutes (gc-jmy04 root cause).
func TestFindStrandedConvoys_SingleSlingContextScan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell-script mock test on Windows")
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

	// Counter file: mock bd appends a marker line on every
	// `bd list --label=gt:sling-context` invocation. Test reads it
	// after findStrandedConvoys returns.
	counterPath := filepath.Join(binDir, "sling-context-call-count")

	// Mock bd that returns 5 open convoys, each with 1 tracked task. If the
	// hoist regresses, this would produce 5+ sling-context calls instead of
	// the asserted 1.
	bdPath := filepath.Join(binDir, "bd")
	script := `#!/bin/sh
COUNTER="` + counterPath + `"
i=0
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) eval "pos$i=\"$arg\""; i=$((i+1)) ;;
  esac
done

case "$pos0" in
  list)
    # Detect sling-context list query and bump the counter.
    case "$*" in
      *--label=gt:sling-context*)
        echo X >> "$COUNTER"
        echo '[]'
        exit 0
        ;;
    esac
    # Otherwise: convoy listing. Return 5 open convoys.
    echo '[{"id":"hq-c1","title":"Convoy 1"},{"id":"hq-c2","title":"Convoy 2"},{"id":"hq-c3","title":"Convoy 3"},{"id":"hq-c4","title":"Convoy 4"},{"id":"hq-c5","title":"Convoy 5"}]'
    exit 0
    ;;
  sql)
    # bdDepListRawIDs: each convoy tracks one bead.
    case "$*" in
      *"issue_id = 'hq-c1'"*) echo '[{"depends_on_id":"gt-r1"}]';;
      *"issue_id = 'hq-c2'"*) echo '[{"depends_on_id":"gt-r2"}]';;
      *"issue_id = 'hq-c3'"*) echo '[{"depends_on_id":"gt-r3"}]';;
      *"issue_id = 'hq-c4'"*) echo '[{"depends_on_id":"gt-r4"}]';;
      *"issue_id = 'hq-c5'"*) echo '[{"depends_on_id":"gt-r5"}]';;
      *) echo '[]';;
    esac
    exit 0
    ;;
  show)
    # Return generic ready issue details.
    echo '[{"id":"gt-r1","title":"R1","status":"open","issue_type":"task","assignee":"","blocked_by":[],"blocked_by_count":0,"dependencies":[]}]'
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := findStrandedConvoys(townRoot); err != nil {
		t.Fatalf("findStrandedConvoys() error: %v", err)
	}

	data, err := os.ReadFile(counterPath)
	if err != nil {
		// File may not exist if zero calls were made. That would also be a
		// regression (0 calls means scheduling status was never checked) but
		// is at least different from the >1 regression. Surface clearly.
		t.Fatalf("expected at least one bd list --label=gt:sling-context call; "+
			"counter file %s: %v", counterPath, err)
	}
	count := strings.Count(string(data), "X")
	if count != 1 {
		t.Errorf("hoist regression: bd list --label=gt:sling-context "+
			"called %d times for 5 convoys; expected 1 hoisted call. "+
			"This is the gu-c6ua regression check — see gc-jmy04 for "+
			"the full daemon-self-DoS background.", count)
	}
}

// TestFindStrandedConvoys_SingleBdShowScan asserts that one full
// findStrandedConvoys() invocation issues exactly one `bd show` subprocess
// regardless of how many convoys are open.
//
// This is the production correctness check for the gu-mxra hoist: if future
// refactoring re-introduces a per-convoy bd show fan-out, this test catches
// it. Pre-hoist this scan would have spawned O(convoys) `bd show` calls;
// post-hoist there's a single town-wide batch.
//
// Also verifies behavioral equivalence: every tracked bead across every
// convoy must still appear in the resulting strandedConvoyInfo, with the
// correct fresh-details fields populated. (gu-mxra acceptance criterion
// "town-wide batch result matches per-convoy batches concatenated".)
func TestFindStrandedConvoys_SingleBdShowScan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell-script mock test on Windows")
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

	// Counter file: mock bd appends a marker line on every `bd show`
	// invocation that targets work beads (i.e. has positional args).
	// Convoy listing uses `bd list`, not `bd show`, so it doesn't trigger
	// this counter.
	counterPath := filepath.Join(binDir, "show-call-count")

	// Mock bd: 3 convoys with distinct tracked beads (5 total). Pre-hoist
	// would issue 3 `bd show` calls (one per convoy); post-hoist exactly 1.
	bdPath := filepath.Join(binDir, "bd")
	script := `#!/bin/sh
COUNTER="` + counterPath + `"
i=0
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) eval "pos$i=\"$arg\""; i=$((i+1)) ;;
  esac
done

case "$pos0" in
  list)
    case "$*" in
      *--label=gt:sling-context*)
        echo '[]'
        exit 0
        ;;
      *--label=gt:agent*)
        # Worker lookup — return no agents.
        echo '[]'
        exit 0
        ;;
    esac
    # Convoy listing.
    echo '[{"id":"hq-c1","title":"Convoy 1"},{"id":"hq-c2","title":"Convoy 2"},{"id":"hq-c3","title":"Convoy 3"}]'
    exit 0
    ;;
  sql)
    # bdDepListRawIDs: per-convoy tracked-IDs query.
    case "$*" in
      *"issue_id = 'hq-c1'"*) echo '[{"depends_on_id":"gt-r1"},{"depends_on_id":"gt-r2"}]';;
      *"issue_id = 'hq-c2'"*) echo '[{"depends_on_id":"gt-r3"}]';;
      *"issue_id = 'hq-c3'"*) echo '[{"depends_on_id":"gt-r4"},{"depends_on_id":"gt-r5"}]';;
      *) echo '[]';;
    esac
    exit 0
    ;;
  show)
    # Bump the counter for any positional show call.
    echo X >> "$COUNTER"
    # Echo back a minimal record for every requested ID so the batch result
    # populates allDetails for all 5 tracked beads in one call.
    out='['
    sep=''
    for arg in "$@"; do
      case "$arg" in
        gt-*)
          out="${out}${sep}{\"id\":\"${arg}\",\"title\":\"T-${arg}\",\"status\":\"open\",\"issue_type\":\"task\",\"assignee\":\"\",\"labels\":[],\"blocked_by\":[],\"blocked_by_count\":0,\"dependencies\":[]}"
          sep=','
          ;;
      esac
    done
    out="${out}]"
    echo "$out"
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stranded, err := findStrandedConvoys(townRoot)
	if err != nil {
		t.Fatalf("findStrandedConvoys() error: %v", err)
	}

	// Subprocess-fanout assertion.
	data, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("expected at least one bd show call; counter file %s: %v",
			counterPath, err)
	}
	count := strings.Count(string(data), "X")
	if count != 1 {
		t.Errorf("hoist regression: bd show called %d times for 3 convoys "+
			"(5 tracked beads); expected 1 hoisted town-wide batch. This is "+
			"the gu-mxra regression check — pre-hoist scans issued one bd "+
			"show per convoy, contributing the dominant remaining subprocess "+
			"cost in stranded scans.", count)
	}

	// Behavioral-equivalence assertion: every tracked bead from every
	// convoy is still discovered, with title materialized from the
	// town-wide bd show result.
	if len(stranded) != 3 {
		t.Fatalf("expected 3 stranded convoys, got %d", len(stranded))
	}
	wantReady := map[string][]string{
		"hq-c1": {"gt-r1", "gt-r2"},
		"hq-c2": {"gt-r3"},
		"hq-c3": {"gt-r4", "gt-r5"},
	}
	for _, s := range stranded {
		want, ok := wantReady[s.ID]
		if !ok {
			t.Errorf("unexpected convoy id %s", s.ID)
			continue
		}
		if s.TrackedCount != len(want) {
			t.Errorf("convoy %s: TrackedCount = %d, want %d",
				s.ID, s.TrackedCount, len(want))
		}
		if s.ReadyCount != len(want) {
			t.Errorf("convoy %s: ReadyCount = %d, want %d", s.ID, s.ReadyCount, len(want))
		}
		gotSet := make(map[string]bool, len(s.ReadyIssues))
		for _, id := range s.ReadyIssues {
			gotSet[id] = true
		}
		for _, id := range want {
			if !gotSet[id] {
				t.Errorf("convoy %s: missing ready issue %s in %v", s.ID, id, s.ReadyIssues)
			}
		}
	}
}

// TestFindStrandedConvoys_HoistedSetUsesHotSlingContexts verifies that
// when a sling-context exists for a tracked bead, the bead is reported as
// scheduled (not ready-stranded) — exercising the hoisted-set lookup path.
func TestFindStrandedConvoys_HoistedSetUsesHotSlingContexts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell-script mock test on Windows")
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

	// Mock bd: returns 1 convoy that tracks gt-scheduled1 and gt-ready1.
	// gt-scheduled1 has an OPEN sling-context (recent CreatedAt) — should be
	// excluded from ready_issues. gt-ready1 has no context — should be ready.
	now := time.Now().UTC().Format(time.RFC3339)
	slingCtx := fmt.Sprintf(
		`{"id":"hq-cv-ctx1","status":"open","created_at":"%s","description":"{\"work_bead_id\":\"gt-scheduled1\",\"target_rig\":\"gastown\"}","labels":["gt:sling-context"]}`,
		now,
	)

	bdPath := filepath.Join(binDir, "bd")
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
    case "$*" in
      *--label=gt:sling-context*)
        echo '[` + slingCtx + `]'
        exit 0
        ;;
    esac
    echo '[{"id":"hq-cmix1","title":"Mixed convoy"}]'
    exit 0
    ;;
  sql)
    echo '[{"depends_on_id":"gt-scheduled1"},{"depends_on_id":"gt-ready1"}]'
    exit 0
    ;;
  show)
    echo '[{"id":"gt-scheduled1","title":"Sched","status":"open","issue_type":"task","assignee":"","blocked_by":[],"blocked_by_count":0,"dependencies":[]},{"id":"gt-ready1","title":"Ready","status":"open","issue_type":"task","assignee":"","blocked_by":[],"blocked_by_count":0,"dependencies":[]}]'
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stranded, err := findStrandedConvoys(townRoot)
	if err != nil {
		t.Fatalf("findStrandedConvoys() error: %v", err)
	}
	if len(stranded) != 1 {
		t.Fatalf("expected 1 stranded convoy, got %d", len(stranded))
	}

	s := stranded[0]
	if s.TrackedCount != 2 {
		t.Errorf("TrackedCount = %d, want 2", s.TrackedCount)
	}
	// gt-scheduled1 should be filtered out (has open sling context).
	// gt-ready1 should be the only ready issue.
	if s.ReadyCount != 1 {
		t.Errorf("ReadyCount = %d, want 1 (gt-scheduled1 should be filtered as scheduled, gt-ready1 should remain)", s.ReadyCount)
	}
	if len(s.ReadyIssues) != 1 || s.ReadyIssues[0] != "gt-ready1" {
		t.Errorf("ReadyIssues = %v, want [gt-ready1]", s.ReadyIssues)
	}
}

// TestFindStrandedConvoys_StaleSlingContextIgnored verifies that a
// sling-context older than slingContextTTL is ignored (orphan from a
// failed spawn) and the underlying work bead is still reported as ready.
func TestFindStrandedConvoys_StaleSlingContextIgnored(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell-script mock test on Windows")
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

	// Stale context: created 2 × slingContextTTL ago.
	stale := time.Now().Add(-2 * slingContextTTL).UTC().Format(time.RFC3339)
	slingCtx := fmt.Sprintf(
		`{"id":"hq-cv-ctx-stale","status":"open","created_at":"%s","description":"{\"work_bead_id\":\"gt-orphan1\",\"target_rig\":\"gastown\"}","labels":["gt:sling-context"]}`,
		stale,
	)

	bdPath := filepath.Join(binDir, "bd")
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
    case "$*" in
      *--label=gt:sling-context*)
        echo '[` + slingCtx + `]'
        exit 0
        ;;
    esac
    echo '[{"id":"hq-cstale","title":"Stale-context convoy"}]'
    exit 0
    ;;
  sql)
    echo '[{"depends_on_id":"gt-orphan1"}]'
    exit 0
    ;;
  show)
    echo '[{"id":"gt-orphan1","title":"Orphan","status":"open","issue_type":"task","assignee":"","blocked_by":[],"blocked_by_count":0,"dependencies":[]}]'
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stranded, err := findStrandedConvoys(townRoot)
	if err != nil {
		t.Fatalf("findStrandedConvoys() error: %v", err)
	}
	if len(stranded) != 1 {
		t.Fatalf("expected 1 stranded convoy, got %d", len(stranded))
	}
	s := stranded[0]
	// gt-orphan1's stale context should be ignored — it should still be ready.
	if s.ReadyCount != 1 {
		t.Errorf("ReadyCount = %d, want 1 (stale sling context should be ignored, gt-orphan1 should still be ready)", s.ReadyCount)
	}
	if len(s.ReadyIssues) != 1 || s.ReadyIssues[0] != "gt-orphan1" {
		t.Errorf("ReadyIssues = %v, want [gt-orphan1]", s.ReadyIssues)
	}
}

// TestComputeTownWideScheduledSet_FailClosedNilOnNoTownRoot verifies that
// computeTownWideScheduledSet returns nil when invoked from a directory
// outside any Gas Town workspace. Callers in findStrandedConvoys treat this
// nil return as a signal to fall back to per-convoy areScheduled (which
// preserves the legacy fail-closed behavior of treating unknown scheduling
// as "scheduled").
func TestComputeTownWideScheduledSet_FailClosedNilOnNoTownRoot(t *testing.T) {
	// Build a tmpdir that is NOT a Gas Town workspace. workspace.FindFromCwd
	// walks up looking for a town root; in /tmp/<random>/non-town with no
	// .beads, no town.json, no mayor/, it should return ErrNotFound.
	nonTown := t.TempDir()
	innerDir := filepath.Join(nonTown, "non-town")
	if err := os.MkdirAll(innerDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	prevWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevWd) })

	if err := os.Chdir(innerDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	// Clear env vars so FindFromCwd's fallback chain doesn't find an
	// unrelated town via GT_TOWN_ROOT/GT_ROOT.
	t.Setenv("GT_TOWN_ROOT", "")
	t.Setenv("GT_ROOT", "")

	// Pass empty townRoot to force the cwd fallback. From a non-town cwd
	// with cleared env vars, FindFromCwd should return empty, and the
	// helper should return nil so findStrandedConvoys can fall back to
	// per-convoy areScheduled (preserving fail-closed semantics).
	got := computeTownWideScheduledSet("")
	if got != nil {
		t.Errorf("computeTownWideScheduledSet from non-town cwd = %v (len=%d), want nil "+
			"so findStrandedConvoys can fall back to per-convoy areScheduled "+
			"(fail-closed semantic preservation)", got, len(got))
	}
}

// TestFindStrandedConvoys_SingleTrackedDepsSQLScan asserts that one full
// findStrandedConvoys() invocation issues exactly one
// `bd sql ... FROM dependencies ... type = 'tracks'` subprocess
// regardless of how many convoys are open.
//
// This is the production correctness check for the gu-6m38 hoist: the
// per-convoy bdDepListRawIDs call inside getTrackedIssues was the
// dominant cost after gu-c6ua landed (~1s × convoys × scan). Hoisting
// it into a single batched IN-clause query closes the remaining N+1.
//
// Pre-hoist this would have spawned 5 calls; with the hoist it must be 1.
func TestFindStrandedConvoys_SingleTrackedDepsSQLScan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell-script mock test on Windows")
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

	// Counter file: mock bd appends a marker line on every
	// `bd sql ... FROM dependencies ... type = 'tracks'` invocation.
	counterPath := filepath.Join(binDir, "tracked-deps-sql-call-count")

	// Mock bd: 5 open convoys, each tracking one bead. If the hoist
	// regresses, this would produce 5+ tracked-deps SQL calls.
	bdPath := filepath.Join(binDir, "bd")
	script := `#!/bin/sh
COUNTER="` + counterPath + `"
i=0
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) eval "pos$i=\"$arg\""; i=$((i+1)) ;;
  esac
done

case "$pos0" in
  list)
    case "$*" in
      *--label=gt:sling-context*)
        echo '[]'
        exit 0
        ;;
    esac
    echo '[{"id":"hq-c1","title":"Convoy 1"},{"id":"hq-c2","title":"Convoy 2"},{"id":"hq-c3","title":"Convoy 3"},{"id":"hq-c4","title":"Convoy 4"},{"id":"hq-c5","title":"Convoy 5"}]'
    exit 0
    ;;
  sql)
    # Match the tracked-deps query (single-id or batched IN clause).
    case "$*" in
      *"FROM dependencies"*"type = 'tracks'"*)
        echo X >> "$COUNTER"
        # Batched IN-clause path: return one edge per convoy.
        case "$*" in
          *"issue_id IN ("*)
            echo '[{"issue_id":"hq-c1","depends_on_id":"gt-r1"},{"issue_id":"hq-c2","depends_on_id":"gt-r2"},{"issue_id":"hq-c3","depends_on_id":"gt-r3"},{"issue_id":"hq-c4","depends_on_id":"gt-r4"},{"issue_id":"hq-c5","depends_on_id":"gt-r5"}]'
            ;;
          *"issue_id = 'hq-c1'"*) echo '[{"depends_on_id":"gt-r1"}]';;
          *"issue_id = 'hq-c2'"*) echo '[{"depends_on_id":"gt-r2"}]';;
          *"issue_id = 'hq-c3'"*) echo '[{"depends_on_id":"gt-r3"}]';;
          *"issue_id = 'hq-c4'"*) echo '[{"depends_on_id":"gt-r4"}]';;
          *"issue_id = 'hq-c5'"*) echo '[{"depends_on_id":"gt-r5"}]';;
          *) echo '[]';;
        esac
        exit 0
        ;;
    esac
    echo '[]'
    exit 0
    ;;
  show)
    # Generic ready issue details for any of gt-r{1..5}.
    echo '[{"id":"gt-r1","title":"R1","status":"open","issue_type":"task","assignee":"","blocked_by":[],"blocked_by_count":0,"dependencies":[]}]'
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := findStrandedConvoys(townRoot); err != nil {
		t.Fatalf("findStrandedConvoys() error: %v", err)
	}

	data, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("expected exactly one batched tracked-deps SQL call; "+
			"counter file %s: %v", counterPath, err)
	}
	count := strings.Count(string(data), "X")
	if count != 1 {
		t.Errorf("hoist regression: tracked-deps `bd sql ... type = 'tracks'` "+
			"called %d times for 5 convoys; expected 1 batched call. "+
			"This is the gu-6m38 regression check — see gu-6r5k spike "+
			"step 4 for the per-convoy fan-out background.", count)
	}
}

// TestGetAllTrackedIssuesByConvoy_Equivalence verifies that the batched
// helper returns the same per-convoy ID lists that bdDepListRawIDs would
// return for each convoy individually. Acceptance criterion for gu-6m38.
func TestGetAllTrackedIssuesByConvoy_Equivalence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell-script mock test on Windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	bdPath := filepath.Join(binDir, "bd")
	// Mock bd that returns deterministic edges for both single-id and
	// batched IN-clause queries. Three synthetic convoys with varying
	// tracked counts (0, 1, 2 deps) — must match per-convoy results.
	script := `#!/bin/sh
case "$*" in
  *"FROM dependencies"*"issue_id IN ("*"type = 'tracks'"*)
    echo '[{"issue_id":"hq-c1","depends_on_id":"gt-a"},{"issue_id":"hq-c3","depends_on_id":"gt-b"},{"issue_id":"hq-c3","depends_on_id":"gt-c"}]'
    exit 0
    ;;
  *"FROM dependencies"*"issue_id = 'hq-c1'"*"type = 'tracks'"*)
    echo '[{"depends_on_id":"gt-a"}]'
    exit 0
    ;;
  *"FROM dependencies"*"issue_id = 'hq-c2'"*"type = 'tracks'"*)
    echo '[]'
    exit 0
    ;;
  *"FROM dependencies"*"issue_id = 'hq-c3'"*"type = 'tracks'"*)
    echo '[{"depends_on_id":"gt-b"},{"depends_on_id":"gt-c"}]'
    exit 0
    ;;
  *)
    echo '[]'
    exit 0
    ;;
esac
`
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	convoyIDs := []string{"hq-c1", "hq-c2", "hq-c3"}
	batched, err := getAllTrackedIssuesByConvoy(townRoot, convoyIDs)
	if err != nil {
		t.Fatalf("getAllTrackedIssuesByConvoy: %v", err)
	}

	for _, id := range convoyIDs {
		single, err := bdDepListRawIDs(townRoot, id, "down", "tracks")
		if err != nil {
			t.Fatalf("bdDepListRawIDs(%s): %v", id, err)
		}
		got := batched[id]
		if len(got) != len(single) {
			t.Errorf("convoy %s: batched=%v single=%v (length mismatch)", id, got, single)
			continue
		}
		// Order may differ; compare as sets.
		gotSet := map[string]bool{}
		for _, x := range got {
			gotSet[x] = true
		}
		for _, x := range single {
			if !gotSet[x] {
				t.Errorf("convoy %s: batched missing %q (got=%v single=%v)", id, x, got, single)
			}
		}
	}
}
