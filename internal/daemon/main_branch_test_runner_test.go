package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMainBranchTestInterval(t *testing.T) {
	// Nil config returns default
	if got := mainBranchTestInterval(nil); got != defaultMainBranchTestInterval {
		t.Errorf("expected default %v, got %v", defaultMainBranchTestInterval, got)
	}

	// Configured interval
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MainBranchTest: &MainBranchTestConfig{
				Enabled:     true,
				IntervalStr: "15m",
			},
		},
	}
	if got := mainBranchTestInterval(config); got.Minutes() != 15 {
		t.Errorf("expected 15m, got %v", got)
	}

	// Invalid interval returns default
	config.Patrols.MainBranchTest.IntervalStr = "bad"
	if got := mainBranchTestInterval(config); got != defaultMainBranchTestInterval {
		t.Errorf("expected default for invalid interval, got %v", got)
	}
}

func TestMainBranchTestTimeout(t *testing.T) {
	// Nil config returns default
	if got := mainBranchTestTimeout(nil); got != defaultMainBranchTestTimeout {
		t.Errorf("expected default %v, got %v", defaultMainBranchTestTimeout, got)
	}

	// Configured timeout
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MainBranchTest: &MainBranchTestConfig{
				Enabled:    true,
				TimeoutStr: "5m",
			},
		},
	}
	if got := mainBranchTestTimeout(config); got.Minutes() != 5 {
		t.Errorf("expected 5m, got %v", got)
	}
}

func TestMainBranchTestRigs(t *testing.T) {
	// Nil config returns nil
	if got := mainBranchTestRigs(nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}

	// Configured rigs
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MainBranchTest: &MainBranchTestConfig{
				Enabled: true,
				Rigs:    []string{"gastown", "beads"},
			},
		},
	}
	got := mainBranchTestRigs(config)
	if len(got) != 2 || got[0] != "gastown" || got[1] != "beads" {
		t.Errorf("expected [gastown beads], got %v", got)
	}
}

func TestIsPatrolEnabledMainBranchTest(t *testing.T) {
	// Nil config — disabled (opt-in)
	if IsPatrolEnabled(nil, "main_branch_test") {
		t.Error("expected main_branch_test disabled with nil config")
	}

	// Explicitly disabled
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MainBranchTest: &MainBranchTestConfig{
				Enabled: false,
			},
		},
	}
	if IsPatrolEnabled(config, "main_branch_test") {
		t.Error("expected main_branch_test disabled when Enabled=false")
	}

	// Enabled
	config.Patrols.MainBranchTest.Enabled = true
	if !IsPatrolEnabled(config, "main_branch_test") {
		t.Error("expected main_branch_test enabled when Enabled=true")
	}
}

func TestLoadRigGateConfig(t *testing.T) {
	t.Run("no config file", func(t *testing.T) {
		cfg, err := loadRigGateConfig("/nonexistent/path")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg != nil {
			t.Errorf("expected nil config for nonexistent path, got %+v", cfg)
		}
	})

	t.Run("no merge_queue section", func(t *testing.T) {
		dir := t.TempDir()
		data := `{"type":"rig","version":1,"name":"test"}`
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg != nil {
			t.Errorf("expected nil config for no merge_queue, got %+v", cfg)
		}
	})

	t.Run("test_command only", func(t *testing.T) {
		dir := t.TempDir()
		data := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"test_command": "go test ./...",
			},
		}
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if cfg.TestCommand != "go test ./..." {
			t.Errorf("expected 'go test ./...', got %q", cfg.TestCommand)
		}
	})

	t.Run("gates configured", func(t *testing.T) {
		dir := t.TempDir()
		data := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"gates": map[string]interface{}{
					"build": map[string]interface{}{"cmd": "go build ./..."},
					"test":  map[string]interface{}{"cmd": "go test ./..."},
					"lint":  map[string]interface{}{"cmd": "golangci-lint run"},
				},
			},
		}
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if len(cfg.Gates) != 3 {
			t.Errorf("expected 3 gates, got %d", len(cfg.Gates))
		}
		if cfg.Gates["build"].Cmd != "go build ./..." {
			t.Errorf("expected build gate 'go build ./...', got %q", cfg.Gates["build"].Cmd)
		}
		// Phase is omitted in this fixture, so it should round-trip as "" and
		// be treated as pre-merge by the runner.
		if cfg.Gates["build"].Phase != "" {
			t.Errorf("expected build gate phase empty (pre-merge default), got %q", cfg.Gates["build"].Phase)
		}
	})

	t.Run("no test commands", func(t *testing.T) {
		dir := t.TempDir()
		data := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"enabled": true,
			},
		}
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg != nil {
			t.Errorf("expected nil for no test commands, got %+v", cfg)
		}
	})

	// The following subtests cover the settings/config.json migration:
	// merge_queue is moving from <rig>/config.json (rig identity) to
	// <rig>/settings/config.json (behavioral RigSettings). Resolution order
	// prefers settings/config.json but falls back to config.json so
	// pre-migration rigs keep working.

	t.Run("settings/config.json preferred over rig-root config.json", func(t *testing.T) {
		dir := t.TempDir()

		// Rig-root config.json has the old value.
		root := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"test_command": "OLD",
			},
		}
		rootRaw, _ := json.Marshal(root)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), rootRaw, 0644); err != nil {
			t.Fatal(err)
		}

		// settings/config.json has the new value — should win.
		settingsDir := filepath.Join(dir, "settings")
		if err := os.MkdirAll(settingsDir, 0755); err != nil {
			t.Fatal(err)
		}
		settings := map[string]interface{}{
			"type":    "rig-settings",
			"version": 1,
			"merge_queue": map[string]interface{}{
				"test_command": "NEW",
			},
		}
		settingsRaw, _ := json.Marshal(settings)
		if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), settingsRaw, 0644); err != nil {
			t.Fatal(err)
		}

		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if cfg.TestCommand != "NEW" {
			t.Errorf("expected 'NEW' (from settings/config.json), got %q", cfg.TestCommand)
		}
	})

	t.Run("falls back to rig-root config.json when settings/config.json absent", func(t *testing.T) {
		dir := t.TempDir()
		root := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"test_command": "go test ./...",
			},
		}
		raw, _ := json.Marshal(root)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0644); err != nil {
			t.Fatal(err)
		}

		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config (fallback to rig-root)")
		}
		if cfg.TestCommand != "go test ./..." {
			t.Errorf("expected fallback value, got %q", cfg.TestCommand)
		}
	})

	t.Run("falls back when settings/config.json has no merge_queue block", func(t *testing.T) {
		// This is the exact scenario rc-2ux enables: remove merge_queue from
		// rig-root config.json (identity-only) while it lives in settings.
		// But during migration, an operator might have a settings/config.json
		// with no merge_queue block yet — we should still find the legacy one.
		dir := t.TempDir()

		settingsDir := filepath.Join(dir, "settings")
		if err := os.MkdirAll(settingsDir, 0755); err != nil {
			t.Fatal(err)
		}
		settings := map[string]interface{}{
			"type":    "rig-settings",
			"version": 1,
			"theme":   map[string]interface{}{"name": "ocean"},
		}
		settingsRaw, _ := json.Marshal(settings)
		if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), settingsRaw, 0644); err != nil {
			t.Fatal(err)
		}

		root := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"test_command": "legacy-test",
			},
		}
		rootRaw, _ := json.Marshal(root)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), rootRaw, 0644); err != nil {
			t.Fatal(err)
		}

		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config (fallback past settings without merge_queue)")
		}
		if cfg.TestCommand != "legacy-test" {
			t.Errorf("expected 'legacy-test' from fallback, got %q", cfg.TestCommand)
		}
	})

	t.Run("settings/config.json alone is sufficient after migration", func(t *testing.T) {
		// Post-migration: rig-root config.json has no merge_queue (or is
		// missing entirely). settings/config.json is the canonical source.
		dir := t.TempDir()
		settingsDir := filepath.Join(dir, "settings")
		if err := os.MkdirAll(settingsDir, 0755); err != nil {
			t.Fatal(err)
		}
		settings := map[string]interface{}{
			"type":    "rig-settings",
			"version": 1,
			"merge_queue": map[string]interface{}{
				"test_command": "canonical-test",
			},
		}
		raw, _ := json.Marshal(settings)
		if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), raw, 0644); err != nil {
			t.Fatal(err)
		}

		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config from settings/config.json")
		}
		if cfg.TestCommand != "canonical-test" {
			t.Errorf("expected 'canonical-test', got %q", cfg.TestCommand)
		}
	})

	t.Run("gate phase is parsed and preserved", func(t *testing.T) {
		// Regression for gu-j1f7: main_branch_test must preserve per-gate
		// phase so the runner can skip post-squash gates. Previously the
		// loader dropped phase on the floor and the runner executed every
		// gate blindly, producing spurious failures (e.g., a post-squash
		// brazil-build gate run without its merge-squash context).
		dir := t.TempDir()
		data := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"gates": map[string]interface{}{
					"lint":          map[string]interface{}{"cmd": "lint.sh"}, // phase omitted
					"build":         map[string]interface{}{"cmd": "build.sh", "phase": "pre-merge"},
					"live-integ":    map[string]interface{}{"cmd": "integ.sh", "phase": "post-squash"},
					"downstream-ck": map[string]interface{}{"cmd": "ds.sh", "phase": "post-squash"},
				},
			},
		}
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if cfg.Gates["lint"].Phase != "" {
			t.Errorf("lint phase: got %q, want empty (pre-merge default)", cfg.Gates["lint"].Phase)
		}
		if cfg.Gates["build"].Phase != "pre-merge" {
			t.Errorf("build phase: got %q, want %q", cfg.Gates["build"].Phase, "pre-merge")
		}
		if cfg.Gates["live-integ"].Phase != "post-squash" {
			t.Errorf("live-integ phase: got %q, want %q", cfg.Gates["live-integ"].Phase, "post-squash")
		}
		if cfg.Gates["downstream-ck"].Phase != "post-squash" {
			t.Errorf("downstream-ck phase: got %q, want %q", cfg.Gates["downstream-ck"].Phase, "post-squash")
		}
	})
}

