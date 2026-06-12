// gt upstream — upstream sync management CLI surface.
//
// Phase 1 (gu-xdc6) shipped `gt upstream status` as the read-only
// inspection verb. Phase 2 (gu-4mj2) added the full mutating verb set:
// sync, pause, resume, history, config — split across upstream_pause.go,
// upstream_history.go, and upstream_sync.go to keep each file focused.
//
// Design context: .designs/cv-2s6tq/api.md §"Command Group: gt upstream"
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/upstreamsync"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	upstreamRig  string
	upstreamJSON bool
)

var upstreamCmd = &cobra.Command{
	Use:     "upstream",
	Aliases: []string{"us"},
	GroupID: GroupServices,
	Short:   "Manage upstream sync (fork ← upstream merge automation)",
	Long: `Manage the upstream-sync feature — automated merge of upstream/main
into the fork's origin/main with CI gating and agent-based conflict resolution.

Upstream sync is opt-in per rig (default OFF). When enabled, the Deacon
patrol periodically checks the upstream remote for new commits and
triggers a sync cycle: fetch → merge → gate → push.

Common flows:

  gt upstream status                 # Check sync health
  gt upstream sync                   # Trigger an immediate cycle
  gt upstream pause --reason "…"     # Halt automatic syncs
  gt upstream resume                 # Re-enable automatic syncs
  gt upstream history --limit=10     # Inspect attempt history
  gt upstream config --set key=val   # Tune the rig's config`,
	RunE: requireSubcommand,
}

var upstreamStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show upstream sync health for a rig",
	Long: `Show the current upstream sync status for a rig.

Displays: current state, last sync time, commits behind, pause status,
and configuration. Uses the per-rig pinned state bead as the data source.

When no --rig is specified, defaults to the current worktree's rig
(detected from cwd).

Examples:

  gt upstream status
  gt upstream status --rig=gastown_upstream
  gt upstream status --json`,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runUpstreamStatus,
}

func init() {
	upstreamStatusCmd.Flags().StringVar(&upstreamRig, "rig", "",
		"Target rig (defaults to current worktree's rig)")
	upstreamStatusCmd.Flags().BoolVar(&upstreamJSON, "json", false,
		"Machine-parseable JSON output")

	upstreamCmd.AddCommand(upstreamStatusCmd)
	rootCmd.AddCommand(upstreamCmd)
}

func runUpstreamStatus(cmd *cobra.Command, args []string) error {
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("locating town root: %w", err)
	}

	// Resolve rig name.
	rigName := upstreamRig
	if rigName == "" {
		rigName = resolveCurrentRig(townRoot)
		if rigName == "" {
			fmt.Fprintln(stderr, "gt upstream status: could not determine current rig")
			fmt.Fprintln(stderr, "  hint: use --rig=<name> or cd into a rig worktree")
			return NewSilentExit(2)
		}
	}

	// Check rig exists.
	rigPath := filepath.Join(townRoot, rigName)
	if _, err := os.Stat(rigPath); err != nil {
		return fmt.Errorf("rig directory not found at %s: %w", rigPath, err)
	}

	// Load rig settings to check if upstream sync is configured.
	settingsPath := filepath.Join(rigPath, "settings", "config.json")
	settings, err := config.LoadRigSettings(settingsPath)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return fmt.Errorf("loading rig settings: %w", err)
	}

	syncCfg := settings.UpstreamSync
	if !syncCfg.IsEnabled() {
		if upstreamJSON {
			summary := upstreamsync.StatusSummary{
				Rig:     rigName,
				State:   "disabled",
				Enabled: false,
			}
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(summary)
		}
		fmt.Fprintf(stdout, "Upstream Sync: %s\n", rigName)
		fmt.Fprintln(stdout, "  Status: disabled")
		fmt.Fprintln(stdout, "  hint: enable with settings/config.json upstream_sync.enabled=true")
		return nil
	}

	// Try to load the state bead.
	rigPrefix := resolveRigPrefix(rigName)
	bd := beads.NewWithBeadsDir(townRoot, filepath.Join(townRoot, ".beads"))
	state, err := upstreamsync.LoadSyncState(bd, rigPrefix)
	if err != nil {
		if errors.Is(err, upstreamsync.ErrStateBeadNotProvisioned) {
			// Not provisioned yet — show config but no state.
			if upstreamJSON {
				summary := upstreamsync.StatusSummary{
					Rig:            rigName,
					State:          "not-provisioned",
					Enabled:        true,
					UpstreamRemote: syncCfg.GetUpstreamRemote(),
					UpstreamBranch: syncCfg.GetUpstreamBranch(),
					TargetBranch:   syncCfg.GetTargetBranch(),
				}
				enc := json.NewEncoder(stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(summary)
			}
			fmt.Fprintf(stdout, "Upstream Sync: %s\n", rigName)
			fmt.Fprintln(stdout, "  State:   not-provisioned (enabled but state bead missing)")
			fmt.Fprintf(stdout, "  Remote:  %s\n", syncCfg.GetUpstreamRemote())
			fmt.Fprintf(stdout, "  Branch:  %s → %s\n", syncCfg.GetUpstreamBranch(), syncCfg.GetTargetBranch())
			fmt.Fprintln(stdout, "  hint: run `gt upstream sync` once to provision the state bead")
			return nil
		}
		return fmt.Errorf("loading sync state: %w", err)
	}

	// Compute commits behind (best-effort — uses git, may fail if remote not configured).
	behind := computeCommitsBehind(rigPath, syncCfg)

	// Build summary.
	summary := state.ToStatusSummary()
	summary.Behind = behind
	summary.Enabled = true

	if upstreamJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(summary)
	}

	// Human-readable output.
	printUpstreamStatus(stdout, summary, syncCfg)
	return nil
}

