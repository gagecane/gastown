package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/nudge"
)

func TestPollerDogInterval(t *testing.T) {
	// Default when config is nil.
	if got := pollerDogInterval(nil); got != defaultPollerDogInterval {
		t.Errorf("default interval: got %v want %v", got, defaultPollerDogInterval)
	}

	// Custom interval.
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			PollerDog: &PollerDogConfig{Enabled: true, IntervalStr: "30s"},
		},
	}
	if got := pollerDogInterval(cfg); got != 30*time.Second {
		t.Errorf("custom interval: got %v want 30s", got)
	}

	// Invalid falls back to default.
	cfg.Patrols.PollerDog.IntervalStr = "not-a-duration"
	if got := pollerDogInterval(cfg); got != defaultPollerDogInterval {
		t.Errorf("invalid interval: got %v want default %v", got, defaultPollerDogInterval)
	}

	// Empty IntervalStr => default.
	cfg.Patrols.PollerDog.IntervalStr = ""
	if got := pollerDogInterval(cfg); got != defaultPollerDogInterval {
		t.Errorf("empty interval: got %v want default %v", got, defaultPollerDogInterval)
	}
}

func TestIsPatrolEnabled_PollerDog(t *testing.T) {
	// Nil config: disabled (opt-in).
	if IsPatrolEnabled(nil, "poller_dog") {
		t.Error("poller_dog should be disabled with nil config")
	}

	// Empty patrols: disabled.
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	if IsPatrolEnabled(cfg, "poller_dog") {
		t.Error("poller_dog should be disabled by default")
	}

	// Explicitly enabled.
	cfg.Patrols.PollerDog = &PollerDogConfig{Enabled: true}
	if !IsPatrolEnabled(cfg, "poller_dog") {
		t.Error("poller_dog should be enabled when configured")
	}

	// Explicitly disabled.
	cfg.Patrols.PollerDog = &PollerDogConfig{Enabled: false}
	if IsPatrolEnabled(cfg, "poller_dog") {
		t.Error("poller_dog should be disabled when explicitly disabled")
	}
}

func TestPollerDogConfigJSON(t *testing.T) {
	data := `{"enabled": true, "interval": "45s"}`
	var cfg PollerDogConfig
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !cfg.Enabled {
		t.Error("expected enabled=true")
	}
	if cfg.IntervalStr != "45s" {
		t.Errorf("expected interval=45s, got %q", cfg.IntervalStr)
	}
}

// --- supervisePollers behavior tests ---

type fakeSupervisor struct {
	listResult []nudge.PollerEntry
	listErr    error
	startCalls []string // sessions respawned
	startPID   int
	startErr   error
	removed    []string // sessions whose PID files we removed
	removeErr  error
	mu         sync.Mutex
}

func (f *fakeSupervisor) ListPollers(_ string) ([]nudge.PollerEntry, error) {
	return f.listResult, f.listErr
}
func (f *fakeSupervisor) StartPoller(_ string, session string) (int, error) {
	f.mu.Lock()
	f.startCalls = append(f.startCalls, session)
	f.mu.Unlock()
	return f.startPID, f.startErr
}
func (f *fakeSupervisor) RemoveStalePIDFile(_ string, session string) error {
	f.mu.Lock()
	f.removed = append(f.removed, session)
	f.mu.Unlock()
	return f.removeErr
}

type fakeSessionChecker struct {
	alive map[string]bool
	err   error
}

func (f *fakeSessionChecker) HasSession(name string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.alive[name], nil
}

func captureLogger() (func(string, ...interface{}), func() []string) {
	var (
		mu   sync.Mutex
		msgs []string
	)
	return func(format string, args ...interface{}) {
			mu.Lock()
			defer mu.Unlock()
			msgs = append(msgs, fmt.Sprintf(format, args...))
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			out := make([]string, len(msgs))
			copy(out, msgs)
			return out
		}
}

func TestSupervisePollers_Empty(t *testing.T) {
	sup := &fakeSupervisor{listResult: nil}
	sess := &fakeSessionChecker{alive: map[string]bool{}}
	logf, _ := captureLogger()
	supervisePollers("/town", sup, sess, logf)
	if len(sup.startCalls) != 0 || len(sup.removed) != 0 {
		t.Errorf("empty input should take no action: start=%v remove=%v", sup.startCalls, sup.removed)
	}
}

func TestSupervisePollers_RespawnsDeadPollerForLiveSession(t *testing.T) {
	sup := &fakeSupervisor{
		listResult: []nudge.PollerEntry{
			{Session: "gt-rig-crew-one", PID: 111, Alive: false},
		},
		startPID: 222,
	}
	sess := &fakeSessionChecker{alive: map[string]bool{"gt-rig-crew-one": true}}
	logf, getLogs := captureLogger()

	supervisePollers("/town", sup, sess, logf)

	if len(sup.startCalls) != 1 || sup.startCalls[0] != "gt-rig-crew-one" {
		t.Errorf("expected respawn for gt-rig-crew-one, got %v", sup.startCalls)
	}
	if len(sup.removed) != 0 {
		t.Errorf("should not remove PID file for live session, removed=%v", sup.removed)
	}
	found := false
	for _, l := range getLogs() {
		if strings.Contains(l, "respawned nudge-poller for") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected respawn log line, got %v", getLogs())
	}
}

