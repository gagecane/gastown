package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
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

	if err := d.runGatesOnWorktree(ctx, "testrig", workDir, gates); err != nil {
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

	err := d.runGatesOnWorktree(context.Background(), "testrig", workDir, gates)
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

	if err := d.runGatesOnWorktree(context.Background(), "testrig", workDir, gates); err != nil {
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
		if err := d.runGatesOnWorktree(ctx, "testrig", workDir, gates); err != nil {
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

	if err := d.runGatesOnWorktree(context.Background(), "testrig", workDir, gates); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logs := logBuf.String()
	want := "apple(post-squash), banana(post-squash), cherry(post-squash)"
	if !strings.Contains(logs, want) {
		t.Errorf("expected skip log in alphabetical order %q, got:\n%s", want, logs)
	}
}