func TestContains(t *testing.T) {
	if !sliceContains([]string{"a", "b", "c"}, "b") {
		t.Error("expected true for 'b' in [a b c]")
	}
	if sliceContains([]string{"a", "b", "c"}, "d") {
		t.Error("expected false for 'd' in [a b c]")
	}
	if sliceContains(nil, "a") {
		t.Error("expected false for nil slice")
	}
}

func TestDefaultLifecycleConfigIncludesMainBranchTest(t *testing.T) {
	config := DefaultLifecycleConfig()
	if config.Patrols.MainBranchTest == nil {
		t.Fatal("expected MainBranchTest in default lifecycle config")
	}
	if !config.Patrols.MainBranchTest.Enabled {
		t.Error("expected MainBranchTest.Enabled=true")
	}
	if config.Patrols.MainBranchTest.IntervalStr != "30m" {
		t.Errorf("expected interval '30m', got %q", config.Patrols.MainBranchTest.IntervalStr)
	}
	if config.Patrols.MainBranchTest.TimeoutStr != "10m" {
		t.Errorf("expected timeout '10m', got %q", config.Patrols.MainBranchTest.TimeoutStr)
	}
}

func TestGetPatrolRigsMainBranchTest(t *testing.T) {
	t.Run("nil config returns nil", func(t *testing.T) {
		if got := GetPatrolRigs(nil, "main_branch_test"); got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("no rigs configured returns nil", func(t *testing.T) {
		config := &DaemonPatrolConfig{
			Patrols: &PatrolsConfig{
				MainBranchTest: &MainBranchTestConfig{Enabled: true},
			},
		}
		if got := GetPatrolRigs(config, "main_branch_test"); got != nil {
			t.Errorf("expected nil for empty rigs, got %v", got)
		}
	})

	t.Run("configured rigs are returned", func(t *testing.T) {
		config := &DaemonPatrolConfig{
			Patrols: &PatrolsConfig{
				MainBranchTest: &MainBranchTestConfig{
					Enabled: true,
					Rigs:    []string{"gastown", "beads"},
				},
			},
		}
		got := GetPatrolRigs(config, "main_branch_test")
		if len(got) != 2 || got[0] != "gastown" || got[1] != "beads" {
			t.Errorf("expected [gastown beads], got %v", got)
		}
	})
}

func TestEnsureLifecycleDefaultsFillsMainBranchTest(t *testing.T) {
	config := &DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Patrols: &PatrolsConfig{}, // All nil
	}
	changed := EnsureLifecycleDefaults(config)
	if !changed {
		t.Error("expected changed=true when MainBranchTest was nil")
	}
	if config.Patrols.MainBranchTest == nil {
		t.Fatal("expected MainBranchTest to be populated")
	}
	if !config.Patrols.MainBranchTest.Enabled {
		t.Error("expected MainBranchTest.Enabled=true after defaults")
	}
}

func TestGateEnv(t *testing.T) {
	townRoot := t.TempDir()
	env := gateEnv(townRoot)

	// Find the last PATH= and CI=true entries (last occurrence wins in exec.Cmd.Env)
	var lastPath string
	var hasCI bool
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			lastPath = strings.TrimPrefix(e, "PATH=")
		}
		if e == "CI=true" {
			hasCI = true
		}
	}

	if !hasCI {
		t.Error("CI=true missing from gate env")
	}
	if lastPath == "" {
		t.Fatal("no PATH entry in gate env")
	}

	home, _ := os.UserHomeDir()
	wantDirs := []string{
		filepath.Join(townRoot, "bin"),
		filepath.Join(home, "go", "bin"),
		filepath.Join(home, ".local", "bin"),
		"/usr/local/bin",
	}
	for _, d := range wantDirs {
		if !strings.Contains(lastPath, d) {
			t.Errorf("PATH missing %s; got %s", d, lastPath)
		}
	}

	// Original PATH is preserved
	if orig := os.Getenv("PATH"); orig != "" && !strings.Contains(lastPath, orig) {
		t.Errorf("PATH missing original PATH %q; got %s", orig, lastPath)
	}
}

// TestGateEnvScrubsDoltRouting is the gu-5ja0e regression: the daemon runs with
// GT_DOLT_PORT/BEADS_DOLT_PORT pinned to the production Dolt server (3307). If
// gateEnv passes those through to the gate's `go test`, beads-backed tests
// connect to production Dolt and leak orphan databases into .dolt-data/. gateEnv
// must scrub every Dolt-routing variable so the beads test-isolation safety net
// (PreventTestDoltLeak) engages.
func TestGateEnvScrubsDoltRouting(t *testing.T) {
	t.Setenv("GT_DOLT_PORT", "3307")
	t.Setenv("BEADS_DOLT_PORT", "3307")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("DOLT_ROOT_PATH", "/should/be/stripped")
	// A non-Dolt variable that must survive untouched.
	t.Setenv("BEADS_DIR", "/keep/me")

	env := gateEnv(t.TempDir())

	denied := []string{"GT_DOLT_PORT=", "BEADS_DOLT_PORT=", "BEADS_DOLT_SERVER_HOST=", "DOLT_ROOT_PATH="}
	var keptBeadsDir bool
	for _, e := range env {
		for _, bad := range denied {
			if strings.HasPrefix(e, bad) {
				t.Errorf("gate env leaked production Dolt-routing var: %q", e)
			}
		}
		if e == "BEADS_DIR=/keep/me" {
			keptBeadsDir = true
		}
	}
	if !keptBeadsDir {
		t.Error("gate env dropped non-Dolt var BEADS_DIR; scrubbing is too broad")
	}
}

