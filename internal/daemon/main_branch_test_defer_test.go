package daemon

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// TestMainBranchTestDeferLoadPerCore pins the threshold resolution: nil/absent
// config falls back to the default, an explicit value is honored, and a value
// <= 0 disables deferral by being returned verbatim (the caller treats <= 0 as
// "always run").
func TestMainBranchTestDeferLoadPerCore(t *testing.T) {
	f := func(v float64) *float64 { return &v }

	cases := []struct {
		name   string
		config *DaemonPatrolConfig
		want   float64
	}{
		{"nil config → default", nil, defaultMainBranchTestDeferLoadPerCore},
		{"nil patrols → default", &DaemonPatrolConfig{}, defaultMainBranchTestDeferLoadPerCore},
		{"nil MainBranchTest → default", &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}, defaultMainBranchTestDeferLoadPerCore},
		{"unset field → default", &DaemonPatrolConfig{Patrols: &PatrolsConfig{MainBranchTest: &MainBranchTestConfig{}}}, defaultMainBranchTestDeferLoadPerCore},
		{"configured value honored", &DaemonPatrolConfig{Patrols: &PatrolsConfig{MainBranchTest: &MainBranchTestConfig{DeferLoadPerCore: f(2.5)}}}, 2.5},
		{"zero disables (returned verbatim)", &DaemonPatrolConfig{Patrols: &PatrolsConfig{MainBranchTest: &MainBranchTestConfig{DeferLoadPerCore: f(0)}}}, 0},
		{"negative disables (returned verbatim)", &DaemonPatrolConfig{Patrols: &PatrolsConfig{MainBranchTest: &MainBranchTestConfig{DeferLoadPerCore: f(-1)}}}, -1},
	}
	for _, tc := range cases {
		if got := mainBranchTestDeferLoadPerCore(tc.config); got != tc.want {
			t.Errorf("%s: mainBranchTestDeferLoadPerCore = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// newDeferTestDaemon builds a Daemon with the main_branch_test patrol enabled
// and the given defer threshold, capturing its log output for assertions.
func newDeferTestDaemon(t *testing.T, threshold *float64) (*Daemon, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(&buf, "", 0),
		patrolConfig: &DaemonPatrolConfig{
			Patrols: &PatrolsConfig{
				MainBranchTest: &MainBranchTestConfig{
					Enabled:          true,
					DeferLoadPerCore: threshold,
				},
			},
		},
	}
	return d, &buf
}

// withLoadSampler swaps the package load sampler for the duration of a test.
func withLoadSampler(t *testing.T, load float64) {
	t.Helper()
	orig := mainBranchTestLoadSampler
	mainBranchTestLoadSampler = func() float64 { return load }
	t.Cleanup(func() { mainBranchTestLoadSampler = orig })
}

// TestRunMainBranchTests_DefersUnderExtremeLoad proves the hq-5em9k deferral:
// when host load/core exceeds the threshold the cycle is SKIPPED before any
// gate work begins (no "starting patrol cycle" log), avoiding piling a heavy
// suite onto a saturated host.
func TestRunMainBranchTests_DefersUnderExtremeLoad(t *testing.T) {
	threshold := 4.0
	d, buf := newDeferTestDaemon(t, &threshold)
	withLoadSampler(t, threshold+1.0) // extreme: above threshold

	d.runMainBranchTests()

	out := buf.String()
	if !strings.Contains(out, "DEFERRED this cycle") {
		t.Fatalf("expected a DEFERRED log under extreme load, got:\n%s", out)
	}
	if strings.Contains(out, "starting patrol cycle") {
		t.Fatalf("must NOT start the patrol cycle when deferring; got:\n%s", out)
	}
}

// TestRunMainBranchTests_ProceedsUnderNormalLoad proves load at/below the
// threshold does NOT defer: the cycle starts normally (it then no-ops on an
// empty town, which is fine — we only assert the deferral gate let it through).
func TestRunMainBranchTests_ProceedsUnderNormalLoad(t *testing.T) {
	threshold := 4.0
	d, buf := newDeferTestDaemon(t, &threshold)
	withLoadSampler(t, threshold-1.0) // below threshold

	d.runMainBranchTests()

	out := buf.String()
	if strings.Contains(out, "DEFERRED this cycle") {
		t.Fatalf("must NOT defer when load is below threshold; got:\n%s", out)
	}
	if !strings.Contains(out, "starting patrol cycle") {
		t.Fatalf("expected the patrol cycle to start under normal load, got:\n%s", out)
	}
}

// TestRunMainBranchTests_DeferralDisabled proves a threshold <= 0 disables the
// gate entirely: even an absurd load proceeds (deferral is opt-outable).
func TestRunMainBranchTests_DeferralDisabled(t *testing.T) {
	disabled := 0.0
	d, buf := newDeferTestDaemon(t, &disabled)
	withLoadSampler(t, 999.0) // would defer if the gate were active

	d.runMainBranchTests()

	out := buf.String()
	if strings.Contains(out, "DEFERRED this cycle") {
		t.Fatalf("threshold <= 0 must disable deferral; got:\n%s", out)
	}
	if !strings.Contains(out, "starting patrol cycle") {
		t.Fatalf("expected the patrol cycle to start when deferral disabled, got:\n%s", out)
	}
}

// TestRunMainBranchTests_UnknownLoadDoesNotDefer proves a 0 load reading
// (metric unavailable, e.g. Windows) is treated as "not extreme" — we never
// skip a cycle on an unknown metric.
func TestRunMainBranchTests_UnknownLoadDoesNotDefer(t *testing.T) {
	threshold := 4.0
	d, buf := newDeferTestDaemon(t, &threshold)
	withLoadSampler(t, 0) // unavailable

	d.runMainBranchTests()

	if strings.Contains(buf.String(), "DEFERRED this cycle") {
		t.Fatalf("an unavailable load metric (0) must not defer; got:\n%s", buf.String())
	}
}
