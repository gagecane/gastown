// auto-test-pr CLI surface.
//
// Phase 0 task 2d shipped the conventions-template emit verbs.
// Phase 0 task 2a (gu-xpsnt, this file) wires the full
// `gt auto-test-pr enable` and `gt auto-test-pr disable` flow on top
// of the per-rig settings JSON loader (task 1) and the town-state
// pinned bead (task 8).
//
// Both verbs operate on TWO surfaces atomically:
//
//  1. The per-rig settings JSON (durable record of intent;
//     authoritative ground truth).
//  2. The town-state pinned bead's `enabled_rigs[]` slice
//     (denormalized read-cache used by `gt auto-test-pr status`).
//
// `enable` writes the flag THEN CAS-appends `target_rig` to
// `enabled_rigs[]`; `disable` writes the flag false THEN CAS-removes
// from `enabled_rigs[]`. If the second step fails after the first
// commits, the CLI exits non-zero with a clear "settings-JSON updated
// but town bead out-of-sync" notice — the Mayor's reconcile cycle
// (Phase 0 task 4) heals on its next iteration.
//
// v1 allow-lists per Q1/Q4 (synthesis):
//   - language: "go" only; other values produce a static error
//     pointing at the v2 follow-up bead (gu-t8cp).
//   - rig: "gastown_upstream" only in v1; other values produce a
//     static error.
//
// The remaining verbs (`pause`, `resume`, `status`, `show`,
// `history`, `revise`) ship in Phase 0 tasks 2b-c and Phase 1.
//
// Design context: .designs/auto-test-pr/synthesis.md §"Implementation
// Plan, Phase 0 task 2a".
package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/autotestpr"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/workspace"
)

// autoTestPRPilotRig is the v1 allow-list of one. Other rigs are
// rejected by `enable` with a static error pointing at gu-t8cp until
// that follow-up bead expands the allow-list. Kept as a slice so
// tests can vary it without mutating a const.
var autoTestPRPilotRigs = []string{"gastown_upstream"}

// autoTestPRV2FollowUpBead is the human-readable pointer surfaced in
// "not allowed in v1" error messages so operators have a clear next
// step (read the bead → understand the constraint → wait for v2 or
// argue for a different scoping).
const autoTestPRV2FollowUpBead = "gu-t8cp"

var (
	// CLI flag bindings for `enable`. Captured as package-level vars
	// so the existing `--emit-template` plumbing can keep its
	// stub-mode behavior without us threading state through cobra
	// args. The flag bindings are reset between tests in the cobra
	// SetArgs path; no global state survives between commands.
	autoTestPREmitTemplate    bool
	autoTestPREnableRig       string
	autoTestPREnableLanguage  string
	autoTestPRDisableRig      string
)

var autoTestPRCmd = &cobra.Command{
	Use:     "auto-test-pr",
	GroupID: GroupServices,
	Short:   "Manage auto-test-pr (automated test-improvement PRs per rig)",
	Long: `Manage the auto-test-pr feature — automated, human-reviewed
test-improvement PRs per rig.

Auto-test-pr is opt-in per rig (default OFF). When enabled, the Mayor
dispatches a polecat on a cadence to land small, reviewable, test-only
MRs that improve coverage on recently-churned code.

In v1 the enable/disable verbs accept the pilot rig (gastown_upstream)
and the Go language only. Other values are rejected with a pointer to
the v2 follow-up bead.

Common flows:

  ` + autoTestPREnableExample + `

  ` + autoTestPRDisableExample + `

  ` + autoTestPRShowTemplateExample + `

The full pause/resume/status/show/history/revise surface ships in
subsequent Phase 0 tasks. See .designs/auto-test-pr/synthesis.md for
the v1 plan.`,
	RunE: requireSubcommand,
}

const (
	autoTestPREnableExample       = "gt auto-test-pr enable --rig=gastown_upstream --language=go"
	autoTestPRDisableExample      = "gt auto-test-pr disable --rig=gastown_upstream"
	autoTestPRShowTemplateExample = "gt auto-test-pr show-template"
	autoTestPREmitTemplateExample = "gt auto-test-pr enable --emit-template > .gt/auto-test-pr/conventions.md"
)