// TestStripGateDoltEnv exercises the pure filter directly, including the
// defensive malformed-entry path.
func TestStripGateDoltEnv(t *testing.T) {
	in := []string{
		"GT_DOLT_PORT=3307",
		"BEADS_DOLT_SERVER_PORT=3307",
		"DOLT_ROOT_PATH=/x",
		"PATH=/usr/bin",
		"GT_RIG=gastown_upstream", // GT_ but not GT_DOLT_ — must survive
		"malformed-no-equals",     // defensive: kept verbatim
		"=leadingequals",          // defensive: kept verbatim
	}
	got := stripGateDoltEnv(in)
	want := map[string]bool{
		"PATH=/usr/bin":           true,
		"GT_RIG=gastown_upstream": true,
		"malformed-no-equals":     true,
		"=leadingequals":          true,
	}
	if len(got) != len(want) {
		t.Fatalf("stripGateDoltEnv returned %d entries, want %d: %v", len(got), len(want), got)
	}
	for _, e := range got {
		if !want[e] {
			t.Errorf("unexpected entry survived/changed: %q", e)
		}
	}
}

func TestRunPreBuildInstall_SkipsWithoutSetupCommand(t *testing.T) {
	// Core regression test for gu-pcm5: when merge_queue.setup_command is NOT
	// set, runPreBuildInstall must be a no-op regardless of what lockfiles
	// happen to exist in the worktree. Brazil-build rigs carry package.json
	// + package-lock.json for IDE/type-checking purposes but rely on the gate
	// itself (refinery-gate.sh → brazil-build) to install deps. Auto-running
	// `npm ci` against those rigs either does redundant work or fails with
	// E404 because package.json references Brazil-only @amzn/* deps that are
	// not published to CodeArtifact.
	cases := []struct {
		name  string
		files []string
		cfg   *rigGateConfig
	}{
		{
			name:  "nil cfg + full lockfile set → no-op",
			files: []string{"package.json", "package-lock.json"},
			cfg:   nil,
		},
		{
			name:  "empty setup_command + package-lock.json → no-op (brazil-build case)",
			files: []string{"package.json", "package-lock.json"},
			cfg:   &rigGateConfig{TestCommand: "make test"},
		},
		{
			name:  "empty setup_command + pnpm-lock → no-op",
			files: []string{"package.json", "pnpm-lock.yaml"},
			cfg:   &rigGateConfig{TestCommand: "make test"},
		},
		{
			name:  "empty setup_command + pyproject → no-op",
			files: []string{"pyproject.toml", "uv.lock"},
			cfg:   &rigGateConfig{TestCommand: "pytest"},
		},
		{
			name:  "empty setup_command + no lockfile → no-op",
			files: nil,
			cfg:   &rigGateConfig{TestCommand: "go test ./..."},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				// Seed with a file that would fail loudly if npm/pnpm/uv were
				// actually invoked ("{}" is not a valid package.json for npm
				// ci, but we never get that far — the test asserts we bail
				// before touching the filesystem).
				if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0644); err != nil {
					t.Fatal(err)
				}
			}

			d := &Daemon{
				ctx:    context.Background(),
				config: &Config{TownRoot: dir},
				logger: log.New(io.Discard, "", 0),
			}

			err := d.runPreBuildInstall(context.Background(), "testrig", dir, tc.cfg)
			if err != nil {
				t.Errorf("expected nil (no-op) for no setup_command, got: %v", err)
			}
		})
	}
}

func TestRunPreBuildInstall_RunsSetupCommandWhenSet(t *testing.T) {
	// When setup_command IS declared, it must be executed verbatim — this is
	// the opt-in path a rig uses to say "I really do need a pre-build
	// install." We assert the side-effect (marker file) rather than mock the
	// executor, since runCommandOnWorktree shells out.
	dir := t.TempDir()
	marker := filepath.Join(dir, "install.ran")

	d := &Daemon{
		ctx:    context.Background(),
		config: &Config{TownRoot: dir},
		logger: log.New(io.Discard, "", 0),
	}

	cfg := &rigGateConfig{
		SetupCommand: "touch " + marker,
		TestCommand:  "true",
	}

	if err := d.runPreBuildInstall(context.Background(), "testrig", dir, cfg); err != nil {
		t.Fatalf("runPreBuildInstall: unexpected error: %v", err)
	}

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("expected setup_command to run (marker %s missing): %v", marker, err)
	}
}

func TestLoadRigGateConfig_SetupCommand(t *testing.T) {
	t.Run("setup_command parsed from merge_queue", func(t *testing.T) {
		dir := t.TempDir()
		data := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"setup_command": "pnpm install --frozen-lockfile",
				"test_command":  "pnpm test",
			},
		}
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if cfg.SetupCommand != "pnpm install --frozen-lockfile" {
			t.Errorf("expected 'pnpm install --frozen-lockfile', got %q", cfg.SetupCommand)
		}
	})

	t.Run("setup_command omitted leaves SetupCommand empty", func(t *testing.T) {
		dir := t.TempDir()
		data := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"test_command": "go test ./...",
			},
		}
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if cfg.SetupCommand != "" {
			t.Errorf("expected empty SetupCommand, got %q", cfg.SetupCommand)
		}
	})

	t.Run("setup_command alone is NOT enough — still needs gates or test_command", func(t *testing.T) {
		// setup_command without any gates or test_command is not runnable;
		// there's nothing to run after install. Config is treated as "no
		// commands" (nil return) to match existing behavior.
		dir := t.TempDir()
		data := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"setup_command": "pnpm install",
			},
		}
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg != nil {
			t.Errorf("expected nil config (no runnable commands), got %+v", cfg)
		}
	})
}