func TestSupervisePollers_RemovesStalePIDForDeadSession(t *testing.T) {
	sup := &fakeSupervisor{
		listResult: []nudge.PollerEntry{
			{Session: "gt-rig-crew-gone", PID: 333, Alive: false},
		},
	}
	sess := &fakeSessionChecker{alive: map[string]bool{"gt-rig-crew-gone": false}}
	logf, _ := captureLogger()

	supervisePollers("/town", sup, sess, logf)

	if len(sup.startCalls) != 0 {
		t.Errorf("should not respawn for dead session, got %v", sup.startCalls)
	}
	if len(sup.removed) != 1 || sup.removed[0] != "gt-rig-crew-gone" {
		t.Errorf("expected stale PID file removed for gt-rig-crew-gone, got %v", sup.removed)
	}
}

func TestSupervisePollers_IgnoresAlivePollers(t *testing.T) {
	sup := &fakeSupervisor{
		listResult: []nudge.PollerEntry{
			{Session: "gt-rig-crew-ok", PID: 444, Alive: true},
		},
	}
	sess := &fakeSessionChecker{alive: map[string]bool{"gt-rig-crew-ok": true}}
	logf, _ := captureLogger()

	supervisePollers("/town", sup, sess, logf)

	if len(sup.startCalls) != 0 || len(sup.removed) != 0 {
		t.Errorf("alive poller should be left alone: start=%v remove=%v", sup.startCalls, sup.removed)
	}
}

func TestSupervisePollers_TmuxErrorLeavesEntryAlone(t *testing.T) {
	sup := &fakeSupervisor{
		listResult: []nudge.PollerEntry{
			{Session: "gt-rig-crew-err", PID: 555, Alive: false},
		},
	}
	sess := &fakeSessionChecker{err: fmt.Errorf("tmux blew up")}
	logf, getLogs := captureLogger()

	supervisePollers("/town", sup, sess, logf)

	if len(sup.startCalls) != 0 {
		t.Errorf("should not respawn when tmux check errored, got %v", sup.startCalls)
	}
	if len(sup.removed) != 0 {
		t.Errorf("should not remove PID file when tmux check errored, got %v", sup.removed)
	}
	found := false
	for _, l := range getLogs() {
		if strings.Contains(l, "HasSession") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected HasSession error log, got %v", getLogs())
	}
}

func TestSupervisePollers_ListPollersErrorLogs(t *testing.T) {
	sup := &fakeSupervisor{listErr: fmt.Errorf("listing borked")}
	sess := &fakeSessionChecker{}
	logf, getLogs := captureLogger()

	supervisePollers("/town", sup, sess, logf)

	if len(sup.startCalls) != 0 || len(sup.removed) != 0 {
		t.Errorf("list error should short-circuit: start=%v remove=%v", sup.startCalls, sup.removed)
	}
	found := false
	for _, l := range getLogs() {
		if strings.Contains(l, "list pollers failed") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected list error log, got %v", getLogs())
	}
}

func TestSupervisePollers_MixedFleet(t *testing.T) {
	// Simulates a realistic fleet: 1 alive, 1 dead+live-session (respawn),
	// 1 dead+dead-session (cleanup), 1 corrupt pid + live session (respawn).
	sup := &fakeSupervisor{
		listResult: []nudge.PollerEntry{
			{Session: "hq-mayor", PID: 1000, Alive: true},
			{Session: "gt-rig-crew-one", PID: 1001, Alive: false},
			{Session: "gt-old-session", PID: 1002, Alive: false},
			{Session: "gt-rig-crew-two", PID: 0, Alive: false}, // corrupt
		},
		startPID: 9999,
	}
	sess := &fakeSessionChecker{
		alive: map[string]bool{
			"hq-mayor":         true,
			"gt-rig-crew-one":  true,
			"gt-old-session":   false,
			"gt-rig-crew-two":  true,
		},
	}
	logf, _ := captureLogger()

	supervisePollers("/town", sup, sess, logf)

	// Respawn expected for gt-rig-crew-one and gt-rig-crew-two.
	wantRespawn := map[string]bool{"gt-rig-crew-one": true, "gt-rig-crew-two": true}
	if len(sup.startCalls) != 2 {
		t.Fatalf("expected 2 respawns, got %v", sup.startCalls)
	}
	for _, s := range sup.startCalls {
		if !wantRespawn[s] {
			t.Errorf("unexpected respawn for %q", s)
		}
	}
	// Removal expected only for gt-old-session.
	if len(sup.removed) != 1 || sup.removed[0] != "gt-old-session" {
		t.Errorf("expected stale removal for gt-old-session only, got %v", sup.removed)
	}
}
