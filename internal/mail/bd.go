package mail

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/util"
)

const (
	// bdReadTimeout is the timeout for bd read operations (list, show, query).
	// 60s accommodates concurrent agent load where multiple bd processes compete
	// for Dolt locks and memory (was 30s, caused signal:killed under contention).
	bdReadTimeout = 60 * time.Second
	// bdWriteTimeout is the timeout for bd write operations (create, close, label, reopen).
	bdWriteTimeout = 60 * time.Second
)

// bdError represents an error from running a bd command.
// It wraps the underlying error and includes the stderr output for inspection.
type bdError struct {
	Err    error
	Stderr string
}

// Error implements the error interface.
func (e *bdError) Error() string {
	if e.Stderr != "" {
		return e.Stderr
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return "unknown bd error"
}

// Unwrap returns the underlying error for errors.Is/As compatibility.
func (e *bdError) Unwrap() error {
	return e.Err
}

// ContainsError checks if the stderr message contains the given substring.
func (e *bdError) ContainsError(substr string) bool {
	return strings.Contains(e.Stderr, substr)
}

// runBdCommand executes a bd command with a context timeout and proper environment setup.
// ctx controls the deadline/timeout for the subprocess.
// workDir is the directory to run the command in.
// beadsDir is the BEADS_DIR environment variable value.
// Returns stdout bytes on success, or a *bdError on failure.
func runBdCommand(ctx context.Context, args []string, workDir, beadsDir string) (_ []byte, retErr error) {
	defer func() { telemetry.RecordMail(ctx, "bd."+firstArg(args), retErr) }()

	// Remove stale dolt-server.pid before spawning bd. A stale PID file causes
	// bd to connect to port 3307 which may be occupied by a different Dolt server
	// serving different databases, resulting in hangs until the read timeout kills it.
	beads.CleanStaleDoltServerPID(beadsDir)

	// bd v0.59+ requires --flat for list --json to produce JSON output.
	// Without it, bd returns human-readable tree format that fails JSON parsing.
	// The mail package calls bd directly (not via beads.Run), so it needs its
	// own injection. (GH#2746)
	args = beads.InjectFlatForListJSON(args)

	readOnly := isMailBdReadCommand(args)

	var stdout, stderr bytes.Buffer

	// runOnce builds and runs a single bd subprocess with the standard
	// process-group/env/pipe wiring. Buffers are reset by the caller.
	runOnce := func(cmdArgs []string) error {
		cmd := exec.CommandContext(ctx, "bd", cmdArgs...) //nolint:gosec // G204: bd is a trusted internal tool
		cmd.Dir = workDir
		util.SetDetachedProcessGroup(cmd)
		cmd.Env = bdSubprocessEnv(cmd.Environ(), beadsDir, readOnly, nil)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		return cmd.Run()
	}

	// runAttempt performs one full attempt: the initial run plus the --flat
	// fallback for bd versions that don't support it (< v0.59, GH#2746).
	runAttempt := func() error {
		stdout.Reset()
		stderr.Reset()
		runErr := runOnce(args)
		if runErr != nil && strings.Contains(stderr.String(), "unknown flag: --flat") {
			retryArgs := make([]string, 0, len(args))
			for _, a := range args {
				if a != "--flat" {
					retryArgs = append(retryArgs, a)
				}
			}
			stdout.Reset()
			stderr.Reset()
			runErr = runOnce(retryArgs)
		}
		return runErr
	}

	// Retry transient Dolt connection-establishment failures (gu-vsts5): under
	// concurrent load the shared gt Dolt server momentarily refuses/times out a
	// TCP dial even though it is up; a flat retry clears it. Only dial-level
	// failures are retried (the socket never opened, so no server-side work ran).
	runErr := runAttempt()
	for attempt := 1; runErr != nil && attempt < beads.DoltConnRetryAttempts && beads.IsTransientDoltDialError(stderr.String()); attempt++ {
		time.Sleep(beads.DoltConnRetryBackoff * time.Duration(attempt))
		runErr = runAttempt()
	}

	if runErr != nil {
		// Rewrite bd's misleading "run 'bd dolt start'" advice into gt-aware
		// guidance (gu-vsts5): gt owns one shared server; starting a second
		// risks split-brain.
		return nil, &bdError{
			Err:    runErr,
			Stderr: beads.RewriteDoltUnreachableMessage(strings.TrimSpace(stderr.String())),
		}
	}

	return stdout.Bytes(), nil
}

// firstArg returns args[0] or "" when the slice is empty.
func firstArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

func bdSubprocessEnv(baseEnv []string, beadsDir string, readOnly bool, extraEnv []string) []string {
	base := append(append([]string{}, baseEnv...), extraEnv...)
	mode := beads.MutationRouting
	if readOnly {
		mode = beads.ReadOnlyRouting
	}
	if beadsDir != "" {
		if readOnly {
			mode = beads.ReadOnlyPinned
		} else {
			mode = beads.MutationPinned
		}
	}
	env := beads.EnvForSubprocessMode(base, beadsDir, mode)
	env = append(env, telemetry.OTELEnvForSubprocess()...)
	return env
}

func isMailBdReadCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "list", "show", "search":
		return true
	case "mol":
		return len(args) >= 3 && args[1] == "wisp" && args[2] == "list"
	case "sql":
		query := ""
		for i := len(args) - 1; i >= 1; i-- {
			if !strings.HasPrefix(args[i], "-") {
				query = args[i]
				break
			}
		}
		q := strings.ToLower(strings.TrimSpace(query))
		return strings.HasPrefix(q, "select") || strings.HasPrefix(q, "show") || strings.HasPrefix(q, "explain") || strings.HasPrefix(q, "describe") || strings.HasPrefix(q, "with")
	default:
		return false
	}
}

// bdReadCtx returns a context with the standard bd read timeout.
//
//nolint:gosec // The cancel function is returned to callers, who are responsible for invoking it.
func bdReadCtx() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), bdReadTimeout)
	return ctx, cancel
}

// bdWriteCtx returns a context with the standard bd write timeout.
//
//nolint:gosec // The cancel function is returned to callers, who are responsible for invoking it.
func bdWriteCtx() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), bdWriteTimeout)
	return ctx, cancel
}
