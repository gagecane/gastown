// Package cmd supervisor wrapper for kiro-cli polecat sessions.
//
// gu-m3ne: kiro-cli 2.2.1 has a clean-exit bug (gu-ronb) where sessions end
// with status 0 mid-task without calling `gt done`. The polecat's bead stays
// HOOKED and the scheduler respawns it, triggering an infinite loop.
//
// This wrapper supervises kiro-cli invocations and recovers from clean-exit
// deaths by re-invoking with --resume and a continuation prompt, up to a
// bounded number of iterations. When the agent legitimately finishes (called
// gt done, heartbeat state "exiting"/"idle"), the wrapper exits success.
package cmd

import (
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

	// heartbeatSettleDelay gives gt done time to write the heartbeat state
	// update before the wrapper reads it after kiro-cli exits.
	heartbeatSettleDelay = 500 * time.Millisecond

	// kiroContinuePrompt is injected on resume iterations. Matches the
	// polecat formula's exit semantics: run gt done when actually finished,
	// gt done --status DEFERRED if truly nothing to do.
	kiroContinuePrompt = "Your previous kiro-cli turn exited before you completed the assigned formula. " +
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
with --resume and a continuation prompt, up to GT_KIRO_MAX_ITERATIONS
(default 5). The session is resumed in the same working directory so the
conversation context and any tool state carry over.

Exit conditions:
  - kiro-cli exits non-zero                       -> propagate exit status
  - kiro-cli exits 0 + heartbeat "exiting"/"idle" -> success (polecat done)
  - max iterations reached                        -> exit 0 (best effort;
                                                    deacon/witness will
                                                    handle the stuck polecat)

Environment:
  GT_SESSION                 session name for heartbeat lookup (required
                             for recovery; without it every iteration is
                             treated as a potential gu-ronb case)
  GT_TOWN_ROOT               town root (auto-discovered if unset)
  GT_KIRO_MAX_ITERATIONS     override recovery cap (default 5)

Usage (from gastown agent preset):
  gt polecat-kiro-wrapper -- kiro-cli chat --no-interactive \
      --trust-all-tools --agent gastown`

func init() {
	rootCmd.AddCommand(polecatKiroWrapperCmd)
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

	maxIter := defaultMaxKiroIterations
	if s := os.Getenv("GT_KIRO_MAX_ITERATIONS"); s != "" {
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
			maxIter = n
		}
	}

	sessionName := os.Getenv("GT_SESSION")
	townRoot := os.Getenv("GT_TOWN_ROOT")
	if townRoot == "" {
		if tr, _, _ := workspace.FindFromCwdWithFallback(); tr != "" {
			townRoot = tr
		}
	}

	// Base args for iteration 1 — pass through exactly as received.
	// On iteration 2+, append "--resume" (resumes most recent conversation
	// in cwd) and kiroContinuePrompt (becomes the INPUT positional arg).
	for iter := 1; iter <= maxIter; iter++ {
		iterArgs := args
		if iter > 1 {
			iterArgs = append(append([]string{}, args...), "--resume", kiroContinuePrompt)
			fmt.Fprintf(os.Stderr, "polecat-kiro-wrapper: iteration %d — resuming kiro-cli with continuation prompt (gu-ronb recovery)\n", iter)
		}

		c := exec.Command(iterArgs[0], iterArgs[1:]...) //nolint:gosec // G204: args come from the agent preset config, same trust boundary as a direct kiro-cli invocation.
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		// Inherit env — GT_POLECAT/GT_RIG/GT_SESSION/etc. flow through.
		c.Env = os.Environ()

		runErr := c.Run()
		exitCode := 0
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if runErr != nil {
			// Spawn/IO failure — not a normal exit. Propagate.
			return fmt.Errorf("launching kiro-cli (iter %d): %w", iter, runErr)
		}

		// Non-zero exit is a real error (panic, signal, etc.) — propagate.
		// Resume-and-retry is only for the clean-exit-mid-task bug.
		if exitCode != 0 {
			os.Exit(exitCode) //nolint:revive // deep-exit is appropriate here; we must propagate the child's exit status.
		}

		// Clean exit. Give the agent's gt done (if any) a moment to flush
		// the heartbeat state to disk before we read it.
		time.Sleep(heartbeatSettleDelay)

		if isPolecatDone(townRoot, sessionName) {
			return nil
		}

		// Clean exit without gt done = gu-ronb signature. Loop to resume.
		fmt.Fprintf(os.Stderr, "polecat-kiro-wrapper: kiro-cli exited clean but polecat not done (iter %d/%d)\n", iter, maxIter)
	}

	fmt.Fprintf(os.Stderr,
		"polecat-kiro-wrapper: max iterations (%d) reached without completion — giving up; witness will clean up\n",
		maxIter)
	// Return success so the tmux session closes cleanly; deacon/witness
	// will detect the polecat is stalled and handle recovery (nuke or
	// escalate). Returning non-zero would just add noise.
	return nil
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

// (compile-time assertion: kiroContinuePrompt avoids newlines/shell metachars)
var _ = strings.TrimSpace(kiroContinuePrompt)
