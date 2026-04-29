// Package beads — bd CLI capability probing.
//
// This file owns detection of optional features in the installed bd binary
// (e.g., --allow-stale, --flat for list --json) and the argv-rewriting helpers
// that apply those features conditionally.
package beads

import (
	"bytes"
	"os/exec"
	"strings"
	"sync"

	"github.com/steveyegge/gastown/internal/util"
)

// bdAllowStale caches whether the installed bd supports --allow-stale.
// The cache is keyed by the resolved bd path so tests and subprocess stubs that
// replace bd on PATH get re-probed instead of reusing stale capability state.
var (
	bdAllowStaleMu     sync.Mutex
	bdAllowStalePath   string
	bdAllowStaleResult bool
)

// ResetBdAllowStaleCacheForTest clears the cached bd --allow-stale capability.
// It exists for tests that swap bd binaries on PATH within a single process.
func ResetBdAllowStaleCacheForTest() {
	bdAllowStaleMu.Lock()
	bdAllowStalePath = ""
	bdAllowStaleResult = false
	bdAllowStaleMu.Unlock()
}

// BdSupportsAllowStale returns true if the installed bd binary accepts --allow-stale.
func BdSupportsAllowStale() bool {
	return BdSupportsAllowStaleWithEnv(nil)
}

// BdSupportsAllowStaleWithEnv returns true if the installed bd binary accepts
// --allow-stale, probing with the provided environment when supplied.
func BdSupportsAllowStaleWithEnv(env []string) bool {
	bdPath, err := exec.LookPath("bd")
	if err != nil {
		return false
	}

	bdAllowStaleMu.Lock()
	cachedPath := bdAllowStalePath
	cachedResult := bdAllowStaleResult
	bdAllowStaleMu.Unlock()

	if cachedPath == bdPath {
		return cachedResult
	}

	cmd := exec.Command(bdPath, "--allow-stale", "version") //nolint:gosec // G204: bd is a trusted internal tool
	util.SetDetachedProcessGroup(cmd)
	if env != nil {
		cmd.Env = env
	}
	var combinedOut bytes.Buffer
	cmd.Stdout = &combinedOut
	cmd.Stderr = &combinedOut
	_ = cmd.Run()
	// bd v0.60+ exits 0 even on unknown flags, printing the error to stderr.
	// Check output for "unknown flag" to detect lack of support.
	supported := !strings.Contains(combinedOut.String(), "unknown flag")

	bdAllowStaleMu.Lock()
	if bdAllowStalePath != bdPath {
		bdAllowStalePath = bdPath
		bdAllowStaleResult = supported
	}
	result := bdAllowStaleResult
	bdAllowStaleMu.Unlock()
	return result
}

// MaybePrependAllowStale prepends --allow-stale to args if bd supports it.
// Exported for use by other packages that shell out to bd directly.
func MaybePrependAllowStale(args []string) []string {
	if BdSupportsAllowStale() {
		return append([]string{"--allow-stale"}, args...)
	}
	return args
}

// MaybePrependAllowStaleWithEnv prepends --allow-stale to args if bd supports it,
// probing with the provided environment when supplied.
func MaybePrependAllowStaleWithEnv(env []string, args []string) []string {
	if BdSupportsAllowStaleWithEnv(env) {
		return append([]string{"--allow-stale"}, args...)
	}
	return args
}

// InjectFlatForListJSON adds --flat to bd list commands that use --json.
// bd v0.59+ tree-format output ignores --json; --flat is required for JSON.
// Exported for use by other packages that call bd list directly.
func InjectFlatForListJSON(args []string) []string {
	// Only apply to top-level "bd list" commands (args[0] == "list"),
	// not subcommands like "bd dep list" where --flat is unsupported.
	if len(args) == 0 || args[0] != "list" {
		return args
	}
	hasJSON := false
	hasFlat := false
	for _, a := range args[1:] {
		switch {
		case a == "--json":
			hasJSON = true
		case a == "--flat":
			hasFlat = true
		}
	}
	if hasJSON && !hasFlat {
		return append(args, "--flat")
	}
	return args
}
