package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func TestRunCurioPatrolCheck_InactivePatrol(t *testing.T) {
	// When curio patrol is inactive, the check should return an empty result.
	d := &Daemon{
		patrolConfig: nil, // nil config = curio disabled (opt-in)
	}

	result := d.runCurioPatrolCheck()

	if result.NeedsAttention {
		t.Error("inactive patrol should not need attention")
	}
	if result.Result.RulesRun != 0 {
		t.Errorf("RulesRun = %d, want 0 for inactive patrol", result.Result.RulesRun)
	}
}

func TestRunCurioPatrolCheck_NoAdmissions(t *testing.T) {
	// When curio is active but there are no admission reservations, the check
	// should complete cleanly with zero findings.
	townRoot := t.TempDir()

	// Create the runtime dir so the admission collector doesn't error
	if err := os.MkdirAll(filepath.Join(townRoot, ".runtime", "polecat-admission"), 0755); err != nil {
		t.Fatal(err)
	}
	// Create an empty events.jsonl so the rate collector doesn't error
	if err := os.WriteFile(filepath.Join(townRoot, ".events.jsonl"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	// Create daemon log dir for kill-signal collector
	if err := os.MkdirAll(filepath.Join(townRoot, "daemon"), 0755); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{
		config:       &Config{TownRoot: townRoot},
		patrolConfig: &DaemonPatrolConfig{Patrols: &PatrolsConfig{Curio: &CurioConfig{Enabled: true}}},
		logger:       log.New(io.Discard, "", 0),
	}

	result := d.runCurioPatrolCheck()

	if result.NeedsAttention {
		t.Error("no admissions should mean no attention needed")
	}
	if result.Result.RulesRun == 0 {
		t.Error("RulesRun should be > 0 when patrol is active")
	}
}

func TestRunCurioPatrolCheck_WithDeadAdmission(t *testing.T) {
	// When there's a dead-owner admission, the check should find it.
	townRoot := t.TempDir()

	// Set up admission dir with a dead-PID reservation
	admDir := filepath.Join(townRoot, ".runtime", "polecat-admission")
	if err := os.MkdirAll(admDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Use PID 999999999 (almost certainly dead)
	reservation := `{"id":"test-res","pid":999999999,"rig":"test_rig"}`
	if err := os.WriteFile(filepath.Join(admDir, "test-res.json"), []byte(reservation), 0644); err != nil {
		t.Fatal(err)
	}
	// Create empty events.jsonl
	if err := os.WriteFile(filepath.Join(townRoot, ".events.jsonl"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	// Create daemon log dir
	if err := os.MkdirAll(filepath.Join(townRoot, "daemon"), 0755); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{
		config:       &Config{TownRoot: townRoot},
		patrolConfig: &DaemonPatrolConfig{Patrols: &PatrolsConfig{Curio: &CurioConfig{Enabled: true}}},
		logger:       log.New(io.Discard, "", 0),
	}

	result := d.runCurioPatrolCheck()

	if !result.NeedsAttention {
		t.Error("dead-owner admission should trigger NeedsAttention")
	}
	if len(result.Result.Findings) == 0 {
		t.Error("expected at least 1 verified finding for dead PID")
	}
	if result.Recommendation == "" {
		t.Error("recommendation should be non-empty when findings exist")
	}
}
