package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestRunBeadMove_RoutesCreateAndCloseToCorrectRigs is the regression test for
// gu-sycrn.
//
// Bug: runBeadMove created the destination bead with `bd create --prefix <tg>`
// and closed the source with `bd close <src>` using bare exec.Command — no
// working directory or BEADS_DIR. Since beads v0.62 dropped cross-rig routing,
// --prefix only sets the prefix in the LOCAL (cwd) database, so the new bead
// landed in the wrong rig DB and the source close ran against the wrong DB too.
//
// Fix: pin `bd create` (and any cleanup close) to the TARGET rig's directory
// via resolveBeadDirForPrefix, and route `bd close <src>` to the SOURCE rig's
// directory via resolveBeadDir — exactly as the `bd show <src>` call does.
//
// This test stubs `bd` to assert each invocation runs in the expected rig dir
// with the expected BEADS_DIR, failing the command (and thus the test) on a
// mismatch.
func TestRunBeadMove_RoutesCreateAndCloseToCorrectRigs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, expectedWD := makeRoutingTownWorkspace(t)

	routes := `{"prefix":"sb-","path":"sourcerig"}
{"prefix":"tg-","path":"targetrig"}
`
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
	for _, sub := range []string{"sourcerig", "targetrig"} {
		if err := os.MkdirAll(filepath.Join(townRoot, sub, ".beads"), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	chdirConvoyTest(t, townRoot)
	// An inherited BEADS_DIR must not leak into the routed commands.
	t.Setenv("BEADS_DIR", "/wrong/.beads")

	sourceDir := filepath.Join(expectedWD, "sourcerig")
	targetDir := filepath.Join(expectedWD, "targetrig")

	scriptBody := fmt.Sprintf(`
# Allow-stale version probe is exempt from the routing checks.
if [ "$*" = "--allow-stale version" ]; then
  exit 0
fi

case "$1" in
  show)
    if [ "$PWD" != "%[1]s" ]; then
      echo "show: expected source rig dir %[1]s, got $PWD" >&2
      exit 1
    fi
    if [ "$BEADS_DIR" != "%[1]s/.beads" ]; then
      echo "show: expected stripped/pinned BEADS_DIR, got $BEADS_DIR" >&2
      exit 1
    fi
    echo '[{"id":"sb-xyz","title":"Move me","status":"open","issue_type":"task","priority":2}]'
    ;;
  create)
    if [ "$PWD" != "%[2]s" ]; then
      echo "create: expected target rig dir %[2]s, got $PWD" >&2
      exit 1
    fi
    if [ "$BEADS_DIR" != "%[2]s/.beads" ]; then
      echo "create: expected pinned target BEADS_DIR, got $BEADS_DIR" >&2
      exit 1
    fi
    echo 'tg-new1'
    ;;
  close)
    if [ "$PWD" != "%[1]s" ]; then
      echo "close: expected source rig dir %[1]s, got $PWD" >&2
      exit 1
    fi
    if [ "$BEADS_DIR" != "%[1]s/.beads" ]; then
      echo "close: expected pinned source BEADS_DIR, got $BEADS_DIR" >&2
      exit 1
    fi
    exit 0
    ;;
  *)
    echo "unexpected bd args: $*" >&2
    exit 1
    ;;
esac
`, sourceDir, targetDir)
	writeRoutingBdStub(t, scriptBody)

	oldDryRun := beadMoveDryRun
	beadMoveDryRun = false
	t.Cleanup(func() { beadMoveDryRun = oldDryRun })

	_, err := captureConvoyStdoutErr(t, func() error {
		return runBeadMove(nil, []string{"sb-xyz", "tg-"})
	})
	if err != nil {
		t.Fatalf("runBeadMove: %v", err)
	}
}
