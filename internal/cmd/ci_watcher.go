// Package cmd: ci_watcher.go implements the `gt ci-watcher` command tree
// for the post-merge CI watcher (gu-xuzc).
//
// `gt ci-watcher poll`     — one-shot poll-and-react. Suitable invocation
//
//	target for cron / deacon patrol. Inspects recent
//	completed CI runs on the rig's target branch and
//	applies the reopen-and-freeze / clear-freeze policy.
//
// `gt ci-watcher status`   — print the current freeze state for a rig (or
//
//	the inferred rig when run from inside a rig directory).
//
// `gt ci-watcher unfreeze` — manually clear the freeze flag with a reason.
//
//	Used when an operator decides the post-merge CI
//	failure is benign (flake, infra) and the queue
//	should resume without waiting for a passing run.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/ciwatcher"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/workspace"
)

// ci-watcher command flags.
var (
	ciWatcherRig            string
	ciWatcherTargetBranch   string
	ciWatcherRunLimit       int
	ciWatcherJSON           bool
	ciWatcherUnfreezeReason string
)

var ciWatcherCmd = &cobra.Command{
	Use:     "ci-watcher",
	GroupID: GroupServices,
	Short:   "Post-merge CI watcher (reopens beads, mails mayor, freezes the merge queue on broken main)",
	Long: `Post-merge CI watcher.

The ci-watcher is the last line of defense against bad commits landing on
main. When pre-push gates miss something — infrastructure flakes, transitive
dependency rot, or a polecat skip that slipped past the audit — the watcher
catches it after the fact and:

  1. Reopens the bead that landed the commit (when attributable) with the
     'broke-main-ci' label.
  2. Mails the mayor at HIGH priority with a link to the failed CI run.
  3. Writes a freeze flag at <townRoot>/.runtime/mq-frozen-<rig> that the
     refinery checks before processing the next merge request.

A subsequent passing run on main clears the freeze automatically and notifies
the mayor that main is healthy again. The freeze can also be cleared manually
via 'gt ci-watcher unfreeze --reason "<why>"'.

This command does not run as a daemon; invoke 'poll' from cron or a deacon
patrol on the desired cadence (every 2-5 minutes is typical).`,
	RunE: requireSubcommand,
}

var ciWatcherPollCmd = &cobra.Command{
	Use:   "poll",
	Short: "Run one watch cycle: fetch recent runs, apply freeze/clear policy",
	Long: `Fetch recent completed CI runs on the target branch and apply the
freeze policy:

  - Failed run → reopen bead, mail mayor, write freeze flag.
  - Passing run after a freeze → clear freeze, mail mayor.

The watcher is idempotent across polls: once a run has been processed it is
recorded in <townRoot>/.runtime/ci-watcher-seen-<rig> and skipped on
subsequent invocations. Safe to invoke from cron at any cadence.`,
	RunE: runCIWatcherPoll,
}

var ciWatcherStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the current freeze state for a rig",
	Long: `Print whether the merge queue is currently frozen for a rig and,
if so, why. Use --json for machine-readable output.`,
	RunE: runCIWatcherStatus,
}

var ciWatcherUnfreezeCmd = &cobra.Command{
	Use:   "unfreeze",
	Short: "Manually clear the merge-queue freeze flag",
	Long: `Manually clear the freeze flag set by the watcher. Use when an
operator has confirmed the post-merge CI failure is benign (flake, infra,
already fixed forward) and the queue should resume without waiting for the
next passing run.

A reason is required and is written to the structured events log so the
operator action is auditable.`,
	RunE: runCIWatcherUnfreeze,
}

