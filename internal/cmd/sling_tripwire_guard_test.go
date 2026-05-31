package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestExecuteSling_TripwireBead verifies that executeSling rejects beads
// labeled do-not-dispatch / pinned (gs-9ct, follow-up to hq-9jeyo). These are
// permanent reference/gate tripwires that must stay OPEN, never hooked. The
// earlier hq-9jeyo guards (scheduleBead, isScheduledWorkBeadReady) miss the
// other paths that funnel through executeSling — direct `gt sling`, deacon /
// relay-convoy redispatch, batch sling — which is how lb-rtjr.12 kept getting
// hooked despite the labels (hq-xulzo).
func TestExecuteSling_TripwireBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	bdShow := func(labels string) string {
		return `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"gate tripwire — keep open","status":"open","assignee":"","description":"","labels":` + labels + `}]'
    ;;
esac
exit 0
`
	}

	cases := []struct {
		name   string
		labels string
		force  bool
	}{
		{"do-not-dispatch", `["do-not-dispatch"]`, false},
		{"pinned", `["pinned"]`, false},
		{"force does not bypass", `["do-not-dispatch","pinned"]`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			townRoot := t.TempDir()
			if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
				t.Fatal(err)
			}
			binDir := filepath.Join(townRoot, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			writeBDStub(t, binDir, bdShow(tc.labels), "")
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			result, err := executeSling(SlingParams{
				BeadID:   "trip-1",
				RigName:  "testrig",
				TownRoot: townRoot,
				Force:    tc.force,
			})
			if err == nil {
				t.Fatal("expected error slinging a tripwire bead, got nil")
			}
			if result.ErrMsg != "do-not-dispatch" {
				t.Errorf("ErrMsg = %q, want do-not-dispatch", result.ErrMsg)
			}
			if !strings.Contains(err.Error(), "do-not-dispatch / pinned reference tripwire") {
				t.Errorf("error should name the tripwire: %v", err)
			}
		})
	}
}

// TestExecuteSling_ReferenceTypeBead verifies the issue_type=reference prong of
// the tripwire guard (a bead can be a tripwire by type even without the labels).
func TestExecuteSling_ReferenceTypeBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bdScript := `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"reference doc","status":"open","assignee":"","description":"","issue_type":"reference","labels":[]}]'
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := executeSling(SlingParams{BeadID: "ref-1", RigName: "testrig", TownRoot: townRoot})
	if err == nil || !strings.Contains(err.Error(), "tripwire") {
		t.Errorf("issue_type=reference must be rejected as a tripwire, got: %v", err)
	}
}

// TestExecuteSling_RealTaskPassesTripwireGuard is the negative case: an ordinary
// task with unrelated labels must not be mis-classified as a tripwire.
func TestExecuteSling_RealTaskPassesTripwireGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bdScript := `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"Fix parser NPE","status":"open","assignee":"","description":"","labels":["priority-high","gt:task"]}]'
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := executeSling(SlingParams{BeadID: "real-1", RigName: "nonexistent-rig", TownRoot: townRoot})
	if err != nil && strings.Contains(err.Error(), "tripwire") {
		t.Errorf("ordinary task must pass the tripwire guard, got: %v", err)
	}
}
