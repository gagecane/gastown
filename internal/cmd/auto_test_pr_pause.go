// auto-test-pr CLI: pause / resume / status / show / history verbs.
//
// Phase 0 task 2b (gu-uez5w). Together these five verbs are the
// runtime-control surface for the auto-test-pr feature — operators
// pause it (per-rig or town-wide), resume it (with the optional
// `--override-circuit-breaker` D16 escape hatch), inspect the
// town-wide state, drill into a single rig, or page through the
// audit log.
//
// All five verbs operate on the `town-auto-test-pr-state` pinned bead
// (provisioned by Phase 0 task 8). The synthesis (line 1175) accepts
// that "no patrol consumes them yet" in Phase 0 — these writes are
// for audit + read-back only. Phase 1 task 15 wires the per-rig state
// beads into the cycle handler.
//
// Why one file: the five verbs share the same I/O backbone
// (LoadTownState + EnsureTownStateBead + actor resolution) and the
// implementation reads top-to-bottom. Splitting per verb made the
// shared helpers harder to follow during review.
//
// Design context: .designs/auto-test-pr/synthesis.md §"Implementation
// Plan, Phase 0 task 2b" and §"Interface".
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/autotestpr"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/workspace"
)

// nowFn is the indirection point for tests. Production code resolves
// `time.Now()` lazily through this var so unit tests can pin it.
// Reset between tests via the helper at the bottom of
// auto_test_pr_pause_test.go.
var nowFn = func() time.Time { return time.Now() }

// CLI flag bindings. Captured as package-level vars per the existing
// auto_test_pr.go convention. Tests set + restore them around each
// run; we don't thread state through cobra args.
var (
	autoTestPRPauseRig         string
	autoTestPRPauseAll         bool
	autoTestPRPauseDuration    string
	autoTestPRPauseReason      string

	autoTestPRResumeRig         string
	autoTestPRResumeAll         bool
	autoTestPRResumeOverride    bool

	autoTestPRStatusFormat string

	autoTestPRShowRig     string
	autoTestPRShowVerbose bool
	autoTestPRShowRaw     bool

	autoTestPRHistoryRig  string
	autoTestPRHistoryLast int
)

// pauseDurationDefault is the default --duration when an operator
// runs `pause` without one. The synthesis CLI surface (line 312)
// shows `--duration=24h` as the example; we make that the actual
// default so the common case doesn't require the flag at all.
const pauseDurationDefault = 24 * time.Hour

// historyLastDefault is the default --last for `history`. Mirrors the
// synthesis CLI surface (line 316): `history --rig=<rig> [--last=10]`.
// Ten entries is half the MaxIncidents cap — enough to cover a
// typical operator session without overwhelming a terminal.
const historyLastDefault = 10

var autoTestPRPauseCmd = &cobra.Command{
	Use:   "pause",
	Short: "Pause auto-test-pr (per-rig or town-wide)",
	Long: `Pause auto-test-pr cycles for a specific rig (--rig=<rig>) or
town-wide (--all). Pauses are timed: they expire automatically after
--duration (default 24h).

In Phase 0 these writes are recorded on the town-state bead but no
patrol consumes them yet — the verb exists so operators have an
audit-trail surface during the pilot. Phase 1 wires the per-rig
state beads into the Mayor cycle.

Examples:

  gt auto-test-pr pause --rig=gastown_upstream --duration=2h
  gt auto-test-pr pause --all --duration=24h --reason="release window"`,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runAutoTestPRPause,
}

var autoTestPRResumeCmd = &cobra.Command{
	Use:   "resume",
	Short: "Resume auto-test-pr (per-rig or town-wide)",
	Long: `Resume auto-test-pr cycles previously paused by ` + "`gt auto-test-pr pause`" + `.

The --override-circuit-breaker flag bypasses the
` + "`paused-by-circuit-breaker`" + ` state per D16 — it is the only
operator-driven path out of that state (no auto-release). Using it
emits an audit-log entry on the town-state bead naming the operator
and timestamp.

Examples:

  gt auto-test-pr resume --rig=gastown_upstream
  gt auto-test-pr resume --rig=gastown_upstream --override-circuit-breaker
  gt auto-test-pr resume --all`,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runAutoTestPRResume,
}

var autoTestPRStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print town-wide auto-test-pr status",
	Long: `Print the town-wide auto-test-pr status: opted-in rigs,
global pause flag, and circuit-breaker counter.

When the town bead has zero entries (no rigs opted in via
` + "`gt auto-test-pr enable`" + `), status reports
"no rigs opted in".

--format=table (default) is the operator-readable surface; --format=json
emits the StatusJSON shape for scripts. JSON output is stable across
versions per the schema-versioning policy.`,
	Args:          cobra.NoArgs,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runAutoTestPRStatus,
}

var autoTestPRShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show per-rig auto-test-pr state",
	Long: `Show the auto-test-pr state for a single rig: opt-in flag,
operator pause (if any), and the rig's row of the audit log.

In Phase 0 the per-rig state bead does not yet exist — show reads
the rig's slice of the town-state bead (enabled_rigs membership +
RigPauses entry). --raw emits the underlying JSON for debugging.

Examples:

  gt auto-test-pr show --rig=gastown_upstream
  gt auto-test-pr show --rig=gastown_upstream --verbose
  gt auto-test-pr show --rig=gastown_upstream --raw`,
	Args:          cobra.NoArgs,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runAutoTestPRShow,
}

var autoTestPRHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "Print the auto-test-pr audit log for a rig",
	Long: `Print the most-recent auto-test-pr audit-log entries for a rig.

The audit log is bounded to the most-recent ` + fmt.Sprintf("%d", autotestpr.MaxIncidents) + ` entries
town-wide; --last filters that down to the most-recent N entries
that match --rig (or town-wide entries with no rig field). Default
--last=10.

In Phase 0 the log records operator pause/resume events and
circuit-breaker overrides only — Mayor-driven state transitions
live on attachment beads (OQ4 fallback) and are not surfaced here.`,
	Args:          cobra.NoArgs,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runAutoTestPRHistory,
}

func init() {
	autoTestPRPauseCmd.Flags().StringVar(&autoTestPRPauseRig, "rig", "",
		"Rig to pause (mutually exclusive with --all)")
	autoTestPRPauseCmd.Flags().BoolVar(&autoTestPRPauseAll, "all", false,
		"Pause every rig town-wide (mutually exclusive with --rig)")
	autoTestPRPauseCmd.Flags().StringVar(&autoTestPRPauseDuration, "duration", pauseDurationDefault.String(),
		"Pause duration (Go duration format, e.g. 24h, 2h30m). Default 24h.")
	autoTestPRPauseCmd.Flags().StringVar(&autoTestPRPauseReason, "reason", "",
		"Free-form reason recorded on the audit log (optional)")

	autoTestPRResumeCmd.Flags().StringVar(&autoTestPRResumeRig, "rig", "",
		"Rig to resume (mutually exclusive with --all)")
	autoTestPRResumeCmd.Flags().BoolVar(&autoTestPRResumeAll, "all", false,
		"Resume every rig town-wide (mutually exclusive with --rig)")
	autoTestPRResumeCmd.Flags().BoolVar(&autoTestPRResumeOverride, "override-circuit-breaker", false,
		"Reset town-wide circuit-breaker counter (D16 SEV-1 manual recovery path). Emits an audit-log entry.")

	autoTestPRStatusCmd.Flags().StringVar(&autoTestPRStatusFormat, "format", "table",
		"Output format: table | json")

	autoTestPRShowCmd.Flags().StringVar(&autoTestPRShowRig, "rig", "",
		"Rig to inspect (required)")
	autoTestPRShowCmd.Flags().BoolVar(&autoTestPRShowVerbose, "verbose", false,
		"Include the rig's incident-log entries inline")
	autoTestPRShowCmd.Flags().BoolVar(&autoTestPRShowRaw, "raw", false,
		"Print the underlying JSON state instead of the human-readable summary")

	autoTestPRHistoryCmd.Flags().StringVar(&autoTestPRHistoryRig, "rig", "",
		"Rig to filter the audit log by (required)")
	autoTestPRHistoryCmd.Flags().IntVar(&autoTestPRHistoryLast, "last", historyLastDefault,
		"Number of most-recent entries to print (default 10)")

	autoTestPRCmd.AddCommand(autoTestPRPauseCmd)
	autoTestPRCmd.AddCommand(autoTestPRResumeCmd)
	autoTestPRCmd.AddCommand(autoTestPRStatusCmd)
	autoTestPRCmd.AddCommand(autoTestPRShowCmd)
	autoTestPRCmd.AddCommand(autoTestPRHistoryCmd)
}

