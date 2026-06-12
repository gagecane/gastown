package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

// setSchedulerGlobalCeiling writes a town settings file with the given
// global_max_polecats (and max_polecats) so the admission gate reads a real
// configured ceiling.
func setSchedulerGlobalCeiling(t *testing.T, townRoot string, maxPolecats, globalCeiling int) {
	t.Helper()
	settings := config.NewTownSettings()
	settings.Scheduler = &capacity.SchedulerConfig{
		MaxPolecats:       &maxPolecats,
		GlobalMaxPolecats: &globalCeiling,
	}
	writeJSONFile(t, config.TownSettingsPath(townRoot), settings)
}

func withStubTownWideWorking(t *testing.T, count int) {
	t.Helper()
	orig := countWorkingPolecatsTownWideFn
	countWorkingPolecatsTownWideFn = func() int { return count }
	t.Cleanup(func() { countWorkingPolecatsTownWideFn = orig })
}

// TestGlobalCeilingRefusesDirectDispatchWhenFull is the core gu-su334 behavior:
// the global ceiling must be enforced even on the uncapped direct-dispatch path
// (max_polecats=-1), where max_polecats provides no global cap at all.
func TestGlobalCeilingRefusesDirectDispatchWhenFull(t *testing.T) {
	townRoot := t.TempDir()
	setSchedulerGlobalCeiling(t, townRoot, -1, 8)
	withStubLoadPerCore(t, 0) // disable load throttle
	withStubTownWideWorking(t, 8)

	handle, _, err := acquirePolecatAdmission(townRoot, "gastown", "gt-one", "test")
	if handle != nil {
		defer handle.Release()
	}
	if err == nil {
		t.Fatal("expected admission denial at global ceiling in direct dispatch, got nil")
	}
	if !strings.Contains(err.Error(), "global_max_polecats") {
		t.Fatalf("denial error %q should mention global_max_polecats", err.Error())
	}
	// No reservation should be written when ceiling-blocked on the direct path.
	if _, statErr := os.Stat(polecatAdmissionDir(townRoot)); !os.IsNotExist(statErr) {
		t.Fatalf("no reservation should be written when ceiling-blocked: %v", statErr)
	}
}

// TestGlobalCeilingAllowsDirectDispatchUnderCeiling confirms the disabled
// handle is still returned when town-wide working count is below the ceiling.
func TestGlobalCeilingAllowsDirectDispatchUnderCeiling(t *testing.T) {
	townRoot := t.TempDir()
	setSchedulerGlobalCeiling(t, townRoot, -1, 8)
	withStubLoadPerCore(t, 0)
	withStubTownWideWorking(t, 7) // one slot free

	handle, snapshot, err := acquirePolecatAdmission(townRoot, "gastown", "gt-one", "test")
	if err != nil {
		t.Fatalf("admission under ceiling: %v", err)
	}
	defer handle.Release()
	if !handle.disabled {
		t.Fatal("direct-dispatch admission handle should be disabled (no reservation)")
	}
	if snapshot.Max != -1 {
		t.Fatalf("snapshot.Max = %d, want -1", snapshot.Max)
	}
}

// TestGlobalCeilingDisabledByDefault verifies that with no ceiling configured
// (global_max_polecats absent / 0) the gate never fires, even at high count.
func TestGlobalCeilingDisabledByDefault(t *testing.T) {
	townRoot := t.TempDir()
	configureScheduler(t, townRoot, -1, 1) // no global_max_polecats
	withStubLoadPerCore(t, 0)
	withStubTownWideWorking(t, 999)

	handle, _, err := acquirePolecatAdmission(townRoot, "gastown", "gt-one", "test")
	if err != nil {
		t.Fatalf("disabled ceiling must not deny admission: %v", err)
	}
	defer handle.Release()
	if !handle.disabled {
		t.Fatal("direct-dispatch handle should be disabled")
	}
}

// TestGlobalCeilingRefusesDeferredDispatch confirms the ceiling also gates the
// capped (deferred) path before any reservation is written.
func TestGlobalCeilingRefusesDeferredDispatch(t *testing.T) {
	townRoot := t.TempDir()
	setSchedulerGlobalCeiling(t, townRoot, 4, 8)
	withStubLoadPerCore(t, 0)
	withStubTownWideWorking(t, 8)

	handle, _, err := acquirePolecatAdmission(townRoot, "gastown", "gt-one", "test")
	if handle != nil {
		defer handle.Release()
	}
	if err == nil {
		t.Fatal("expected admission denial at global ceiling in deferred dispatch")
	}
	if _, statErr := os.Stat(polecatAdmissionDir(townRoot)); !os.IsNotExist(statErr) {
		t.Fatalf("no reservation should be written when ceiling-blocked: %v", statErr)
	}
}

// TestGetGlobalMaxPolecats covers the config accessor's defaulting.
func TestGetGlobalMaxPolecats(t *testing.T) {
	if got := (*capacity.SchedulerConfig)(nil).GetGlobalMaxPolecats(); got != 0 {
		t.Fatalf("nil config GetGlobalMaxPolecats() = %d, want 0", got)
	}
	zero := 0
	if got := (&capacity.SchedulerConfig{GlobalMaxPolecats: &zero}).GetGlobalMaxPolecats(); got != 0 {
		t.Fatalf("zero ceiling = %d, want 0 (unbounded)", got)
	}
	neg := -5
	if got := (&capacity.SchedulerConfig{GlobalMaxPolecats: &neg}).GetGlobalMaxPolecats(); got != 0 {
		t.Fatalf("negative ceiling = %d, want 0 (unbounded)", got)
	}
	eight := 8
	if got := (&capacity.SchedulerConfig{GlobalMaxPolecats: &eight}).GetGlobalMaxPolecats(); got != 8 {
		t.Fatalf("ceiling = %d, want 8", got)
	}
}
