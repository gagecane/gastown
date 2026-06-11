package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeRoutesForRigTest writes a routes.jsonl mapping bead prefixes to rigs so
// that beads.GetRigNameForPrefix / IsKnownRig resolve in convoyRig.
func writeRoutesForRigTest(t *testing.T, townRoot string) {
	t.Helper()
	routes := `{"prefix":"casw-","path":"casc_webapp/refinery/rig"}
{"prefix":"gt-","path":"gastown_upstream/refinery/rig"}
`
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
}

// TestConvoyRig_DerivesFromFirstTrackedBeadPrefix verifies that convoyRig
// resolves a convoy's rig from the prefix of its first tracked bead when the
// convoy carries no gt:rig: label override.
func TestConvoyRig_DerivesFromFirstTrackedBeadPrefix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, _ := makeRoutingTownWorkspace(t)
	writeRoutesForRigTest(t, townRoot)
	chdirConvoyTest(t, townRoot)

	scriptBody := `
case "$*" in
  "--allow-stale version") exit 0 ;;
  *sql*dependencies*) echo '[{"target":"casw-y66"}]' ;;
  *) echo "unexpected bd args: $*" >&2; exit 1 ;;
esac
`
	writeRoutingBdStub(t, scriptBody)

	got := convoyRig(townRoot, convoyListIssue{ID: "hq-cv-qv7iy", Labels: []string{"gt:convoy"}})
	if got != "casc_webapp" {
		t.Fatalf("convoyRig = %q, want %q", got, "casc_webapp")
	}
}

// TestConvoyRig_HonorsLabelOverride verifies that a gt:rig:<name> label on the
// convoy outranks prefix-based resolution when it names a real rig.
func TestConvoyRig_HonorsLabelOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, _ := makeRoutingTownWorkspace(t)
	writeRoutesForRigTest(t, townRoot)
	chdirConvoyTest(t, townRoot)

	// No bd stub needed: the label override short-circuits before any tracked
	// bead lookup. Use a stub that fails loudly if it is ever called.
	writeRoutingBdStub(t, `echo "bd should not be called" >&2; exit 1`)

	got := convoyRig(townRoot, convoyListIssue{
		ID:     "hq-cv-x",
		Labels: []string{"gt:convoy", "gt:rig:casc_webapp"},
	})
	if got != "casc_webapp" {
		t.Fatalf("convoyRig = %q, want %q", got, "casc_webapp")
	}
}

// TestConvoyRig_EmptyForTownLevelOnly verifies convoyRig returns "" when no
// tracked bead prefix maps to a rig (e.g. a convoy tracking only town beads).
func TestConvoyRig_EmptyForTownLevelOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, _ := makeRoutingTownWorkspace(t)
	writeRoutesForRigTest(t, townRoot)
	chdirConvoyTest(t, townRoot)

	scriptBody := `
case "$*" in
  "--allow-stale version") exit 0 ;;
  *sql*dependencies*) echo '[{"target":"hq-only"}]' ;;
  *) echo "unexpected bd args: $*" >&2; exit 1 ;;
esac
`
	writeRoutingBdStub(t, scriptBody)

	got := convoyRig(townRoot, convoyListIssue{ID: "hq-cv-town", Labels: []string{"gt:convoy"}})
	if got != "" {
		t.Fatalf("convoyRig = %q, want empty", got)
	}
}

// TestRunConvoyList_RigFilterAndJSONField verifies the JSON output carries a
// top-level `rig` field and that --rig filters convoys to a single rig.
func TestRunConvoyList_RigFilterAndJSONField(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, _ := makeRoutingTownWorkspace(t)
	writeRoutesForRigTest(t, townRoot)
	chdirConvoyTest(t, townRoot)

	// Two convoys: one tracking a casw- bead (casc_webapp), one tracking a gt-
	// bead (gastown_upstream).
	scriptBody := `
case "$*" in
  "--allow-stale version") exit 0 ;;
  "list --label=gt:convoy --json --limit=0 --all --flat")
    echo '[{"id":"hq-cv-casw","title":"casw convoy","status":"open","created_at":"2026-03-09T00:00:00Z","labels":["gt:convoy"]},{"id":"hq-cv-gt","title":"gt convoy","status":"open","created_at":"2026-03-09T00:00:00Z","labels":["gt:convoy"]}]'
    ;;
  "list --json --limit=0 --all --flat") echo '[]' ;;
  *sql*dependencies*WHERE*hq-cv-casw*) echo '[{"target":"casw-y66"}]' ;;
  *sql*dependencies*WHERE*hq-cv-gt*) echo '[{"target":"gt-123"}]' ;;
  "show casw-y66 --json") echo '[{"id":"casw-y66","title":"casw bead","status":"open","issue_type":"task"}]' ;;
  "show gt-123 --json") echo '[{"id":"gt-123","title":"gt bead","status":"open","issue_type":"task"}]' ;;
  *workers*|*worker*) echo '[]' ;;
  *) echo '[]' ;;
esac
`
	writeRoutingBdStub(t, scriptBody)

	oldJSON, oldAll, oldStatus, oldTree, oldRig := convoyListJSON, convoyListAll, convoyListStatus, convoyListTree, convoyListRig
	convoyListJSON = true
	convoyListAll = true
	convoyListStatus = ""
	convoyListTree = false
	convoyListRig = "casc_webapp"
	t.Cleanup(func() {
		convoyListJSON, convoyListAll, convoyListStatus, convoyListTree, convoyListRig = oldJSON, oldAll, oldStatus, oldTree, oldRig
	})

	out, err := captureConvoyStdoutErr(t, func() error {
		return runConvoyList(nil, nil)
	})
	if err != nil {
		t.Fatalf("runConvoyList: %v", err)
	}

	var entries []struct {
		ID  string `json:"id"`
		Rig string `json:"rig"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("unmarshal output: %v\noutput:\n%s", err, out)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 convoy after --rig filter, got %d:\n%s", len(entries), out)
	}
	if entries[0].ID != "hq-cv-casw" {
		t.Fatalf("filtered convoy ID = %q, want hq-cv-casw", entries[0].ID)
	}
	if entries[0].Rig != "casc_webapp" {
		t.Fatalf("convoy rig field = %q, want casc_webapp", entries[0].Rig)
	}
	if strings.Contains(out, "hq-cv-gt") {
		t.Fatalf("--rig filter leaked the gastown convoy:\n%s", out)
	}
}
