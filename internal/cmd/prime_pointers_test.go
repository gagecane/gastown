// Package: internal/cmd (gastown)
// File: prime_pointers_test.go
//
// Tests for the config-driven pointer injection feature.
// Apply to: github.com/gagecane/gastown :: internal/cmd/prime_pointers_test.go

package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPointerConfigPaths(t *testing.T) {
	t.Parallel()

	ctx := RoleContext{
		TownRoot: "/home/user/gt",
		Rig:      "myrig",
		WorkDir:  "/home/user/gt/myrig/polecats/foo/myrig",
	}

	paths := pointerConfigPaths(ctx)

	expected := []string{
		"/home/user/gt/myrig/polecats/foo/myrig/configs/gt-prime-pointers.yaml",
		"/home/user/gt/myrig/polecats/foo/myrig/.beads/configs/gt-prime-pointers.yaml",
		"/home/user/gt/myrig/configs/gt-prime-pointers.yaml",
		"/home/user/gt/configs/gt-prime-pointers.yaml",
	}

	if len(paths) != len(expected) {
		t.Fatalf("expected %d paths, got %d: %v", len(expected), len(paths), paths)
	}
	for i, p := range paths {
		if p != expected[i] {
			t.Errorf("path[%d]: got %q, want %q", i, p, expected[i])
		}
	}
}

func TestPointerConfigPathsEmptyRig(t *testing.T) {
	t.Parallel()

	ctx := RoleContext{
		TownRoot: "/home/user/gt",
		WorkDir:  "/home/user/gt/mayor",
	}

	paths := pointerConfigPaths(ctx)

	// Should skip rig-level path when Rig is empty
	expected := []string{
		"/home/user/gt/mayor/configs/gt-prime-pointers.yaml",
		"/home/user/gt/mayor/.beads/configs/gt-prime-pointers.yaml",
		"/home/user/gt/configs/gt-prime-pointers.yaml",
	}

	if len(paths) != len(expected) {
		t.Fatalf("expected %d paths, got %d: %v", len(expected), len(paths), paths)
	}
	for i, p := range paths {
		if p != expected[i] {
			t.Errorf("path[%d]: got %q, want %q", i, p, expected[i])
		}
	}
}

func TestLoadPointersValidYAML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configDir := filepath.Join(dir, "configs")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}

	yamlContent := `pointers:
  - label: "Pipeline guardian"
    command: "bd show hq-casc-guardian-digest"
  - label: "Runbook"
    command: "cat docs/runbook.md"
    roles: ["polecat", "crew"]
`
	if err := os.WriteFile(filepath.Join(configDir, pointerConfigFileName), []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		WorkDir:  dir,
		TownRoot: "/tmp/town",
	}

	cfg := loadPointers(ctx)
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Pointers) != 2 {
		t.Fatalf("expected 2 pointers, got %d", len(cfg.Pointers))
	}
	if cfg.Pointers[0].Label != "Pipeline guardian" {
		t.Errorf("expected label 'Pipeline guardian', got %q", cfg.Pointers[0].Label)
	}
	if cfg.Pointers[0].Command != "bd show hq-casc-guardian-digest" {
		t.Errorf("expected command 'bd show hq-casc-guardian-digest', got %q", cfg.Pointers[0].Command)
	}
	if len(cfg.Pointers[1].Roles) != 2 {
		t.Errorf("expected 2 roles, got %d", len(cfg.Pointers[1].Roles))
	}
}

func TestLoadPointersNoFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ctx := RoleContext{
		WorkDir:  dir,
		TownRoot: dir,
	}

	cfg := loadPointers(ctx)
	if cfg != nil {
		t.Error("expected nil config when no file exists")
	}
}

func TestLoadPointersEmptyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configDir := filepath.Join(dir, "configs")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, pointerConfigFileName), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		WorkDir:  dir,
		TownRoot: dir,
	}

	cfg := loadPointers(ctx)
	if cfg != nil {
		t.Error("expected nil config for empty file")
	}
}

func TestLoadPointersInvalidYAML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configDir := filepath.Join(dir, "configs")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write invalid YAML
	if err := os.WriteFile(filepath.Join(configDir, pointerConfigFileName), []byte("{{invalid"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		WorkDir:  dir,
		TownRoot: dir,
	}

	cfg := loadPointers(ctx)
	if cfg != nil {
		t.Error("expected nil config for invalid YAML")
	}
}

func TestContainsRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		roles    []string
		role     string
		expected bool
	}{
		{[]string{"polecat", "crew"}, "polecat", true},
		{[]string{"polecat", "crew"}, "Polecat", true},
		{[]string{"polecat", "crew"}, "witness", false},
		{[]string{}, "polecat", false},
		{nil, "polecat", false},
	}

	for _, tt := range tests {
		got := containsRole(tt.roles, tt.role)
		if got != tt.expected {
			t.Errorf("containsRole(%v, %q) = %v, want %v", tt.roles, tt.role, got, tt.expected)
		}
	}
}

func TestLoadPointersResolutionOrder(t *testing.T) {
	t.Parallel()

	// Set up a hierarchy: project, rig, town
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	projDir := filepath.Join(rigDir, "polecats", "foo", "myrig")

	// Create configs at all levels
	for _, dir := range []string{
		filepath.Join(projDir, "configs"),
		filepath.Join(rigDir, "configs"),
		filepath.Join(townRoot, "configs"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Write different content at each level
	writePointerYAML := func(dir, label string) {
		content := "pointers:\n  - label: " + label + "\n    command: test\n"
		if err := os.WriteFile(filepath.Join(dir, pointerConfigFileName), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writePointerYAML(filepath.Join(projDir, "configs"), "project-level")
	writePointerYAML(filepath.Join(rigDir, "configs"), "rig-level")
	writePointerYAML(filepath.Join(townRoot, "configs"), "town-level")

	// Project-level should win
	ctx := RoleContext{
		WorkDir:  projDir,
		TownRoot: townRoot,
		Rig:      "myrig",
	}
	cfg := loadPointers(ctx)
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Pointers[0].Label != "project-level" {
		t.Errorf("expected project-level pointer, got %q", cfg.Pointers[0].Label)
	}

	// Remove project-level, rig-level should win
	os.Remove(filepath.Join(projDir, "configs", pointerConfigFileName))
	cfg = loadPointers(ctx)
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Pointers[0].Label != "rig-level" {
		t.Errorf("expected rig-level pointer, got %q", cfg.Pointers[0].Label)
	}

	// Remove rig-level, town-level should win
	os.Remove(filepath.Join(rigDir, "configs", pointerConfigFileName))
	cfg = loadPointers(ctx)
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Pointers[0].Label != "town-level" {
		t.Errorf("expected town-level pointer, got %q", cfg.Pointers[0].Label)
	}
}