// TestRunGatesOnWorktree_SkipsPostSquashPhase is the core regression test for
// gu-j1f7. Post-squash gates must be skipped by main_branch_test so a rig
// config that mixes pre-merge and post-squash gates doesn't produce spurious
// failures from gates invoked outside their merge-squash context.
//
// The test relies on real shell execution (marking files) rather than mocks
// because runCommandOnWorktree shells out; the side-effects are directly
// observable in the filesystem.
func TestRunGatesOnWorktree_SkipsPostSquashPhase(t *testing.T) {
	workDir := t.TempDir()
	marker := func(name string) string { return filepath.Join(workDir, name) }

	// Capture logs so we can assert the skip message is emitted — operators
	// rely on it to understand why a configured gate didn't execute.
	var logBuf bytes.Buffer
	d := &Daemon{
		ctx:    context.Background(),
		config: &Config{TownRoot: workDir},
		logger: log.New(&logBuf, "", 0),
	}

	gates := map[string]rigGate{
		// Pre-merge (explicit) — should run.
		"lint": {Cmd: "touch " + marker("lint.ran"), Phase: "pre-merge"},
		// Phase omitted — defaults to pre-merge, should run.
		"build": {Cmd: "touch " + marker("build.ran"), Phase: ""},
		// Post-squash — must be skipped.
		"live-integ": {Cmd: "touch " + marker("live-integ.ran"), Phase: "post-squash"},
		// Another post-squash (mirroring the casc_constructs "downstream-check"
		// gate that originally triggered gu-j1f7) — must also be skipped.
		"downstream-check": {Cmd: "touch " + marker("downstream-check.ran"), Phase: "post-squash"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := d.runGatesOnWorktree(ctx, "testrig", workDir, gates, false); err != nil {
		t.Fatalf("runGatesOnWorktree: unexpected error: %v", err)
	}

	mustExist := []string{"lint.ran", "build.ran"}
	mustNotExist := []string{"live-integ.ran", "downstream-check.ran"}

	for _, f := range mustExist {
		if _, err := os.Stat(marker(f)); err != nil {
			t.Errorf("expected pre-merge gate to run (%s): %v", f, err)
		}
	}
	for _, f := range mustNotExist {
		if _, err := os.Stat(marker(f)); !os.IsNotExist(err) {
			t.Errorf("expected post-squash gate %s to be SKIPPED, but the marker file exists (err=%v)", f, err)
		}
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "skipped non-pre-merge gates") {
		t.Errorf("expected skip-log line, got:\n%s", logs)
	}
	// Both post-squash gates should be named in the skip log.
	for _, gate := range []string{"live-integ", "downstream-check"} {
		if !strings.Contains(logs, gate) {
			t.Errorf("expected skip log to mention gate %q, got:\n%s", gate, logs)
		}
	}
}

// TestRunGatesOnWorktree_PropagatesPreMergeFailures guards against an
// over-eager skip rule: pre-merge gate failures MUST still fail the patrol
// (that's the whole point of main_branch_test). Only post-squash gates are
// exempt.
func TestRunGatesOnWorktree_PropagatesPreMergeFailures(t *testing.T) {
	workDir := t.TempDir()

	d := &Daemon{
		ctx:    context.Background(),
		config: &Config{TownRoot: workDir},
		logger: log.New(io.Discard, "", 0),
	}

	gates := map[string]rigGate{
		"failing-gate": {Cmd: "exit 1", Phase: "pre-merge"},
	}

	err := d.runGatesOnWorktree(context.Background(), "testrig", workDir, gates, false)
	if err == nil {
		t.Fatal("expected error from failing pre-merge gate, got nil")
	}
	if !strings.Contains(err.Error(), "failing-gate") {
		t.Errorf("expected error to name failing-gate, got: %v", err)
	}
}

// TestRunGatesOnWorktree_AllPostSquashIsNoOp ensures a rig that configures
// only post-squash gates (and nothing else) passes main_branch_test cleanly
// rather than failing for lack of runnable work. This is the exact scenario
// gu-j1f7 would produce in miniature: casc_constructs has pre-merge "build"
// but a rig could legitimately have only post-squash gates for a period
// during migration.
func TestRunGatesOnWorktree_AllPostSquashIsNoOp(t *testing.T) {
	workDir := t.TempDir()

	var logBuf bytes.Buffer
	d := &Daemon{
		ctx:    context.Background(),
		config: &Config{TownRoot: workDir},
		logger: log.New(&logBuf, "", 0),
	}

	gates := map[string]rigGate{
		"integ": {Cmd: "exit 1", Phase: "post-squash"}, // would fail if run
	}

	if err := d.runGatesOnWorktree(context.Background(), "testrig", workDir, gates, false); err != nil {
		t.Errorf("expected nil (all gates skipped), got: %v", err)
	}
	if !strings.Contains(logBuf.String(), "skipped non-pre-merge gates") {
		t.Errorf("expected skip log, got:\n%s", logBuf.String())
	}
}

// TestRunGatesOnWorktree_DeterministicOrder is the regression test for gu-i0mb.
// Go map iteration is randomised per-process, so a rig whose gates have
// implicit ordering dependencies (classic case: an "install" gate that
// populates node_modules before a "test" gate consumes them) saw ~50%
// false-failures depending on which map key was visited first. The runner
// must iterate gates in a stable (alphabetical) order so identical inputs
// produce identical execution sequences.
//
// The test is deliberately dependency-shaped: the "test" gate FAILS unless
// "install" ran before it (creating a sentinel file). With random iteration
// this would flap ~50% of runs; with sorted iteration it passes every time.
// Running the assertion in a loop guards against the case where Go's
// randomisation happens to coincide with alphabetical order in a single run.
func TestRunGatesOnWorktree_DeterministicOrder(t *testing.T) {
	for i := 0; i < 20; i++ {
		workDir := t.TempDir()
		sentinel := filepath.Join(workDir, "installed")
		orderLog := filepath.Join(workDir, "order.log")

		d := &Daemon{
			ctx:    context.Background(),
			config: &Config{TownRoot: workDir},
			logger: log.New(io.Discard, "", 0),
		}

		// Three gates whose correct execution order (install -> lint -> test)
		// is also the alphabetical order. "test" fails if "install" hasn't
		// created the sentinel. "lint" is a neutral middle gate; its presence
		// in the order log lets us verify the full sequence, not just the
		// install<test relationship.
		gates := map[string]rigGate{
			"install": {
				Cmd:   fmt.Sprintf("echo install >> %s && touch %s", orderLog, sentinel),
				Phase: "pre-merge",
			},
			"lint": {
				Cmd:   fmt.Sprintf("echo lint >> %s", orderLog),
				Phase: "pre-merge",
			},
			"test": {
				// Fails if sentinel is missing — mirrors the real-world
				// pattern where `bun run test` needs node_modules.
				Cmd:   fmt.Sprintf("echo test >> %s && test -f %s", orderLog, sentinel),
				Phase: "pre-merge",
			},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := d.runGatesOnWorktree(ctx, "testrig", workDir, gates, false); err != nil {
			cancel()
			t.Fatalf("iter %d: runGatesOnWorktree: %v (execution order was non-deterministic)", i, err)
		}
		cancel()

		raw, err := os.ReadFile(orderLog)
		if err != nil {
			t.Fatalf("iter %d: reading order log: %v", i, err)
		}
		got := strings.TrimSpace(string(raw))
		want := "install\nlint\ntest"
		if got != want {
			t.Fatalf("iter %d: execution order not deterministic\nwant:\n%s\ngot:\n%s", i, want, got)
		}
	}
}

// TestRunGatesOnWorktree_SkippedGatesLoggedInOrder verifies that the
// skipped-gates log line is also emitted in a deterministic (alphabetical)
// order. Operators grep these logs to reason about why gates didn't run;
// randomised ordering made those greps flap and made log-diff tooling
// (e.g. comparing cycles to find real changes) unreliable.
func TestRunGatesOnWorktree_SkippedGatesLoggedInOrder(t *testing.T) {
	workDir := t.TempDir()

	var logBuf bytes.Buffer
	d := &Daemon{
		ctx:    context.Background(),
		config: &Config{TownRoot: workDir},
		logger: log.New(&logBuf, "", 0),
	}

	// All post-squash (skipped). Names intentionally chosen so alphabetical
	// order (apple, banana, cherry) is distinguishable from declaration
	// order if the underlying iteration happens to preserve insertion.
	gates := map[string]rigGate{
		"cherry": {Cmd: "true", Phase: "post-squash"},
		"apple":  {Cmd: "true", Phase: "post-squash"},
		"banana": {Cmd: "true", Phase: "post-squash"},
	}

	if err := d.runGatesOnWorktree(context.Background(), "testrig", workDir, gates, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logs := logBuf.String()
	want := "apple(post-squash), banana(post-squash), cherry(post-squash)"
	if !strings.Contains(logs, want) {
		t.Errorf("expected skip log in alphabetical order %q, got:\n%s", want, logs)
	}
}

// TestFormatFailureOutput_PreservesFailSignalsOnLargeGoTestOutput is the core
// regression test for gu-m5w9: when `go test ./...` on a big module emits
// dozens of "ok <pkg>" lines and the actually-failing test's "--- FAIL:"
// marker lands ABOVE the 50-line tail window, the old truncation dropped it
// and left operators with only "ok ... ok ... FAIL". The fixed formatter
// must rescue such signal lines.
func TestFormatFailureOutput_PreservesFailSignalsOnLargeGoTestOutput(t *testing.T) {
	// Synthesize a realistic go-test-on-big-module output:
	//   - 60 passing packages  (pushes the --- FAIL: line toward the tail edge)
	//   - one top-level --- FAIL: marker with an indented subtest detail
	//   - 60 more passing packages after the failing one (guarantees the
	//     FAIL: marker sits above the 50-line tail window)
	//   - a package-level FAIL summary and the bare "FAIL" line at the end
	//     (these already fit in the tail, but we assert they survive too)
	var b strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "ok  \tgithub.com/example/pkg%02d\t0.005s\n", i)
	}
	b.WriteString("--- FAIL: TestTheRealCulprit (0.01s)\n")
	b.WriteString("    my_test.go:42: expected 1, got 2\n")
	for i := 60; i < 120; i++ {
		fmt.Fprintf(&b, "ok  \tgithub.com/example/pkg%03d\t0.005s\n", i)
	}
	b.WriteString("FAIL\tgithub.com/example/pkgculprit\t0.012s\n")
	b.WriteString("FAIL\n")

	got := formatFailureOutput(b.String(), 50)

	// The failing-test identity must survive the tail chop.
	if !strings.Contains(got, "--- FAIL: TestTheRealCulprit") {
		t.Errorf("expected --- FAIL: line to be rescued above the tail, got:\n%s", got)
	}
	// Package-level FAIL line — in the tail naturally, still required.
	if !strings.Contains(got, "FAIL\tgithub.com/example/pkgculprit") {
		t.Errorf("expected package-level FAIL line in output, got:\n%s", got)
	}
	// The tail window itself must still reach the operator.
	if !strings.Contains(got, "github.com/example/pkg119") {
		t.Errorf("expected last tail line present, got:\n%s", got)
	}
	// And a clear marker so readers can tell rescued signals from tail content.
	if !strings.Contains(got, "truncated") {
		t.Errorf("expected truncation marker when prefix is dropped, got:\n%s", got)
	}
}

// TestFormatFailureOutput_ShortOutputPassesThrough: when output fits in the
// tail window, there's no truncation and no injected separators — the
// operator sees the raw command output unchanged.
func TestFormatFailureOutput_ShortOutputPassesThrough(t *testing.T) {
	raw := "line1\nline2\nline3"
	got := formatFailureOutput(raw, 50)
	if got != raw {
		t.Errorf("expected raw passthrough %q, got %q", raw, got)
	}
	if strings.Contains(got, "truncated") {
		t.Errorf("no truncation expected for short input; got:\n%s", got)
	}
}

// TestFormatFailureOutput_Empty: trivial inputs should not introduce noise.
func TestFormatFailureOutput_Empty(t *testing.T) {
	if got := formatFailureOutput("", 50); got != "" {
		t.Errorf("empty input: expected empty output, got %q", got)
	}
	if got := formatFailureOutput("   \n\n", 50); got != "" {
		t.Errorf("whitespace input: expected empty output after trim, got %q", got)
	}
}

// TestFormatFailureOutput_NoSignalsOmitsTruncationMarker: when no rescue is
// needed (no signal lines in the chopped prefix), the formatter returns just
// the tail without the "... truncated ..." marker. This keeps the output
// byte-compatible with the pre-gu-m5w9 behavior for non-go-test commands,
// which don't emit FAIL-style signal lines.
func TestFormatFailureOutput_NoSignalsOmitsTruncationMarker(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&b, "info line %d\n", i)
	}
	got := formatFailureOutput(b.String(), 50)

	if strings.Contains(got, "truncated") {
		t.Errorf("no signal lines above tail — truncation header should be omitted; got:\n%s", got)
	}
	if !strings.Contains(got, "info line 79") {
		t.Errorf("expected last line in tail, got:\n%s", got)
	}
	if strings.Contains(got, "info line 0\n") {
		t.Errorf("first line should have been truncated, got:\n%s", got)
	}
}

