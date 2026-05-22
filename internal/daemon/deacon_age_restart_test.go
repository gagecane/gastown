package daemon

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/tmux"
)

// writeMaxSessionAge writes a townRoot settings/config.json that sets
// operational.daemon.deacon_max_session_age to the given duration string.
// Empty string disables the knob (default behaviour).
func writeMaxSessionAge(t *testing.T, townRoot, maxAge string) {
	t.Helper()
	dir := filepath.Join(townRoot, "settings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}
	var body string
	if maxAge == "" {
		body = `{"operational":{"daemon":{}}}`
	} else {
		body = `{"operational":{"daemon":{"deacon_max_session_age":"` + maxAge + `"}}}`
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

func countKillSession(t *testing.T, tmuxLog string) int {
	t.Helper()
	data, err := os.ReadFile(tmuxLog)
	if err != nil {
		t.Fatalf("read tmux log: %v", err)
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "kill-session ") {
			n++
		}
	}
	return n
}

// TestCheckDeaconAge covers the scheduled age-based restart logic (gs-a0x):
// disabled by default, soft trigger with active-work deferral, soft trigger
// without active work, and hard cap forcing restart through active work.
func TestCheckDeaconAge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}

	tests := []struct {
		name             string
		maxAge           string                       // operational config value
		sessionAge       time.Duration                // age of deacon's session
		stores           map[string]beadsdk.Storage   // active-work backing
		wantKillSessions int                          // tmux kill-session calls
		wantLogContains  string                       // substring expected in logs
		wantLogAbsent    string                       // substring NOT expected
	}{
		{
			name:             "disabled: no max-age config — no-op",
			maxAge:           "",
			sessionAge:       10 * time.Hour,
			stores:           emptyStores(),
			wantKillSessions: 0,
			wantLogAbsent:    "Scheduled deacon restart",
		},
		{
			name:             "explicit zero: max-age 0s — no-op",
			maxAge:           "0s",
			sessionAge:       10 * time.Hour,
			stores:           emptyStores(),
			wantKillSessions: 0,
			wantLogAbsent:    "Scheduled deacon restart",
		},
		{
			name:             "young session: age under max — no-op",
			maxAge:           "3h",
			sessionAge:       1 * time.Hour,
			stores:           emptyStores(),
			wantKillSessions: 0,
			wantLogAbsent:    "Scheduled deacon restart",
		},
		{
			name:             "soft trigger, idle: age > max, no work — restart",
			maxAge:           "3h",
			sessionAge:       4 * time.Hour,
			stores:           emptyStores(),
			wantKillSessions: 1,
			wantLogContains:  "Scheduled deacon restart:",
		},
		{
			name:             "soft trigger, busy: age > max, active work — deferred",
			maxAge:           "3h",
			sessionAge:       4 * time.Hour,
			stores:           storesWithInProgress(),
			wantKillSessions: 0,
			wantLogContains:  "deferred",
		},
		{
			name:             "hard cap: age > 2*max with active work — forced restart",
			maxAge:           "3h",
			sessionAge:       7 * time.Hour,
			stores:           storesWithInProgress(),
			wantKillSessions: 1,
			wantLogContains:  "HARD CAP",
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

			writeMaxSessionAge(t, townRoot, tc.maxAge)

			d := &Daemon{
				config:            &Config{TownRoot: townRoot},
				logger:            log.New(io.Discard, "", 0),
				tmux:              tmux.NewTmux(),
				beadsStores:       tc.stores,
				ctx:               context.Background(),
				deaconLastStarted: time.Now().Add(-tc.sessionAge),
				// Stub start path: ErrAlreadyRunning so the respawn after kill
				// returns cleanly without trying to launch a real Claude session.
				deaconStartFn: func() error { return deacon.ErrAlreadyRunning },
			}

			logBuf := &strings.Builder{}
			d.logger = log.New(logBuf, "", 0)

			d.checkDeaconAge()

			gotKills := countKillSession(t, tmuxLog)
			if gotKills != tc.wantKillSessions {
				t.Errorf("kill-session count = %d, want %d\nlog:\n%s",
					gotKills, tc.wantKillSessions, logBuf.String())
			}
			if tc.wantLogContains != "" && !strings.Contains(logBuf.String(), tc.wantLogContains) {
				t.Errorf("log missing %q\nlog:\n%s", tc.wantLogContains, logBuf.String())
			}
			if tc.wantLogAbsent != "" && strings.Contains(logBuf.String(), tc.wantLogAbsent) {
				t.Errorf("log contains forbidden %q\nlog:\n%s", tc.wantLogAbsent, logBuf.String())
			}
		})
	}
}

// TestCheckDeaconAge_NeverStarted verifies the no-op early return when
// deaconLastStarted is zero — there's nothing to restart yet, and the
// existing ensure/heartbeat path will start it on the next tick.
func TestCheckDeaconAge_NeverStarted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}
	townRoot := t.TempDir()
	fakeBinDir := t.TempDir()
	tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
	if err := os.WriteFile(tmuxLog, []byte{}, 0o644); err != nil {
		t.Fatalf("create tmux log: %v", err)
	}

	writeFakeTmuxWithSession(t, fakeBinDir)
	t.Setenv("PATH", fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_LOG", tmuxLog)
	writeMaxSessionAge(t, townRoot, "1h")

	d := &Daemon{
		config:        &Config{TownRoot: townRoot},
		logger:        log.New(io.Discard, "", 0),
		tmux:          tmux.NewTmux(),
		beadsStores:   emptyStores(),
		ctx:           context.Background(),
		deaconStartFn: func() error { return deacon.ErrAlreadyRunning },
	}

	logBuf := &strings.Builder{}
	d.logger = log.New(logBuf, "", 0)

	d.checkDeaconAge()

	if got := countKillSession(t, tmuxLog); got != 0 {
		t.Errorf("kill-session count = %d, want 0 (never started)\nlog:\n%s", got, logBuf.String())
	}
}

func emptyStores() map[string]beadsdk.Storage {
	return map[string]beadsdk.Storage{
		"hq": &searchStorage{results: map[string][]*beadsdk.Issue{}},
	}
}

func storesWithInProgress() map[string]beadsdk.Storage {
	return map[string]beadsdk.Storage{
		"hq": &searchStorage{results: map[string][]*beadsdk.Issue{
			"in_progress": {{ID: "sc-busy"}},
		}},
	}
}
