// Package beads — subprocess execution and environment plumbing.
//
// This file owns the mechanics of invoking the bd CLI: argv assembly with
// capability flags, environment construction (isolated vs live mode, Dolt
// connection translation, BEADS_DIR handling), stdout/stderr parsing, and
// the shared error-wrapping + crash-detection helpers.
package beads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/util"
)

// run executes a bd command and returns stdout.
func (b *Beads) run(args ...string) (_ []byte, retErr error) {
	start := time.Now()
	// Declare buffers before defer so the closure captures them after cmd.Run.
	var stdout, stderr bytes.Buffer
	defer func() {
		telemetry.RecordBDCall(context.Background(), args, float64(time.Since(start).Milliseconds()), retErr, stdout.Bytes(), stderr.String())
	}()
	// bd v0.59+ requires --flat for --json to produce JSON output on "list" commands.
	// Without --flat, bd list --json silently returns human-readable tree format,
	// causing all JSON parsing to fail. Inject --flat before --allow-stale prepend
	// (which changes args[0] from "list" to "--allow-stale").
	args = InjectFlatForListJSON(args)

	// Conditionally use --allow-stale to prevent failures when db is temporarily stale
	// (e.g., after daemon is killed during shutdown). Only if bd supports it.
	beadsDir := b.beadsDir
	if beadsDir == "" {
		beadsDir = ResolveBeadsDir(b.workDir)
	}
	runEnv := append(b.buildRunEnv(), "BEADS_DIR="+beadsDir)
	fullArgs := MaybePrependAllowStaleWithEnv(runEnv, args)

	// Always explicitly set BEADS_DIR to prevent inherited env vars from
	// causing prefix mismatches. Use explicit beadsDir if set, otherwise
	// resolve from working directory.
	cmd := exec.Command("bd", fullArgs...) //nolint:gosec // G204: bd is a trusted internal tool
	util.SetDetachedProcessGroup(cmd)
	cmd.Dir = b.workDir

	cmd.Env = runEnv
	cmd.Env = append(cmd.Env, telemetry.OTELEnvForSubprocess()...)

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// If bd doesn't support --flat, retry without it. The retry is done here
	// (not in callers like List) so that InjectFlatForListJSON doesn't re-add
	// --flat on the retry path.
	if err != nil && strings.Contains(stderr.String(), "unknown flag: --flat") {
		retryArgs := make([]string, 0, len(fullArgs))
		for _, a := range fullArgs {
			if a != "--flat" {
				retryArgs = append(retryArgs, a)
			}
		}
		stdout.Reset()
		stderr.Reset()
		cmd = exec.Command("bd", retryArgs...) //nolint:gosec // G204: bd is a trusted internal tool
		util.SetDetachedProcessGroup(cmd)
		cmd.Dir = b.workDir
		cmd.Env = runEnv
		cmd.Env = append(cmd.Env, telemetry.OTELEnvForSubprocess()...)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err = cmd.Run()
	}

	if err != nil {
		return nil, b.wrapError(err, stderr.String(), args)
	}

	// Handle bd exit code 0 bug: when issue not found,
	// bd may exit 0 but write error to stderr with empty stdout.
	// Detect this case and treat as error to avoid JSON parse failures.
	if stdout.Len() == 0 && stderr.Len() > 0 {
		return nil, b.wrapError(fmt.Errorf("command produced no output"), stderr.String(), args)
	}

	return stripStdoutWarnings(stdout.Bytes()), nil
}

// runWithRouting executes a bd command without setting BEADS_DIR, allowing bd's
// native prefix-based routing via routes.jsonl to resolve cross-prefix beads.
// This is needed for slot operations that reference beads with different prefixes
// (e.g., setting an hq-* hook bead on a gt-* agent bead).
// See: sling_helpers.go verifyBeadExists/hookBeadWithRetry for the same pattern.
func (b *Beads) runWithRouting(args ...string) (_ []byte, retErr error) { //nolint:unparam // mirrors run() signature for consistency
	start := time.Now()
	var stdout, stderr bytes.Buffer
	defer func() {
		telemetry.RecordBDCall(context.Background(), args, float64(time.Since(start).Milliseconds()), retErr, stdout.Bytes(), stderr.String())
	}()
	runEnv := b.buildRoutingEnv()
	fullArgs := MaybePrependAllowStaleWithEnv(runEnv, args)

	cmd := exec.Command("bd", fullArgs...) //nolint:gosec // G204: bd is a trusted internal tool
	util.SetDetachedProcessGroup(cmd)
	cmd.Dir = b.workDir

	cmd.Env = runEnv
	cmd.Env = append(cmd.Env, telemetry.OTELEnvForSubprocess()...)

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return nil, b.wrapError(err, stderr.String(), args)
	}

	if stdout.Len() == 0 && stderr.Len() > 0 {
		return nil, b.wrapError(fmt.Errorf("command produced no output"), stderr.String(), args)
	}

	return stripStdoutWarnings(stdout.Bytes()), nil
}

