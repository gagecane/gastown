package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParsePluginMD_HarmonyNpmRefresh verifies the harmony-npm-refresh
// plugin.md file parses correctly. Added for gu-ntnz.
func TestParsePluginMD_HarmonyNpmRefresh(t *testing.T) {
	// Locate the plugin.md relative to the repo root. The test runs in
	// internal/plugin/, so plugins/ is two directories up.
	pluginMD := filepath.Join("..", "..", "plugins", "harmony-npm-refresh", "plugin.md")
	content, err := os.ReadFile(pluginMD)
	if err != nil {
		t.Fatalf("read plugin.md: %v", err)
	}

	p, err := parsePluginMD(content, filepath.Dir(pluginMD), LocationTown, "")
	if err != nil {
		t.Fatalf("parsePluginMD failed: %v", err)
	}

	if p.Name != "harmony-npm-refresh" {
		t.Errorf("name: got %q, want harmony-npm-refresh", p.Name)
	}
	if p.Version != 1 {
		t.Errorf("version: got %d, want 1", p.Version)
	}
	if p.Gate == nil || p.Gate.Type != GateCooldown {
		t.Fatalf("gate: got %+v, want cooldown", p.Gate)
	}
	// 22h token TTL → 18h refresh cadence gives 4h safety margin.
	// Verify the cooldown is strictly less than the 22h expiry.
	if p.Gate.Duration != "18h" {
		t.Errorf("gate.duration: got %q, want 18h (4h safety buffer before 22h token expiry)",
			p.Gate.Duration)
	}
	if p.Tracking == nil {
		t.Fatal("tracking: got nil")
	}
	foundPluginLabel := false
	for _, l := range p.Tracking.Labels {
		if l == "plugin:harmony-npm-refresh" {
			foundPluginLabel = true
		}
	}
	if !foundPluginLabel {
		t.Errorf("tracking.labels: missing 'plugin:harmony-npm-refresh', got %v", p.Tracking.Labels)
	}
	if p.Execution == nil {
		t.Fatal("execution: got nil")
	}
	if p.Execution.Timeout != "2m" {
		t.Errorf("execution.timeout: got %q, want 2m", p.Execution.Timeout)
	}
	if !p.Execution.NotifyOnFailure {
		t.Error("execution.notify_on_failure: want true")
	}
	if p.Execution.Severity != "medium" {
		t.Errorf("execution.severity: got %q, want medium", p.Execution.Severity)
	}
}
