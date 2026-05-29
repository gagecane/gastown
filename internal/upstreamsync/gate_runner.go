// Gate runner — executes a rig's configured CI gate commands and
// collects pass/fail results.
//
// Phase 2 (gu-4mj2). The gate suite is the contract between the
// upstream-sync system and the merge queue: a sync attempt only
// proceeds to PUSHING after all configured gates pass on the merged
// result. The runner is intentionally minimal — it shells out to
// the gate command via `sh -c`, captures combined stdout+stderr,
// and emits a structured result for each gate.
//
// Why `sh -c`: the design's gate config is a list of free-form shell
// commands ("go build ./...", "scripts/check-upstream-rebased.sh", …).
// The Refinery already runs gates this way (see internal/refinery
// gate runner) — keeping the same surface means rigs can move gate
// commands between subsystems without re-quoting.
//
// Failure semantics:
//   - First non-zero exit → stop and return GateFail for that command.
//   - Subsequent commands are reported as GateSkip (not run).
//   - Stdout+stderr capture survives in the GateRunResult so the
//     caller can attach the failure tail to a SyncAttempt or escalate
//     bead. The runner itself does NOT log to stdout — callers
//     control whether output streams live (e.g., the CLI sync verb)
//     or is silenced (e.g., a deacon patrol).
//
// Design context: .designs/cv-2s6tq/integration.md §"Gates" and
// .designs/cv-2s6tq/data.md §"Sync Attempt".
package upstreamsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/util"
)

// GateRunResult captures the outcome of running one gate command.
type GateRunResult struct {
	// Command is the original command string from gate config.
	Command string

	// Result is pass / fail / skip.
	Result GateResult

	// Output is the combined stdout+stderr captured during execution.
	// Empty for skipped commands. Truncated to MaxGateOutputBytes to
	// keep state-bead writes bounded.
	Output string

	// Duration is the wall-clock time spent executing the command.
	// Zero for skipped commands.
	Duration time.Duration

	// ExitCode is the process exit code (0 for pass, non-zero for fail).
	// -1 if the command could not be started (e.g., shell not found).
	// Zero for skipped commands.
	ExitCode int

	// Err carries the underlying error for fail/error cases. Nil for
	// pass and skip. Callers may use errors.Is / errors.As to
	// distinguish context-deadline-exceeded from generic exec failures.
	Err error
}

// MaxGateOutputBytes bounds how much captured output is kept per gate
// in the GateRunResult. Gate output goes into the per-attempt history
// on the state bead; the bead has a soft cap on metadata size, so we
// truncate aggressively here. Tail bytes (most recent) are preserved
// because the failure usually shows up at the end.
const MaxGateOutputBytes = 8 * 1024

// DefaultGateTimeout is the per-gate wall-clock cap. The Refinery
// runs the same gate suite with longer timeouts, but for the
// upstream-sync runner we keep a tighter ceiling so a hung gate
// doesn't strand the state machine in StateGating forever. Operators
// can override via GateRunOptions.PerCommandTimeout.
const DefaultGateTimeout = 10 * time.Minute

