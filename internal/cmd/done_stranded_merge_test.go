package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubBdRecordingCloseAndLabel logs every bd close + bd update --add-label
// + bd note add invocation to the closes log. Used to assert what
// updateAgentStateOnDone did (or did not) do to the hooked bead.
func stubBdRecordingCloseAndLabel() string {
	return `#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nitro)
        echo '[{"id":"gt-gastown-polecat-nitro","title":"Polecat nitro","status":"open","hook_bead":"gt-base-123","agent_state":"working"}]'
        ;;
      gt-base-123)
        echo '[{"id":"gt-base-123","title":"Base bead","status":"in_progress","description":"some description"}]'
        ;;
    esac
    ;;
  list)
    echo '[]'
    ;;
  close)
    ids=""
    for arg in "$@"; do
      case "$arg" in
        --*) continue;;
      esac
      ids="$ids $arg"
    done
    for id in $ids; do
      echo "close $id" >> "__CLOSES_LOG__"
    done
    ;;
  update)
    # Capture --add-label=<label> and --defer=...
    target=""
    add_label=""
    defer_val=""
    for arg in "$@"; do
      case "$arg" in
        --add-label=*) add_label="${arg#--add-label=}";;
        --defer=*) defer_val="${arg#--defer=}";;
        --*) continue;;
        *) [ -z "$target" ] && target="$arg";;
      esac
    done
    if [ -n "$add_label" ]; then
      echo "label $target $add_label" >> "__CLOSES_LOG__"
    fi
    if [ -n "$defer_val" ]; then
      echo "defer $target $defer_val" >> "__CLOSES_LOG__"
    fi
    ;;
  note)
    # bd note <id> <text...>
    target="$1"
    if [ -n "$target" ]; then
      echo "note $target" >> "__CLOSES_LOG__"
    fi
    ;;
  agent|slot|comments)
    exit 0
    ;;
esac
exit 0
`
}

// TestDoneStrandedMerge_RefusesToCloseHookedBead reproduces the gu-rh0g
// Pattern B bug. When a polecat reaches the hooked-bead close path with
// stranded=true (push or MR step failed), updateAgentStateOnDone must:
//   - NOT issue `bd close` against the hooked bead
//   - DO add the `stranded-merge` label so audits catch it
//   - DO add an audit note explaining why
//
// Before the fix, a stranded polecat exit closed the hooked bead with
// reason "Completed via gt done (exit=COMPLETED)" — falsely reporting
// shipped work even when the commits never reached origin/main. (gu-rh0g)
func TestDoneStrandedMerge_RefusesToCloseHookedBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}
	withFastCloseBackoff(t)

	townRoot, closesLog := setupCloseHookedBeadTestEnv(t, stubBdRecordingCloseAndLabel())

	// Stranded=true: simulates push/MR failure.
	updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitCompleted, "gt-base-123", true, false, "", "")

	logBytes, err := os.ReadFile(closesLog)
	log := ""
	if err == nil {
		log = string(logBytes)
	}

	// Must NOT have issued `bd close gt-base-123`.
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, "close gt-base-123") {
			t.Errorf("stranded merge incorrectly closed hooked bead — gu-rh0g regression. log:\n%s", log)
		}
	}

	// Must have added the stranded-merge label.
	if !strings.Contains(log, "label gt-base-123 stranded-merge") {
		t.Errorf("expected `stranded-merge` label on hooked bead, log:\n%s", log)
	}

	// Must have left an audit note.
	if !strings.Contains(log, "note gt-base-123") {
		t.Errorf("expected audit note on hooked bead, log:\n%s", log)
	}
}

// TestDoneStrandedMerge_FalseDoesNotChangeBehavior verifies the fix is
// gated correctly: when stranded=false (the normal happy path), the
// hooked-bead close still happens. Without this, a regression in the
// guard would silently break every successful gt done. (gu-rh0g)
func TestDoneStrandedMerge_FalseDoesNotChangeBehavior(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}
	withFastCloseBackoff(t)

	townRoot, closesLog := setupCloseHookedBeadTestEnv(t, stubBdRecordingCloseAndLabel())

	// stranded=false: normal happy-path completion.
	updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitCompleted, "gt-base-123", false, false, "", "")

	logBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("read closes log: %v", err)
	}
	log := string(logBytes)

	if !strings.Contains(log, "close gt-base-123") {
		t.Errorf("non-stranded ExitCompleted should still close hooked bead, log:\n%s", log)
	}
	if strings.Contains(log, "label gt-base-123 stranded-merge") {
		t.Errorf("non-stranded path must NOT add stranded-merge label, log:\n%s", log)
	}
}