// Run executes a bd command and returns stdout.
// This is a public wrapper around the internal run method for cases where
// callers need to run arbitrary bd commands.
func (b *Beads) Run(args ...string) ([]byte, error) {
	return b.run(args...)
}

// wrapError wraps bd errors with context.
// ZFC: Avoid parsing stderr to make decisions. Transport errors to agents instead.
// Exception: ErrNotInstalled (exec.ErrNotFound) and ErrNotFound (issue lookup) are
// acceptable as they enable basic error handling without decision-making.
func (b *Beads) wrapError(err error, stderr string, args []string) error {
	stderr = strings.TrimSpace(stderr)

	// Check for bd not installed
	if execErr, ok := err.(*exec.Error); ok && errors.Is(execErr.Err, exec.ErrNotFound) {
		return ErrNotInstalled
	}

	// ErrNotFound is widely used for issue lookups - acceptable exception
	// Match various "not found" error patterns from bd
	if strings.Contains(stderr, "not found") || strings.Contains(stderr, "Issue not found") ||
		strings.Contains(stderr, "no issue found") {
		return ErrNotFound
	}

	if stderr != "" {
		return fmt.Errorf("bd %s: %s", strings.Join(args, " "), stderr)
	}
	return fmt.Errorf("bd %s: %w", strings.Join(args, " "), err)
}

// isSubprocessCrash returns true if the error indicates the subprocess crashed
// (e.g., Dolt nil pointer dereference causing SIGSEGV). This is used to detect
// recoverable failures where a fallback strategy should be attempted (GH#1769).
func isSubprocessCrash(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// Detect signals from crashed subprocesses (bd panic → SIGSEGV)
	return strings.Contains(errStr, "signal:") ||
		strings.Contains(errStr, "segmentation") ||
		strings.Contains(errStr, "nil pointer") ||
		strings.Contains(errStr, "panic:")
}

// buildRunEnv builds the environment for run() calls.
// In isolated mode: strips all beads-related env vars for test isolation.
// Otherwise: strips inherited BEADS_DIR so the caller can append the correct value.
// Without this, getenv() returns the first occurrence, so an inherited BEADS_DIR
// (e.g., from a parent process or shell context) would shadow the explicit value
// appended by run(). This was the root cause of gt-uygpe / GH #803.
func (b *Beads) buildRunEnv() []string {
	if b.isolated {
		env := filterBeadsEnv(os.Environ())
		if b.serverPort > 0 {
			env = append(env, fmt.Sprintf("GT_DOLT_PORT=%d", b.serverPort))
			env = append(env, fmt.Sprintf("BEADS_DOLT_PORT=%d", b.serverPort))
		}
		return env
	}
	env := stripEnvPrefixes(os.Environ(), "BEADS_DIR=")
	env = overrideDoltEnvFromBeadsDir(env, b.getResolvedBeadsDir())
	return translateDoltPort(env)
}

// buildRoutingEnv builds the environment for runWithRouting() calls.
// Always strips BEADS_DIR so bd uses native routing.
// In isolated mode: also strips BD_ACTOR, BEADS_*, GT_ROOT, HOME.
func (b *Beads) buildRoutingEnv() []string {
	if b.isolated {
		env := filterBeadsEnv(os.Environ())
		if b.serverPort > 0 {
			env = append(env, fmt.Sprintf("GT_DOLT_PORT=%d", b.serverPort))
			env = append(env, fmt.Sprintf("BEADS_DOLT_PORT=%d", b.serverPort))
		}
		return env
	}
	env := stripEnvPrefixes(os.Environ(), "BEADS_DIR=")
	env = overrideDoltEnvFromBeadsDir(env, b.getResolvedBeadsDir())
	return translateDoltPort(env)
}

// filterBeadsEnv removes beads-related environment variables from the given
// environment slice. This ensures test isolation by preventing inherited
// BD_ACTOR, BEADS_DB, GT_ROOT, HOME etc. from routing commands to production databases.
//
// Preserves GT_DOLT_PORT, BEADS_DOLT_PORT, and BEADS_DOLT_SERVER_HOST so that
// isolated-mode tests can reach a test Dolt server on a non-default port/host.
func filterBeadsEnv(environ []string) []string {
	filtered := make([]string, 0, len(environ))
	for _, env := range environ {
		// Preserve Dolt connection env vars needed to reach test/remote Dolt servers.
		// These must be checked before the broad BEADS_ prefix strip below.
		if strings.HasPrefix(env, "BEADS_DOLT_PORT=") ||
			strings.HasPrefix(env, "BEADS_DOLT_SERVER_HOST=") ||
			strings.HasPrefix(env, "GT_DOLT_PORT=") {
			filtered = append(filtered, env)
			continue
		}
		// Skip beads-related env vars that could interfere with test isolation
		// BD_ACTOR, BEADS_* - direct beads config
		// GT_ROOT - causes bd to find global routes file
		// HOME - causes bd to find ~/.beads-planning routing
		if strings.HasPrefix(env, "BD_ACTOR=") ||
			strings.HasPrefix(env, "BEADS_") ||
			strings.HasPrefix(env, "GT_ROOT=") ||
			strings.HasPrefix(env, "HOME=") {
			continue
		}
		filtered = append(filtered, env)
	}
	return filtered
}

