package crew

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// stubBD is a no-op `bd` shell script installed on PATH for the crew test
// binary. Read commands return an empty JSON array (so best-effort agent-bead
// lookups resolve to "not found"); everything else exits 0 without contacting
// any server.
const stubBD = `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    show|list|ready) printf '[]\n'; exit 0 ;;
  esac
done
exit 0
`

// TestMain installs the no-op bd stub so crew tests never write agent beads to a
// real Dolt server.
//
// crew.Manager.Add/Remove/Rename perform best-effort agent-bead upserts via the
// real `bd` CLI (Manager.upsertAgentBead). The crew tests construct a Manager
// against a bare temp rig with no GT_DOLT_PORT and no routes.jsonl, so those bd
// calls fall back to the production Dolt server on port 3307 and create orphan
// databases named after the default "gt" prefix — e.g. the literal "gt"
// database that accumulated gt-test-rig-crew-bob, created once per Add across
// several tests (bead gs-z76). Non-namespaced orphans trip deacon's safety
// gate. The crew tests assert only on directory and git state, never on bead
// contents, so stubbing bd fully isolates them without Docker or a test
// container.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	// The stub is a POSIX shell script; skip it on Windows and leave prior
	// behavior intact (the leak incident is Linux-only).
	if runtime.GOOS != "windows" {
		if dir, err := os.MkdirTemp("", "crew-stub-bd-*"); err == nil {
			defer os.RemoveAll(dir)
			if err := os.WriteFile(filepath.Join(dir, "bd"), []byte(stubBD), 0o755); err == nil {
				os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			}
		}
	}
	return m.Run()
}
