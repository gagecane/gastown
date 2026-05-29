// gt upstream history / config — read-mostly inspection verbs for
// upstream sync.
//
// Phase 2 (gu-4mj2). These verbs surface the per-rig state bead
// for human and machine consumers:
//
//   * history — print the bounded attempt log (last N entries)
//   * config  — print the rig's upstream-sync configuration, with
//               --set <key=value> to mutate settings/config.json
//
// Both default to the current rig (inferred from cwd) and accept
// --rig=<name> for cross-rig operation.
//
// Design context: .designs/cv-2s6tq/api.md §"gt upstream history"
// and §"gt upstream config".
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/upstreamsync"
)

// CLI flag bindings — kept as package-level vars to mirror the
// upstream.go pattern.
var (
	upstreamHistoryRig   string
	upstreamHistoryJSON  bool
	upstreamHistoryLimit int

	upstreamConfigRig  string
	upstreamConfigJSON bool
	upstreamConfigSet  []string
)

// historyDefaultLimit is the default --limit for `gt upstream
// history`. Matches the design's example (line 167 of api.md showing
// 5 entries by default; we default to 10 to match auto-test-pr).
const historyDefaultLimit = 10

var upstreamHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "Show upstream sync attempt history",
	Long: `Show the bounded attempt history for a rig — successes,
conflicts, gate failures, and skips, ordered oldest → newest.

The state bead retains the last N attempts (typically 30); --limit
filters that to the most-recent K entries.

Examples:

  gt upstream history
  gt upstream history --rig=gastown_upstream --limit=20
  gt upstream history --json`,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runUpstreamHistory,
}

var upstreamConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Show or update upstream sync configuration",
	Long: `Show the upstream-sync configuration for a rig (read from
settings/config.json under the upstream_sync key), or update individual
keys via --set key=value.

Supported --set keys:

  enabled                   true | false
  upstream_remote           remote name (default: upstream)
  upstream_branch           branch on upstream remote (default: main)
  target_branch             local branch synced into (default: main)
  strategy                  merge | rebase (default: merge)
  cadence_minutes           integer minutes between checks (default: 360)
  max_consecutive_failures  integer (default: 3)
  conflict_resolution       agent | escalate (default: agent)

Examples:

  gt upstream config
  gt upstream config --rig=gastown_upstream --json
  gt upstream config --set cadence_minutes=240
  gt upstream config --set strategy=rebase --set conflict_resolution=escalate`,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runUpstreamConfig,
}

func init() {
	upstreamHistoryCmd.Flags().StringVar(&upstreamHistoryRig, "rig", "",
		"Target rig (defaults to current worktree's rig)")
	upstreamHistoryCmd.Flags().BoolVar(&upstreamHistoryJSON, "json", false,
		"Machine-parseable JSON output")
	upstreamHistoryCmd.Flags().IntVar(&upstreamHistoryLimit, "limit", historyDefaultLimit,
		"Maximum number of attempts to print (most recent first in output order)")

	upstreamConfigCmd.Flags().StringVar(&upstreamConfigRig, "rig", "",
		"Target rig (defaults to current worktree's rig)")
	upstreamConfigCmd.Flags().BoolVar(&upstreamConfigJSON, "json", false,
		"Machine-parseable JSON output")
	upstreamConfigCmd.Flags().StringSliceVar(&upstreamConfigSet, "set", nil,
		"Update a config key: --set key=value (repeatable)")

	upstreamCmd.AddCommand(upstreamHistoryCmd)
	upstreamCmd.AddCommand(upstreamConfigCmd)
}

func runUpstreamHistory(cmd *cobra.Command, args []string) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	if upstreamHistoryLimit <= 0 {
		fmt.Fprintf(stderr, "gt upstream history: --limit must be positive, got %d\n",
			upstreamHistoryLimit)
		return NewSilentExit(2)
	}

	townRoot, rigName, _, settings, err := resolveUpstreamRigContext(cmd, "history", upstreamHistoryRig)
	if err != nil {
		return err
	}

	if settings == nil || !settings.UpstreamSync.IsEnabled() {
		fmt.Fprintf(stderr, "gt upstream history: upstream sync is not enabled for rig %s\n", rigName)
		return NewSilentExit(2)
	}

	rigPrefix := resolveRigPrefix(rigName)
	bd := beads.NewWithBeadsDir(townRoot, filepath.Join(townRoot, ".beads"))

	state, err := upstreamsync.LoadSyncState(bd, rigPrefix)
	if err != nil {
		if errors.Is(err, upstreamsync.ErrStateBeadNotProvisioned) {
			fmt.Fprintf(stderr, "gt upstream history: state bead not provisioned for rig %s\n", rigName)
			return NewSilentExit(3)
		}
		return fmt.Errorf("loading sync state: %w", err)
	}

	matches := state.Attempts
	if len(matches) > upstreamHistoryLimit {
		matches = matches[len(matches)-upstreamHistoryLimit:]
	}

	if upstreamHistoryJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(matches)
	}

	if len(matches) == 0 {
		fmt.Fprintf(stdout, "no history for rig %s\n", rigName)
		return nil
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WHEN\tOUTCOME\tSTRATEGY\tDETAIL")
	for _, a := range matches {
		when := a.CompletedAt
		if when == "" {
			when = a.StartedAt
		}
		when = compactTime(when)
		outcome := a.Outcome
		if outcome == "" {
			outcome = "(in-progress)"
		}
		detail := buildHistoryDetail(a)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", when, outcome, valueOrDashUpstream(a.Strategy), detail)
	}
	return tw.Flush()
}

