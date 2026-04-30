package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
)

// setupSessionRegistryForStart installs a test prefix registry and restores
// the previous one at test cleanup. The shutdown session categorization
// relies on session.ParseSessionName, which uses the default registry.
func setupSessionRegistryForStart(t *testing.T) {
	t.Helper()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("bd", "beads")
	reg.Register("hq", "knjn")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

// saveShutdownFlags captures the package-level shutdown flags and restores
// them at cleanup, so tests can mutate them without leaking state.
func saveShutdownFlags(t *testing.T) {
	t.Helper()
	origAll := shutdownAll
	origPolecatsOnly := shutdownPolecatsOnly
	t.Cleanup(func() {
		shutdownAll = origAll
		shutdownPolecatsOnly = origPolecatsOnly
	})
}

// sortedCopy returns a sorted copy of s for order-independent comparisons.
func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// --- categorizeSessions tests ------------------------------------------------

func TestCategorizeSessions_FiltersUnknownSessions(t *testing.T) {
	setupSessionRegistryForStart(t)
	saveShutdownFlags(t)

	// Default behavior: preserve crew, stop everything else; no crew-only / all.
	shutdownAll = false
	shutdownPolecatsOnly = false

	sessions := []string{
		"hq-mayor",          // town-level → stop
		"gt-witness",        // rig witness → stop
		"gt-crew-max",       // crew → preserve (default)
		"gt-thunder",        // polecat → stop
		"not-a-gt-session",  // unknown → filtered out
		"random-tmux-thing", // unknown → filtered out
	}

	toStop, preserved := categorizeSessions(sessions)

	wantStop := []string{"hq-mayor", "gt-witness", "gt-thunder"}
	wantPreserved := []string{"gt-crew-max"}

	if !reflect.DeepEqual(sortedCopy(toStop), sortedCopy(wantStop)) {
		t.Errorf("toStop = %v, want %v", toStop, wantStop)
	}
	if !reflect.DeepEqual(sortedCopy(preserved), sortedCopy(wantPreserved)) {
		t.Errorf("preserved = %v, want %v", preserved, wantPreserved)
	}
}

func TestCategorizeSessions_DefaultPreservesCrew(t *testing.T) {
	setupSessionRegistryForStart(t)
	saveShutdownFlags(t)

	shutdownAll = false
	shutdownPolecatsOnly = false

	sessions := []string{
		"gt-crew-alice",
		"gt-crew-bob",
		"gt-thunder",
		"gt-refinery",
		"hq-deacon",
	}

	toStop, preserved := categorizeSessions(sessions)

	wantStop := []string{"gt-thunder", "gt-refinery", "hq-deacon"}
	wantPreserved := []string{"gt-crew-alice", "gt-crew-bob"}

	if !reflect.DeepEqual(sortedCopy(toStop), sortedCopy(wantStop)) {
		t.Errorf("toStop = %v, want %v", toStop, wantStop)
	}
	if !reflect.DeepEqual(sortedCopy(preserved), sortedCopy(wantPreserved)) {
		t.Errorf("preserved = %v, want %v", preserved, wantPreserved)
	}
}

func TestCategorizeSessions_AllFlagStopsEverything(t *testing.T) {
	setupSessionRegistryForStart(t)
	saveShutdownFlags(t)

	shutdownAll = true
	shutdownPolecatsOnly = false

	sessions := []string{
		"gt-crew-alice",
		"gt-thunder",
		"gt-witness",
		"hq-mayor",
	}

	toStop, preserved := categorizeSessions(sessions)

	if len(preserved) != 0 {
		t.Errorf("preserved = %v, want empty when --all is set", preserved)
	}
	if !reflect.DeepEqual(sortedCopy(toStop), sortedCopy(sessions)) {
		t.Errorf("toStop = %v, want all sessions = %v", toStop, sessions)
	}
}

func TestCategorizeSessions_PolecatsOnlyPreservesEverythingElse(t *testing.T) {
	setupSessionRegistryForStart(t)
	saveShutdownFlags(t)

	shutdownAll = false
	shutdownPolecatsOnly = true

	sessions := []string{
		"gt-thunder",      // polecat → stop
		"bd-stormy",       // polecat in another rig → stop
		"gt-crew-max",     // crew → preserve
		"gt-witness",      // witness → preserve
		"gt-refinery",     // refinery → preserve
		"hq-mayor",        // mayor → preserve
		"hq-deacon",       // deacon → preserve
	}

	toStop, preserved := categorizeSessions(sessions)

	wantStop := []string{"gt-thunder", "bd-stormy"}
	wantPreserved := []string{
		"gt-crew-max",
		"gt-witness",
		"gt-refinery",
		"hq-mayor",
		"hq-deacon",
	}

	if !reflect.DeepEqual(sortedCopy(toStop), sortedCopy(wantStop)) {
		t.Errorf("toStop = %v, want %v", toStop, wantStop)
	}
	if !reflect.DeepEqual(sortedCopy(preserved), sortedCopy(wantPreserved)) {
		t.Errorf("preserved = %v, want %v", preserved, wantPreserved)
	}
}

func TestCategorizeSessions_PolecatsOnlyTakesPrecedenceOverAll(t *testing.T) {
	// When both --polecats-only and --all are set, --polecats-only wins
	// because the categorizeSessions code checks it first. This test pins
	// that behavior so the precedence doesn't silently change.
	setupSessionRegistryForStart(t)
	saveShutdownFlags(t)

	shutdownAll = true
	shutdownPolecatsOnly = true

	sessions := []string{"gt-thunder", "gt-crew-max", "gt-witness"}
	toStop, preserved := categorizeSessions(sessions)

	wantStop := []string{"gt-thunder"}
	wantPreserved := []string{"gt-crew-max", "gt-witness"}

	if !reflect.DeepEqual(sortedCopy(toStop), sortedCopy(wantStop)) {
		t.Errorf("toStop = %v, want %v (polecats-only should take precedence)", toStop, wantStop)
	}
	if !reflect.DeepEqual(sortedCopy(preserved), sortedCopy(wantPreserved)) {
		t.Errorf("preserved = %v, want %v", preserved, wantPreserved)
	}
}

func TestCategorizeSessions_EmptyInput(t *testing.T) {
	setupSessionRegistryForStart(t)
	saveShutdownFlags(t)

	shutdownAll = false
	shutdownPolecatsOnly = false

	toStop, preserved := categorizeSessions(nil)
	if len(toStop) != 0 || len(preserved) != 0 {
		t.Errorf("expected empty result for nil input, got toStop=%v preserved=%v", toStop, preserved)
	}

	toStop, preserved = categorizeSessions([]string{})
	if len(toStop) != 0 || len(preserved) != 0 {
		t.Errorf("expected empty result for empty input, got toStop=%v preserved=%v", toStop, preserved)
	}
}

func TestCategorizeSessions_UnparseableHQPolecatFallsBackToStop(t *testing.T) {
	// Sessions that IsKnownSession accepts but ParseSessionName does not
	// (shouldn't normally happen, but the code has a default branch that
	// treats them as workers/polecats). Use hq-overseer as a simple case:
	// with "hq" registered as a rig prefix, overseer isn't a recognized role.
	// IsKnownSession returns true (hq- prefix), categorizeSessions should
	// fall through to the default "stop as worker" bucket.
	setupSessionRegistryForStart(t)
	saveShutdownFlags(t)

	shutdownAll = false
	shutdownPolecatsOnly = false

	// "hq-overseer" matches the hq- prefix (IsKnownSession=true) and parses
	// as a polecat named "overseer" in the "knjn" rig (because hq is a
	// registered prefix). It is NOT crew, so the default branch stops it.
	sessions := []string{"hq-overseer"}
	toStop, preserved := categorizeSessions(sessions)

	if len(preserved) != 0 {
		t.Errorf("preserved = %v, want empty", preserved)
	}
	if len(toStop) != 1 || toStop[0] != "hq-overseer" {
		t.Errorf("toStop = %v, want [hq-overseer]", toStop)
	}
}

// --- getCrewToStart tests ----------------------------------------------------

// writeRigSettings writes a RigSettings JSON file at the conventional rig
// settings path (<rigPath>/settings/config.json) and returns the *rig.Rig
// that points at it.
func writeRigSettings(t *testing.T, rigPath string, settings *config.RigSettings) *rig.Rig {
	t.Helper()

	settingsDir := filepath.Join(rigPath, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}

	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}

	settingsPath := filepath.Join(settingsDir, "config.json")
	if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	return &rig.Rig{Name: filepath.Base(rigPath), Path: rigPath}
}