func init() {
	// Shared flags.
	for _, c := range []*cobra.Command{ciWatcherPollCmd, ciWatcherStatusCmd, ciWatcherUnfreezeCmd} {
		c.Flags().StringVar(&ciWatcherRig, "rig", "", "Rig name (defaults to inferring from cwd)")
	}
	ciWatcherPollCmd.Flags().StringVar(&ciWatcherTargetBranch, "branch", "", "Target branch (defaults to rig's default branch or 'main')")
	ciWatcherPollCmd.Flags().IntVar(&ciWatcherRunLimit, "limit", ciwatcher.DefaultRunLimit, "Maximum number of recent runs to inspect per poll")
	ciWatcherPollCmd.Flags().BoolVar(&ciWatcherJSON, "json", false, "Emit per-poll summary as JSON")
	ciWatcherStatusCmd.Flags().BoolVar(&ciWatcherJSON, "json", false, "Emit status as JSON")
	ciWatcherUnfreezeCmd.Flags().StringVar(&ciWatcherUnfreezeReason, "reason", "", "Required: reason for manual unfreeze")
	_ = ciWatcherUnfreezeCmd.MarkFlagRequired("reason")

	ciWatcherCmd.AddCommand(ciWatcherPollCmd)
	ciWatcherCmd.AddCommand(ciWatcherStatusCmd)
	ciWatcherCmd.AddCommand(ciWatcherUnfreezeCmd)
	rootCmd.AddCommand(ciWatcherCmd)
}

// resolveRigContext figures out the (townRoot, rigName, rigDir) that the
// command should operate on. --rig overrides cwd inference; when neither is
// supplied we fall back to inferRigFromCwd.
func resolveRigContext() (townRoot, rigName, rigDir string, err error) {
	townRoot, err = workspace.FindFromCwdOrError()
	if err != nil {
		return "", "", "", fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	rigName = ciWatcherRig
	if rigName == "" {
		rigName, err = inferRigFromCwd(townRoot)
		if err != nil {
			return "", "", "", fmt.Errorf("could not infer rig (use --rig <name>): %w", err)
		}
	}
	rigDir = filepath.Join(townRoot, rigName)
	if _, statErr := os.Stat(rigDir); statErr != nil {
		return "", "", "", fmt.Errorf("rig %q not found at %s", rigName, rigDir)
	}
	return townRoot, rigName, rigDir, nil
}

// resolveTargetBranch picks the watcher's target branch. Precedence:
//  1. --branch flag
//  2. rig's default branch (LoadRigConfig.DefaultBranch)
//  3. literal "main"
func resolveTargetBranch(rigDir string) string {
	if ciWatcherTargetBranch != "" {
		return ciWatcherTargetBranch
	}
	if cfg, err := rig.LoadRigConfig(rigDir); err == nil && cfg.DefaultBranch != "" {
		return cfg.DefaultBranch
	}
	return "main"
}

func runCIWatcherPoll(cmd *cobra.Command, args []string) error {
	townRoot, rigName, rigDir, err := resolveRigContext()
	if err != nil {
		return err
	}
	branch := resolveTargetBranch(rigDir)

	// Production deps: gh-CLI fetcher, real beads + mail adapters.
	// We anchor the fetcher and the mail adapter at the rig's repo clone so
	// they pick up the right git remote and mail-router context.
	repoDir := filepath.Join(rigDir, "refinery", "rig")
	if _, statErr := os.Stat(repoDir); statErr != nil {
		// Fallback to mayor/rig (legacy layout) — same fallback the engineer uses.
		repoDir = filepath.Join(rigDir, "mayor", "rig")
	}

	fetcher := ciwatcher.NewGHRunFetcher(repoDir)
	beadsAdapter := ciwatcher.NewBeadsAdapter(rigDir)
	mailAdapter := ciwatcher.NewMailAdapter(rigDir)

	cfg := ciwatcher.Config{
		TownRoot:     townRoot,
		Rig:          rigName,
		TargetBranch: branch,
		RunLimit:     ciWatcherRunLimit,
	}
	w := ciwatcher.NewWatcher(cfg, fetcher, beadsAdapter, mailAdapter, nil, cmd.OutOrStdout())

	// Cap the poll at a generous wall-clock budget so a hung gh-CLI doesn't
	// stall a cron invocation forever. Two minutes is well above the typical
	// gh-CLI latency and matches the deacon's per-step budget.
	ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
	defer cancel()

	res, err := w.Process(ctx)
	if err != nil {
		return fmt.Errorf("ciwatcher poll: %w", err)
	}

	if ciWatcherJSON {
		out := struct {
			Rig                 string `json:"rig"`
			Branch              string `json:"branch"`
			RunsConsidered      int    `json:"runs_considered"`
			RunsProcessed       int    `json:"runs_processed"`
			FailuresHandled     int    `json:"failures_handled"`
			FreezeWritten       bool   `json:"freeze_written"`
			FreezeCleared       bool   `json:"freeze_cleared"`
			ColdStartSuppressed int    `json:"cold_start_suppressed"`
		}{rigName, branch, res.RunsConsidered, res.RunsProcessed, res.FailuresHandled, res.FreezeWritten, res.FreezeCleared, res.ColdStartSuppressed}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(),
		"ciwatcher: rig=%s branch=%s considered=%d processed=%d failures=%d freeze_written=%v freeze_cleared=%v cold_start_suppressed=%d\n",
		rigName, branch, res.RunsConsidered, res.RunsProcessed, res.FailuresHandled, res.FreezeWritten, res.FreezeCleared, res.ColdStartSuppressed,
	)
	return nil
}

func runCIWatcherStatus(cmd *cobra.Command, args []string) error {
	townRoot, rigName, _, err := resolveRigContext()
	if err != nil {
		return err
	}

	frozen, err := ciwatcher.IsFrozen(townRoot, rigName)
	if err != nil {
		return fmt.Errorf("checking freeze: %w", err)
	}
	ff, _ := ciwatcher.ReadFreeze(townRoot, rigName)

	if ciWatcherJSON {
		payload := map[string]any{
			"rig":    rigName,
			"frozen": frozen,
			"path":   ciwatcher.FreezePath(townRoot, rigName),
		}
		if ff != nil {
			payload["freeze"] = ff
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(payload)
	}

	if !frozen {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "rig=%s: NOT frozen\n", rigName)
		return nil
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "rig=%s: FROZEN\n", rigName)
	if ff != nil {
		if ff.Reason != "" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  reason:  %s\n", ff.Reason)
		}
		if ff.BeadID != "" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  bead:    %s\n", ff.BeadID)
		}
		if ff.RunID != "" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  run:     %s\n", ff.RunID)
		}
		if ff.RunURL != "" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  run-url: %s\n", ff.RunURL)
		}
		if !ff.FrozenAt.IsZero() {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  since:   %s\n", ff.FrozenAt.Format(time.RFC3339))
		}
	}
	return nil
}

