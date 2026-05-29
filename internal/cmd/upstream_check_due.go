// gt upstream check-due — adaptive-cooldown verdict for one or all rigs.
//
// Phase 3 (gu-nw6g). The deacon patrol runs `gt upstream check-due
// --all` once per cycle. For each enabled rig the verb consults the
// pinned state bead, applies the adaptive-cooldown policy from
// internal/upstreamsync/cooldown.go, and prints a verdict (due /
// skipped) plus a short reason. With --invoke, due rigs are handed
// off to `gt upstream sync` immediately; without it, the verb is a
// pure status read suitable for dashboards or dry-runs.
//
// This is the integration seam between the upstream-sync state machine
// and the deacon. Keeping the deacon-side code thin (one CLI call) is
// deliberate: the deacon shells out to gt commands rather than
// importing internal/upstreamsync directly so its formula step stays
// language-neutral and testable from the shell.
//
// Design context: .designs/cv-2s6tq/api.md §"Deacon patrol integration"
// and .designs/cv-2s6tq/scale.md §"Adaptive cooldown".
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/upstreamsync"
	"github.com/steveyegge/gastown/internal/workspace"
)

// CLI flag bindings — kept package-level to mirror the rest of the
// upstream verb family.
var (
	upstreamCheckDueRig    string
	upstreamCheckDueAll    bool
	upstreamCheckDueJSON   bool
	upstreamCheckDueInvoke bool
)

// upstreamCheckDueNowFn is the time source used by check-due. Tests
// override it to pin the cooldown evaluator's "now". Production code
// reads time.Now() lazily through this var so the check-due verb
// participates in the same clock-injection pattern the rest of the
// upstream package uses (upstreamNowFn).
var upstreamCheckDueNowFn = func() time.Time { return time.Now() }

// CheckDueDecision is the JSON shape printed by `gt upstream check-due
// --json`. One entry per rig surveyed. The shape is stable: deacon
// patrol scripts and dashboards will parse this.
type CheckDueDecision struct {
	Rig                 string `json:"rig"`
	Enabled             bool   `json:"enabled"`
	Provisioned         bool   `json:"provisioned"`
	Due                 bool   `json:"due"`
	State               string `json:"state,omitempty"`
	SkipReason          string `json:"skip_reason,omitempty"`
	EffectiveCadenceSec int    `json:"effective_cadence_seconds"`
	NextDueAt           string `json:"next_due_at,omitempty"`
	LastSyncAt          string `json:"last_sync_at,omitempty"`
	Invoked             bool   `json:"invoked,omitempty"`
	InvokeError         string `json:"invoke_error,omitempty"`
}

var upstreamCheckDueCmd = &cobra.Command{
	Use:   "check-due",
	Short: "Report which rigs are due for an upstream sync (adaptive cooldown)",
	Long: `Report which rigs are eligible for an upstream-sync attempt right now,
according to the per-rig state bead and the adaptive cooldown policy.

This is the integration seam used by the deacon patrol. With --all the
verb iterates every rig in the town; without --all it inspects a single
rig (--rig=<name>, defaulting to the current worktree's rig). With
--invoke the verb hands off due rigs to ` + "`gt upstream sync`" + ` in sequence.

Examples:

  gt upstream check-due                       # current rig only
  gt upstream check-due --all --json          # full survey, JSON
  gt upstream check-due --all --invoke        # patrol mode: sync due rigs
  gt upstream check-due --rig=gastown_upstream

Exit codes:

  0 — survey completed (some rigs may be skipped, that's not an error)
  1 — internal error (cwd not in a town, etc.)
  2 — invalid flag combination
  3 — at least one --invoke run failed`,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runUpstreamCheckDue,
}

func init() {
	upstreamCheckDueCmd.Flags().StringVar(&upstreamCheckDueRig, "rig", "",
		"Survey a single rig (defaults to current worktree's rig)")
	upstreamCheckDueCmd.Flags().BoolVar(&upstreamCheckDueAll, "all", false,
		"Survey every rig in the town (mutually exclusive with --rig)")
	upstreamCheckDueCmd.Flags().BoolVar(&upstreamCheckDueJSON, "json", false,
		"Machine-parseable JSON output (one object per rig)")
	upstreamCheckDueCmd.Flags().BoolVar(&upstreamCheckDueInvoke, "invoke", false,
		"For each due rig, run `gt upstream sync --rig=<name>` after reporting")

	upstreamCmd.AddCommand(upstreamCheckDueCmd)
}

func runUpstreamCheckDue(cmd *cobra.Command, args []string) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	if upstreamCheckDueAll && upstreamCheckDueRig != "" {
		fmt.Fprintln(stderr, "gt upstream check-due: --all and --rig are mutually exclusive")
		return NewSilentExit(2)
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("locating town root: %w", err)
	}

	rigs, err := collectCheckDueTargets(townRoot)
	if err != nil {
		return err
	}

	if len(rigs) == 0 {
		fmt.Fprintln(stderr, "gt upstream check-due: no rigs to survey")
		return NewSilentExit(2)
	}

	bd := beads.NewWithBeadsDir(townRoot, filepath.Join(townRoot, ".beads"))
	now := upstreamCheckDueNowFn()
	policy := upstreamsync.DefaultCooldownPolicy()

	decisions := make([]CheckDueDecision, 0, len(rigs))
	for _, rigName := range rigs {
		dec := evaluateRigCheckDue(townRoot, rigName, bd, policy, now)
		decisions = append(decisions, dec)
	}

	// Optional --invoke: run gt upstream sync for due rigs in order.
	// We run sequentially because `gt upstream sync` may take minutes
	// (gates), and parallel runs would saturate the host. The deacon
	// patrol expects sync to complete before the next patrol step.
	invokeFailed := false
	if upstreamCheckDueInvoke {
		for i := range decisions {
			if !decisions[i].Due {
				continue
			}
			if err := invokeUpstreamSync(decisions[i].Rig, cmd); err != nil {
				decisions[i].InvokeError = err.Error()
				invokeFailed = true
			} else {
				decisions[i].Invoked = true
			}
		}
	}

	if upstreamCheckDueJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(decisions); err != nil {
			return fmt.Errorf("encoding JSON: %w", err)
		}
	} else {
		printCheckDueTable(stdout, decisions)
	}

	if invokeFailed {
		return NewSilentExit(3)
	}
	return nil
}