// buildHistoryDetail produces a one-line summary of an attempt's
// distinguishing fields. Kept compact so the table doesn't wrap.
func buildHistoryDetail(a upstreamsync.SyncAttempt) string {
	switch a.Outcome {
	case "success":
		if a.PostSyncSHA != "" && a.PreSyncSHA != "" {
			return fmt.Sprintf("%s → %s", shortSHA(a.PreSyncSHA), shortSHA(a.PostSyncSHA))
		}
		return shortSHA(a.UpstreamSHA)
	case "conflict":
		if len(a.Conflicts) > 0 {
			return fmt.Sprintf("conflicts: %s", strings.Join(a.Conflicts, ", "))
		}
		return "conflict"
	case "gate-failure":
		var failed []string
		for k, v := range a.GateResults {
			if v == upstreamsync.GateFail {
				failed = append(failed, k)
			}
		}
		if len(failed) > 0 {
			return fmt.Sprintf("gate failed: %s", strings.Join(failed, ", "))
		}
		return "gate failure"
	case "push-failure":
		return "push rejected"
	case "skipped":
		return "no upstream changes"
	}
	if a.Actor != "" {
		return "actor=" + a.Actor
	}
	return "—"
}

// compactTime renders an RFC3339 timestamp as "2026-05-25 18:30 UTC"
// for table display. Falls back to the raw input if parsing fails.
func compactTime(rfc3339 string) string {
	if rfc3339 == "" {
		return "—"
	}
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	return t.UTC().Format("2006-01-02 15:04 MST")
}

// valueOrDashUpstream collapses an empty string to a unicode em-dash.
// Named -Upstream to avoid colliding with valueOrDash in
// auto_test_pr_pause.go (same package).
func valueOrDashUpstream(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// upstreamConfigOutput is the JSON shape printed by
// `gt upstream config --json`. Inlines the pieces of UpstreamSyncConfig
// that operators care about; deliberately distinct from the raw
// settings struct so future schema changes don't break scripts.
type upstreamConfigOutput struct {
	Rig                    string   `json:"rig"`
	Enabled                bool     `json:"enabled"`
	UpstreamRemote         string   `json:"upstream_remote"`
	UpstreamBranch         string   `json:"upstream_branch"`
	TargetBranch           string   `json:"target_branch"`
	Strategy               string   `json:"strategy"`
	CadenceMinutes         int      `json:"cadence_minutes"`
	GateCommands           []string `json:"gate_commands"`
	MaxConsecutiveFailures int      `json:"max_consecutive_failures"`
	ConflictResolution     string   `json:"conflict_resolution"`
}

func runUpstreamConfig(cmd *cobra.Command, args []string) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	townRoot, rigName, rigPath, settings, err := resolveUpstreamRigContext(cmd, "config", upstreamConfigRig)
	if err != nil {
		return err
	}
	_ = townRoot

	settingsPath := filepath.Join(rigPath, "settings", "config.json")
	if settings == nil {
		// Operator may want to enable upstream sync from a fresh rig;
		// allow --set to bootstrap the config file.
		if len(upstreamConfigSet) == 0 {
			fmt.Fprintf(stderr, "gt upstream config: settings/config.json not found for rig %s\n", rigName)
			fmt.Fprintln(stderr, "  hint: use --set to create the config (e.g. --set enabled=true)")
			return NewSilentExit(2)
		}
		settings = &config.RigSettings{}
	}

	// Apply --set updates if any.
	if len(upstreamConfigSet) > 0 {
		if settings.UpstreamSync == nil {
			settings.UpstreamSync = &config.UpstreamSyncConfig{}
		}
		for _, kv := range upstreamConfigSet {
			if err := applyUpstreamConfigSet(settings.UpstreamSync, kv); err != nil {
				fmt.Fprintf(stderr, "gt upstream config: %v\n", err)
				return NewSilentExit(2)
			}
		}
		if err := config.SaveRigSettings(settingsPath, settings); err != nil {
			return fmt.Errorf("saving rig settings: %w", err)
		}
		fmt.Fprintf(stdout, "✓ updated %d setting(s) in %s\n", len(upstreamConfigSet), settingsPath)
	}

	out := buildUpstreamConfigOutput(rigName, settings.UpstreamSync)

	if upstreamConfigJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	printUpstreamConfig(stdout, out)
	return nil
}

