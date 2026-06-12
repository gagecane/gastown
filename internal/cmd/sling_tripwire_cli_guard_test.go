package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
)

// TestRunSlingRejectsReferenceTripwireOnNonRigTarget verifies the gu-nid89.32
// fix on the path it actually protects: a non-rig target (e.g.
// `gt sling <id> <rig>/polecats/<name>`) flows through the inline runSling
// dispatch path, NOT executeSling. That inline guard sequence jumped from the
// awaiting-merge guard straight to the open-children guard, so a reference /
// tripwire bead — do-not-dispatch / pinned label, or issue_type=reference —
// reached resolveTarget and got hooked to a polecat that would then CLOSE it,
// taking down a safety gate meant to stay OPEN forever. The fix adds the
// reference-tripwire guard to that sequence; this test asserts the refusal
// fires before any target resolution or polecat spawn.
func TestRunSlingRejectsReferenceTripwireOnNonRigTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	cases := []struct {
		name     string
		showJSON string
		force    bool
	}{
		{
			name:     "do-not-dispatch label",
			showJSON: `[{"title":"gate tripwire — keep open","status":"open","assignee":"","description":"","labels":["do-not-dispatch"]}]`,
		},
		{
			name:     "pinned label",
			showJSON: `[{"title":"gate tripwire — keep open","status":"open","assignee":"","description":"","labels":["pinned"]}]`,
		},
		{
			name:     "issue_type reference",
			showJSON: `[{"title":"reference doc — keep open","status":"open","assignee":"","description":"","issue_type":"reference","labels":[]}]`,
		},
		{
			name:     "force does not bypass",
			showJSON: `[{"title":"gate tripwire — keep open","status":"open","assignee":"","description":"","labels":["do-not-dispatch","pinned"]}]`,
			force:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			townRoot := t.TempDir()
			if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0o755); err != nil {
				t.Fatalf("mkdir mayor/rig: %v", err)
			}
			if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"version":1}`), 0o644); err != nil {
				t.Fatalf("write town marker: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(townRoot, "gastown", "mayor", "rig"), 0o755); err != nil {
				t.Fatalf("mkdir gastown mayor rig: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
				t.Fatalf("mkdir .beads: %v", err)
			}
			if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(`{"prefix":"gt-","path":"gastown/mayor/rig"}`+"\n"), 0o644); err != nil {
				t.Fatalf("write routes: %v", err)
			}
			rigs := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{
				"gastown": {GitURL: "git@github.com:test/gastown.git", AddedAt: time.Now().Truncate(time.Second), BeadsConfig: &config.BeadsConfig{Repo: "local", Prefix: "gt-"}},
			}}
			if err := config.SaveRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"), rigs); err != nil {
				t.Fatalf("SaveRigsConfig: %v", err)
			}

			binDir := filepath.Join(townRoot, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatalf("mkdir binDir: %v", err)
			}
			// bd stub: `show` returns the tripwire bead; any mutation (update/mol/cook)
			// is a side-effect that must never run because the guard fires first.
			bdScript := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    show) echo '` + tc.showJSON + `'; exit 0 ;;
    update|mol|cook) echo "unexpected side effect: $arg" >&2; exit 2 ;;
  esac
done
exit 0
`
			_ = writeBDStub(t, binDir, bdScript, "")
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv(EnvGTRole, "mayor")
			t.Setenv("GT_TEST_NO_NUDGE", "1")
			t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")

			cwd, err := os.Getwd()
			if err != nil {
				t.Fatalf("getwd: %v", err)
			}
			t.Cleanup(func() { _ = os.Chdir(cwd) })
			if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
				t.Fatalf("chdir: %v", err)
			}

			prevNoConvoy := slingNoConvoy
			prevNoBoot := slingNoBoot
			prevForce := slingForce
			prevSpawn := spawnPolecatForSling
			prevResolveTargetAgent := resolveTargetAgentFn
			t.Cleanup(func() {
				slingNoConvoy = prevNoConvoy
				slingNoBoot = prevNoBoot
				slingForce = prevForce
				spawnPolecatForSling = prevSpawn
				resolveTargetAgentFn = prevResolveTargetAgent
			})
			slingNoConvoy = true
			slingNoBoot = true
			slingForce = tc.force

			// Fail loudly if dispatch ever reaches target resolution: the guard
			// must reject the tripwire before any of these run.
			spawnCalled := false
			spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
				spawnCalled = true
				return &SpawnedPolecatInfo{RigName: rigName, PolecatName: "toast", ClonePath: filepath.Join(townRoot, "fake-polecat")}, nil
			}
			resolveCalled := false
			resolveTargetAgentFn = func(target string) (agentID string, pane string, hookRoot string, err error) {
				resolveCalled = true
				return "gastown/polecats/toast", "%1", filepath.Join(townRoot, "fake-polecat"), nil
			}

			err = runSling(nil, []string{"gt-trip1", "gastown/polecats/toast"})
			if err == nil {
				t.Fatal("expected runSling to refuse a reference/tripwire bead, got nil")
			}
			if !strings.Contains(err.Error(), "reference tripwire") {
				t.Fatalf("error should name the tripwire guard, got: %v", err)
			}
			if resolveCalled {
				t.Fatal("resolveTargetAgentFn was called — the tripwire guard must reject before target resolution")
			}
			if spawnCalled {
				t.Fatal("spawnPolecatForSling was called — the tripwire guard must reject before any polecat spawn")
			}
		})
	}
}