// TestFormatFailureOutput_CapsSignalLines: a pathological failure with
// thousands of FAIL signal lines must not blow up the escalation body.
func TestFormatFailureOutput_CapsSignalLines(t *testing.T) {
	var b strings.Builder
	// 200 failing packages above the tail window.
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "FAIL\tgithub.com/example/pkg%03d\t0.001s\n", i)
	}
	// Push them all above the tail with filler.
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "ok  \tgithub.com/example/okpkg%02d\t0.001s\n", i)
	}
	got := formatFailureOutput(b.String(), 50)

	if !strings.Contains(got, "additional FAIL signal line(s) omitted") {
		t.Errorf("expected omitted-signal marker for 200 FAIL lines, got:\n%s", got)
	}
	// The signal cap is 50; the 51st pkg (pkg050) must be inside the omitted
	// bucket, not printed verbatim.
	// Signals are emitted in encounter order (pkg000..pkg049), so pkg050 +
	// must be absent from the signal block. They also cannot appear in the
	// tail (which is all okpkg lines).
	if strings.Contains(got, "pkg050\t") {
		t.Errorf("expected signal cap to drop pkg050+, but found it; got:\n%s", got)
	}
	// First ~50 packages should still be present.
	if !strings.Contains(got, "pkg000\t") {
		t.Errorf("expected pkg000 (first signal) preserved, got:\n%s", got)
	}
	if !strings.Contains(got, "pkg049\t") {
		t.Errorf("expected pkg049 (last in cap) preserved, got:\n%s", got)
	}
}

func TestIsGoTestFailSignal(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		// Matches
		{"--- FAIL: TestFoo (0.00s)", true},
		{"--- FAIL: TestFoo/sub (0.00s)", true},
		{"FAIL", true},
		{"FAIL\tgithub.com/x/y\t0.005s", true},
		{"FAIL github.com/x/y 0.005s", true},

		// Non-matches (must NOT pull these into the rescue set)
		{"    --- FAIL: TestFoo/sub (0.00s)", false}, // indented subtest marker
		{"ok  \tgithub.com/x/y\t0.005s", false},
		{"FAILED", false}, // FAILED != FAIL
		{"FAILURE: stuff", false},
		{"FAIL:something", false}, // colon, no whitespace after FAIL
		{"", false},
		{"some FAIL in the middle", false},
	}
	for _, tc := range cases {
		if got := isGoTestFailSignal(tc.line); got != tc.want {
			t.Errorf("isGoTestFailSignal(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

// TestIsCleanupOnlyTimeout verifies the false-positive suppression logic for
// deadline-exceeded kills that occur during post-step cleanup. The function
// must only suppress errors when a success marker is present AND no failure
// marker follows it — preventing real failures from being silently dropped.
func TestIsCleanupOnlyTimeout(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name: "act success then cleanup — suppress",
			output: `[Build] Running...
[Build] ✅  Success - Build
[Build] Done in 133s
Cleaning up container for job Build
`,
			want: true,
		},
		{
			name: "act Done-in line alone — suppress",
			output: `[Lint] Running...
[Lint] ✅  Success - Lint
[Lint]
Done in 42s
Cleaning up container for job Lint
`,
			want: true,
		},
		{
			name: "pre-commit all passed then cleanup — suppress",
			output: `check json...............................................Passed
fix end of files.........................................Passed
lint.....................................................Passed
Cleaning up container
`,
			want: true,
		},
		{
			name: "no success marker — real timeout, don't suppress",
			output: `[Build] Running step 1
[Build] Running step 2
`,
			want: false,
		},
		{
			name:   "empty output — don't suppress",
			output: "",
			want:   false,
		},
		{
			name: "act failure after success — real failure, don't suppress",
			output: `[Build] ✅  Success - Build
[Test] ❌  Failure - Test
Cleaning up container for job Test
`,
			want: false,
		},
		{
			name: "pre-commit hook failed after passed hooks — real failure, don't suppress",
			output: `check json...............................................Passed
lint.....................................................Failed
`,
			want: false,
		},
		{
			name: "FAILED marker after pre-commit passed — real failure, don't suppress",
			output: `lint.....................................................Passed
Run FAILED
`,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isCleanupOnlyTimeout([]byte(tc.output))
			if got != tc.want {
				t.Errorf("isCleanupOnlyTimeout: got %v, want %v\noutput: %q", got, tc.want, tc.output)
			}
		})
	}
}

func TestIsActContainerName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"act job container", "act-Acceptance-Check-Unit-Tests-d1fb26a0", true},
		{"act with leading slash", "/act-CDK--Lint-and-Compile-dda36f3f", true},
		{"bare act- prefix", "act-x", true},
		{"react substring not matched", "react-app", false},
		{"compact substring not matched", "compact-db", false},
		{"unrelated container", "onyx-api_server-1", false},
		{"testcontainers dolt", "reverent_mahavira", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isActContainerName(tc.in); got != tc.want {
				t.Errorf("isActContainerName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestShouldReapLeakedContainer(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		started time.Time
		want    bool
	}{
		{"just started", now.Add(-1 * time.Minute), false},
		{"within rig timeout", now.Add(-10 * time.Minute), false},
		{"just under threshold", now.Add(-(leakedActContainerAge - time.Minute)), false},
		{"just over threshold", now.Add(-(leakedActContainerAge + time.Minute)), true},
		{"leaked for hours", now.Add(-5 * time.Hour), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldReapLeakedContainer(tc.started, now); got != tc.want {
				t.Errorf("shouldReapLeakedContainer(started=%v) = %v, want %v", tc.started, got, tc.want)
			}
		})
	}
}