func TestGetCrewToStart_NoSettingsFile(t *testing.T) {
	dir := t.TempDir()
	r := &rig.Rig{Name: "test-rig", Path: dir}

	got := getCrewToStart(r)
	if got != nil {
		t.Errorf("getCrewToStart with missing settings = %v, want nil", got)
	}
}

func TestGetCrewToStart_NoCrewConfig(t *testing.T) {
	dir := t.TempDir()
	r := writeRigSettings(t, dir, &config.RigSettings{
		Type:    "rig-settings",
		Version: config.CurrentRigSettingsVersion,
		// Crew is nil
	})

	got := getCrewToStart(r)
	if got != nil {
		t.Errorf("getCrewToStart with nil Crew = %v, want nil", got)
	}
}

func TestGetCrewToStart_EmptyStartup(t *testing.T) {
	dir := t.TempDir()
	r := writeRigSettings(t, dir, &config.RigSettings{
		Type:    "rig-settings",
		Version: config.CurrentRigSettingsVersion,
		Crew:    &config.CrewConfig{Startup: ""},
	})

	got := getCrewToStart(r)
	if got != nil {
		t.Errorf("getCrewToStart with empty startup = %v, want nil", got)
	}
}

func TestGetCrewToStart_NoneStartup(t *testing.T) {
	dir := t.TempDir()
	r := writeRigSettings(t, dir, &config.RigSettings{
		Type:    "rig-settings",
		Version: config.CurrentRigSettingsVersion,
		Crew:    &config.CrewConfig{Startup: "none"},
	})

	got := getCrewToStart(r)
	if got != nil {
		t.Errorf("getCrewToStart with \"none\" = %v, want nil", got)
	}
}

