package beads

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFilterBeadsEnv_NilInput verifies filterBeadsEnv does not panic on nil.
func TestFilterBeadsEnv_NilInput(t *testing.T) {
	got := filterBeadsEnv(nil)
	if got == nil {
		t.Fatal("filterBeadsEnv(nil) returned nil, want empty slice")
	}
	if len(got) != 0 {
		t.Errorf("filterBeadsEnv(nil) returned %d items, want 0", len(got))
	}
}

// TestFilterBeadsEnv_EmptyInput verifies filterBeadsEnv on empty slice.
func TestFilterBeadsEnv_EmptyInput(t *testing.T) {
	got := filterBeadsEnv([]string{})
	if len(got) != 0 {
		t.Errorf("filterBeadsEnv([]) returned %d items, want 0", len(got))
	}
}

// TestFilterBeadsEnv_PreservesDoltPortVars verifies that GT_DOLT_PORT and
// BEADS_DOLT_PORT are preserved by filterBeadsEnv even though BEADS_* vars
// are otherwise stripped. Tests need these to reach test Dolt servers.
func TestFilterBeadsEnv_PreservesDoltPortVars(t *testing.T) {
	environ := []string{
		"BD_ACTOR=test-actor",
		"BEADS_DIR=/tmp/beads",
		"BEADS_DB=/tmp/beads.db",
		"BEADS_DOLT_PORT=13306",
		"GT_DOLT_PORT=13307",
		"GT_ROOT=/tmp/gt",
		"HOME=/home/test",
		"PATH=/usr/bin",
	}
	got := filterBeadsEnv(environ)
	want := []string{
		"BEADS_DOLT_PORT=13306",
		"GT_DOLT_PORT=13307",
		"PATH=/usr/bin",
	}
	if len(got) != len(want) {
		t.Fatalf("filterBeadsEnv returned %d items, want %d\n  got:  %v\n  want: %v",
			len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestNewIsolatedWithPort verifies the constructor sets serverPort.
func TestNewIsolatedWithPort(t *testing.T) {
	b := NewIsolatedWithPort("/tmp/test", 13307)
	if !b.isolated {
		t.Error("expected isolated=true")
	}
	if b.serverPort != 13307 {
		t.Errorf("serverPort = %d, want 13307", b.serverPort)
	}
}

// TestStripEnvPrefixes verifies the generic prefix stripping used by runWithRouting.
func TestStripEnvPrefixes(t *testing.T) {
	tests := []struct {
		name     string
		environ  []string
		prefixes []string
		want     []string
	}{
		{
			name:     "strips single prefix",
			environ:  []string{"BEADS_DIR=/tmp", "PATH=/usr/bin", "HOME=/home"},
			prefixes: []string{"BEADS_DIR="},
			want:     []string{"PATH=/usr/bin", "HOME=/home"},
		},
		{
			name:     "strips multiple prefixes",
			environ:  []string{"BEADS_DIR=/tmp", "BD_ACTOR=test-actor", "PATH=/usr/bin"},
			prefixes: []string{"BEADS_DIR=", "BD_ACTOR="},
			want:     []string{"PATH=/usr/bin"},
		},
		{
			name:     "no matches",
			environ:  []string{"PATH=/usr/bin", "HOME=/home"},
			prefixes: []string{"BEADS_DIR=", "BD_ACTOR="},
			want:     []string{"PATH=/usr/bin", "HOME=/home"},
		},
		{
			name:     "empty prefixes",
			environ:  []string{"PATH=/usr/bin"},
			prefixes: []string{},
			want:     []string{"PATH=/usr/bin"},
		},
		{
			name:     "nil environ",
			environ:  nil,
			prefixes: []string{"BEADS_DIR="},
			want:     []string{},
		},
		{
			name:     "empty environ",
			environ:  []string{},
			prefixes: []string{"BEADS_DIR="},
			want:     []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripEnvPrefixes(tt.environ, tt.prefixes...)
			if len(got) != len(tt.want) {
				t.Fatalf("stripEnvPrefixes() returned %d items, want %d\n  got:  %v\n  want: %v",
					len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestStripEnvPrefixes_PreservesOrder verifies output ordering is stable.
func TestStripEnvPrefixes_PreservesOrder(t *testing.T) {
	environ := []string{"A=1", "BEADS_DIR=/tmp", "B=2", "BD_ACTOR=x", "C=3"}
	got := stripEnvPrefixes(environ, "BEADS_DIR=", "BD_ACTOR=")
	want := []string{"A=1", "B=2", "C=3"}

	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestTranslateDoltPort verifies GT_DOLT_PORT → BEADS_DOLT_PORT translation.
// This is the core fix for hq-27t: gastown sets GT_DOLT_PORT but bd only reads
// BEADS_DOLT_PORT. Without translation, bd falls back to metadata.json port 3307.
func TestTranslateDoltPort(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want []string
	}{
		{
			name: "translates GT to BEADS",
			env:  []string{"GT_DOLT_PORT=12345", "PATH=/usr/bin"},
			want: []string{"GT_DOLT_PORT=12345", "PATH=/usr/bin", "BEADS_DOLT_PORT=12345"},
		},
		{
			name: "skips if BEADS_DOLT_PORT already set",
			env:  []string{"GT_DOLT_PORT=12345", "BEADS_DOLT_PORT=99999"},
			want: []string{"GT_DOLT_PORT=12345", "BEADS_DOLT_PORT=99999"},
		},
		{
			name: "no-op without GT_DOLT_PORT",
			env:  []string{"PATH=/usr/bin"},
			want: []string{"PATH=/usr/bin"},
		},
		{
			name: "empty env",
			env:  []string{},
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := translateDoltPort(tt.env)
			if len(got) != len(tt.want) {
				t.Fatalf("translateDoltPort() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestFilterBeadsEnv_Integration verifies filterBeadsEnv strips all expected
// vars from a real os.Environ() with multiple beads vars set.
func TestFilterBeadsEnv_Integration(t *testing.T) {
	t.Setenv("BD_ACTOR", "gastown/polecats/TestPolecat")
	t.Setenv("BEADS_DIR", "/tmp/test-beads")
	t.Setenv("GT_ROOT", "/tmp/test-gt-root")

	env := filterBeadsEnv(os.Environ())

	// BEADS_DOLT_PORT and GT_DOLT_PORT are explicitly preserved (test server access).
	// Check that other BEADS_* vars are still stripped.
	forbidden := []string{"BD_ACTOR=", "BEADS_DIR=", "BEADS_DB=", "GT_ROOT=", "HOME="}
	for _, e := range env {
		for _, prefix := range forbidden {
			if strings.HasPrefix(e, prefix) {
				t.Errorf("filterBeadsEnv did not strip %s (found: %s)", prefix, e)
			}
		}
	}
}

// TestBdBranch_SystemScenario_FilterBeadsEnvIsolation verifies filterBeadsEnv
// strips all beads-related vars from subprocess environment.
func TestBdBranch_SystemScenario_FilterBeadsEnvIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping system test in short mode")
	}

	t.Setenv("BD_ACTOR", "gastown/polecats/FilterTest")
	t.Setenv("BEADS_DIR", "/tmp/filter-test-beads")
	t.Setenv("GT_ROOT", "/tmp/filter-test-gt")

	filtered := filterBeadsEnv(os.Environ())

	// Verify beads-specific vars are stripped from the filtered env.
	forbidden := []string{"BD_ACTOR=", "BEADS_DIR=", "GT_ROOT="}
	for _, entry := range filtered {
		for _, prefix := range forbidden {
			if strings.HasPrefix(entry, prefix) {
				t.Errorf("filterBeadsEnv result still contains %s", entry)
			}
		}
	}
}

// TestBuildRunEnv verifies buildRunEnv() returns the correct environment
// for each mode: default (passthrough) and isolated (strip all beads vars).
func TestBuildRunEnv(t *testing.T) {
	tests := []struct {
		name     string
		isolated bool
		// envVars to inject via t.Setenv
		envVars map[string]string
		// mustContain: prefixes that MUST be present in the result
		mustContain []string
		// mustNotContain: prefixes that MUST NOT be present in the result
		mustNotContain []string
	}{
		{
			name:           "default preserves all vars",
			envVars:        map[string]string{"PATH": "/usr/bin"},
			mustContain:    []string{"PATH="},
			mustNotContain: nil,
		},
		{
			name:           "isolated strips all beads vars",
			isolated:       true,
			envVars:        map[string]string{"BD_ACTOR": "test-actor", "BEADS_DIR": "/tmp/beads"},
			mustNotContain: []string{"BD_ACTOR=", "BEADS_DIR="},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}
			b := &Beads{workDir: "/tmp", isolated: tt.isolated}
			env := b.buildRunEnv()

			for _, prefix := range tt.mustContain {
				found := false
				for _, e := range env {
					if strings.HasPrefix(e, prefix) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected %s to be present", prefix)
				}
			}
			for _, prefix := range tt.mustNotContain {
				for _, e := range env {
					if strings.HasPrefix(e, prefix) {
						t.Errorf("expected %s to be absent, got %s", prefix, e)
					}
				}
			}
		})
	}
}

// TestBuildRoutingEnv verifies buildRoutingEnv() returns the correct environment
// for each mode: default (strip BEADS_DIR only) and isolated (strip all beads vars).
func TestBuildRoutingEnv(t *testing.T) {
	tests := []struct {
		name           string
		isolated       bool
		envVars        map[string]string
		mustContain    []string
		mustNotContain []string
	}{
		{
			name:           "default strips BEADS_DIR only",
			envVars:        map[string]string{"BEADS_DIR": "/tmp/beads", "PATH": "/usr/bin"},
			mustContain:    []string{"PATH="},
			mustNotContain: []string{"BEADS_DIR="},
		},
		{
			name:           "isolated strips all beads vars",
			isolated:       true,
			envVars:        map[string]string{"BD_ACTOR": "test-actor", "BEADS_DIR": "/tmp/beads"},
			mustNotContain: []string{"BD_ACTOR=", "BEADS_DIR="},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}
			b := &Beads{workDir: "/tmp", isolated: tt.isolated}
			env := b.buildRoutingEnv()

			for _, prefix := range tt.mustContain {
				found := false
				for _, e := range env {
					if strings.HasPrefix(e, prefix) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected %s to be present", prefix)
				}
			}
			for _, prefix := range tt.mustNotContain {
				for _, e := range env {
					if strings.HasPrefix(e, prefix) {
						t.Errorf("expected %s to be absent, got %s", prefix, e)
					}
				}
			}
		})
	}
}

func TestBuildRunEnv_OverridesStaleDoltPortFromBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte("43113\n"), 0644); err != nil {
		t.Fatalf("write dolt-server.port: %v", err)
	}

	t.Setenv("BEADS_DOLT_PORT", "3307")

	env := (&Beads{workDir: tmpDir}).buildRunEnv()

	found := false
	for _, e := range env {
		switch e {
		case "BEADS_DOLT_PORT=43113":
			found = true
		case "BEADS_DOLT_PORT=3307":
			t.Fatalf("stale BEADS_DOLT_PORT preserved in env: %v", env)
		}
	}
	if !found {
		t.Fatalf("expected BEADS_DOLT_PORT=43113 in env, got %v", env)
	}
}

func TestBuildRoutingEnv_OverridesStaleDoltPortFromBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte("43113\n"), 0644); err != nil {
		t.Fatalf("write dolt-server.port: %v", err)
	}

	t.Setenv("BEADS_DOLT_PORT", "3307")

	env := (&Beads{workDir: tmpDir}).buildRoutingEnv()

	found := false
	for _, e := range env {
		switch e {
		case "BEADS_DOLT_PORT=43113":
			found = true
		case "BEADS_DOLT_PORT=3307":
			t.Fatalf("stale BEADS_DOLT_PORT preserved in env: %v", env)
		}
	}
	if !found {
		t.Fatalf("expected BEADS_DOLT_PORT=43113 in env, got %v", env)
	}
}

func TestIsSubprocessCrash(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"normal error", fmt.Errorf("bd create: exit status 1"), false},
		{"not found", fmt.Errorf("bd show: not found"), false},
		{"signal segfault", fmt.Errorf("bd create: signal: segmentation fault"), true},
		{"signal killed", fmt.Errorf("bd create: signal: killed"), true},
		{"nil pointer in stderr", fmt.Errorf("bd create: nil pointer dereference"), true},
		{"panic in stderr", fmt.Errorf("bd create: panic: runtime error"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSubprocessCrash(tt.err); got != tt.want {
				t.Errorf("isSubprocessCrash(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