// autoTestPREnableCmd is the operator-facing on switch. It validates
// the (rig, language) pair against the v1 allow-list, writes
// `auto_test_pr.enabled=true` to the rig's settings JSON, then
// CAS-appends the rig to the town-state bead's `enabled_rigs[]`.
var autoTestPREnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Enable auto-test-pr for a rig",
	Long: `Enable auto-test-pr for a rig.

This verb operates on two surfaces atomically:

  1. Writes auto_test_pr.enabled=true (plus language and any other
     fields) to the rig's settings JSON. The settings JSON is the
     authoritative ground truth.
  2. CAS-appends the rig name to the town-state bead's enabled_rigs[]
     slice — the denormalized read-cache used by
     ` + "`gt auto-test-pr status`" + `.

If step 2 fails after step 1 commits, the CLI exits non-zero with a
"settings-JSON updated but town bead out-of-sync" notice. The Mayor's
reconcile cycle (Phase 0 task 4) heals enabled_rigs[] on its next
iteration.

In v1 the rig must be the pilot (gastown_upstream) and the language
must be go. Other values are rejected with a pointer to the v2
follow-up bead.

The --emit-template flag is preserved from Phase 0 task 2d for
operators who want to redirect the embedded conventions-sheet template
into an in-repo file:

  ` + autoTestPREmitTemplateExample,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runAutoTestPREnable,
}

// autoTestPRShowTemplateCmd is the read-only sibling of
// `enable --emit-template`. It prints the embedded conventions
// template to stdout without writing anywhere.
var autoTestPRShowTemplateCmd = &cobra.Command{
	Use:   "show-template",
	Short: "Print the embedded auto-test-pr conventions-sheet template",
	Long: `Print the embedded auto-test-pr conventions-sheet template to stdout
(read-only; nothing is written).

This is the canonical source of truth for the constraints the auto-test-pr
polecat reads at the start of every cycle. To persist a rig-local copy,
use:

  ` + autoTestPREmitTemplateExample + `

Or, if your rig already has a copy and you want to diff:

  gt auto-test-pr show-template | diff - .gt/auto-test-pr/conventions.md`,
	Args:          cobra.NoArgs,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runAutoTestPRShowTemplate,
}

// autoTestPRDisableCmd is the operator-facing off switch. It does NOT
// cancel in-flight work (per design D2a) — the cycle's first step
// re-reads `auto_test_pr.enabled` and exits on the next tick.
var autoTestPRDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Disable auto-test-pr for a rig",
	Long: `Disable auto-test-pr for a rig.

This verb operates on two surfaces atomically:

  1. Writes auto_test_pr.enabled=false to the rig's settings JSON.
  2. CAS-removes the rig from the town-state bead's enabled_rigs[]
     slice.

If step 2 fails after step 1 commits, the CLI exits non-zero with a
"settings-JSON updated but town bead out-of-sync" notice. The Mayor's
reconcile cycle heals enabled_rigs[] on its next iteration.

Disable does NOT cancel in-flight work (design D2a). Any cycle
already in flight (state ∈ {picking, dispatched, mr-pending,
mr-revising}) completes its lifecycle normally; the next-cycle
read of auto_test_pr.enabled returns false and no further cycles
fire.`,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runAutoTestPRDisable,
}

func init() {
	autoTestPREnableCmd.Flags().BoolVar(&autoTestPREmitTemplate, "emit-template", false,
		"Print the embedded conventions-sheet template to stdout and exit (Phase 0 task 2d)")
	autoTestPREnableCmd.Flags().StringVar(&autoTestPREnableRig, "rig", "",
		"Rig to enable (v1: gastown_upstream only)")
	autoTestPREnableCmd.Flags().StringVar(&autoTestPREnableLanguage, "language", "",
		"Language allow-list (v1: go only)")

	autoTestPRDisableCmd.Flags().StringVar(&autoTestPRDisableRig, "rig", "",
		"Rig to disable")

	autoTestPRCmd.AddCommand(autoTestPREnableCmd)
	autoTestPRCmd.AddCommand(autoTestPRDisableCmd)
	autoTestPRCmd.AddCommand(autoTestPRShowTemplateCmd)
	rootCmd.AddCommand(autoTestPRCmd)
}

