package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// skipIfAgentBinaryMissing skips the test if any of the specified agent binaries
// are not found in PATH. This allows tests that depend on specific agents to be
// skipped in environments where those agents aren't installed.
func skipIfAgentBinaryMissing(t *testing.T, agents ...string) {
	t.Helper()
	for _, agent := range agents {
		if _, err := exec.LookPath(agent); err != nil {
			t.Skipf("skipping test: agent binary %q not found in PATH", agent)
		}
	}
}

func writeAgentStub(t *testing.T, binDir, name string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		path := filepath.Join(binDir, name+".cmd")
		if err := os.WriteFile(path, []byte("@echo off\r\nexit /b 0\r\n"), 0644); err != nil {
			t.Fatalf("write %s stub: %v", name, err)
		}
		return
	}

	path := filepath.Join(binDir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write %s stub: %v", name, err)
	}
}

// isClaudeCommand checks if a command is claude (either "claude" or a path ending in "/claude").
// This handles the case where resolveClaudePath returns the full path to the claude binary.
// Also handles Windows paths with .exe extension.
func isClaudeCommand(cmd string) bool {
	base := filepath.Base(cmd)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return base == "claude"
}