// TestRunCommandOnWorktree_CleanupTimeoutSuppressed is the integration test
// for gs-llj: a gate command that succeeds but whose cleanup runs past the
// deadline must NOT be reported as a failure. We simulate this by:
//  1. Running a script that prints the act-style success marker then sleeps
//     (simulating cleanup) past a very short deadline.
//  2. Asserting runCommandOnWorktree returns nil, not an error.
func TestRunCommandOnWorktree_CleanupTimeoutSuppressed(t *testing.T) {
	workDir := t.TempDir()
	var logBuf bytes.Buffer
	d := &Daemon{
		ctx:    context.Background(),
		config: &Config{TownRoot: workDir},
		logger: log.New(&logBuf, "", 0),
	}

	// Very short deadline so the sleep (cleanup simulation) hits it.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Script prints act-style success marker then sleeps (simulates cleanup).
	// Sleep is longer than the deadline so the kill fires during "cleanup".
	script := `printf '✅  Success - Build\nDone in 1s\nCleaning up container\n'; sleep 2`
	err := d.runCommandOnWorktree(ctx, "testrig", workDir, "build", script)
	if err != nil {
		t.Errorf("expected nil (cleanup-only timeout suppressed), got: %v", err)
	}
	if !strings.Contains(logBuf.String(), "post-step cleanup") {
		t.Errorf("expected 'post-step cleanup' in log, got:\n%s", logBuf.String())
	}
}

// TestRunCommandOnWorktree_RealFailureNotSuppressed guards against the inverse:
// a gate that actually fails must still propagate the error even when the
// context times out around the same time. Without a success marker in output,
// isCleanupOnlyTimeout returns false and the error surfaces normally.
func TestRunCommandOnWorktree_RealFailureNotSuppressed(t *testing.T) {
	workDir := t.TempDir()
	d := &Daemon{
		ctx:    context.Background(),
		config: &Config{TownRoot: workDir},
		logger: log.New(io.Discard, "", 0),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Script prints nothing recognisable then sleeps — no success markers.
	script := `echo 'build step running'; sleep 2`
	err := d.runCommandOnWorktree(ctx, "testrig", workDir, "build", script)
	if err == nil {
		t.Error("expected error (no success marker + timeout = real failure), got nil")
	}
}

// TestLoadRigGateConfig_PerGateTimeout regresses the silent-drop in gu-z76g:
// per-gate `timeout` was parsed but discarded by an anonymous struct that
// only kept {cmd, phase}. talontriage rigs declared `build=5m` + `test=10m`
// expecting per-gate budgets and instead got a single 10m rig-level ceiling
// shared across both, which SIGTERM'd pytest under host load.
func TestLoadRigGateConfig_PerGateTimeout(t *testing.T) {
	t.Run("valid timeout durations are parsed", func(t *testing.T) {
		dir := t.TempDir()
		data := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"gates": map[string]interface{}{
					"build": map[string]interface{}{"cmd": "go build ./...", "timeout": "5m"},
					"test":  map[string]interface{}{"cmd": "go test ./...", "timeout": "10m"},
					"vet":   map[string]interface{}{"cmd": "go vet ./..."}, // no timeout — inherits parent
				},
			},
		}
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if got := cfg.Gates["build"].Timeout; got != 5*time.Minute {
			t.Errorf("build timeout: got %v, want 5m", got)
		}
		if got := cfg.Gates["test"].Timeout; got != 10*time.Minute {
			t.Errorf("test timeout: got %v, want 10m", got)
		}
		if got := cfg.Gates["vet"].Timeout; got != 0 {
			t.Errorf("vet timeout: got %v, want 0 (unset → inherit parent)", got)
		}
	})

	t.Run("malformed timeout falls back to inherit (Timeout=0)", func(t *testing.T) {
		// Operators occasionally typo "5min" or "5 minutes". A bad value
		// shouldn't fail config load — the gate just inherits the parent
		// deadline. The runner still works; the operator sees a clean run.
		dir := t.TempDir()
		data := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"gates": map[string]interface{}{
					"build": map[string]interface{}{"cmd": "go build", "timeout": "5min"}, // bad
				},
			},
		}
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if got := cfg.Gates["build"].Timeout; got != 0 {
			t.Errorf("build timeout: got %v, want 0 (malformed → fallback)", got)
		}
		if cfg.Gates["build"].Cmd == "" {
			t.Errorf("expected gate to still be loaded with cmd preserved")
		}
	})
}

// TestLoadRigGateConfig_GatesParallel regresses the silent-drop in gu-z76g:
// `merge_queue.gates_parallel=true` was accepted by `gt rig settings set`
// but `runGatesOnWorktree` was a sequential for-loop, so the flag had no
// effect on main_branch_test.
func TestLoadRigGateConfig_GatesParallel(t *testing.T) {
	cases := []struct {
		name     string
		mqExtras map[string]interface{}
		want     bool
	}{
		{"true", map[string]interface{}{"gates_parallel": true}, true},
		{"false", map[string]interface{}{"gates_parallel": false}, false},
		{"absent_defaults_false", map[string]interface{}{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			mq := map[string]interface{}{
				"gates": map[string]interface{}{
					"build": map[string]interface{}{"cmd": "true"},
				},
			}
			for k, v := range tc.mqExtras {
				mq[k] = v
			}
			data := map[string]interface{}{"merge_queue": mq}
			raw, _ := json.Marshal(data)
			if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0644); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadRigGateConfig(dir)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg == nil {
				t.Fatal("expected non-nil config")
			}
			if cfg.GatesParallel != tc.want {
				t.Errorf("GatesParallel: got %v, want %v", cfg.GatesParallel, tc.want)
			}
		})
	}
}