func runCIWatcherUnfreeze(cmd *cobra.Command, args []string) error {
	townRoot, rigName, _, err := resolveRigContext()
	if err != nil {
		return err
	}

	frozen, err := ciwatcher.IsFrozen(townRoot, rigName)
	if err != nil {
		return fmt.Errorf("checking freeze: %w", err)
	}
	if !frozen {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "rig=%s: not frozen — nothing to do\n", rigName)
		return nil
	}

	prior, _ := ciwatcher.ReadFreeze(townRoot, rigName)
	if err := ciwatcher.ClearFreeze(townRoot, rigName); err != nil {
		return fmt.Errorf("clearing freeze: %w", err)
	}

	payload := map[string]any{
		"rig":    rigName,
		"reason": ciWatcherUnfreezeReason,
		"manual": true,
	}
	if prior != nil {
		if prior.BeadID != "" {
			payload["prior_bead_id"] = prior.BeadID
		}
		if prior.RunID != "" {
			payload["prior_run_id"] = prior.RunID
		}
		if prior.Reason != "" {
			payload["prior_reason"] = prior.Reason
		}
	}
	_ = events.LogAudit("mq_frozen_cleared", "ci-watcher/manual", payload)

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "rig=%s: freeze cleared (reason=%q)\n", rigName, ciWatcherUnfreezeReason)
	return nil
}