func runAutoTestPREnable(cmd *cobra.Command, args []string) error {
	// --emit-template is the legacy stub path from task 2d; it ships
	// the embedded template to stdout and exits cleanly without
	// touching any settings JSON or beads. Keep this branch first so
	// operators who only want the template don't need to satisfy
	// --rig / --language requirements.
	if autoTestPREmitTemplate {
		return writeConventionsTemplate(cmd.OutOrStdout())
	}

	// Validate flags before any disk or bead I/O so we fail fast with
	// a clear error when the operator forgets a required flag.
	if autoTestPREnableRig == "" {
		fmt.Fprintln(cmd.ErrOrStderr(),
			"gt auto-test-pr enable: --rig is required")
		fmt.Fprintln(cmd.ErrOrStderr(),
			"  example: "+autoTestPREnableExample)
		return NewSilentExit(2)
	}
	if autoTestPREnableLanguage == "" {
		fmt.Fprintln(cmd.ErrOrStderr(),
			"gt auto-test-pr enable: --language is required")
		fmt.Fprintln(cmd.ErrOrStderr(),
			"  example: "+autoTestPREnableExample)
		return NewSilentExit(2)
	}

	// Allow-list checks. Both checks return a pointer to the v2
	// follow-up bead so the operator has somewhere to go next rather
	// than just a flat "no". Order: language first because that's
	// the more frequently-tweaked argument; rig second because it's
	// the hard pilot constraint.
	if !isAutoTestPRSupportedLanguage(autoTestPREnableLanguage) {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"gt auto-test-pr enable: language %q is not in the v1 allow-list (%v)\n",
			autoTestPREnableLanguage, config.AutoTestPRSupportedLanguages)
		fmt.Fprintf(cmd.ErrOrStderr(),
			"  v2 follow-up: %s\n", autoTestPRV2FollowUpBead)
		return NewSilentExit(2)
	}
	if !isAutoTestPRPilotRig(autoTestPREnableRig) {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"gt auto-test-pr enable: rig %q is not in the v1 pilot allow-list (%v)\n",
			autoTestPREnableRig, autoTestPRPilotRigs)
		fmt.Fprintf(cmd.ErrOrStderr(),
			"  v2 follow-up: %s\n", autoTestPRV2FollowUpBead)
		return NewSilentExit(2)
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("locating town root: %w", err)
	}
	rigPath := filepath.Join(townRoot, autoTestPREnableRig)
	if _, err := os.Stat(rigPath); err != nil {
		return fmt.Errorf("rig directory not found at %s: %w", rigPath, err)
	}

	// Step 1: write settings JSON (authoritative ground truth).
	if err := writeAutoTestPREnabled(rigPath, autoTestPREnableLanguage, true); err != nil {
		return fmt.Errorf("writing rig settings: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"✓ auto_test_pr.enabled=true written to %s\n",
		filepath.Join(rigPath, "settings", "config.json"))

	// Step 2: CAS-append to town state. Surface a clear partial-write
	// notice if it fails — settings JSON is durable, so the Mayor
	// reconcile cycle will heal enabled_rigs[] on the next tick.
	bd := beads.NewWithBeadsDir(townRoot, filepath.Join(townRoot, ".beads"))
	if err := autotestpr.AppendEnabledRig(bd, autoTestPREnableRig); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: settings-JSON updated but town bead out-of-sync: %v\n", err)
		fmt.Fprintln(cmd.ErrOrStderr(),
			"  the Mayor reconcile cycle will heal enabled_rigs[] on the next tick.")
		// Distinguish "bead missing" from "transient retry exhausted"
		// so operators know whether to provision the bead or wait.
		if errors.Is(err, autotestpr.ErrTownStateNotProvisioned) {
			fmt.Fprintln(cmd.ErrOrStderr(),
				"  hint: run `gt install` (or the auto-test-pr provisioner) to create the town-state bead.")
		}
		return NewSilentExit(3)
	}

	fmt.Fprintf(cmd.OutOrStdout(),
		"✓ %s appended to town-state enabled_rigs[]\n", autoTestPREnableRig)
	return nil
}

