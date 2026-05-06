// Package cmd supervisor wrapper for kiro-cli polecat sessions.
//
// gu-m3ne: kiro-cli 2.2.1+ has a clean-exit bug (gu-ronb) where sessions end
// with status 0 mid-task without calling `gt done`. The polecat's bead stays
// HOOKED and the scheduler respawns it, triggering an infinite loop.
//
// This wrapper supervises kiro-cli invocations and recovers from clean-exit
// deaths by re-invoking with --resume and a continuation prompt, up to a
// bounded number of iterations OR a bounded wallclock budget (gu-ronb
// hardening 2026-05-06). When the agent legitimately finishes (called
// gt done, heartbeat state "exiting"/"idle"), the wrapper exits success.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/workspace"
)

const (
	// defaultMaxKiroIterations caps the supervisor recovery loop to avoid
	// burning credits indefinitely on a bead the model can't finish.
	defaultMaxKiroIterations = 5

	// defaultIterationTimeout caps how long ONE kiro-cli invocation is
	// allowed to run before the wrapper kills it. A hung kiro-cli would
	// otherwise block the polecat slot forever, breaking the scheduler's
	// capacity accounting. 15 minutes comfortably covers normal long-turn
	// work (observed gu-ronb deaths: 22s to ~10m) while still bounding
	// pathological hangs. Overridable via GT_KIRO_ITERATION_TIMEOUT.
	defaultIterationTimeout = 15 * time.Minute

	// defaultTotalTimeout is the overall wallclock budget for the wrapper,
	// covering all iterations + backoff sleeps. Guards against pathological
	// credit burn when the model legitimately cannot finish but each
	// iteration is individually fast. Max-iterations alone is not enough —
	// 5 iterations × 15m + backoff could exceed 75 min. Overridable via
	// GT_KIRO_TOTAL_TIMEOUT.
	defaultTotalTimeout = 30 * time.Minute

	// defaultRetryBackoff is the pause between a clean-exit-mid-task and
	// the next --resume attempt. Gives gt done time to flush heartbeat
	// state to disk, gives the scheduler a breath to reconcile, and
	// reduces the chance of tight retry loops. Overridable via
	// GT_KIRO_RETRY_BACKOFF; "0" disables.
	defaultRetryBackoff = 1500 * time.Millisecond

	// heartbeatSettleDelay gives gt done time to write the heartbeat state
	// update before the wrapper reads it after kiro-cli exits. Shorter
	// than defaultRetryBackoff because it's read-after-exit, not a retry
	// cooldown. Kept distinct so the backoff can be tuned/disabled
	// independently without breaking the settle read.
	heartbeatSettleDelay = 500 * time.Millisecond

	// kiroContinuePromptBase is the generic continuation prompt injected
	// on resume iterations when no hooked bead ID is available. Matches
	// the polecat formula's exit semantics: run gt done when actually
	// finished, gt done --status DEFERRED if truly nothing to do.
	kiroContinuePromptBase = "Your previous kiro-cli turn exited before you completed the assigned formula. " +
		"Continue your work. When you have finished all formula steps including the final submit/cleanup, " +
		"run `gt done` to submit to the merge queue and exit. If you have nothing to submit (no commits), " +
		"run `gt done --status DEFERRED` to release the hook cleanly. Do NOT simply stop — drive to an explicit gt done."
)

var polecatKiroWrapperCmd = &cobra.Command{
	Use:                "polecat-kiro-wrapper -- <kiro-cli> <args>...",
	Short:              "Supervise kiro-cli to recover from mid-task clean exits (gu-ronb workaround, gu-m3ne)",
	Long:               kiroWrapperLongHelp,
	RunE:               runPolecatKiroWrapper,
	DisableFlagParsing: true, // Pass all flags through to kiro-cli unchanged.
	SilenceUsage:       true,
}