// GateRunOptions tunes the runner's behavior. The zero value uses the
// safe defaults: shell="/bin/sh", per-command timeout =
// DefaultGateTimeout, no environment overlay, no sandboxing.
type GateRunOptions struct {
	// Dir is the working directory passed to each gate command.
	// Required: gate commands assume they run at the repo root.
	Dir string

	// Shell is the shell used to interpret each gate command.
	// Defaults to "/bin/sh" when empty.
	Shell string

	// PerCommandTimeout is the wall-clock cap per gate. Zero means
	// DefaultGateTimeout; negative disables the timeout (not
	// recommended outside tests).
	PerCommandTimeout time.Duration

	// Env adds entries to the child process environment. The runner
	// does NOT scrub the inherited env unless SandboxedEnv is set;
	// callers control how much of the parent shell leaks through
	// (deacon patrol may want to strip BD_ACTOR; the CLI sync verb
	// wants to keep it).
	Env []string

	// SandboxedEnv controls credential scrubbing for upstream-sync
	// gates. The upstream-sync system executes gate commands (`go
	// build`, `go test`, …) on freshly-merged upstream code — code
	// that originated outside the fork's review boundary. A malicious
	// `init()`, `TestMain()`, or build-time generator could exfiltrate
	// any env-resident credential the child inherits.
	//
	// When SandboxedEnv is true, RunGates filters the parent env
	// before exec, dropping known credential-bearing variables (see
	// SandboxedEnvDenyPrefixes / SandboxedEnvDenyExact). Required
	// system vars (PATH, HOME, GOPATH, GOCACHE, etc.) are preserved.
	// Entries from Env are appended after filtering, so callers can
	// re-inject specific variables a gate genuinely needs.
	//
	// Defaults to false to preserve legacy behavior — Phase 5 wires
	// the upstream-sync CLI verb to default-enable it. Tests and the
	// Refinery's gate runner (which has its own sandboxing model)
	// continue to default off.
	//
	// Design context: .designs/cv-hpnja/security.md §C-SEC-1 and
	// .designs/cv-2s6tq/security.md §"T2: Credential theft via
	// malicious upstream code".
	SandboxedEnv bool
}

// SandboxedEnvDenyPrefixes lists env variable name prefixes that are
// stripped when GateRunOptions.SandboxedEnv is true. Prefix-match catches
// the common credential families without enumerating every variant.
//
// Update ordering note: when adding new prefixes, keep them lowercase-
// equivalent unique — the matcher is case-sensitive, matching unix
// convention. Don't shorten existing entries (e.g. "GH_" subsumes
// "GH_TOKEN"); we want explicit reviewable entries.
var SandboxedEnvDenyPrefixes = []string{
	"AWS_",         // AWS access keys, session tokens, profile
	"BD_",          // beads actor / overrides
	"DOLT_",        // dolt server credentials, mysql password, branch overrides
	"GT_DOLT_",     // gas-town dolt overrides
	"GITHUB_",      // GH App tokens, runner tokens, workflow context
	"GH_",          // gh CLI tokens (GH_TOKEN, GH_ENTERPRISE_TOKEN)
	"ANTHROPIC_",   // model provider keys
	"OPENAI_",      // model provider keys
	"GOOGLE_",      // GCP service account env / API keys
	"AZURE_",       // Azure SP creds
	"NPM_",         // npm registry tokens
	"PYPI_",        // pypi upload tokens
	"DOCKER_",      // docker registry creds (DOCKER_AUTH_CONFIG, DOCKER_PASSWORD)
	"CARGO_",       // crates.io tokens
	"HUGGINGFACE_", // HF tokens
	"SLACK_",       // slack webhooks/tokens
	"PAGERDUTY_",   // PD api keys
}

// SandboxedEnvDenyExact lists env variable names with no prefix match
// that are stripped when GateRunOptions.SandboxedEnv is true. Used for
// single-variable secrets that don't have a natural namespace prefix.
var SandboxedEnvDenyExact = []string{
	"NETRC",
	"CURL_NETRC",
	"SSH_AUTH_SOCK", // ssh-agent forwarding — denies access to keys
	"SSH_AGENT_PID",
	"SSL_CLIENT_CERT",
	"GIT_ASKPASS",
	"GCM_INTERACTIVE",
	"VAULT_TOKEN",
	"VAULT_ADDR",
	"KUBECONFIG",
	"GOOGLE_APPLICATION_CREDENTIALS",
}