// TestComputeEffectiveTimeout verifies that the parent context budget grows
// to fit declared per-gate budgets so a rig that asks for build=5m + test=10m
// doesn't get clamped to a 10m rig-level ceiling.
func TestComputeEffectiveTimeout(t *testing.T) {
	base := 10 * time.Minute

	t.Run("nil config returns base", func(t *testing.T) {
		if got := computeEffectiveTimeout(base, nil); got != base {
			t.Errorf("got %v, want %v", got, base)
		}
	})

	t.Run("no gates returns base", func(t *testing.T) {
		cfg := &rigGateConfig{}
		if got := computeEffectiveTimeout(base, cfg); got != base {
			t.Errorf("got %v, want %v", got, base)
		}
	})

	t.Run("gates with no declared timeouts return base", func(t *testing.T) {
		cfg := &rigGateConfig{Gates: map[string]rigGate{
			"build": {Cmd: "true"},
			"test":  {Cmd: "true"},
		}}
		if got := computeEffectiveTimeout(base, cfg); got != base {
			t.Errorf("got %v, want %v", got, base)
		}
	})

	t.Run("sequential: parent = sum of per-gate when sum > base", func(t *testing.T) {
		cfg := &rigGateConfig{Gates: map[string]rigGate{
			"build": {Cmd: "true", Timeout: 5 * time.Minute},
			"test":  {Cmd: "true", Timeout: 10 * time.Minute},
		}}
		// 5m + 10m = 15m > base(10m) → 15m
		want := 15 * time.Minute
		if got := computeEffectiveTimeout(base, cfg); got != want {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("sequential: parent stays at base when sum < base", func(t *testing.T) {
		cfg := &rigGateConfig{Gates: map[string]rigGate{
			"build": {Cmd: "true", Timeout: 30 * time.Second},
		}}
		if got := computeEffectiveTimeout(base, cfg); got != base {
			t.Errorf("got %v, want %v (base safety net)", got, base)
		}
	})

	t.Run("parallel: parent = max of per-gate when max > base", func(t *testing.T) {
		cfg := &rigGateConfig{
			Gates: map[string]rigGate{
				"build": {Cmd: "true", Timeout: 5 * time.Minute},
				"test":  {Cmd: "true", Timeout: 15 * time.Minute},
			},
			GatesParallel: true,
		}
		want := 15 * time.Minute
		if got := computeEffectiveTimeout(base, cfg); got != want {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("parallel: parent stays at base when max < base", func(t *testing.T) {
		cfg := &rigGateConfig{
			Gates: map[string]rigGate{
				"build": {Cmd: "true", Timeout: 2 * time.Minute},
				"test":  {Cmd: "true", Timeout: 3 * time.Minute},
			},
			GatesParallel: true,
		}
		if got := computeEffectiveTimeout(base, cfg); got != base {
			t.Errorf("got %v, want %v (base safety net)", got, base)
		}
	})

	t.Run("gates without timeouts are ignored in sum/max", func(t *testing.T) {
		// "vet" has no timeout — it inherits the parent ctx and shouldn't
		// inflate the parent budget. Sum is 5m+10m=15m, vet contributes 0.
		cfg := &rigGateConfig{Gates: map[string]rigGate{
			"build": {Cmd: "true", Timeout: 5 * time.Minute},
			"test":  {Cmd: "true", Timeout: 10 * time.Minute},
			"vet":   {Cmd: "true"},
		}}
		want := 15 * time.Minute
		if got := computeEffectiveTimeout(base, cfg); got != want {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

// TestRunGatesOnWorktree_PerGateTimeout verifies that a gate with its own
// declared timeout is killed at that boundary even when the parent context
// is much wider. This is the core fix for gu-z76g: a rig that declared
// per-gate budgets used to silently get a single 10m rig-level ceiling.
func TestRunGatesOnWorktree_PerGateTimeout(t *testing.T) {
	workDir := t.TempDir()
	d := &Daemon{
		ctx:    context.Background(),
		config: &Config{TownRoot: workDir},
		logger: log.New(io.Discard, "", 0),
	}

	gates := map[string]rigGate{
		// Gate timeout (100ms) is much shorter than the parent ctx (10s),
		// and the command would happily run for 5s if uninterrupted. The
		// per-gate timeout MUST kick in.
		"slow": {Cmd: "sleep 5", Phase: "pre-merge", Timeout: 100 * time.Millisecond},
	}

	parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	err := d.runGatesOnWorktree(parent, "testrig", workDir, gates, false)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected per-gate timeout to fail the run, got nil")
	}
	if elapsed > 5*time.Second {
		t.Errorf("per-gate timeout (100ms) ignored: ran %v before failing", elapsed)
	}
	if !strings.Contains(err.Error(), "slow") {
		t.Errorf("expected error to name failing gate, got: %v", err)
	}
}

// TestRunGatesOnWorktree_PerGateTimeoutDoesNotCancelParent verifies that one
// gate's per-gate timeout does NOT cancel a sibling gate's context. This
// matters for parallel mode: gateA's WithTimeout(parent, 100ms) firing must
// only kill gateA, not gateB.
func TestRunGatesOnWorktree_PerGateTimeoutDoesNotCancelParent(t *testing.T) {
	workDir := t.TempDir()
	d := &Daemon{
		ctx:    context.Background(),
		config: &Config{TownRoot: workDir},
		logger: log.New(io.Discard, "", 0),
	}

	marker := filepath.Join(workDir, "ok.ran")
	gates := map[string]rigGate{
		// "fast-fail" has a very short per-gate timeout and will fail.
		"fast-fail": {Cmd: "sleep 5", Phase: "pre-merge", Timeout: 100 * time.Millisecond},
		// "ok" runs after fast-fail (sequential, alphabetical order) and
		// must complete normally — its parent context is still alive.
		"ok": {Cmd: "touch " + marker, Phase: "pre-merge"},
	}

	parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := d.runGatesOnWorktree(parent, "testrig", workDir, gates, false); err == nil {
		t.Fatal("expected fast-fail to surface error")
	}

	// Even though fast-fail timed out, the ok gate should have run.
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("expected sibling gate to run after per-gate timeout: %v", err)
	}
}

// TestRunGatesOnWorktree_Parallel verifies the gates_parallel=true path:
// two gates that each sleep 300ms must complete in well under 600ms when
// run concurrently. The sequential path takes ~600ms; this proves we
// actually fanned out.
func TestRunGatesOnWorktree_Parallel(t *testing.T) {
	workDir := t.TempDir()
	d := &Daemon{
		ctx:    context.Background(),
		config: &Config{TownRoot: workDir},
		logger: log.New(io.Discard, "", 0),
	}

	const sleep = 300 * time.Millisecond
	gates := map[string]rigGate{
		"a": {Cmd: fmt.Sprintf("sleep %f", sleep.Seconds()), Phase: "pre-merge"},
		"b": {Cmd: fmt.Sprintf("sleep %f", sleep.Seconds()), Phase: "pre-merge"},
		"c": {Cmd: fmt.Sprintf("sleep %f", sleep.Seconds()), Phase: "pre-merge"},
	}

	parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	if err := d.runGatesOnWorktree(parent, "testrig", workDir, gates, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	elapsed := time.Since(start)

	// Sequential lower bound is 3*sleep = 900ms. Parallel should be ~sleep
	// plus shell-fork overhead. Allow generous slack for CI host load.
	limit := 2 * sleep
	if elapsed >= limit {
		t.Errorf("parallel gates took %v (>= %v) — likely sequential", elapsed, limit)
	}
}

// TestRunGatesOnWorktree_ParallelPropagatesAllFailures verifies that with
// gates_parallel=true, a failure in one gate doesn't suppress the report of
// another. This matches refinery's contract: any single gate failure = run
// failure, but all failures are reported so operators see the full picture.
func TestRunGatesOnWorktree_ParallelPropagatesAllFailures(t *testing.T) {
	workDir := t.TempDir()
	d := &Daemon{
		ctx:    context.Background(),
		config: &Config{TownRoot: workDir},
		logger: log.New(io.Discard, "", 0),
	}

	gates := map[string]rigGate{
		"alpha": {Cmd: "exit 1", Phase: "pre-merge"},
		"beta":  {Cmd: "exit 2", Phase: "pre-merge"},
		"gamma": {Cmd: "true", Phase: "pre-merge"}, // passes
	}

	err := d.runGatesOnWorktree(context.Background(), "testrig", workDir, gates, true)
	if err == nil {
		t.Fatal("expected error from parallel failures, got nil")
	}
	for _, name := range []string{"alpha", "beta"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("expected error to mention failing gate %q, got: %v", name, err)
		}
	}
	if strings.Contains(err.Error(), "gamma") {
		t.Errorf("did not expect passing gate 'gamma' in error, got: %v", err)
	}
}

// TestCleanupStaleWorktree_NoOpWhenMissing verifies the helper is idempotent
// when there's nothing to clean up — the common case on a fresh runner.
func TestCleanupStaleWorktree_NoOpWhenMissing(t *testing.T) {
	rigDir := t.TempDir()
	worktreePath := filepath.Join(rigDir, ".main-test-worktree")
	bareRepoPath := filepath.Join(rigDir, ".repo.git")

	if err := cleanupStaleWorktree(bareRepoPath, worktreePath); err != nil {
		t.Fatalf("expected nil error when path missing, got %v", err)
	}
}

// TestCleanupStaleWorktree_RemovesOrphanDirectory is the regression test for
// gu-dob2f. A previous run was killed mid-build, leaving build artifacts at
// .main-test-worktree with no .git link and no registration in the bare repo.
// `git worktree remove` is a no-op against such a directory, so without
// unconditional RemoveAll the path persists and the next `git worktree add`
// fails with "already exists".
func TestCleanupStaleWorktree_RemovesOrphanDirectory(t *testing.T) {
	rigDir := t.TempDir()
	worktreePath := filepath.Join(rigDir, ".main-test-worktree")
	bareRepoPath := filepath.Join(rigDir, ".repo.git")

	// Simulate the orphan-build-artifacts case: directory with files but no
	// .git link, and no registration in any bare repo. (We don't even need
	// bareRepoPath to exist — `git worktree remove` will fail; the helper
	// must still clean up.)
	if err := os.MkdirAll(filepath.Join(worktreePath, "target", "debug"), 0o755); err != nil {
		t.Fatalf("setup MkdirAll: %v", err)
	}
	leftover := filepath.Join(worktreePath, "target", "debug", "leftover.bin")
	if err := os.WriteFile(leftover, []byte("orphan build artifact"), 0o644); err != nil {
		t.Fatalf("setup WriteFile: %v", err)
	}

	if err := cleanupStaleWorktree(bareRepoPath, worktreePath); err != nil {
		t.Fatalf("cleanupStaleWorktree returned error: %v", err)
	}

	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree path %s to be removed, stat err=%v", worktreePath, err)
	}
}

// TestCleanupStaleWorktree_RemovesRegisteredWorktree exercises the
// happy-path case: a properly registered git worktree from a clean prior
// run is removed. This guards against regressing the pre-existing behavior
// while adding the orphan-directory handling.
func TestCleanupStaleWorktree_RemovesRegisteredWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available in test environment")
	}
	rigDir := t.TempDir()
	bareRepoPath := filepath.Join(rigDir, ".repo.git")
	worktreePath := filepath.Join(rigDir, ".main-test-worktree")

	seed := filepath.Join(rigDir, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatalf("MkdirAll seed: %v", err)
	}
	mustRun := func(dir, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v (%s)", name, args, err, string(out))
		}
	}
	mustRun(seed, "git", "init", "-q", "-b", "main")
	mustRun(seed, "git", "config", "user.email", "test@example.com")
	mustRun(seed, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(seed, "f"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	mustRun(seed, "git", "add", "f")
	mustRun(seed, "git", "commit", "-q", "-m", "seed")
	if out, err := exec.Command("git", "clone", "-q", "--bare", seed, bareRepoPath).CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare: %v (%s)", err, string(out))
	}

	addCmd := exec.Command("git", "worktree", "add", "--detach", worktreePath, "main")
	addCmd.Dir = bareRepoPath
	if out, err := addCmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v (%s)", err, string(out))
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("worktree did not get created: %v", err)
	}

	if err := cleanupStaleWorktree(bareRepoPath, worktreePath); err != nil {
		t.Fatalf("cleanupStaleWorktree: %v", err)
	}

	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree path %s to be removed, stat err=%v", worktreePath, err)
	}

	listCmd := exec.Command("git", "worktree", "list", "--porcelain")
	listCmd.Dir = bareRepoPath
	if out, err := listCmd.CombinedOutput(); err == nil {
		if strings.Contains(string(out), worktreePath) {
			t.Errorf("worktree still registered after cleanup:\n%s", string(out))
		}
	}
}

// TestAcquireGlobalGateLock_Serializes verifies that the town-global gate lock
// (gs-b1l) is mutually exclusive: while one holder owns it, a second acquire
// blocks, and only succeeds after the first releases. This is the invariant
// that prevents two act/Docker CI suites from running concurrently.
func TestAcquireGlobalGateLock_Serializes(t *testing.T) {
	townRoot := t.TempDir()
	ctx := context.Background()

	release1, err := acquireGlobalGateLock(ctx, townRoot)
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}

	// A second acquire must NOT succeed while the first is held. Run it in a
	// goroutine and assert it is still blocked after a short wait.
	acquired := make(chan func(), 1)
	go func() {
		release2, err := acquireGlobalGateLock(ctx, townRoot)
		if err != nil {
			t.Errorf("second acquire errored: %v", err)
			return
		}
		acquired <- release2
	}()

	select {
	case <-acquired:
		t.Fatal("second acquire succeeded while first lock was held — lock is not exclusive")
	case <-time.After(2 * gateLockPollInterval):
		// Still blocked, as expected.
	}

	// Release the first lock; the waiter should now acquire it.
	release1()

	select {
	case release2 := <-acquired:
		release2()
	case <-time.After(5 * gateLockPollInterval):
		t.Fatal("second acquire did not proceed after first lock was released")
	}
}

// TestAcquireGlobalGateLock_ContextCancel verifies that a waiter blocked on the
// gate lock bails out when its context is canceled (daemon shutdown) rather
// than pinning the patrol loop forever.
func TestAcquireGlobalGateLock_ContextCancel(t *testing.T) {
	townRoot := t.TempDir()

	release1, err := acquireGlobalGateLock(context.Background(), townRoot)
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	defer release1()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := acquireGlobalGateLock(ctx, townRoot)
		errCh <- err
	}()

	// Give the waiter a moment to enter the poll loop, then cancel.
	time.Sleep(gateLockPollInterval / 2)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected context-cancel error, got nil (lock acquired despite held)")
		}
	case <-time.After(5 * gateLockPollInterval):
		t.Fatal("waiter did not return after context cancel")
	}
}