// resolveOperatorActor resolves the audit-log Actor field. We prefer
// BD_ACTOR (set by every Gas Town agent session) and fall back to
// GT_ROLE for direct human invocation, then to a literal "overseer"
// when neither is set. Mirrors the resolution policy in
// detectSenderFallback (escalate_impl.go) — kept duplicated here so
// the auto-test-pr CLI doesn't depend on the mail subsystem.
func resolveOperatorActor() string {
	if a := os.Getenv("BD_ACTOR"); a != "" {
		return a
	}
	if r := os.Getenv("GT_ROLE"); r != "" {
		return r
	}
	return "overseer"
}

// loadTownStateForCLI is the shared read path used by status / show /
// history. It auto-provisions the bead on first read so an operator
// running `status` against a fresh town doesn't see a confusing
// "bead not provisioned" error — that case is what
// EnsureTownStateBead is for. Mutating verbs (pause/resume) do NOT
// auto-provision: writing to a non-existent bead is a configuration
// problem the operator should see explicitly.
func loadTownStateForCLI(b *beads.Beads) (autotestpr.TownState, error) {
	state, err := autotestpr.LoadTownState(b)
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, autotestpr.ErrTownStateNotProvisioned) {
		return autotestpr.TownState{}, err
	}
	// Auto-provision on read paths only.
	if _, err := autotestpr.EnsureTownStateBead(b); err != nil {
		return autotestpr.TownState{}, fmt.Errorf("provisioning town-state bead: %w", err)
	}
	return autotestpr.LoadTownState(b)
}

// newAutoTestPRBeads returns a Beads wrapper rooted at the town root
// with .beads/ resolved beneath it. The same pattern as the enable /
// disable CLI verbs in auto_test_pr.go — kept as a small helper so
// the five verbs share identical resolution.
func newAutoTestPRBeads() (*beads.Beads, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return nil, fmt.Errorf("locating town root: %w", err)
	}
	return beads.NewWithBeadsDir(townRoot, filepath.Join(townRoot, ".beads")), nil
}

// validateRigOrAll enforces the mutually-exclusive --rig / --all
// flags shared by pause and resume. Both must not be set; at least
// one must be set. Returns the rig name (empty for --all) and a flag
// indicating which mode is in effect.
func validateRigOrAll(stderr io.Writer, verb, rig string, all bool, exampleVerb string) (rigName string, isAll bool, err error) {
	if rig != "" && all {
		fmt.Fprintln(stderr, "gt auto-test-pr "+verb+": --rig and --all are mutually exclusive")
		return "", false, NewSilentExit(2)
	}
	if rig == "" && !all {
		fmt.Fprintln(stderr, "gt auto-test-pr "+verb+": one of --rig=<rig> or --all is required")
		fmt.Fprintln(stderr, "  example: "+exampleVerb)
		return "", false, NewSilentExit(2)
	}
	return rig, all, nil
}