func TestGetCrewToStart_SingleName(t *testing.T) {
	dir := t.TempDir()
	r := writeRigSettings(t, dir, &config.RigSettings{
		Type:    "rig-settings",
		Version: config.CurrentRigSettingsVersion,
		Crew:    &config.CrewConfig{Startup: "max"},
	})

	got := getCrewToStart(r)
	want := []string{"max"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("getCrewToStart = %v, want %v", got, want)
	}
}

func TestGetCrewToStart_ParsesMultipleForms(t *testing.T) {
	cases := []struct {
		name    string
		startup string
		want    []string
	}{
		{
			name:    "and-separated",
			startup: "max and joe",
			want:    []string{"max", "joe"},
		},
		{
			name:    "comma-separated",
			startup: "max,joe",
			want:    []string{"max", "joe"},
		},
		{
			name:    "comma-space-separated",
			startup: "max, joe",
			want:    []string{"max", "joe"},
		},
		{
			name:    "three names with mixed separators",
			startup: "max, joe and emma",
			want:    []string{"max", "joe", "emma"},
		},
		{
			name:    "whitespace around names is trimmed",
			startup: "  max  ,  joe  ",
			want:    []string{"max", "joe"},
		},
		{
			name:    "empty entries between commas are skipped",
			startup: "max,,joe",
			want:    []string{"max", "joe"},
		},
		{
			name:    "trailing comma is tolerated",
			startup: "max,",
			want:    []string{"max"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			r := writeRigSettings(t, dir, &config.RigSettings{
				Type:    "rig-settings",
				Version: config.CurrentRigSettingsVersion,
				Crew:    &config.CrewConfig{Startup: tc.startup},
			})

			got := getCrewToStart(r)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("getCrewToStart(%q) = %v, want %v", tc.startup, got, tc.want)
			}
		})
	}
}

func TestGetCrewToStart_MalformedSettingsReturnsNil(t *testing.T) {
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Write garbage that is not valid JSON; LoadRigSettings should fail and
	// getCrewToStart should swallow the error and return nil.
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"),
		[]byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	r := &rig.Rig{Name: "bad-rig", Path: dir}
	got := getCrewToStart(r)
	if got != nil {
		t.Errorf("getCrewToStart on malformed JSON = %v, want nil", got)
	}
}

// --- defaultOrphanGraceSecs sanity check ------------------------------------

func TestDefaultOrphanGraceSecs_IsShorterThanUserDefault(t *testing.T) {
	// The automatic-sweep grace period must be less than the user-specified
	// --cleanup-orphans-grace-secs default (60s). If someone bumps one
	// without the other, this test flags the inconsistency.
	const userDefault = 60
	if defaultOrphanGraceSecs <= 0 {
		t.Errorf("defaultOrphanGraceSecs = %d, want > 0", defaultOrphanGraceSecs)
	}
	if defaultOrphanGraceSecs >= userDefault {
		t.Errorf("defaultOrphanGraceSecs = %d, want < %d (user-default for --cleanup-orphans-grace-secs)",
			defaultOrphanGraceSecs, userDefault)
	}
}