const kiroWrapperLongHelp = `Wraps kiro-cli polecat invocations to recover from the kiro-cli 2.2.1 bug
(gu-ronb) where sessions exit cleanly with status 0 mid-task, without
calling gt done. Without this wrapper, the polecat's bead stays HOOKED,
the scheduler respawns it, and the loop repeats.

When kiro-cli exits with status 0 but the polecat has not called gt done
(checked via polecat heartbeat state), the wrapper re-invokes kiro-cli
with --resume and a continuation prompt, bounded by BOTH an iteration
count AND a wallclock budget. The session is resumed in the same working
directory so the conversation context and any tool state carry over.

Recovery is bounded by three caps (whichever trips first):
  - GT_KIRO_MAX_ITERATIONS     iteration count (default 5)
  - GT_KIRO_TOTAL_TIMEOUT      total wallclock budget (default 30m)
  - GT_KIRO_ITERATION_TIMEOUT  per-invocation timeout (default 15m)

Between a clean-exit-mid-task detection and the next --resume attempt,
the wrapper sleeps GT_KIRO_RETRY_BACKOFF (default 1.5s) so heartbeat
state can flush and retry loops don't go tight. Set "0" to disable.

Exit conditions:
  - kiro-cli exits non-zero                       -> propagate exit status
  - kiro-cli exits 0 + heartbeat "exiting"/"idle" -> success (polecat done)
  - per-iteration timeout                         -> kill + retry if budget
                                                     remains, else give up
  - total timeout OR max iterations reached       -> exit 0 (best effort;
                                                     deacon/witness will
                                                     handle the stuck
                                                     polecat)

Environment:
  GT_SESSION                 session name for heartbeat lookup (required
                             for recovery; without it every iteration is
                             treated as a potential gu-ronb case)
  GT_TOWN_ROOT               town root (auto-discovered if unset)
  GT_KIRO_MAX_ITERATIONS     iteration count cap (default 5)
  GT_KIRO_ITERATION_TIMEOUT  per-invocation timeout as Go duration
                             (default 15m; e.g., "10m", "30s")
  GT_KIRO_TOTAL_TIMEOUT      total wallclock budget as Go duration
                             (default 30m)
  GT_KIRO_RETRY_BACKOFF      sleep between clean-exit-mid-task and the
                             next --resume as Go duration (default 1.5s;
                             "0" to disable)

Usage (from gastown agent preset):
  gt polecat-kiro-wrapper -- kiro-cli chat --no-interactive \
      --trust-all-tools --agent gastown`

func init() {
	rootCmd.AddCommand(polecatKiroWrapperCmd)
}

// wrapperConfig carries the resolved knobs for a single wrapper invocation,
// so the main loop doesn't re-read env vars on every iteration and tests
// can construct explicit configs without touching process env.
type wrapperConfig struct {
	maxIterations    int
	iterationTimeout time.Duration
	totalTimeout     time.Duration
	retryBackoff     time.Duration
	sessionName      string
	townRoot         string
}

// loadWrapperConfig resolves all env-var overrides with safe fallbacks.
// Invalid values (unparseable, non-positive for durations, non-positive for
// maxIterations) silently fall back to defaults — the wrapper must not
// refuse to run just because an operator typo'd an env var.
//
// Note: retryBackoff=0 is a VALID explicit disable, distinct from "unset"
// (which yields the default). That's why parseDurationEnv accepts zero for
// backoff but not for the timeouts.
func loadWrapperConfig() wrapperConfig {
	cfg := wrapperConfig{
		maxIterations:    defaultMaxKiroIterations,
		iterationTimeout: defaultIterationTimeout,
		totalTimeout:     defaultTotalTimeout,
		retryBackoff:     defaultRetryBackoff,
		sessionName:      os.Getenv("GT_SESSION"),
		townRoot:         os.Getenv("GT_TOWN_ROOT"),
	}
	if cfg.townRoot == "" {
		if tr, _, _ := workspace.FindFromCwdWithFallback(); tr != "" {
			cfg.townRoot = tr
		}
	}
	if s := os.Getenv("GT_KIRO_MAX_ITERATIONS"); s != "" {
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
			cfg.maxIterations = n
		}
	}
	if d, ok := parseDurationEnv("GT_KIRO_ITERATION_TIMEOUT", false); ok {
		cfg.iterationTimeout = d
	}
	if d, ok := parseDurationEnv("GT_KIRO_TOTAL_TIMEOUT", false); ok {
		cfg.totalTimeout = d
	}
	if d, ok := parseDurationEnv("GT_KIRO_RETRY_BACKOFF", true); ok {
		cfg.retryBackoff = d
	}
	return cfg
}

