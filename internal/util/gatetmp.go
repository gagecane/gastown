package util

import (
	"os"
	"path/filepath"
	"strings"
)

// GateTmpDir returns a disk-backed temp directory for Go gate subprocesses
// (build/link/test), or "" when one cannot be resolved or scoping is disabled.
//
// Why (gu-l4aue): on hosts where /tmp is a small tmpfs (16G on the Gas Town
// build host) shared by every rig's merge gate, concurrent full-suite
// `go test ./...` runs fill it with live go-build/go-link working dirs — 5.4G
// across 21 dirs at peak — until the linker dies with "no space left on
// device" mid-link. The stale-dir sweep (gu-vzkyh) can't help: it only reclaims
// dirs older than 30m, and these are live by definition. Pointing the gate's
// TMPDIR/GOTMPDIR at disk-backed storage (the root fs, ~850G free) removes the
// contention at the source — the same fix the reporter applied by hand with
// TMPDIR=~/.cache/gotmp. Mirrors the rig-scoped GOCACHE approach (gu-sav6u).
//
// The directory is <base>/gt-gate-tmp, where <base> is GT_GATE_TMPDIR_BASE when
// set, else os.UserCacheDir() (e.g. $HOME/.cache, which is disk-backed on hosts
// whose /tmp is tmpfs). Returns "" if the base cannot be resolved — callers then
// leave TMPDIR inherited, preserving legacy behavior. Set GT_GATE_TMPDIR=off to
// opt out entirely.
func GateTmpDir() string {
	if os.Getenv("GT_GATE_TMPDIR") == "off" {
		return ""
	}
	base := os.Getenv("GT_GATE_TMPDIR_BASE")
	if base == "" {
		cacheDir, err := os.UserCacheDir()
		if err != nil {
			return ""
		}
		base = cacheDir
	}
	return filepath.Join(base, "gt-gate-tmp")
}

// WithGateTmpEnv returns env with TMPDIR and GOTMPDIR overridden to a
// disk-backed gate temp directory (see GateTmpDir), so Go gate subprocesses
// don't fill a small /tmp tmpfs and fail their link step with ENOSPC
// (gu-l4aue).
//
// env is the base environment (typically os.Environ() or an already-customized
// slice). Any existing TMPDIR/GOTMPDIR entries are replaced, not duplicated, so
// exec.Cmd's last-wins resolution lands on the override. The temp directory is
// created best-effort; if creation fails — or scoping is unavailable/disabled —
// env is returned unchanged so the gate still runs against the inherited TMPDIR.
func WithGateTmpEnv(env []string) []string {
	dir := GateTmpDir()
	if dir == "" {
		return env
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return env
	}
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		if strings.HasPrefix(kv, "TMPDIR=") || strings.HasPrefix(kv, "GOTMPDIR=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "TMPDIR="+dir, "GOTMPDIR="+dir)
}
