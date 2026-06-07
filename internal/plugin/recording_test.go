package plugin

import (
	"testing"
	"time"
)

func TestPluginRunRecord(t *testing.T) {
	record := PluginRunRecord{
		PluginName: "test-plugin",
		RigName:    "gastown",
		Result:     ResultSuccess,
		Body:       "Test run completed successfully",
	}

	if record.PluginName != "test-plugin" {
		t.Errorf("expected plugin name 'test-plugin', got %q", record.PluginName)
	}
	if record.RigName != "gastown" {
		t.Errorf("expected rig name 'gastown', got %q", record.RigName)
	}
	if record.Result != ResultSuccess {
		t.Errorf("expected result 'success', got %q", record.Result)
	}
}

func TestRunResultConstants(t *testing.T) {
	if ResultSuccess != "success" {
		t.Errorf("expected ResultSuccess to be 'success', got %q", ResultSuccess)
	}
	if ResultFailure != "failure" {
		t.Errorf("expected ResultFailure to be 'failure', got %q", ResultFailure)
	}
	if ResultSkipped != "skipped" {
		t.Errorf("expected ResultSkipped to be 'skipped', got %q", ResultSkipped)
	}
}

func TestNewRecorder(t *testing.T) {
	recorder := NewRecorder("/tmp/test-town")
	if recorder == nil {
		t.Fatal("NewRecorder returned nil")
	}
	if recorder.townRoot != "/tmp/test-town" {
		t.Errorf("expected townRoot '/tmp/test-town', got %q", recorder.townRoot)
	}
}

func TestCooldownDurationParsing(t *testing.T) {
	t.Parallel()
	// Verify that plugin gate durations (Go time.ParseDuration format)
	// are parsed correctly. This is critical because bd's compact duration
	// uses "m" for months, while Go uses "m" for minutes. The fix computes
	// an absolute RFC3339 cutoff instead of passing the raw duration to bd.
	cases := []struct {
		input   string
		wantDur time.Duration
		wantErr bool
	}{
		{"5m", 5 * time.Minute, false},
		{"30m", 30 * time.Minute, false},
		{"1h", 1 * time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"1h30m", 90 * time.Minute, false},
		{"500ms", 500 * time.Millisecond, false},
		{"bogus", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			d, err := time.ParseDuration(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error for %q, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
			if d != tc.wantDur {
				t.Errorf("ParseDuration(%q) = %v, want %v", tc.input, d, tc.wantDur)
			}
			// Verify the cutoff time is in the past and approximately correct.
			cutoff := time.Now().Add(-d)
			elapsed := time.Since(cutoff)
			if elapsed < d-time.Second || elapsed > d+time.Second {
				t.Errorf("cutoff drift: expected ~%v ago, got %v ago", d, elapsed)
			}
		})
	}
}

func TestResultInflightConstant(t *testing.T) {
	// The literal value MUST be "inflight", NOT "dispatched": the auto-dispatch
	// plugin's dog records result:dispatched as a successful TERMINAL outcome,
	// so reusing that string would make a real success look non-terminal.
	if ResultInflight != "inflight" {
		t.Errorf("expected ResultInflight to be 'inflight', got %q", ResultInflight)
	}
	if ResultInflight == "dispatched" {
		t.Error("ResultInflight must not collide with the auto-dispatch result:dispatched terminal outcome")
	}
}