// SandboxFilterEnv returns a copy of `parent` with credential-bearing
// variables removed per SandboxedEnvDenyPrefixes / SandboxedEnvDenyExact.
// Pure function so callers can unit-test the filter without exec.
//
// Inputs are KEY=VALUE strings as returned by os.Environ. Entries
// without an '=' are dropped (defensive — os.Environ never produces
// these, but a hand-built input might).
func SandboxFilterEnv(parent []string) []string {
	out := make([]string, 0, len(parent))
	for _, kv := range parent {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		key := kv[:eq]
		if isSandboxDenied(key) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// isSandboxDenied reports whether `key` matches any deny prefix or
// exact-name entry. Internal: callers go through SandboxFilterEnv.
func isSandboxDenied(key string) bool {
	for _, p := range SandboxedEnvDenyPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	for _, exact := range SandboxedEnvDenyExact {
		if key == exact {
			return true
		}
	}
	return false
}

// GateRunSummary is the aggregate outcome of running a gate suite.
type GateRunSummary struct {
	// Results in input order. len == len(commands) — skipped commands
	// after the first failure get a GateSkip entry so callers can map
	// command name → result without bookkeeping.
	Results []GateRunResult

	// AllPassed is the headline boolean for the state machine: if
	// true, the runner recommends transitioning gating → pushing.
	AllPassed bool

	// FailedCommand is the command string that failed (empty when
	// AllPassed is true). Convenient for error messages.
	FailedCommand string
}

// GateResultsMap projects the results into the map[string]GateResult
// shape used by SyncAttempt.GateResults on the state bead. The key is
// the command string; if two gates have the same command string, the
// later one overwrites the earlier one (tests should not rely on this
// — gate configs are expected to be unique).
func (s GateRunSummary) GateResultsMap() map[string]GateResult {
	out := make(map[string]GateResult, len(s.Results))
	for _, r := range s.Results {
		out[r.Command] = r.Result
	}
	return out
}

// RunGates executes a list of shell-style gate commands sequentially
// and returns a GateRunSummary describing the outcome. Stops on the
// first failure; remaining commands are reported as GateSkip.
//
// `ctx` provides the overall deadline for the suite — a too-large
// suite will be cut short by ctx cancellation, with the in-progress
// command receiving SIGTERM via exec.CommandContext. Per-command
// timeouts (opts.PerCommandTimeout) layer on top of ctx so a single
// slow gate doesn't burn the whole budget.
//
// `commands` is the gate list (ordered). Empty list returns a
// trivially-passing summary with len(Results) == 0 — callers should
// treat that as "no gates configured" rather than "all gates passed";
// the design requires at least one gate (typically `go build`) but
// the runner doesn't enforce that policy.
func RunGates(ctx context.Context, commands []string, opts GateRunOptions) GateRunSummary {
	summary := GateRunSummary{
		Results:   make([]GateRunResult, 0, len(commands)),
		AllPassed: true,
	}
	if len(commands) == 0 {
		return summary
	}

	shell := opts.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	timeout := opts.PerCommandTimeout
	if timeout == 0 {
		timeout = DefaultGateTimeout
	}

	for i, cmdStr := range commands {
		// Once a gate fails, mark all remaining as skipped without
		// running them.
		if !summary.AllPassed {
			summary.Results = append(summary.Results, GateRunResult{
				Command: cmdStr,
				Result:  GateSkip,
			})
			continue
		}

		// Honor outer ctx cancellation: emit skips for the rest.
		if ctx.Err() != nil {
			// Mark this and all remaining as skipped.
			for ; i < len(commands); i++ {
				summary.Results = append(summary.Results, GateRunResult{
					Command: commands[i],
					Result:  GateSkip,
					Err:     ctx.Err(),
				})
			}
			summary.AllPassed = false
			summary.FailedCommand = cmdStr
			return summary
		}

		res := runOneGate(ctx, shell, cmdStr, opts, timeout)
		summary.Results = append(summary.Results, res)
		if res.Result != GatePass {
			summary.AllPassed = false
			summary.FailedCommand = cmdStr
		}
	}

	return summary
}

// runOneGate executes a single gate command. Internal: callers go
// through RunGates for the suite-level orchestration.
func runOneGate(parentCtx context.Context, shell, cmdStr string, opts GateRunOptions, timeout time.Duration) GateRunResult {
	res := GateRunResult{
		Command: cmdStr,
		Result:  GateFail, // pessimistic until proven otherwise
	}

	// Layer the per-command timeout on top of the parent ctx.
	cmdCtx := parentCtx
	var cancel context.CancelFunc
	if timeout > 0 {
		cmdCtx, cancel = context.WithTimeout(parentCtx, timeout)
		defer cancel()
	}

	c := exec.CommandContext(cmdCtx, shell, "-c", cmdStr) //nolint:gosec // gate commands are operator-authored
	if opts.Dir != "" {
		c.Dir = opts.Dir
	}
	// Build the child env. Default behavior (SandboxedEnv=false): inherit
	// the parent env unchanged via cmd.Env zero-value, then optionally
	// append opts.Env entries. Sandboxed: explicitly populate Env with a
	// scrubbed parent so credentials never reach the child, then append
	// opts.Env so callers can inject specific variables a gate genuinely
	// needs (e.g., GOFLAGS, CGO_ENABLED). This is the C-SEC-1 enforcement
	// point — see GateRunOptions.SandboxedEnv for design context.
	if opts.SandboxedEnv {
		c.Env = SandboxFilterEnv(os.Environ())
		c.Env = append(c.Env, opts.Env...)
	} else if len(opts.Env) > 0 {
		// Legacy path: append to inherited env, not replace — gate
		// commands rely on PATH, HOME, GOPATH, etc.
		c.Env = append(c.Env, opts.Env...)
	}
	// Run the gate in its own process group and install a Cancel that
	// SIGKILLs the whole group on context expiry. Without this, gate
	// commands like `sleep 5` survive PerCommandTimeout cancellation:
	// CommandContext only signals the leader (sh), but sleep is reparented
	// to PID 1 and keeps writing to the inherited stdout pipe — `c.Run()`
	// then blocks in Wait() until the child closes the pipe naturally
	// (i.e., after the full sleep). Process-group SIGKILL terminates the
	// child too. WaitDelay belt-and-suspenders: bound the post-cancel
	// pipe wait at 100ms so any leftover I/O can't keep Run() blocked.
	// (gu-4mj2 follow-up: TestRunGates_PerCommandTimeout was failing on
	// CI with Duration ≈ 5s for a 100ms timeout.)
	util.SetProcessGroup(c)
	c.WaitDelay = 100 * time.Millisecond

	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf

	start := time.Now()
	err := c.Run()
	res.Duration = time.Since(start)
	res.Output = truncateGateOutput(buf.String())

	if err == nil {
		res.Result = GatePass
		res.ExitCode = 0
		return res
	}

	res.Err = err
	// Distinguish "could not start" (-1) from "exited non-zero" (positive code).
	var execErr *exec.ExitError
	if errors.As(err, &execErr) {
		res.ExitCode = execErr.ExitCode()
	} else {
		res.ExitCode = -1
	}
	return res
}

// truncateGateOutput keeps the last MaxGateOutputBytes of `s`,
// prepending a "[truncated]" marker so readers know the head was cut.
// Tail-preservation matches Refinery convention — gate failures
// usually surface at the end of the output.
func truncateGateOutput(s string) string {
	if len(s) <= MaxGateOutputBytes {
		return s
	}
	cut := len(s) - MaxGateOutputBytes
	// Avoid breaking mid-rune by advancing to the next newline.
	if nl := strings.IndexByte(s[cut:], '\n'); nl > 0 && nl < 256 {
		cut += nl + 1
	}
	const marker = "[truncated " // followed by N bytes
	return fmt.Sprintf("%s%d bytes]\n%s", marker, cut, s[cut:])
}
