package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/plugin"
)

// writeRunScript creates a plugin dir containing run.sh with the given body and
// returns the plugin dir and the run.sh path.
func writeRunScript(t *testing.T, body string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\n"+body), 0o755); err != nil {
		t.Fatalf("write run.sh: %v", err)
	}
	return dir, script
}

// TestExecutePluginScript_Success verifies a run.sh exiting 0 returns no error.
func TestExecutePluginScript_Success(t *testing.T) {
	dir, script := writeRunScript(t, "echo ran; exit 0\n")
	p := &plugin.Plugin{Name: "test", Path: dir, HasRunScript: true}

	if err := executePluginScript(p, script, dir); err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
}

// TestExecutePluginScript_Failure verifies a non-zero exit surfaces as an error
// rather than a silent (misleading) success — the core of gu-pf764.
func TestExecutePluginScript_Failure(t *testing.T) {
	dir, script := writeRunScript(t, "echo boom >&2; exit 3\n")
	p := &plugin.Plugin{Name: "test", Path: dir, HasRunScript: true}

	if err := executePluginScript(p, script, dir); err == nil {
		t.Fatal("expected error for non-zero exit, got nil")
	}
}

// TestExecutePluginScript_SetsTownRootEnv verifies GT_TOWN_ROOT is exported to
// the script — the workaround in the bug report relied on this env var, so the
// in-process path must set it too.
func TestExecutePluginScript_SetsTownRootEnv(t *testing.T) {
	dir, script := writeRunScript(t, `[[ "$GT_TOWN_ROOT" == "$EXPECTED_ROOT" ]] || exit 1`+"\n")
	t.Setenv("EXPECTED_ROOT", dir)
	p := &plugin.Plugin{Name: "test", Path: dir, HasRunScript: true}

	if err := executePluginScript(p, script, dir); err != nil {
		t.Fatalf("GT_TOWN_ROOT not propagated to run.sh: %v", err)
	}
}
