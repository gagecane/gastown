package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
)

func TestWispReaperInterval(t *testing.T) {
	// Default (now 1h after Dog-driven refactor)
	if got := wispReaperInterval(nil); got != defaultWispReaperInterval {
		t.Errorf("expected default %v, got %v", defaultWispReaperInterval, got)
	}

	// Custom
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:     true,
				IntervalStr: "2h",
			},
		},
	}
	if got := wispReaperInterval(config); got != 2*time.Hour {
		t.Errorf("expected 2h, got %v", got)
	}

	// Invalid falls back to default
	config.Patrols.WispReaper.IntervalStr = "nope"
	if got := wispReaperInterval(config); got != defaultWispReaperInterval {
		t.Errorf("expected default for invalid, got %v", got)
	}
}

func TestWispReaperMaxAge(t *testing.T) {
	if got := wispReaperMaxAge(nil); got != defaultWispMaxAge {
		t.Errorf("expected default %v, got %v", defaultWispMaxAge, got)
	}

	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:   true,
				MaxAgeStr: "48h",
			},
		},
	}
	if got := wispReaperMaxAge(config); got != 48*time.Hour {
		t.Errorf("expected 48h, got %v", got)
	}
}

func TestWispDeleteAge(t *testing.T) {
	if got := wispDeleteAge(nil); got != defaultWispDeleteAge {
		t.Errorf("expected default %v, got %v", defaultWispDeleteAge, got)
	}

	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:      true,
				DeleteAgeStr: "336h",
			},
		},
	}
	if got := wispDeleteAge(config); got != 14*24*time.Hour {
		t.Errorf("expected 336h, got %v", got)
	}
}

func TestDefaultReaperIntervalIsOneHour(t *testing.T) {
	// Verify the default changed from 30m to 1h per issue gt-caf7.
	if defaultWispReaperInterval != 1*time.Hour {
		t.Errorf("expected default interval 1h, got %v", defaultWispReaperInterval)
	}
}

func TestDefaultHookedMolTTLIsGreaterThanInterval(t *testing.T) {
	// TTL must be at least one full reaper cycle so a running dog has time to
	// pick up the dispatch wisp before it is closed as orphaned. GH#3767.
	if defaultHookedMolTTL <= defaultWispReaperInterval {
		t.Errorf("defaultHookedMolTTL (%v) must exceed defaultWispReaperInterval (%v)",
			defaultHookedMolTTL, defaultWispReaperInterval)
	}
}

func TestKennelHasRunningDogsEmptyKennel(t *testing.T) {
	// An empty or missing kennel dir must return false without panicking.
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		tmux:   nil, // no tmux needed — kennel dir check is first
	}
	if d.kennelHasRunningDogs() {
		t.Error("expected false for missing kennel dir")
	}
}

func TestKennelHasRunningDogsNoValidDogs(t *testing.T) {
	// A kennel dir with subdirs that have no .dog.json must return false.
	dir := t.TempDir()
	kennelPath := dir + "/deacon/dogs/alpha"
	if err := os.MkdirAll(kennelPath, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// No .dog.json in alpha/ — not a valid dog.
	d := &Daemon{
		config: &Config{TownRoot: dir},
		tmux:   nil,
	}
	if d.kennelHasRunningDogs() {
		t.Error("expected false when no valid dog state files exist")
	}
}

func TestDispatchReaperDogUsesDogPoolSling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock")
	}

	townRoot := t.TempDir()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "gt-args.log")
	fakeGT := filepath.Join(binDir, "gt")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\n", logPath)
	if err := os.WriteFile(fakeGT, []byte(script), 0755); err != nil {
		t.Fatalf("write fake gt: %v", err)
	}

	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		gtPath: fakeGT,
	}
	if err := d.dispatchReaperDog(map[string]string{"max_age": "1h"}); err != nil {
		t.Fatalf("dispatchReaperDog() error = %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read gt args log: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	wantPrefix := []string{"sling", constants.MolDogReaper, "deacon/dogs"}
	if len(args) < len(wantPrefix) {
		t.Fatalf("gt args = %v, want prefix %v", args, wantPrefix)
	}
	for i, want := range wantPrefix {
		if args[i] != want {
			t.Fatalf("gt arg %d = %q, want %q (all args: %v)", i, args[i], want, args)
		}
	}
}
