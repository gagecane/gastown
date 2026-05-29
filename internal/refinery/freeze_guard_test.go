package refinery

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/ciwatcher"
	"github.com/steveyegge/gastown/internal/rig"
)

// makeFrozenEngineer returns an Engineer rooted under a temp townRoot whose
// rig directory is townRoot/<rigName>. The freeze flag location is
// derived as filepath.Dir(rig.Path), matching the production layout
// (TownRoot/<rig>/).
func makeFrozenEngineer(t *testing.T, rigName string) (*Engineer, string, *bytes.Buffer) {
	t.Helper()
	town := t.TempDir()
	rigDir := filepath.Join(town, rigName)
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &rig.Rig{Name: rigName, Path: rigDir}
	// Bypass NewEngineer's git/clone discovery — we only exercise CheckMQFreeze.
	e := &Engineer{
		rig:    r,
		output: &bytes.Buffer{},
	}
	return e, town, e.output.(*bytes.Buffer)
}

func TestCheckMQFreeze_NoFlag(t *testing.T) {
	e, _, _ := makeFrozenEngineer(t, "alpha")
	frozen, _ := e.CheckMQFreeze()
	if frozen {
		t.Errorf("expected not frozen on clean town")
	}
}

func TestCheckMQFreeze_Frozen(t *testing.T) {
	e, town, buf := makeFrozenEngineer(t, "alpha")

	if err := ciwatcher.WriteFreeze(town, ciwatcher.FreezeFile{
		Rig:       "alpha",
		Reason:    "broke-main-ci: gu-aaa",
		BeadID:    "gu-aaa",
		RunID:     "100",
		RunURL:    "https://example.test/run/100",
		CommitSHA: "deadbeef",
	}); err != nil {
		t.Fatal(err)
	}

	frozen, res := e.CheckMQFreeze()
	if !frozen {
		t.Fatal("expected frozen=true")
	}
	if !res.NoMerge {
		t.Errorf("expected NoMerge=true, got %+v", res)
	}
	if !strings.Contains(res.Error, "mq-frozen") {
		t.Errorf("expected mq-frozen in error, got %q", res.Error)
	}
	if !strings.Contains(buf.String(), "FROZEN") {
		t.Errorf("expected FROZEN log line, got %q", buf.String())
	}
}

func TestCheckMQFreeze_RigIsolation(t *testing.T) {
	// Freeze on alpha must not affect beta.
	town := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(town, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := ciwatcher.WriteFreeze(town, ciwatcher.FreezeFile{Rig: "alpha", Reason: "x"}); err != nil {
		t.Fatal(err)
	}

	betaRig := &rig.Rig{Name: "beta", Path: filepath.Join(town, "beta")}
	e := &Engineer{rig: betaRig, output: &bytes.Buffer{}}
	frozen, _ := e.CheckMQFreeze()
	if frozen {
		t.Errorf("freeze on alpha should not freeze beta")
	}
}

func TestCheckMQFreeze_EmptyMetadata(t *testing.T) {
	e, town, _ := makeFrozenEngineer(t, "alpha")
	// Minimal freeze (no bead/run IDs) must still trigger.
	if err := ciwatcher.WriteFreeze(town, ciwatcher.FreezeFile{Rig: "alpha"}); err != nil {
		t.Fatal(err)
	}
	frozen, res := e.CheckMQFreeze()
	if !frozen || !res.NoMerge {
		t.Errorf("freeze with bare metadata should still block: frozen=%v res=%+v", frozen, res)
	}
}