// printUpstreamStatus renders the human-readable status output.
func printUpstreamStatus(w interface{ Write(p []byte) (int, error) }, summary upstreamsync.StatusSummary, cfg *config.UpstreamSyncConfig) {
	fmt.Fprintf(w, "Upstream Sync: %s\n", summary.Rig)
	fmt.Fprintf(w, "  Remote:     %s (%s)\n", summary.UpstreamRemote, summary.UpstreamBranch)
	fmt.Fprintf(w, "  Target:     origin/%s\n", summary.TargetBranch)

	// State line with emoji.
	stateIcon := stateIcon(summary.State)
	if summary.Behind > 0 {
		fmt.Fprintf(w, "  State:      %s %s (%d commits behind)\n", stateIcon, summary.State, summary.Behind)
	} else {
		fmt.Fprintf(w, "  State:      %s %s\n", stateIcon, summary.State)
	}

	// Last sync.
	fmt.Fprintf(w, "  Last sync:  %s\n", upstreamsync.FormatLastSync(summary.LastSyncAt))

	// Pause info.
	if summary.Paused {
		if summary.PauseReason != "" {
			fmt.Fprintf(w, "  Paused:     yes (%s)\n", summary.PauseReason)
		} else {
			fmt.Fprintln(w, "  Paused:     yes")
		}
	}

	// Consecutive failures.
	if summary.ConsecutiveFailures > 0 {
		fmt.Fprintf(w, "  Failures:   %d consecutive\n", summary.ConsecutiveFailures)
	}

	// Config summary.
	if cfg != nil {
		fmt.Fprintf(w, "  Cadence:    every %s\n", formatCadence(cfg.GetCadenceMinutes()))
		fmt.Fprintf(w, "  Strategy:   %s\n", cfg.GetStrategy())
		fmt.Fprintf(w, "  Conflicts:  %s\n", cfg.GetConflictResolution())
	}
}

// stateIcon returns an emoji/symbol for the sync state.
func stateIcon(state string) string {
	switch state {
	case "idle":
		return "✓"
	case "synced":
		return "✓"
	case "checking", "syncing", "gating", "pushing":
		return "⟳"
	case "resolving":
		return "⚡"
	case "failed":
		return "✗"
	case "paused":
		return "⏸"
	default:
		return "?"
	}
}

// formatCadence converts minutes to a human-readable duration string.
func formatCadence(minutes int) string {
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	hours := minutes / 60
	remaining := minutes % 60
	if remaining == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dm", hours, remaining)
}

// computeCommitsBehind uses git to count how many commits the fork's
// target branch is behind the upstream branch. Returns 0 on any error
// (best-effort; the status command still works without git access).
func computeCommitsBehind(rigPath string, cfg *config.UpstreamSyncConfig) int {
	if cfg == nil {
		return 0
	}

	// Find a git repo to run commands against.
	// Try the rig's refinery clone first, then the rig root itself.
	gitDir := filepath.Join(rigPath, "refinery", "rig")
	if _, err := os.Stat(filepath.Join(gitDir, ".git")); err != nil {
		gitDir = rigPath
		if _, err := os.Stat(filepath.Join(gitDir, ".git")); err != nil {
			return 0
		}
	}

	upstream := cfg.GetUpstreamRemote() + "/" + cfg.GetUpstreamBranch()
	target := "origin/" + cfg.GetTargetBranch()

	// git rev-list --count <target>..<upstream>
	out, err := exec.Command("git", "-C", gitDir, "rev-list", "--count",
		target+".."+upstream).Output()
	if err != nil {
		return 0
	}

	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return count
}

// resolveCurrentRig attempts to determine the rig name from the current
// working directory. Returns empty string if it cannot be determined.
func resolveCurrentRig(townRoot string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	// Check if cwd is under a rig directory.
	rel, err := filepath.Rel(townRoot, cwd)
	if err != nil {
		return ""
	}

	// The first path component under town root is typically the rig name.
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 || parts[0] == "." || parts[0] == "" {
		return ""
	}

	// Validate it's actually a rig (has settings dir or .beads).
	candidate := parts[0]
	rigPath := filepath.Join(townRoot, candidate)
	if _, err := os.Stat(filepath.Join(rigPath, "settings")); err == nil {
		return candidate
	}
	if _, err := os.Stat(filepath.Join(rigPath, ".beads")); err == nil {
		return candidate
	}

	return ""
}

// resolveRigPrefix extracts the standard 2-char prefix for a rig name.
// Convention: "gastown_upstream" → "gu" (first char of each word).
// Falls back to first 2 chars if single word.
func resolveRigPrefix(rigName string) string {
	parts := strings.Split(rigName, "_")
	if len(parts) >= 2 {
		var prefix strings.Builder
		for _, p := range parts {
			if len(p) > 0 {
				prefix.WriteByte(p[0])
			}
		}
		result := prefix.String()
		if len(result) >= 2 {
			return result[:2]
		}
		return result
	}
	if len(rigName) >= 2 {
		return rigName[:2]
	}
	return rigName
}
