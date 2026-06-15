package cmd

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestRunConvoyScheduleByID_SkipsDeferred is the regression gate for the
// gu-pi35l convoy-driven re-attach bypass. The df7da9bf fix added the deferred
// guard to runSling / executeSling / scheduleBead / the stranded scan, but NOT
// to runConvoyScheduleByID's candidate selection. So `gt sling <convoy>` (the
// convoy re-attach path) selected a DEFERRED tracked bead as a candidate and
// re-attached a fresh molecule every cycle (observed on gu-n5dvk: status=DEFERRED
// with a future defer_until, yet polecats were dispatched idle-clean).
//
// This test drives runConvoyScheduleByID in DryRun mode over a convoy tracking
// two beads — one status=deferred and one open — and asserts ONLY the open bead
// is a schedule candidate while the deferred bead is skipped.
func TestRunConvoyScheduleByID_SkipsDeferred(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, expectedWD := makeRoutingTownWorkspace(t)
	chdirConvoyTest(t, townRoot)

	// Route the gt- prefix to a rig so resolveRigForBead resolves a non-empty
	// rig name (the first path segment) for the open candidate. The deferred
	// bead is filtered before rig resolution, so its rig need not resolve.
	writeTestRoutes(t, townRoot, []beads.Route{
		{Prefix: "gt-", Path: "testrig/rig"},
	})

	const convoyID = "hq-cv-defer"
	const openBead = "gt-open01"
	const deferredBead = "gt-defer1"

	// bd stub: handle the subcommands runConvoyScheduleByID (DryRun) reaches —
	// verifyBeadExists (show convoy), getTrackedIssues (sql dep tracks + batch
	// show), and areScheduled (sql/list). The deferred bead carries
	// status="deferred" so the new guard filters it before the rig-resolution
	// and scheduling steps.
	scriptBody := fmt.Sprintf(`
if [ "$*" = "--allow-stale version" ]; then
  exit 0
fi

# Find the subcommand (skip global flags).
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done

case "$cmd" in
  sql)
    # dep tracks query for the convoy → return the two tracked bead IDs.
    # areScheduled also uses sql; return no scheduled contexts for it.
    for arg in "$@"; do
      case "$arg" in
        *dependencies*) echo '[{"target":"%s"},{"target":"%s"}]'; exit 0 ;;
        *sling_context*|*WorkBeadID*) echo '[]'; exit 0 ;;
      esac
    done
    echo '[]'
    exit 0
    ;;
  show)
    # Convoy verify/show.
    case "$*" in
      *%s*) echo '[{"id":"%s","title":"Defer convoy","status":"open","issue_type":"convoy","dependencies":[]}]'; exit 0 ;;
    esac
    # Batch issue details for the tracked beads: one open, one deferred.
    echo '[{"id":"%s","title":"Open work","status":"open","issue_type":"task"},{"id":"%s","title":"Deferred work","status":"deferred","issue_type":"task"}]'
    exit 0
    ;;
  list)
    echo '[]'
    exit 0
    ;;
  dep)
    echo '[]'
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`, openBead, deferredBead, convoyID, convoyID, openBead, deferredBead)
	writeRoutingBdStub(t, scriptBody)
	_ = expectedWD

	out, err := captureConvoyStdoutErr(t, func() error {
		return runConvoyScheduleByID(convoyID, convoyScheduleOpts{DryRun: true})
	})
	if err != nil {
		t.Fatalf("runConvoyScheduleByID: %v", err)
	}

	// The deferred bead must be skipped, the open bead must be a candidate.
	if strings.Contains(out, deferredBead) {
		t.Errorf("deferred bead %s should be skipped, but appears in schedule output:\n%s", deferredBead, out)
	}
	if !strings.Contains(out, openBead) {
		t.Errorf("open bead %s should be a candidate, but is absent from output:\n%s", openBead, out)
	}
	if !strings.Contains(out, "1 deferred") {
		t.Errorf("expected the skip summary to report '1 deferred', got:\n%s", out)
	}
}