// parseDurationEnv reads a Go-style duration from an env var. Returns
// (value, ok) where ok=false means "fall back to the caller's default"
// (var unset, unparseable, or failed allowZero check). allowZero=true
// lets "0" through as a valid explicit zero.
func parseDurationEnv(name string, allowZero bool) (time.Duration, bool) {
	s := os.Getenv(name)
	if s == "" {
		return 0, false
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, false
	}
	if d < 0 {
		return 0, false
	}
	if d == 0 && !allowZero {
		return 0, false
	}
	return d, true
}

func runPolecatKiroWrapper(_ *cobra.Command, args []string) error {
	// Strip an optional leading "--" separator — cobra with DisableFlagParsing
	// can still receive it, but exec.Command mustn't.
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return fmt.Errorf("no kiro-cli command provided; usage: gt polecat-kiro-wrapper -- kiro-cli chat ...")
	}

	cfg := loadWrapperConfig()
	start := time.Now()

	// Base args for iteration 1 — pass through exactly as received.
	// On iteration 2+, append "--resume" (resumes most recent conversation
	// in cwd) and a continuation prompt (becomes the INPUT positional arg).
	for iter := 1; iter <= cfg.maxIterations; iter++ {
		// Wallclock guard at the top of the iteration: if the total
		// budget is already exhausted we don't even start a new kiro-cli
		// invocation. This is checked BEFORE we compute the per-iteration
		// deadline so we don't submit a zero-duration context.
		elapsed := time.Since(start)
		if elapsed >= cfg.totalTimeout {
			fmt.Fprintf(os.Stderr,
				"polecat-kiro-wrapper: total timeout (%s) reached before iter %d — giving up; witness will clean up\n",
				cfg.totalTimeout, iter)
			return nil
		}

		iterArgs := args
		if iter > 1 {
			iterArgs = buildResumeArgs(args, cfg.townRoot, cfg.sessionName)
			fmt.Fprintf(os.Stderr,
				"polecat-kiro-wrapper: iteration %d — resuming kiro-cli with continuation prompt (gu-ronb recovery)\n",
				iter)
		}

		// Per-iteration timeout. Budget is the MIN of the configured
		// per-iteration cap and the remaining total budget, so a late
		// iteration doesn't exceed the wallclock just because it started
		// with time to spare. If kiro-cli needs longer than the cap, it
		// gets killed by the context and we treat that as a clean-exit-
		// mid-task signal (worth retrying if budget remains).
		iterDeadline := cfg.iterationTimeout
		if remaining := cfg.totalTimeout - elapsed; remaining < iterDeadline {
			iterDeadline = remaining
		}
		ctx, cancel := context.WithTimeout(context.Background(), iterDeadline)

		c := exec.CommandContext(ctx, iterArgs[0], iterArgs[1:]...) //nolint:gosec // G204: args come from the agent preset config, same trust boundary as a direct kiro-cli invocation.
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		c.Env = os.Environ()

		runErr := c.Run()
		// Cancel the context as soon as the process returns. Deferring
		// would leak N contexts across the loop; explicit cancel keeps
		// resource use bounded.
		cancel()

		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)

		if runErr != nil {
			var exitErr *exec.ExitError
			if errors.As(runErr, &exitErr) {
				exitCode := exitErr.ExitCode()
				if timedOut {
					// CommandContext killed the process — not an agent-
					// reported error. Treat as a retry-able clean-exit-
					// mid-task candidate: the agent was making progress
					// (or hung) when we pulled the plug.
					fmt.Fprintf(os.Stderr,
						"polecat-kiro-wrapper: kiro-cli exceeded per-iteration timeout (%s) on iter %d/%d — killed\n",
						iterDeadline, iter, cfg.maxIterations)
				} else if exitCode != 0 {
					// Real non-zero exit (panic, signal other than our
					// kill, etc.) — propagate. Resume-and-retry is only
					// for the clean-exit-mid-task bug.
					os.Exit(exitCode) //nolint:revive // deep-exit is appropriate here; we must propagate the child's exit status.
				}
			} else {
				// Spawn/IO failure — not a normal exit. Propagate.
				return fmt.Errorf("launching kiro-cli (iter %d): %w", iter, runErr)
			}
		}

		// Clean exit (or timeout kill). Give any in-flight gt done a
		// moment to flush the heartbeat state before we read it.
		time.Sleep(heartbeatSettleDelay)

		if !timedOut && isPolecatDone(cfg.townRoot, cfg.sessionName) {
			return nil
		}

		// Not done — either a clean-exit-mid-task (gu-ronb) or a timeout
		// kill. Announce the retry intent and back off before the next
		// iteration, unless this was the final iteration.
		if !timedOut {
			fmt.Fprintf(os.Stderr,
				"polecat-kiro-wrapper: kiro-cli exited clean but polecat not done (iter %d/%d)\n",
				iter, cfg.maxIterations)
		}
		if iter < cfg.maxIterations && cfg.retryBackoff > 0 {
			// Cap the backoff at the remaining budget so we don't sleep
			// past the wallclock deadline just to wake up, check, and
			// give up.
			sleep := cfg.retryBackoff
			if remaining := cfg.totalTimeout - time.Since(start); remaining < sleep {
				if remaining > 0 {
					sleep = remaining
				} else {
					sleep = 0
				}
			}
			if sleep > 0 {
				time.Sleep(sleep)
			}
		}
	}

	fmt.Fprintf(os.Stderr,
		"polecat-kiro-wrapper: max iterations (%d) reached without completion — giving up; witness will clean up\n",
		cfg.maxIterations)
	// Return success so the tmux session closes cleanly; deacon/witness
	// will detect the polecat is stalled and handle recovery (nuke or
	// escalate). Returning non-zero would just add noise.
	return nil
}