func buildUpstreamConfigOutput(rigName string, c *config.UpstreamSyncConfig) upstreamConfigOutput {
	return upstreamConfigOutput{
		Rig:                    rigName,
		Enabled:                c.IsEnabled(),
		UpstreamRemote:         c.GetUpstreamRemote(),
		UpstreamBranch:         c.GetUpstreamBranch(),
		TargetBranch:           c.GetTargetBranch(),
		Strategy:               c.GetStrategy(),
		CadenceMinutes:         c.GetCadenceMinutes(),
		GateCommands:           c.GetGateCommands(),
		MaxConsecutiveFailures: c.GetMaxConsecutiveFailures(),
		ConflictResolution:     c.GetConflictResolution(),
	}
}

func printUpstreamConfig(w io.Writer, out upstreamConfigOutput) {
	fmt.Fprintf(w, "Upstream Sync Config: %s\n", out.Rig)
	fmt.Fprintf(w, "  Enabled:                  %t\n", out.Enabled)
	fmt.Fprintf(w, "  Upstream remote:          %s\n", out.UpstreamRemote)
	fmt.Fprintf(w, "  Upstream branch:          %s\n", out.UpstreamBranch)
	fmt.Fprintf(w, "  Target branch:            %s\n", out.TargetBranch)
	fmt.Fprintf(w, "  Strategy:                 %s\n", out.Strategy)
	fmt.Fprintf(w, "  Cadence:                  %s\n", formatCadence(out.CadenceMinutes))
	fmt.Fprintf(w, "  Max consecutive failures: %d\n", out.MaxConsecutiveFailures)
	fmt.Fprintf(w, "  Conflict resolution:      %s\n", out.ConflictResolution)
	if len(out.GateCommands) > 0 {
		fmt.Fprintln(w, "  Gates:")
		for _, g := range out.GateCommands {
			fmt.Fprintf(w, "    - %s\n", g)
		}
	}
}

// applyUpstreamConfigSet parses a "key=value" string and updates the
// corresponding field on cfg. Returns a descriptive error for unknown
// keys or unparseable values.
func applyUpstreamConfigSet(cfg *config.UpstreamSyncConfig, kv string) error {
	idx := strings.IndexByte(kv, '=')
	if idx < 0 {
		return fmt.Errorf("--set %q: expected key=value", kv)
	}
	key := strings.TrimSpace(kv[:idx])
	val := strings.TrimSpace(kv[idx+1:])
	if key == "" {
		return fmt.Errorf("--set %q: empty key", kv)
	}

	switch key {
	case "enabled":
		b, err := strconv.ParseBool(val)
		if err != nil {
			return fmt.Errorf("--set enabled=%q: not a boolean", val)
		}
		cfg.Enabled = b
	case "upstream_remote":
		cfg.UpstreamRemote = val
	case "upstream_branch":
		cfg.UpstreamBranch = val
	case "target_branch":
		cfg.TargetBranch = val
	case "strategy":
		if val != "merge" && val != "rebase" && val != "fast-forward" {
			return fmt.Errorf("--set strategy=%q: must be one of merge|rebase|fast-forward", val)
		}
		cfg.Strategy = val
	case "cadence_minutes":
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return fmt.Errorf("--set cadence_minutes=%q: must be a positive integer", val)
		}
		cfg.CadenceMinutes = n
	case "max_consecutive_failures":
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return fmt.Errorf("--set max_consecutive_failures=%q: must be a positive integer", val)
		}
		cfg.MaxConsecutiveFailures = n
	case "conflict_resolution":
		if val != "agent" && val != "escalate" {
			return fmt.Errorf("--set conflict_resolution=%q: must be one of agent|escalate", val)
		}
		cfg.ConflictResolution = val
	default:
		return fmt.Errorf("--set %q: unknown config key", key)
	}
	return nil
}
