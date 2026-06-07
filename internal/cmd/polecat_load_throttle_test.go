package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

// setSchedulerMaxLoadPerCore writes a town settings file with the given
// max_load_per_core (and max_polecats) so the admission gate reads a real
// configured threshold.
func setSchedulerMaxLoadPerCore(t *testing.T, townRoot string, maxPolecats int, maxLoadPerCore float64) {
	t.Helper()
	settings := config.NewTownSettings()
	settings.Scheduler = &capacity.SchedulerConfig{
		MaxPolecats:    &maxPolecats,
		MaxLoadPerCore: &maxLoadPerCore,
	}
	writeJSONFile(t, config.TownSettingsPath(townRoot), settings)
}

func withStubLoadPerCore(t *testing.T, load float64) {
	t.Helper()
	orig := polecatAdmissionLoadPerCore
	polecatAdmissionLoadPerCore = func() float64 { return load }
	t.Cleanup(func() { polecatAdmissionLoadPerCore = orig })
}

// TestLoadThrottleRefusesDirectDispatchUnderPressure is the core gu-5j7p4
// regression: in uncapped direct dispatch (max_polecats=-1) admission used to
// be granted immediately with zero load backpressure. With max_load_per_core
// set and host load above it, admission must now be refused even on that path.
func TestLoadThrottleRefusesDirectDispatchUnderPressure(t *testing.T) {
	townRoot := t.TempDir()
	setSchedulerMaxLoadPerCore(t, townRoot, -1, 2.0)
	withStubLoadPerCore(t, 3.0) // 3.0 > 2.0 threshold

	handle, _, err := acquirePolecatAdmission(townRoot, "gastown", "gt-one", "test")
	if handle != nil {
		defer handle.Release()
	}
	if err == nil {
		t.Fatal("expected admission denial under load in direct dispatch, got nil")
	}
	if !strings.Contains(err.Error(), "max_load_per_core") {
		t.Fatalf("denial error %q should mention max_load_per_core", err.Error())
	}
}

// TestLoadThrottleAllowsDirectDispatchUnderThreshold confirms the fast-path
// disabled handle is still returned when load is at or below the threshold.
func TestLoadThrottleAllowsDirectDispatchUnderThreshold(t *testing.T) {
	townRoot := t.TempDir()
	setSchedulerMaxLoadPerCore(t, townRoot, -1, 2.0)
	withStubLoadPerCore(t, 1.5) // below threshold

	handle, snapshot, err := acquirePolecatAdmission(townRoot, "gastown", "gt-one", "test")
	if err != nil {
		t.Fatalf("admission under threshold: %v", err)
	}
	defer handle.Release()
	if !handle.disabled {
		t.Fatal("direct-dispatch admission handle should be disabled (no reservation)")
	}
	if snapshot.Max != -1 {
		t.Fatalf("snapshot.Max = %d, want -1", snapshot.Max)
	}
}

// TestLoadThrottleDisabledByDefault verifies that with no threshold configured
// (max_load_per_core absent / 0) the gate never fires, even at high load.
func TestLoadThrottleDisabledByDefault(t *testing.T) {
	townRoot := t.TempDir()
	configureScheduler(t, townRoot, -1, 1) // no max_load_per_core
	withStubLoadPerCore(t, 99.0)

	handle, _, err := acquirePolecatAdmission(townRoot, "gastown", "gt-one", "test")
	if err != nil {
		t.Fatalf("disabled throttle must not deny admission: %v", err)
	}
	defer handle.Release()
	if !handle.disabled {
		t.Fatal("direct-dispatch handle should be disabled")
	}
}

// TestLoadThrottleFailsOpenOnUnknownLoad pins the fail-open convention: a load
// reading of 0 (unavailable, e.g. Windows) must never block dispatch even with
// a threshold set.
func TestLoadThrottleFailsOpenOnUnknownLoad(t *testing.T) {
	townRoot := t.TempDir()
	setSchedulerMaxLoadPerCore(t, townRoot, -1, 2.0)
	withStubLoadPerCore(t, 0) // unknown load

	handle, _, err := acquirePolecatAdmission(townRoot, "gastown", "gt-one", "test")
	if err != nil {
		t.Fatalf("unknown load must fail open: %v", err)
	}
	defer handle.Release()
	if !handle.disabled {
		t.Fatal("direct-dispatch handle should be disabled")
	}
}

// TestLoadThrottleRefusesCappedDispatchBeforeReservation confirms the gate also
// runs ahead of the capacity-cap path (max_polecats > 0): under load no
// reservation file should be written.
func TestLoadThrottleRefusesCappedDispatchBeforeReservation(t *testing.T) {
	townRoot := t.TempDir()
	setSchedulerMaxLoadPerCore(t, townRoot, 4, 2.0)
	withStubLoadPerCore(t, 5.0)

	handle, _, err := acquirePolecatAdmission(townRoot, "gastown", "gt-one", "test")
	if handle != nil {
		defer handle.Release()
	}
	if err == nil {
		t.Fatal("expected admission denial under load in capped dispatch")
	}
	if _, statErr := os.Stat(polecatAdmissionDir(townRoot)); !os.IsNotExist(statErr) {
		t.Fatalf("no reservation should be written when load-throttled: %v", statErr)
	}
}

// TestGetMaxLoadPerCore covers the config accessor's defaulting.
func TestGetMaxLoadPerCore(t *testing.T) {
	if got := (*capacity.SchedulerConfig)(nil).GetMaxLoadPerCore(); got != 0 {
		t.Fatalf("nil config GetMaxLoadPerCore = %v, want 0", got)
	}
	if got := (&capacity.SchedulerConfig{}).GetMaxLoadPerCore(); got != 0 {
		t.Fatalf("unset GetMaxLoadPerCore = %v, want 0", got)
	}
	neg := -1.0
	if got := (&capacity.SchedulerConfig{MaxLoadPerCore: &neg}).GetMaxLoadPerCore(); got != 0 {
		t.Fatalf("negative GetMaxLoadPerCore = %v, want 0 (disabled)", got)
	}
	pos := 2.5
	if got := (&capacity.SchedulerConfig{MaxLoadPerCore: &pos}).GetMaxLoadPerCore(); got != 2.5 {
		t.Fatalf("GetMaxLoadPerCore = %v, want 2.5", got)
	}
}