func runAutoTestPRPause(cmd *cobra.Command, args []string) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	rigName, isAll, err := validateRigOrAll(stderr, "pause", autoTestPRPauseRig, autoTestPRPauseAll,
		"gt auto-test-pr pause --rig=gastown_upstream --duration=2h")
	if err != nil {
		return err
	}

	dur, err := time.ParseDuration(autoTestPRPauseDuration)
	if err != nil {
		fmt.Fprintf(stderr, "gt auto-test-pr pause: invalid --duration %q: %v\n",
			autoTestPRPauseDuration, err)
		return NewSilentExit(2)
	}
	if dur <= 0 {
		fmt.Fprintf(stderr, "gt auto-test-pr pause: --duration must be positive, got %s\n", dur)
		return NewSilentExit(2)
	}

	now := nowFn()
	req := autotestpr.PauseRequest{
		Until:  now.Add(dur),
		Reason: autoTestPRPauseReason,
		Actor:  resolveOperatorActor(),
		Now:    now,
	}

	bd, err := newAutoTestPRBeads()
	if err != nil {
		return err
	}

	if isAll {
		if err := autotestpr.SetGlobalPause(bd, req); err != nil {
			return fmt.Errorf("recording town-wide pause: %w", err)
		}
		fmt.Fprintf(stdout, "✓ town-wide pause set: until=%s (duration=%s) by=%s\n",
			req.Until.UTC().Format(time.RFC3339), dur, req.Actor)
		return nil
	}

	if err := autotestpr.SetRigPause(bd, rigName, req); err != nil {
		return fmt.Errorf("recording rig pause: %w", err)
	}
	fmt.Fprintf(stdout, "✓ rig %s paused: until=%s (duration=%s) by=%s\n",
		rigName, req.Until.UTC().Format(time.RFC3339), dur, req.Actor)
	return nil
}

func runAutoTestPRResume(cmd *cobra.Command, args []string) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	rigName, isAll, err := validateRigOrAll(stderr, "resume", autoTestPRResumeRig, autoTestPRResumeAll,
		"gt auto-test-pr resume --rig=gastown_upstream")
	if err != nil {
		return err
	}

	now := nowFn()
	req := autotestpr.ResumeRequest{
		Actor:                  resolveOperatorActor(),
		Now:                    now,
		OverrideCircuitBreaker: autoTestPRResumeOverride,
	}

	bd, err := newAutoTestPRBeads()
	if err != nil {
		return err
	}

	if isAll {
		if err := autotestpr.ClearGlobalPause(bd, req); err != nil {
			return fmt.Errorf("clearing town-wide pause: %w", err)
		}
		fmt.Fprintf(stdout, "✓ town-wide resume by=%s", req.Actor)
		if req.OverrideCircuitBreaker {
			fmt.Fprintf(stdout, " (--override-circuit-breaker)")
		}
		fmt.Fprintln(stdout)
		return nil
	}

	if err := autotestpr.ClearRigPause(bd, rigName, req); err != nil {
		return fmt.Errorf("clearing rig pause: %w", err)
	}
	fmt.Fprintf(stdout, "✓ rig %s resumed by=%s", rigName, req.Actor)
	if req.OverrideCircuitBreaker {
		fmt.Fprintf(stdout, " (--override-circuit-breaker)")
	}
	fmt.Fprintln(stdout)
	return nil
}

func runAutoTestPRStatus(cmd *cobra.Command, args []string) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	switch autoTestPRStatusFormat {
	case "table", "json":
		// ok
	default:
		fmt.Fprintf(stderr, "gt auto-test-pr status: unknown --format %q (want: table | json)\n",
			autoTestPRStatusFormat)
		return NewSilentExit(2)
	}

	bd, err := newAutoTestPRBeads()
	if err != nil {
		return err
	}

	state, err := loadTownStateForCLI(bd)
	if err != nil {
		return fmt.Errorf("reading town-state bead: %w", err)
	}

	if autoTestPRStatusFormat == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(state.ToStatusJSON())
	}

	return printStatusTable(stdout, state)
}

