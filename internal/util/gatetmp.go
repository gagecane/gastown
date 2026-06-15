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

// gateDoltEnvDenyPrefixes lists env-var name prefixes scrubbed from a gate
// subprocess environment so that `go test ./...` does NOT inherit a daemon's
// production Dolt-routing variables (gu-5ja0e, gs-bc08).
//
// A long-running daemon (main_branch_test patrol, refinery) runs with
// GT_DOLT_PORT/BEADS_DOLT_PORT pinned to the shared production Dolt server
// (3307). Passing those through to a gate's `go test` defeats the beads
// test-isolation safety net: PreventTestDoltLeak (internal/beads/database.go)
// only pins a test fixture to an isolated embedded data dir when NO Dolt-routing
// var is set — when it sees an inherited GT_DOLT_PORT/BEADS_DOLT_PORT it assumes
// a legitimate test container and bails, so any beads-backed test then connects
// to production :3307 and leaks orphan databases. Container-backed integration
// tests are unaffected: they start their own container and set GT_DOLT_PORT
// process-wide from inside the test (testutil.StartIsolatedDoltContainer /
// RequireDoltContainer), so scrubbing the inherited value cannot route them to
// production.
var gateDoltEnvDenyPrefixes = []string{
	"GT_DOLT_",    // GT_DOLT_PORT, GT_DOLT_HOST
	"BEADS_DOLT_", // BEADS_DOLT_PORT, BEADS_DOLT_SERVER_HOST, BEADS_DOLT_SERVER_PORT
	"DOLT_",       // raw dolt client overrides
}

// StripDoltRoutingEnv returns a copy of env with Dolt-routing variables removed
// per gateDoltEnvDenyPrefixes. It is the single source of truth for which env
// vars a gate `go test` run must NOT inherit from a daemon connected to the
// production Dolt server. Pure function for unit-testing the filter.
func StripDoltRoutingEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			out = append(out, kv)
			continue
		}
		key := kv[:eq]
		denied := false
		for _, p := range gateDoltEnvDenyPrefixes {
			if strings.HasPrefix(key, p) {
				denied = true
				break
			}
		}
		if !denied {
			out = append(out, kv)
		}
	}
	return out
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