func TestCooldownSatisfied(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	grace := 5 * time.Minute

	mk := func(result RunResult, ageMinutes int) *PluginRunBead {
		return &PluginRunBead{
			Result:    result,
			CreatedAt: now.Add(-time.Duration(ageMinutes) * time.Minute),
		}
	}

	cases := []struct {
		name string
		runs []*PluginRunBead
		want bool
	}{
		{
			name: "no runs -> gate open",
			runs: nil,
			want: false,
		},
		{
			name: "terminal success within window -> satisfied",
			runs: []*PluginRunBead{mk(ResultSuccess, 10)},
			want: true,
		},
		{
			name: "terminal failure within window -> satisfied (a real run happened)",
			runs: []*PluginRunBead{mk(ResultFailure, 10)},
			want: true,
		},
		{
			name: "fresh inflight within grace -> satisfied (in-flight, don't storm)",
			runs: []*PluginRunBead{mk(ResultInflight, 2)},
			want: true,
		},
		{
			name: "stale inflight past grace -> gate re-opens (THE BUG: was unbounded drift)",
			runs: []*PluginRunBead{mk(ResultInflight, 9)},
			want: false,
		},
		{
			name: "stale inflight + terminal completion -> satisfied by completion",
			runs: []*PluginRunBead{mk(ResultInflight, 9), mk(ResultSuccess, 8)},
			want: true,
		},
		{
			name: "inflight exactly at grace cutoff -> not after cutoff -> gate open",
			runs: []*PluginRunBead{mk(ResultInflight, 5)},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cooldownSatisfied(tc.runs, now, grace)
			if got != tc.want {
				t.Errorf("cooldownSatisfied() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDispatchGrace(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		exec     *Execution
		cooldown time.Duration
		want     time.Duration
	}{
		{
			name:     "no execution config -> default grace",
			exec:     nil,
			cooldown: 15 * time.Minute,
			want:     defaultDispatchGrace,
		},
		{
			name:     "empty timeout -> default grace",
			exec:     &Execution{Timeout: ""},
			cooldown: 15 * time.Minute,
			want:     defaultDispatchGrace,
		},
		{
			name:     "exec timeout + buffer",
			exec:     &Execution{Timeout: "5m"},
			cooldown: 15 * time.Minute,
			want:     7 * time.Minute, // 5m + 2m buffer
		},
		{
			name:     "grace capped at cooldown",
			exec:     &Execution{Timeout: "20m"},
			cooldown: 15 * time.Minute,
			want:     15 * time.Minute, // 22m capped to 15m
		},
		{
			name:     "bogus timeout -> default grace",
			exec:     &Execution{Timeout: "not-a-duration"},
			cooldown: 15 * time.Minute,
			want:     defaultDispatchGrace,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Plugin{Execution: tc.exec}
			if got := p.DispatchGrace(tc.cooldown); got != tc.want {
				t.Errorf("DispatchGrace() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCronDue(t *testing.T) {
	t.Parallel()
	// Schedule fires daily at 12:00. Evaluate at 13:00 the same day, so the
	// most recent scheduled fire is today 12:00.
	sched, err := parseCron("0 12 * * *")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 4, 13, 0, 0, 0, time.UTC)
	prevFire := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)

	mk := func(result RunResult, at time.Time) *PluginRunBead {
		return &PluginRunBead{Result: result, CreatedAt: at}
	}

	cases := []struct {
		name string
		runs []*PluginRunBead
		want bool
	}{
		{
			name: "no runs -> due",
			runs: nil,
			want: true,
		},
		{
			name: "terminal run after this fire -> already serviced, not due",
			runs: []*PluginRunBead{mk(ResultSuccess, prevFire.Add(10*time.Minute))},
			want: false,
		},
		{
			name: "terminal run exactly at fire -> serviced, not due",
			runs: []*PluginRunBead{mk(ResultSuccess, prevFire)},
			want: false,
		},
		{
			name: "terminal run before this fire (yesterday) -> due",
			runs: []*PluginRunBead{mk(ResultSuccess, prevFire.Add(-24*time.Hour))},
			want: true,
		},
		{
			// The daemon's dispatch record for this fire suppresses re-dispatch
			// for the rest of the day even though no terminal receipt followed —
			// this is the gu-jifj5 storm guard: a cron fire dispatched once must
			// not re-fire until the next scheduled fire.
			name: "inflight at this fire, no terminal receipt -> serviced, not due",
			runs: []*PluginRunBead{mk(ResultInflight, prevFire.Add(time.Minute))},
			want: false,
		},
		{
			// A long-elapsed inflight is still "after this fire" while prevFire is
			// today's 12:00 — it stays suppressed until tomorrow's fire advances
			// prevFire past it. (Old cooldown-style grace-reopen is gone.)
			name: "inflight long after this fire -> still serviced, not due",
			runs: []*PluginRunBead{mk(ResultInflight, now.Add(-30*time.Minute))},
			want: false,
		},
		{
			// Yesterday's dispatch record does not service today's fire.
			name: "inflight before this fire (yesterday) -> due",
			runs: []*PluginRunBead{mk(ResultInflight, prevFire.Add(-24*time.Hour))},
			want: true,
		},
		{
			name: "stale inflight + terminal completion after fire -> serviced, not due",
			runs: []*PluginRunBead{mk(ResultInflight, prevFire.Add(time.Minute)), mk(ResultSuccess, prevFire.Add(2*time.Minute))},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cronDue(sched, tc.runs, now)
			if got != tc.want {
				t.Errorf("cronDue() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCronDue_ImpossibleSchedule(t *testing.T) {
	t.Parallel()
	sched, err := parseCron("0 0 30 2 *") // Feb 30 never occurs
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 4, 13, 0, 0, 0, time.UTC)
	if cronDue(sched, nil, now) {
		t.Error("cronDue for an impossible schedule should never be due")
	}
}

// Integration tests for RecordRun, GetLastRun, GetRunsSince require
// a working beads installation and are skipped in unit tests.
// These functions shell out to `bd` commands.