// translateDoltPort ensures BEADS_DOLT_PORT and BEADS_DOLT_SERVER_HOST are set
// when their GT_ counterparts are present. Gas Town uses GT_DOLT_PORT and
// GT_DOLT_HOST; beads uses BEADS_DOLT_PORT and BEADS_DOLT_SERVER_HOST. This
// translation prevents bd subprocesses from falling back to localhost:3307
// when a test or daemon has set GT_DOLT_* to alternate values.
func translateDoltPort(env []string) []string {
	var gtPort, gtHost string
	hasBDP, hasBDH := false, false
	for _, e := range env {
		if strings.HasPrefix(e, "GT_DOLT_PORT=") {
			gtPort = strings.TrimPrefix(e, "GT_DOLT_PORT=")
		}
		if strings.HasPrefix(e, "GT_DOLT_HOST=") {
			gtHost = strings.TrimPrefix(e, "GT_DOLT_HOST=")
		}
		if strings.HasPrefix(e, "BEADS_DOLT_PORT=") {
			hasBDP = true
		}
		if strings.HasPrefix(e, "BEADS_DOLT_SERVER_HOST=") {
			hasBDH = true
		}
	}
	if gtPort != "" && !hasBDP {
		env = append(env, "BEADS_DOLT_PORT="+gtPort)
	}
	if gtHost != "" && !hasBDH {
		env = append(env, "BEADS_DOLT_SERVER_HOST="+gtHost)
	}
	return env
}

// overrideDoltEnvFromBeadsDir replaces inherited BEADS_DOLT_* values with the
// authoritative connection data for the selected beads directory when present.
// This prevents a parent shell's stale Dolt port from routing bd commands to
// the wrong server when the command explicitly targets another rig's .beads dir.
func overrideDoltEnvFromBeadsDir(env []string, beadsDir string) []string {
	port, host := doltConnectionFromBeadsDir(beadsDir)
	if port != "" {
		env = stripEnvPrefixes(env, "BEADS_DOLT_PORT=")
		env = append(env, "BEADS_DOLT_PORT="+port)
	}
	if host != "" {
		env = stripEnvPrefixes(env, "BEADS_DOLT_SERVER_HOST=")
		env = append(env, "BEADS_DOLT_SERVER_HOST="+host)
	}
	return env
}

// doltConnectionFromBeadsDir reads the preferred Dolt connection info for a
// beads directory. The per-directory port file is authoritative when present;
// metadata.json is used as a fallback and to supply the server host.
func doltConnectionFromBeadsDir(beadsDir string) (port string, host string) {
	if beadsDir == "" {
		return "", ""
	}

	if data, err := os.ReadFile(filepath.Join(beadsDir, "dolt-server.port")); err == nil {
		port = strings.TrimSpace(string(data))
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		return port, ""
	}

	var meta struct {
		DoltServerPort int    `json:"dolt_server_port"`
		DoltServerHost string `json:"dolt_server_host"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return port, ""
	}

	if port == "" && meta.DoltServerPort > 0 {
		port = strconv.Itoa(meta.DoltServerPort)
	}
	host = strings.TrimSpace(meta.DoltServerHost)
	return port, host
}

// stripEnvPrefixes removes entries matching any of the given prefixes from an
// environment variable slice. Used by runWithRouting to strip BEADS_DIR.
func stripEnvPrefixes(environ []string, prefixes ...string) []string {
	filtered := make([]string, 0, len(environ))
	for _, env := range environ {
		skip := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(env, prefix) {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, env)
		}
	}
	return filtered
}

// stripStdoutWarnings removes warning/diagnostic lines that bd may emit to stdout.
// bd sometimes prints "warning: ..." lines to stdout instead of stderr, which
// corrupts JSON output. This strips those lines so downstream JSON parsing works.
func stripStdoutWarnings(data []byte) []byte {
	if !bytes.Contains(data, []byte("warning:")) {
		return data
	}

	lines := bytes.Split(data, []byte("\n"))
	var cleaned [][]byte
	stripped := false
	for _, line := range lines {
		if bytes.HasPrefix(bytes.TrimSpace(line), []byte("warning:")) {
			stripped = true
			continue
		}
		cleaned = append(cleaned, line)
	}

	if !stripped {
		return data
	}
	return bytes.Join(cleaned, []byte("\n"))
}

// isJSONBytes returns true if the byte slice starts with [ or { (after whitespace).
// bd list --json may return plain text like "No issues found." instead of JSON
// when there are no results.
func isJSONBytes(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		case '[', '{':
			return true
		default:
			return false
		}
	}
	return false
}