// collectCheckDueTargets resolves the list of rig names to survey
// based on flags. Returns an error only on fatal issues; an empty list
// is returned (with nil error) when the operator's filters yielded no
// rigs (caller will warn and exit 2).
func collectCheckDueTargets(townRoot string) ([]string, error) {
	if upstreamCheckDueAll {
		all, err := discoverRigsForTownRoot(townRoot)
		if err != nil {
			return nil, fmt.Errorf("discovering rigs: %w", err)
		}
		names := make([]string, 0, len(all))
		for _, r := range all {
			names = append(names, r.Name)
		}
		sort.Strings(names)
		return names, nil
	}

	rigName := upstreamCheckDueRig
	if rigName == "" {
		rigName = resolveCurrentRig(townRoot)
	}
	if rigName == "" {
		return nil, fmt.Errorf("could not determine rig (use --rig=<name> or --all)")
	}
	return []string{rigName}, nil
}

// evaluateRigCheckDue produces a single CheckDueDecision for one rig.
// All errors are converted into "skip with reason" — the survey verb
// must remain robust across rigs (one bad rig should not abort the
// patrol).
func evaluateRigCheckDue(
	townRoot, rigName string,
	bd *beads.Beads,
	policy upstreamsync.CooldownPolicy,
	now time.Time,
) CheckDueDecision {
	dec := CheckDueDecision{Rig: rigName}

	rigPath := filepath.Join(townRoot, rigName)
	if _, err := os.Stat(rigPath); err != nil {
		dec.SkipReason = "rig:not-found"
		return dec
	}

	settingsPath := filepath.Join(rigPath, "settings", "config.json")
	settings, err := config.LoadRigSettings(settingsPath)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		dec.SkipReason = "settings:load-error"
		return dec
	}
	if settings == nil || !settings.UpstreamSync.IsEnabled() {
		dec.Enabled = false
		dec.SkipReason = "disabled"
		return dec
	}
	dec.Enabled = true

	cadence := time.Duration(settings.UpstreamSync.GetCadenceMinutes()) * time.Minute
	if cadence <= 0 {
		cadence = time.Duration(config.DefaultUpstreamSyncCadenceMinutes) * time.Minute
	}

	rigPrefix := resolveRigPrefix(rigName)
	state, err := upstreamsync.LoadSyncState(bd, rigPrefix)
	if err != nil {
		if errors.Is(err, upstreamsync.ErrStateBeadNotProvisioned) {
			dec.Provisioned = false
			dec.SkipReason = "not-provisioned"
			// Surface base cadence so dashboards can show the projected
			// interval after first provision.
			dec.EffectiveCadenceSec = int(cadence.Seconds())
			return dec
		}
		dec.SkipReason = "state:load-error"
		return dec
	}
	dec.Provisioned = true
	dec.State = string(state.State)
	dec.LastSyncAt = state.LastSyncAt

	verdict := upstreamsync.IsDue(state, cadence, policy, now)
	dec.Due = verdict.Due
	dec.SkipReason = verdict.SkipReason
	dec.EffectiveCadenceSec = int(verdict.EffectiveCadence.Seconds())
	if !verdict.NextDueAt.IsZero() {
		dec.NextDueAt = verdict.NextDueAt.UTC().Format(time.RFC3339)
	}
	return dec
}

// invokeUpstreamSync runs `gt upstream sync --rig=<name>` as a child
// process. Inheriting our own argv[0] keeps test harnesses honest —
// `go test` builds the binary fresh and we want the same binary
// running the child sync.
func invokeUpstreamSync(rigName string, cmd *cobra.Command) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving executable: %w", err)
	}
	c := exec.Command(exe, "upstream", "sync", "--rig="+rigName) //nolint:gosec
	c.Stdout = cmd.OutOrStdout()
	c.Stderr = cmd.ErrOrStderr()
	if err := c.Run(); err != nil {
		return fmt.Errorf("gt upstream sync --rig=%s: %w", rigName, err)
	}
	return nil
}

// printCheckDueTable renders a human-friendly aligned table.
func printCheckDueTable(w io.Writer, decisions []CheckDueDecision) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RIG\tDUE\tSTATE\tCADENCE\tNEXT-DUE\tNOTE")
	for _, d := range decisions {
		due := "-"
		if d.Due {
			due = "yes"
		} else if d.Provisioned || d.Enabled {
			due = "no"
		}
		state := d.State
		if state == "" {
			if !d.Enabled {
				state = "disabled"
			} else if !d.Provisioned {
				state = "unprovisioned"
			} else {
				state = "?"
			}
		}
		cadence := "-"
		if d.EffectiveCadenceSec > 0 {
			cadence = (time.Duration(d.EffectiveCadenceSec) * time.Second).String()
		}
		nextDue := d.NextDueAt
		if nextDue == "" {
			nextDue = "-"
		}
		note := d.SkipReason
		if d.Invoked {
			note = "invoked: " + note
			note = strings.TrimSuffix(note, ": ")
		}
		if d.InvokeError != "" {
			note = "invoke-error: " + d.InvokeError
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", d.Rig, due, state, cadence, nextDue, note)
	}
	_ = tw.Flush()
}