// TestResolveMainBranchTestBranch verifies the patrol resolves the branch it
// tests against from the rig config first, falls back to the bare repo's actual
// default branch, and only then to "main" — so a "mainline"-default rig is not
// fetched/worktreed against a nonexistent "main" (gu-ez4as).
func TestResolveMainBranchTestBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available in test environment")
	}

	mustRun := func(dir, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v (%s)", name, args, err, string(out))
		}
	}

	// seedBareRepo creates a bare repo whose HEAD points at branchName.
	seedBareRepo := func(t *testing.T, rigPath, branchName string) string {
		t.Helper()
		seed := filepath.Join(rigPath, "seed")
		if err := os.MkdirAll(seed, 0o755); err != nil {
			t.Fatalf("MkdirAll seed: %v", err)
		}
		mustRun(seed, "git", "init", "-q", "-b", branchName)
		mustRun(seed, "git", "config", "user.email", "test@example.com")
		mustRun(seed, "git", "config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(seed, "f"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write seed file: %v", err)
		}
		mustRun(seed, "git", "add", "f")
		mustRun(seed, "git", "commit", "-q", "-m", "seed")
		bareRepoPath := filepath.Join(rigPath, ".repo.git")
		if out, err := exec.Command("git", "clone", "-q", "--bare", seed, bareRepoPath).CombinedOutput(); err != nil {
			t.Fatalf("git clone --bare: %v (%s)", err, string(out))
		}
		return bareRepoPath
	}

	writeRigConfig := func(t *testing.T, rigPath, defaultBranch string) {
		t.Helper()
		cfg := map[string]any{"type": "rig", "name": "r"}
		if defaultBranch != "" {
			cfg["default_branch"] = defaultBranch
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshal config: %v", err)
		}
		if err := os.WriteFile(filepath.Join(rigPath, "config.json"), raw, 0o644); err != nil {
			t.Fatalf("write config.json: %v", err)
		}
	}

	t.Run("config default_branch wins", func(t *testing.T) {
		rigPath := t.TempDir()
		bareRepoPath := seedBareRepo(t, rigPath, "main")
		writeRigConfig(t, rigPath, "mainline")
		if got := resolveMainBranchTestBranch(rigPath, bareRepoPath); got != "mainline" {
			t.Errorf("got %q, want %q", got, "mainline")
		}
	})

	t.Run("falls back to bare repo HEAD when config has no default_branch", func(t *testing.T) {
		rigPath := t.TempDir()
		bareRepoPath := seedBareRepo(t, rigPath, "mainline")
		writeRigConfig(t, rigPath, "") // no default_branch
		if got := resolveMainBranchTestBranch(rigPath, bareRepoPath); got != "mainline" {
			t.Errorf("got %q, want %q (bare repo HEAD)", got, "mainline")
		}
	})

	t.Run("falls back to main when config and bare repo both unavailable", func(t *testing.T) {
		rigPath := t.TempDir()
		// No config.json, no bare repo.
		bareRepoPath := filepath.Join(rigPath, ".repo.git")
		if got := resolveMainBranchTestBranch(rigPath, bareRepoPath); got != "main" {
			t.Errorf("got %q, want %q", got, "main")
		}
	})
}
