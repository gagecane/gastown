package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/upstreamsync"
)

func TestApplyUpstreamConfigSet_Enabled(t *testing.T) {
	cfg := &config.UpstreamSyncConfig{}
	if err := applyUpstreamConfigSet(cfg, "enabled=true"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Enabled {
		t.Errorf("Enabled = false, want true")
	}
	if err := applyUpstreamConfigSet(cfg, "enabled=false"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Enabled {
		t.Errorf("Enabled = true, want false")
	}
}

func TestApplyUpstreamConfigSet_Strings(t *testing.T) {
	cfg := &config.UpstreamSyncConfig{}
	cases := []struct {
		kv  string
		get func() string
	}{
		{"upstream_remote=fork", func() string { return cfg.UpstreamRemote }},
		{"upstream_branch=trunk", func() string { return cfg.UpstreamBranch }},
		{"target_branch=main", func() string { return cfg.TargetBranch }},
	}
	for _, c := range cases {
		if err := applyUpstreamConfigSet(cfg, c.kv); err != nil {
			t.Fatalf("applyUpstreamConfigSet(%q): %v", c.kv, err)
		}
	}
	if cfg.UpstreamRemote != "fork" {
		t.Errorf("UpstreamRemote = %q, want fork", cfg.UpstreamRemote)
	}
	if cfg.UpstreamBranch != "trunk" {
		t.Errorf("UpstreamBranch = %q, want trunk", cfg.UpstreamBranch)
	}
	if cfg.TargetBranch != "main" {
		t.Errorf("TargetBranch = %q, want main", cfg.TargetBranch)
	}
}

func TestApplyUpstreamConfigSet_Strategy(t *testing.T) {
	cfg := &config.UpstreamSyncConfig{}
	for _, valid := range []string{"merge", "rebase", "fast-forward"} {
		if err := applyUpstreamConfigSet(cfg, "strategy="+valid); err != nil {
			t.Errorf("expected %q to be accepted, got %v", valid, err)
		}
	}
	if err := applyUpstreamConfigSet(cfg, "strategy=cherry-pick"); err == nil {
		t.Error("expected error for invalid strategy")
	}
}

func TestApplyUpstreamConfigSet_Cadence(t *testing.T) {
	cfg := &config.UpstreamSyncConfig{}
	if err := applyUpstreamConfigSet(cfg, "cadence_minutes=120"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.CadenceMinutes != 120 {
		t.Errorf("CadenceMinutes = %d, want 120", cfg.CadenceMinutes)
	}
	for _, bad := range []string{"cadence_minutes=0", "cadence_minutes=-5", "cadence_minutes=abc"} {
		if err := applyUpstreamConfigSet(cfg, bad); err == nil {
			t.Errorf("expected error for %q, got nil", bad)
		}
	}
}

func TestApplyUpstreamConfigSet_ConflictResolution(t *testing.T) {
	cfg := &config.UpstreamSyncConfig{}
	if err := applyUpstreamConfigSet(cfg, "conflict_resolution=agent"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := applyUpstreamConfigSet(cfg, "conflict_resolution=escalate"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := applyUpstreamConfigSet(cfg, "conflict_resolution=ignore"); err == nil {
		t.Error("expected error for invalid mode")
	}
}

func TestApplyUpstreamConfigSet_UnknownKey(t *testing.T) {
	cfg := &config.UpstreamSyncConfig{}
	if err := applyUpstreamConfigSet(cfg, "frobnicate=42"); err == nil {
		t.Error("expected error for unknown key, got nil")
	}
}

func TestApplyUpstreamConfigSet_MalformedInput(t *testing.T) {
	cfg := &config.UpstreamSyncConfig{}
	for _, bad := range []string{"no_equals_sign", "=missing_key"} {
		if err := applyUpstreamConfigSet(cfg, bad); err == nil {
			t.Errorf("expected error for %q, got nil", bad)
		}
	}
}

func TestBuildHistoryDetail(t *testing.T) {
	cases := []struct {
		name string
		a    upstreamsync.SyncAttempt
		want string // substring expected in output
	}{
		{
			name: "success with shas",
			a: upstreamsync.SyncAttempt{
				Outcome:     "success",
				PreSyncSHA:  "1234567abc",
				PostSyncSHA: "abcdef0123",
			},
			want: "1234567",
		},
		{
			name: "conflict with files",
			a: upstreamsync.SyncAttempt{
				Outcome:   "conflict",
				Conflicts: []string{"foo.go", "bar.go"},
			},
			want: "foo.go",
		},
		{
			name: "gate failure",
			a: upstreamsync.SyncAttempt{
				Outcome: "gate-failure",
				GateResults: map[string]upstreamsync.GateResult{
					"go test ./...": upstreamsync.GateFail,
					"go build":      upstreamsync.GatePass,
				},
			},
			want: "go test",
		},
		{
			name: "skipped",
			a:    upstreamsync.SyncAttempt{Outcome: "skipped"},
			want: "no upstream",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildHistoryDetail(tc.a)
			if !strings.Contains(got, tc.want) {
				t.Errorf("buildHistoryDetail = %q, want substring %q", got, tc.want)
			}
		})
	}
}

func TestCompactTime(t *testing.T) {
	cases := []struct {
		input string
		want  string // substring
	}{
		{"", "—"},
		{"not-a-time", "not-a-time"},
		{"2026-05-25T18:30:42Z", "2026-05-25"},
	}
	for _, tc := range cases {
		got := compactTime(tc.input)
		if !strings.Contains(got, tc.want) {
			t.Errorf("compactTime(%q) = %q, want substring %q", tc.input, got, tc.want)
		}
	}
}

func TestBuildUpstreamConfigOutput_NilSafe(t *testing.T) {
	out := buildUpstreamConfigOutput("myrig", nil)
	if out.Rig != "myrig" {
		t.Errorf("Rig = %q, want myrig", out.Rig)
	}
	if out.Enabled {
		t.Errorf("Enabled = true for nil config, want false")
	}
	// Nil-safe defaults should kick in.
	if out.UpstreamRemote == "" {
		t.Errorf("UpstreamRemote should default to non-empty")
	}
	if out.UpstreamBranch == "" {
		t.Errorf("UpstreamBranch should default to non-empty")
	}
	if out.CadenceMinutes <= 0 {
		t.Errorf("CadenceMinutes should default to positive, got %d", out.CadenceMinutes)
	}
}

func TestBuildUpstreamConfigOutput_PopulatedConfig(t *testing.T) {
	cfg := &config.UpstreamSyncConfig{
		Enabled:                true,
		UpstreamRemote:         "fork",
		UpstreamBranch:         "trunk",
		TargetBranch:           "main",
		Strategy:               "rebase",
		CadenceMinutes:         240,
		MaxConsecutiveFailures: 5,
		ConflictResolution:     "escalate",
	}
	out := buildUpstreamConfigOutput("myrig", cfg)
	if !out.Enabled {
		t.Errorf("Enabled = false, want true")
	}
	if out.UpstreamRemote != "fork" {
		t.Errorf("UpstreamRemote = %q, want fork", out.UpstreamRemote)
	}
	if out.Strategy != "rebase" {
		t.Errorf("Strategy = %q, want rebase", out.Strategy)
	}
	if out.CadenceMinutes != 240 {
		t.Errorf("CadenceMinutes = %d, want 240", out.CadenceMinutes)
	}
	if out.ConflictResolution != "escalate" {
		t.Errorf("ConflictResolution = %q, want escalate", out.ConflictResolution)
	}
}

func TestIconForGate(t *testing.T) {
	cases := []struct {
		r    upstreamsync.GateResult
		want string
	}{
		{upstreamsync.GatePass, "✓"},
		{upstreamsync.GateFail, "✗"},
		{upstreamsync.GateSkip, "⊘"},
		{upstreamsync.GateResult("bogus"), "?"},
	}
	for _, c := range cases {
		got := iconForGate(c.r)
		if got != c.want {
			t.Errorf("iconForGate(%q) = %q, want %q", c.r, got, c.want)
		}
	}
}
