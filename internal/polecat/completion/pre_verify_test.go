package completion

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/polecat"
)

// writePreVerifyRigSettings is a tiny helper that writes settings/config.json with the
// supplied MergeQueueConfig, mirroring how loadPreVerifyGates resolves
// rig-local overrides.
func writePreVerifyRigSettings(t *testing.T, townRoot, rigName string, mq *config.MergeQueueConfig) {
	t.Helper()
	settingsDir := filepath.Join(townRoot, rigName, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}
	data, err := json.Marshal(&config.RigSettings{MergeQueue: mq})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

// TestLoadPreVerifyGates_FiltersAndSorts verifies that loadPreVerifyGates
// returns only pre-merge gates (empty phase counted as pre-merge),
// excludes post-squash gates, drops gates with empty Cmd, and emits a
// stable alphabetical order so the polecat sees the same gate sequence
// twice (matching loadRigCommandVars).
func TestLoadPreVerifyGates_FiltersAndSorts(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"
	writePreVerifyRigSettings(t, townRoot, rigName, &config.MergeQueueConfig{
		Gates: map[string]*config.GateConfig{
			"vet":       {Cmd: "go vet ./..."},                          // empty phase = pre-merge
			"lint":      {Cmd: "golangci-lint run", Phase: "pre-merge"}, // explicit pre-merge
			"build":     {Cmd: "go build ./...", Phase: "post-squash"},  // excluded
			"empty-cmd": {Cmd: "  ", Phase: "pre-merge"},                // excluded (whitespace-only)
			"unit-test": {Cmd: "go test ./...", Phase: "pre-merge", Timeout: "30s"},
		},
	})

	gates := loadPreVerifyGates(townRoot, rigName)

	var names []string
	for _, g := range gates {
		names = append(names, g.name)
	}
	wantOrder := []string{"lint", "unit-test", "vet"}
	if len(names) != len(wantOrder) {
		t.Fatalf("expected %d gates %v, got %d %v", len(wantOrder), wantOrder, len(names), names)
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("gates not sorted: %v", names)
	}
	for i, want := range wantOrder {
		if names[i] != want {
			t.Errorf("gate[%d]: got %q want %q (full: %v)", i, names[i], want, names)
		}
	}

	// Verify timeout parsed for unit-test
	for _, g := range gates {
		if g.name == "unit-test" && g.timeout != 30*time.Second {
			t.Errorf("unit-test timeout: got %v want 30s", g.timeout)
		}
	}
}

// TestLoadPreVerifyGates_EmptyConfig returns nil when no rig settings exist
// (no gates configured to verify against).
func TestLoadPreVerifyGates_EmptyConfig(t *testing.T) {
	townRoot := t.TempDir()
	if got := loadPreVerifyGates(townRoot, "noconfigrig"); len(got) != 0 {
		t.Errorf("expected empty gates, got %v", got)
	}
}

// TestLoadPreVerifyGates_NoMergeQueue returns nil when settings exist but
// merge_queue is unset.
func TestLoadPreVerifyGates_NoMergeQueue(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"
	writePreVerifyRigSettings(t, townRoot, rigName, nil)
	if got := loadPreVerifyGates(townRoot, rigName); len(got) != 0 {
		t.Errorf("expected empty gates, got %v", got)
	}
}

// TestRunPreVerifyGates_AllPass returns ok=true when every gate exits 0.
func TestRunPreVerifyGates_AllPass(t *testing.T) {
	gates := []preVerifyGate{
		{name: "lint", cmd: "true"},
		{name: "vet", cmd: "true"},
	}
	var ran []string
	stub := func(_ context.Context, _ string, cmd string) ([]byte, error) {
		ran = append(ran, cmd)
		return nil, nil
	}
	ok, err := runPreVerifyGates(context.Background(), "/tmp", gates, stub)
	if !ok {
		t.Errorf("expected ok=true, got false (err=%v)", err)
	}
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if len(ran) != 2 {
		t.Errorf("expected 2 gates run, got %d: %v", len(ran), ran)
	}
}

// TestRunPreVerifyGates_StopsOnFirstFailure verifies the failure short-circuits
// the gate sequence and the returned error names the failing gate. This is the
// core gu-xp5f guarantee: a polecat cannot bury a red gate inside the bypass.
func TestRunPreVerifyGates_StopsOnFirstFailure(t *testing.T) {
	gates := []preVerifyGate{
		{name: "lint", cmd: "true"},
		{name: "vet", cmd: "false"},
		{name: "unit-test", cmd: "true"},
	}
	var ran []string
	stub := func(_ context.Context, _ string, cmd string) ([]byte, error) {
		ran = append(ran, cmd)
		if cmd == "false" {
			return []byte("vet output: bad code"), errors.New("exit status 1")
		}
		return nil, nil
	}
	ok, err := runPreVerifyGates(context.Background(), "/tmp", gates, stub)
	if ok {
		t.Errorf("expected ok=false on red gate, got true")
	}
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if len(ran) != 2 {
		t.Errorf("expected to stop after 2nd gate, ran %d: %v", len(ran), ran)
	}
	// Error must name the failing gate so operators know what to look at.
	if msg := err.Error(); !strings.Contains(msg, "vet") {
		t.Errorf("error should name failing gate %q, got %q", "vet", msg)
	}
}

// TestRunPreVerifyGates_NoGatesIsOK verifies the no-gates case returns ok=true
// without invoking any runner. This is the safety-net path: rigs without gate
// config fall through and the attestation stays as-is.
func TestRunPreVerifyGates_NoGatesIsOK(t *testing.T) {
	called := false
	stub := func(_ context.Context, _ string, _ string) ([]byte, error) {
		called = true
		return nil, nil
	}
	ok, err := runPreVerifyGates(context.Background(), "/tmp", nil, stub)
	if !ok || err != nil {
		t.Errorf("expected ok=true, nil err for empty gates; got ok=%v err=%v", ok, err)
	}
	if called {
		t.Errorf("runner should not be called when there are no gates")
	}
}

// TestVerifyPreVerifiedAttestation_KeepsHeartbeatFresh is the gu-0x2be
// regression guard: a slow pre-merge gate must keep the session heartbeat
// fresh so the witness does not classify the polecat as stuck-in-done and
// restart it mid-gate. We seed a stale state="exiting" heartbeat (as gt done
// writes one-shot before this path), configure a gate slower than the stale
// threshold would tolerate, and assert the heartbeat timestamp advances past
// the seeded value while the exiting state is preserved.
func TestVerifyPreVerifiedAttestation_KeepsHeartbeatFresh(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"
	sessionName := "polecat-shiny-test"
	t.Setenv("GT_SESSION", sessionName)

	// One real gate so VerifyPreVerifiedAttestation does not early-return.
	writePreVerifyRigSettings(t, townRoot, rigName, &config.MergeQueueConfig{
		Gates: map[string]*config.GateConfig{
			"slow": {Cmd: "sleep 0.3", Phase: "pre-merge"},
		},
	})

	// Seed a STALE exiting heartbeat, the way gt done writes one before the
	// gate run. Backdate it so any keepalive bump is unambiguously newer.
	polecat.TouchSessionHeartbeatWithState(townRoot, sessionName, polecat.HeartbeatExiting, "gt done", "gu-0x2be")
	seeded := polecat.ReadSessionHeartbeat(townRoot, sessionName)
	if seeded == nil {
		t.Fatal("failed to seed heartbeat")
	}

	keep := VerifyPreVerifiedAttestation(context.Background(), townRoot, rigName, t.TempDir())
	if !keep {
		t.Errorf("expected attestation to stay valid for a passing gate")
	}

	after := polecat.ReadSessionHeartbeat(townRoot, sessionName)
	if after == nil {
		t.Fatal("heartbeat missing after gate run")
	}
	// The keepalive ticker bumps immediately on start, so the timestamp must
	// have advanced past the seeded value.
	if !after.Timestamp.After(seeded.Timestamp) {
		t.Errorf("heartbeat not refreshed during gate run: seeded=%v after=%v",
			seeded.Timestamp, after.Timestamp)
	}
	// The exiting state must survive the keepalive bump — the witness's
	// fresh-exiting guard depends on it.
	if after.EffectiveState() != polecat.HeartbeatExiting {
		t.Errorf("keepalive clobbered exiting state: got %q", after.EffectiveState())
	}
}

// TestRunPreVerifyGates_TruncatesNoisyOutput keeps error messages bounded so a
// chatty test failure doesn't blow out the polecat's terminal / context window.
func TestRunPreVerifyGates_TruncatesNoisyOutput(t *testing.T) {
	huge := make([]byte, 4000)
	for i := range huge {
		huge[i] = 'x'
	}
	stub := func(_ context.Context, _ string, _ string) ([]byte, error) {
		return huge, errors.New("exit status 1")
	}
	gates := []preVerifyGate{{name: "noisy", cmd: "fail"}}
	_, err := runPreVerifyGates(context.Background(), "/tmp", gates, stub)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if len(err.Error()) > 1500 {
		t.Errorf("error message too long (%d bytes): truncation guard not working", len(err.Error()))
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("error should mention truncation marker, got: %s", err.Error())
	}
}