func runAutoTestPRDisable(cmd *cobra.Command, args []string) error {
	if autoTestPRDisableRig == "" {
		fmt.Fprintln(cmd.ErrOrStderr(),
			"gt auto-test-pr disable: --rig is required")
		fmt.Fprintln(cmd.ErrOrStderr(),
			"  example: "+autoTestPRDisableExample)
		return NewSilentExit(2)
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("locating town root: %w", err)
	}
	rigPath := filepath.Join(townRoot, autoTestPRDisableRig)
	if _, err := os.Stat(rigPath); err != nil {
		return fmt.Errorf("rig directory not found at %s: %w", rigPath, err)
	}

	// Step 1: flip the durable flag. We pass an empty language so we
	// don't override an operator's previously-set language on the way
	// back to disabled — disable is a status flip, not a wipe.
	if err := writeAutoTestPREnabled(rigPath, "", false); err != nil {
		return fmt.Errorf("writing rig settings: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"✓ auto_test_pr.enabled=false written to %s\n",
		filepath.Join(rigPath, "settings", "config.json"))
	fmt.Fprintln(cmd.OutOrStdout(),
		"  note: in-flight cycles complete normally (D2a); next-tick read disables further dispatch.")

	// Step 2: CAS-remove. Same partial-write story as enable.
	bd := beads.NewWithBeadsDir(townRoot, filepath.Join(townRoot, ".beads"))
	if err := autotestpr.RemoveEnabledRig(bd, autoTestPRDisableRig); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: settings-JSON updated but town bead out-of-sync: %v\n", err)
		fmt.Fprintln(cmd.ErrOrStderr(),
			"  the Mayor reconcile cycle will heal enabled_rigs[] on the next tick.")
		if errors.Is(err, autotestpr.ErrTownStateNotProvisioned) {
			fmt.Fprintln(cmd.ErrOrStderr(),
				"  hint: run `gt install` (or the auto-test-pr provisioner) to create the town-state bead.")
		}
		return NewSilentExit(3)
	}

	fmt.Fprintf(cmd.OutOrStdout(),
		"✓ %s removed from town-state enabled_rigs[]\n", autoTestPRDisableRig)
	return nil
}

func runAutoTestPRShowTemplate(cmd *cobra.Command, args []string) error {
	return writeConventionsTemplate(cmd.OutOrStdout())
}

// writeConventionsTemplate writes the embedded template verbatim to w.
// We split this out so the two CLI verbs share one code path — the
// snapshot test in internal/autotestpr asserts the template content; the
// CLI tests assert that both verbs go through the same writer.
func writeConventionsTemplate(w interface{ Write(p []byte) (int, error) }) error {
	tmpl := autotestpr.ConventionsTemplate()
	if _, err := w.Write([]byte(tmpl)); err != nil {
		return fmt.Errorf("write conventions template: %w", err)
	}
	return nil
}

// Compile-time guard: os.Stdout must satisfy the writer interface used
// by writeConventionsTemplate. cobra's OutOrStdout() returns io.Writer,
// which already satisfies this; this declaration just documents intent
// and protects against accidental refactors.
var _ interface{ Write(p []byte) (int, error) } = os.Stdout

// isAutoTestPRSupportedLanguage reports whether the given language is
// in the v1 allow-list. Mirrors validateAutoTestPRConfig in
// internal/config/loader.go but intentionally lives here too so the
// CLI can produce a v1-specific error message before any disk I/O.
func isAutoTestPRSupportedLanguage(lang string) bool {
	for _, allowed := range config.AutoTestPRSupportedLanguages {
		if lang == allowed {
			return true
		}
	}
	return false
}

// isAutoTestPRPilotRig reports whether the given rig name is in the
// v1 pilot allow-list. Kept as a small function rather than inlined
// so tests can document the allow-list expectation directly.
func isAutoTestPRPilotRig(rig string) bool {
	for _, allowed := range autoTestPRPilotRigs {
		if rig == allowed {
			return true
		}
	}
	return false
}

// writeAutoTestPREnabled is the per-rig settings JSON writer. Loads
// the existing settings (or starts from defaults if missing), sets
// the AutoTestPR block, and saves. We deliberately preserve every
// other field — namepool, merge_queue, runtime, etc. — so an enable
// or disable doesn't act as a settings reset.
//
// The language parameter is intentionally optional: enable() passes
// the validated language; disable() passes an empty string so the
// previously-configured language survives a disable→re-enable round
// trip.
func writeAutoTestPREnabled(rigPath, language string, enabled bool) error {
	settingsPath := filepath.Join(rigPath, "settings", "config.json")
	settings, err := config.LoadRigSettings(settingsPath)
	if err != nil {
		// LoadRigSettings wraps os.IsNotExist with ErrNotFound; treat
		// that as "first time enabling, start from defaults". Any
		// other error (parse error, invalid existing block, etc.)
		// must surface so we don't silently drop operator-authored
		// fields by overwriting a corrupt settings file.
		if !errors.Is(err, config.ErrNotFound) {
			return fmt.Errorf("loading existing settings: %w", err)
		}
		settings = config.NewRigSettings()
	}

	if settings.AutoTestPR == nil {
		settings.AutoTestPR = &config.AutoTestPRConfig{}
	}
	settings.AutoTestPR.Enabled = enabled
	if language != "" {
		settings.AutoTestPR.Language = language
	}

	if err := config.SaveRigSettings(settingsPath, settings); err != nil {
		return fmt.Errorf("saving settings to %s: %w", settingsPath, err)
	}
	return nil
}
