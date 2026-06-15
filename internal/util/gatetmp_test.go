package util

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Gate temp scoping routes Go gate subprocesses off a small /tmp tmpfs onto
// disk-backed storage so concurrent full-suite runs can't fill it and fail the
// linker with ENOSPC (gu-l4aue).

func TestGateTmpDir_UsesBaseOverride(t *testing.T) {
	base := t.TempDir()
	t.Setenv("GT_GATE_TMPDIR_BASE", base)

	want := filepath.Join(base, "gt-gate-tmp")
	if got := GateTmpDir(); got != want {
		t.Errorf("GateTmpDir = %q, want %q", got, want)
	}
}

func TestGateTmpDir_OptOut(t *testing.T) {
	t.Setenv("GT_GATE_TMPDIR_BASE", t.TempDir())
	t.Setenv("GT_GATE_TMPDIR", "off")

	if got := GateTmpDir(); got != "" {
		t.Errorf("GateTmpDir with opt-out = %q, want empty", got)
	}
}

func TestGateTmpDir_FallsBackToUserCacheDir(t *testing.T) {
	t.Setenv("GT_GATE_TMPDIR_BASE", "")
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Skip("cannot determine user cache dir")
	}

	want := filepath.Join(cacheDir, "gt-gate-tmp")
	if got := GateTmpDir(); got != want {
		t.Errorf("GateTmpDir = %q, want %q", got, want)
	}
}

func TestWithGateTmpEnv_OverridesTmpdir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("GT_GATE_TMPDIR_BASE", base)

	// Stale inherited TMPDIR/GOTMPDIR must be replaced, not duplicated.
	in := []string{
		"PATH=/usr/bin",
		"TMPDIR=/tmp",
		"GOTMPDIR=/tmp",
		"FOO=bar",
	}
	env := WithGateTmpEnv(in)

	wantDir := filepath.Join(base, "gt-gate-tmp")
	var tmpCount, goTmpCount int
	var lastTmp, lastGoTmp string
	var keptFoo bool
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "TMPDIR="):
			tmpCount++
			lastTmp = strings.TrimPrefix(kv, "TMPDIR=")
		case strings.HasPrefix(kv, "GOTMPDIR="):
			goTmpCount++
			lastGoTmp = strings.TrimPrefix(kv, "GOTMPDIR=")
		case kv == "FOO=bar":
			keptFoo = true
		}
	}

	if tmpCount != 1 {
		t.Errorf("found %d TMPDIR entries, want exactly 1", tmpCount)
	}
	if goTmpCount != 1 {
		t.Errorf("found %d GOTMPDIR entries, want exactly 1", goTmpCount)
	}
	if lastTmp != wantDir {
		t.Errorf("TMPDIR = %q, want %q", lastTmp, wantDir)
	}
	if lastGoTmp != wantDir {
		t.Errorf("GOTMPDIR = %q, want %q", lastGoTmp, wantDir)
	}
	if !keptFoo {
		t.Error("WithGateTmpEnv dropped unrelated var FOO; filtering is too broad")
	}

	// The override directory must actually be created so the gate can write to it.
	if fi, err := os.Stat(wantDir); err != nil || !fi.IsDir() {
		t.Errorf("gate temp dir %q not created: err=%v", wantDir, err)
	}
}

func TestWithGateTmpEnv_UnchangedWhenDisabled(t *testing.T) {
	t.Setenv("GT_GATE_TMPDIR_BASE", t.TempDir())
	t.Setenv("GT_GATE_TMPDIR", "off")

	in := []string{"PATH=/usr/bin", "TMPDIR=/tmp"}
	env := WithGateTmpEnv(in)

	if len(env) != len(in) {
		t.Fatalf("env length = %d, want %d (unchanged)", len(env), len(in))
	}
	for i := range in {
		if env[i] != in[i] {
			t.Errorf("env[%d] = %q, want %q (unchanged)", i, env[i], in[i])
		}
	}
}

// StripDoltRoutingEnv removes a daemon's production Dolt-routing vars so a gate
// `go test ./...` can't inherit them and leak orphan databases onto the shared
// production Dolt server (gu-5ja0e, gs-bc08).

func TestStripDoltRoutingEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"GT_DOLT_PORT=3307",
		"GT_DOLT_HOST=127.0.0.1",
		"BEADS_DOLT_PORT=3307",
		"BEADS_DOLT_SERVER_HOST=127.0.0.1",
		"DOLT_ROOT_PATH=/x",
		"CI=true",
		// A var that merely contains "DOLT" mid-name must be kept — only the
		// listed name PREFIXES are scrubbed.
		"MY_DOLT_BACKUP=keep",
		// Malformed entry (no '=') is passed through untouched.
		"WEIRD",
	}
	got := StripDoltRoutingEnv(in)
	want := []string{"PATH=/usr/bin", "CI=true", "MY_DOLT_BACKUP=keep", "WEIRD"}
	if len(got) != len(want) {
		t.Fatalf("StripDoltRoutingEnv returned %d entries, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
}
