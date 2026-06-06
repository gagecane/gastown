package daemon

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/deacon"
)

// writeDeaconHeartbeatCycle writes a deacon heartbeat file with an explicit
// timestamp age AND cycle number. Used to simulate a monotonic-age hang where
// the cycle freezes but the heartbeat age stays within the fresh threshold.
func writeDeaconHeartbeatCycle(t *testing.T, townRoot string, age time.Duration, cycle int64) {
	t.Helper()
	hb := &deacon.Heartbeat{
		Timestamp: time.Now().Add(-age),
		Cycle:     cycle,
	}
	if err := deacon.WriteHeartbeat(townRoot, hb); err != nil {
		t.Fatalf("writeDeaconHeartbeatCycle: %v", err)
	}
}

// TestObserveDeaconCycle covers the cycle-stall tracker in isolation (gu-qwjj3).
func TestObserveDeaconCycle(t *testing.T) {
	d := &Daemon{}

	// First observation seeds the tracker and reports no stall.
	if got := d.observeDeaconCycle(&deacon.Heartbeat{Cycle: 100}); got != 0 {
		t.Errorf("first observation: stall = %v, want 0", got)
	}
	if d.lastDeaconCycle != 100 {
		t.Errorf("lastDeaconCycle = %d, want 100", d.lastDeaconCycle)
	}

	// Backdate the change marker, then observe the SAME cycle: the tracker should
	// report a non-zero stall equal to the elapsed time since the change marker.
	d.lastDeaconCycleChange = time.Now().Add(-8 * time.Minute)
	got := d.observeDeaconCycle(&deacon.Heartbeat{Cycle: 100})
	if got < 7*time.Minute {
		t.Errorf("stalled cycle: stall = %v, want >= 7m", got)
	}

	// An advancing cycle resets the tracker to zero stall.
	if got := d.observeDeaconCycle(&deacon.Heartbeat{Cycle: 101}); got != 0 {
		t.Errorf("advancing cycle: stall = %v, want 0 (reset)", got)
	}
	if d.lastDeaconCycle != 101 {
		t.Errorf("lastDeaconCycle = %d, want 101 after advance", d.lastDeaconCycle)
	}

	// nil heartbeat is a no-op.
	if got := d.observeDeaconCycle(nil); got != 0 {
		t.Errorf("nil heartbeat: stall = %v, want 0", got)
	}
}

// TestCheckDeaconHeartbeat_CycleStall is the core gu-qwjj3 regression: a deacon
// whose heartbeat AGE is still fresh (< 16m) but whose CYCLE counter is frozen
// must be detected as hung and nudged — but only when active work is in flight,
// so a legitimately idle deacon in await-signal backoff is not interrupted
// (preserving the gu-70rg false-positive fix).
func TestCheckDeaconHeartbeat_CycleStall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}

	tests := []struct {
		name           string
		heartbeatAge   time.Duration
		stallElapsed   time.Duration // how long the cycle has been frozen
		stores         map[string]beadsdk.Storage
		wantStallNudge bool
		desc           string
	}{
		{
			name:         "hung with work: fresh age, frozen cycle 8m, in_progress — nudge",
			heartbeatAge: 8 * time.Minute, // still < 16m fresh threshold
			stallElapsed: 8 * time.Minute,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{
					"in_progress": {{ID: "sc-active"}},
				}},
			},
			wantStallNudge: true,
			desc:           "Frozen cycle past 7m threshold with work in flight is the gu-qwjj3 hang signal",
		},
		{
			name:         "idle: fresh age, frozen cycle 8m, no work — suppressed",
			heartbeatAge: 8 * time.Minute,
			stallElapsed: 8 * time.Minute,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{}},
			},
			wantStallNudge: false,
			desc:           "Idle await-signal backoff also freezes the cycle; must NOT nudge (gu-70rg guard)",
		},
		{
			name:         "advancing: fresh age, cycle just advanced — no nudge",
			heartbeatAge: 8 * time.Minute,
			stallElapsed: 0, // cycle advanced this tick
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{
					"in_progress": {{ID: "sc-active"}},
				}},
			},
			wantStallNudge: false,
			desc:           "A deacon advancing its cycle is healthy regardless of work state",
		},
		{
			name:         "short stall: frozen only 3m, under threshold — no nudge",
			heartbeatAge: 5 * time.Minute,
			stallElapsed: 3 * time.Minute, // < 7m threshold
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{
					"in_progress": {{ID: "sc-active"}},
				}},
			},
			wantStallNudge: false,
			desc:           "Cycle frozen under the stall threshold is within normal patrol cadence",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			townRoot := t.TempDir()
			fakeBinDir := t.TempDir()
			tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
			if err := os.WriteFile(tmuxLog, []byte{}, 0o644); err != nil {
				t.Fatalf("create tmux log: %v", err)
			}

			writeFakeTmuxWithSession(t, fakeBinDir)
			t.Setenv("PATH", fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TMUX_LOG", tmuxLog)

			const cycle = int64(42)
			writeDeaconHeartbeatCycle(t, townRoot, tc.heartbeatAge, cycle)

			d := newTestDaemonWithStores(t, townRoot, tc.stores)

			// Seed the cycle tracker to the same cycle, with the change marker
			// backdated by stallElapsed. A zero stallElapsed simulates a cycle that
			// advanced on this tick (tracker holds a DIFFERENT prior cycle).
			if tc.stallElapsed == 0 {
				d.lastDeaconCycle = cycle - 1 // different → observe() resets, zero stall
			} else {
				d.lastDeaconCycle = cycle
				d.lastDeaconCycleChange = time.Now().Add(-tc.stallElapsed)
			}

			logBuf := &strings.Builder{}
			d.logger = log.New(logBuf, "", 0)

			d.checkDeaconHeartbeat()

			logOutput := logBuf.String()
			hasStallNudge := strings.Contains(logOutput, "cycle frozen")
			if hasStallNudge != tc.wantStallNudge {
				t.Errorf("%s\ncycle-stall nudge present=%v, want=%v\nlog:\n%s",
					tc.desc, hasStallNudge, tc.wantStallNudge, logOutput)
			}
		})
	}
}