// printStatusTable renders the town-wide status table per the
// synthesis surface (lines 319-326). When no rigs are opted in we
// print the literal "no rigs opted in" line per task 2b's acceptance
// criterion (line 1174).
func printStatusTable(w io.Writer, state autotestpr.TownState) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	if len(state.EnabledRigs) == 0 {
		fmt.Fprintln(tw, "no rigs opted in")
	} else {
		fmt.Fprintln(tw, "RIG\tSTATE\tPAUSE")
		// Sorted-rig order for stable output.
		rigs := append([]string(nil), state.EnabledRigs...)
		sort.Strings(rigs)
		for _, rig := range rigs {
			rigState := "idle"
			pauseCol := "—"
			if pe, ok := state.RigPauses[rig]; ok {
				rigState = "paused"
				pauseCol = pe.PausedUntil
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", rig, rigState, pauseCol)
		}
	}

	// Town-wide footer. Prints a "(town-wide)" row even when no rigs
	// are opted in so operators see the global pause + breaker state.
	if len(state.EnabledRigs) > 0 {
		fmt.Fprintln(tw)
	}
	townState := "running"
	townPause := "—"
	if state.GlobalPauseUntil != "" {
		townState = "paused"
		townPause = state.GlobalPauseUntil
	}
	if state.CircuitBreaker.IsTripped() {
		townState = "paused-by-circuit-breaker"
	}
	fmt.Fprintf(tw, "(town-wide)\t%s\t%s\n", townState, townPause)
	fmt.Fprintf(tw, "circuit_breaker\tcount=%d\ttripped_until=%s\n",
		state.CircuitBreaker.Count, valueOrDash(state.CircuitBreaker.TrippedUntil))
	return tw.Flush()
}

// valueOrDash collapses an empty string to a unicode em-dash. Keeps
// the table output readable when most fields are empty (the common
// case in Phase 0).
func valueOrDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func runAutoTestPRShow(cmd *cobra.Command, args []string) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	if autoTestPRShowRig == "" {
		fmt.Fprintln(stderr, "gt auto-test-pr show: --rig is required")
		return NewSilentExit(2)
	}

	bd, err := newAutoTestPRBeads()
	if err != nil {
		return err
	}

	state, err := loadTownStateForCLI(bd)
	if err != nil {
		return fmt.Errorf("reading town-state bead: %w", err)
	}

	if autoTestPRShowRaw {
		// Project the rig's slice of state into a small JSON object.
		// We don't dump the whole TownState because that's the
		// `status --format=json` surface; show --raw is the per-rig
		// debugging path.
		rigSlice := struct {
			Rig       string                  `json:"rig"`
			Enabled   bool                    `json:"enabled"`
			Pause     *autotestpr.RigPauseEntry `json:"pause,omitempty"`
			Incidents []autotestpr.Incident   `json:"incidents,omitempty"`
		}{
			Rig:       autoTestPRShowRig,
			Enabled:   rigEnabled(state, autoTestPRShowRig),
			Pause:     rigPausePtr(state, autoTestPRShowRig),
			Incidents: incidentsForRig(state, autoTestPRShowRig, len(state.Incidents)),
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rigSlice)
	}

	return printShowSummary(stdout, state, autoTestPRShowRig, autoTestPRShowVerbose)
}

func printShowSummary(w io.Writer, state autotestpr.TownState, rig string, verbose bool) error {
	enabled := rigEnabled(state, rig)
	fmt.Fprintf(w, "rig:     %s\n", rig)
	fmt.Fprintf(w, "enabled: %t\n", enabled)

	if pe, ok := state.RigPauses[rig]; ok {
		fmt.Fprintf(w, "pause:   until=%s by=%s", pe.PausedUntil, pe.PausedBy)
		if pe.Reason != "" {
			fmt.Fprintf(w, " reason=%q", pe.Reason)
		}
		fmt.Fprintln(w)
	} else {
		fmt.Fprintln(w, "pause:   —")
	}

	if state.CircuitBreaker.IsTripped() {
		fmt.Fprintf(w, "circuit-breaker: tripped_until=%s count=%d\n",
			state.CircuitBreaker.TrippedUntil, state.CircuitBreaker.Count)
	}

	if verbose {
		matches := incidentsForRig(state, rig, len(state.Incidents))
		if len(matches) == 0 {
			fmt.Fprintln(w, "incidents: (none)")
			return nil
		}
		fmt.Fprintln(w, "incidents:")
		for _, inc := range matches {
			printIncident(w, inc, "  ")
		}
	}
	return nil
}