// buildResumeArgs constructs the iter-2+ argv with "--resume" and the
// continuation prompt appended. If a hooked bead ID is reachable (via the
// session heartbeat written by earlier gt commands), it's inlined into
// the prompt so the resumed session knows exactly which work to finish.
// Falls back to the generic prompt when no bead can be determined.
func buildResumeArgs(baseArgs []string, townRoot, sessionName string) []string {
	prompt := buildContinuePrompt(townRoot, sessionName)
	out := make([]string, 0, len(baseArgs)+2)
	out = append(out, baseArgs...)
	out = append(out, "--resume", prompt)
	return out
}

// buildContinuePrompt returns the resume prompt to send to kiro-cli. When
// the session heartbeat carries a bead ID (heartbeat v2, gt-3vr5), it's
// appended so the resumed session has an unambiguous pointer to the work
// it's expected to finish. This matters because kiro-cli's resume behavior
// recovers conversation context but a lightweight reminder avoids "what
// was I doing?" drift.
func buildContinuePrompt(townRoot, sessionName string) string {
	beadID := hookedBeadID(townRoot, sessionName)
	if beadID == "" {
		return kiroContinuePromptBase
	}
	// Note: the prompt must not contain newlines/shell-metachars for safe
	// arg passing through exec.Command. A space-joined single line is
	// kept below and tested by TestContinuePromptNoNewlines.
	return kiroContinuePromptBase + " The bead on your hook is " + beadID +
		" — finish that specific bead and run `gt done` (or `gt done --status DEFERRED` if there is truly nothing to submit)."
}

// hookedBeadID returns the bead ID the polecat session is currently
// working on, as recorded by v2 heartbeats. Returns "" when unknown:
// pre-v2 heartbeat, no heartbeat file, missing session/town-root, etc.
// The wrapper must not block on this — a missing bead ID just means the
// generic prompt is used.
func hookedBeadID(townRoot, sessionName string) string {
	if townRoot == "" || sessionName == "" {
		return ""
	}
	hb := polecat.ReadSessionHeartbeat(townRoot, sessionName)
	if hb == nil {
		return ""
	}
	return hb.Bead
}

// isPolecatDone reports whether the polecat session has signaled completion
// via heartbeat state. Returns false on any ambiguity (no session name, no
// heartbeat file, unreadable state) so the wrapper errs toward recovery.
func isPolecatDone(townRoot, sessionName string) bool {
	if sessionName == "" || townRoot == "" {
		return false
	}
	hb := polecat.ReadSessionHeartbeat(townRoot, sessionName)
	if hb == nil {
		return false
	}
	state := hb.EffectiveState()
	return state == polecat.HeartbeatExiting || state == polecat.HeartbeatIdle
}

// (compile-time assertion: base prompt avoids newlines/shell metachars)
var _ = strings.TrimSpace(kiroContinuePromptBase)
