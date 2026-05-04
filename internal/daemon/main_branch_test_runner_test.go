package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		if cfg.Gates["build"] != "go build ./..." {
			t.Errorf("expected build gate 'go build ./...', got %q", cfg.Gates["build"])
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

func TestDetectInstallCommand(t *testing.T) {
	// Each case writes the listed files into a fresh temp dir, then asserts
	// the detected command. The empty-command cases verify we correctly skip
	// install for languages whose toolchains handle deps on demand (Go, Rust).
	cases := []struct {
		name      string
		files     []string
		wantCmd   string
		wantLabel string
	}{
		{
			name:      "bun with bun.lock",
			files:     []string{"package.json", "bun.lock"},
			wantCmd:   "bun install --frozen-lockfile",
			wantLabel: "install (bun)",
		},
		{
			name:      "bun with legacy bun.lockb",
			files:     []string{"package.json", "bun.lockb"},
			wantCmd:   "bun install --frozen-lockfile",
			wantLabel: "install (bun)",
		},
		{
			name:      "pnpm",
			files:     []string{"package.json", "pnpm-lock.yaml"},
			wantCmd:   "pnpm install --frozen-lockfile",
			wantLabel: "install (pnpm)",
		},
		{
			name:      "yarn",
			files:     []string{"package.json", "yarn.lock"},
			wantCmd:   "yarn install --frozen-lockfile",
			wantLabel: "install (yarn)",
		},
		{
			name:      "npm",
			files:     []string{"package.json", "package-lock.json"},
			wantCmd:   "npm ci",
			wantLabel: "install (npm)",
		},
		{
			name:      "uv",
			files:     []string{"pyproject.toml", "uv.lock"},
			wantCmd:   "uv sync",
			wantLabel: "install (uv)",
		},
		{
			name:      "poetry",
			files:     []string{"pyproject.toml", "poetry.lock"},
			wantCmd:   "poetry install",
			wantLabel: "install (poetry)",
		},
		{
			name:      "go rig — no install needed",
			files:     []string{"go.mod"},
			wantCmd:   "",
			wantLabel: "",
		},
		{
			name:      "rust rig — no install needed",
			files:     []string{"Cargo.toml"},
			wantCmd:   "",
			wantLabel: "",
		},
		{
			name:      "empty workdir",
			files:     nil,
			wantCmd:   "",
			wantLabel: "",
		},
		{
			name:      "package.json without lockfile — unknown PM, skip",
			files:     []string{"package.json"},
			wantCmd:   "",
			wantLabel: "",
		},
		{
			name:      "pyproject.toml without lockfile — unknown PM, skip",
			files:     []string{"pyproject.toml"},
			wantCmd:   "",
			wantLabel: "",
		},
		{
			// Deterministic tie-break: bun has highest priority. A leftover
			// package-lock.json from a prior npm era does not demote the repo
			// back to npm once a bun.lock exists.
			name:      "bun.lock + package-lock.json — bun wins",
			files:     []string{"package.json", "bun.lock", "package-lock.json"},
			wantCmd:   "bun install --frozen-lockfile",
			wantLabel: "install (bun)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			gotCmd, gotLabel := detectInstallCommand(dir)
			if gotCmd != tc.wantCmd {
				t.Errorf("cmd: got %q, want %q", gotCmd, tc.wantCmd)
			}
			if gotLabel != tc.wantLabel {
				t.Errorf("label: got %q, want %q", gotLabel, tc.wantLabel)
			}
		})
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