func runAutoTestPRHistory(cmd *cobra.Command, args []string) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	if autoTestPRHistoryRig == "" {
		fmt.Fprintln(stderr, "gt auto-test-pr history: --rig is required")
		return NewSilentExit(2)
	}
	if autoTestPRHistoryLast <= 0 {
		fmt.Fprintf(stderr, "gt auto-test-pr history: --last must be positive, got %d\n",
			autoTestPRHistoryLast)
		return NewSilentExit(2)
	}

	bd, err := newAutoTestPRBeads()
	if err != nil {
		return err
	}

	state, err := loadTownStateForCLI(bd)
	if err != nil {
		return fmt.Errorf("reading town-state bead: %w", err)
	}

	matches := incidentsForRig(state, autoTestPRHistoryRig, autoTestPRHistoryLast)
	if len(matches) == 0 {
		fmt.Fprintf(stdout, "no history for rig %s\n", autoTestPRHistoryRig)
		return nil
	}
	for _, inc := range matches {
		printIncident(stdout, inc, "")
	}
	return nil
}

// rigEnabled reports whether rigName appears in EnabledRigs[]. Linear
// scan since the slice is bounded by the number of opted-in rigs (1
// in v1; ≤10 even in the optimistic Phase-2 expansion).
func rigEnabled(state autotestpr.TownState, rigName string) bool {
	for _, r := range state.EnabledRigs {
		if r == rigName {
			return true
		}
	}
	return false
}

// rigPausePtr returns a pointer to the rig's pause entry, or nil if
// the rig is not paused. Used by show --raw to keep the JSON shape
// small (omitempty + pointer).
func rigPausePtr(state autotestpr.TownState, rigName string) *autotestpr.RigPauseEntry {
	pe, ok := state.RigPauses[rigName]
	if !ok {
		return nil
	}
	return &pe
}

// incidentsForRig filters the audit log to entries that mention the
// rig (Incident.Rig == rigName) plus town-wide incidents (empty Rig)
// because those still affect the rig. Returns the most-recent N
// matches in chronological order (oldest → newest).
//
// limit is a soft cap: we still walk the full slice so the most-
// recent matches survive the trim.
func incidentsForRig(state autotestpr.TownState, rigName string, limit int) []autotestpr.Incident {
	matches := make([]autotestpr.Incident, 0, len(state.Incidents))
	for _, inc := range state.Incidents {
		if inc.Rig == "" || inc.Rig == rigName {
			matches = append(matches, inc)
		}
	}
	if limit > 0 && len(matches) > limit {
		matches = matches[len(matches)-limit:]
	}
	return matches
}

// printIncident formats one Incident. We keep it terse (single line)
// so the most-recent-N print stays scannable in a terminal. Times are
// kept as RFC3339 — the operator audience reads them in JSON tooling
// like jq more often than in a human-eyes scan.
func printIncident(w io.Writer, inc autotestpr.Incident, indent string) {
	rigPart := ""
	if inc.Rig != "" {
		rigPart = " rig=" + inc.Rig
	}
	detailsPart := ""
	if inc.Details != "" {
		detailsPart = " " + inc.Details
	}
	fmt.Fprintf(w, "%s%s [%s] actor=%s%s%s\n", indent, inc.At, inc.Kind, inc.Actor, rigPart, detailsPart)
}
