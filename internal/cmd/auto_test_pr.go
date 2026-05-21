// auto-test-pr CLI surface (Phase 0 task 2d, plus the --emit-template
// hook on the future task 2a `enable` verb). v1 ships only the
// template-emit verbs; `enable`, `disable`, `pause`, `resume`,
// `status`, `show`, `history`, and `revise` arrive in tasks 2a-c and
// 4-6.
//
// Design context: .designs/auto-test-pr/synthesis.md (§Implementation
// Plan, Phase 0 task 2d).
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/autotestpr"
)

var autoTestPREmitTemplate bool

var autoTestPRCmd = &cobra.Command{
	Use:     "auto-test-pr",
	GroupID: GroupServices,
	Short:   "Manage auto-test-pr (automated test-improvement PRs per rig)",
	Long: `Manage the auto-test-pr feature — automated, human-reviewed
test-improvement PRs per rig.

Auto-test-pr is opt-in per rig (default OFF). When enabled, the Mayor
dispatches a polecat on a cadence to land small, reviewable, test-only
MRs that improve coverage on recently-churned code.

In v1 this command tree exposes only the conventions-template verbs:

  ` + autoTestPREnableExample + `

  ` + autoTestPRShowTemplateExample + `

The full enable/disable/pause/resume/status/show/history surface is
shipped in subsequent Phase 0 tasks. See .designs/auto-test-pr/
synthesis.md for the v1 plan.`,
	RunE: requireSubcommand,
}

const (
	autoTestPREnableExample       = "gt auto-test-pr enable --emit-template > .gt/auto-test-pr/conventions.md"
	autoTestPRShowTemplateExample = "gt auto-test-pr show-template"
)

// autoTestPREnableCmd is the v1-stub `enable` verb. Task 2a fleshes out
// the per-rig settings JSON sync. Task 2d (this CR) wires only the
// `--emit-template` flag, which prints the embedded conventions template
// to stdout so a rig owner can redirect it into
// .gt/auto-test-pr/conventions.md before opting in.
var autoTestPREnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Enable auto-test-pr for a rig (v1: --emit-template only)",
	Long: `Enable auto-test-pr for a rig.

In Phase 0 task 2d only the --emit-template hook is wired. Running
` + "`gt auto-test-pr enable --emit-template`" + ` prints the embedded
conventions-sheet template to stdout so the rig owner can persist it:

  gt auto-test-pr enable --emit-template > .gt/auto-test-pr/conventions.md

Without --emit-template, this command exits with an error pointing at
the v2 follow-up bead. The full enable surface (settings-JSON write +
` + "`enabled_rigs[]`" + ` sync) ships in Phase 0 task 2a.`,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runAutoTestPREnable,
}

// autoTestPRShowTemplateCmd is the read-only sibling of
// `enable --emit-template`. It prints the embedded conventions template
// to stdout without writing anywhere. Operators reach for it when they
// want to diff their checked-in conventions.md against the latest
// shipped template, or when they're inspecting the constraints from a
// machine that does not have a checked-in copy yet.
var autoTestPRShowTemplateCmd = &cobra.Command{
	Use:   "show-template",
	Short: "Print the embedded auto-test-pr conventions-sheet template",
	Long: `Print the embedded auto-test-pr conventions-sheet template to stdout
(read-only; nothing is written).

This is the canonical source of truth for the constraints the auto-test-pr
polecat reads at the start of every cycle. To persist a rig-local copy,
use:

  gt auto-test-pr enable --emit-template > .gt/auto-test-pr/conventions.md

Or, if your rig already has a copy and you want to diff:

  gt auto-test-pr show-template | diff - .gt/auto-test-pr/conventions.md`,
	Args:          cobra.NoArgs,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runAutoTestPRShowTemplate,
}

func init() {
	autoTestPREnableCmd.Flags().BoolVar(&autoTestPREmitTemplate, "emit-template", false,
		"Print the embedded conventions-sheet template to stdout and exit (Phase 0 task 2d)")

	autoTestPRCmd.AddCommand(autoTestPREnableCmd)
	autoTestPRCmd.AddCommand(autoTestPRShowTemplateCmd)
	rootCmd.AddCommand(autoTestPRCmd)
}

func runAutoTestPREnable(cmd *cobra.Command, args []string) error {
	if autoTestPREmitTemplate {
		return writeConventionsTemplate(cmd.OutOrStdout())
	}
	// Phase 0 task 2a will wire the full enable path. Until then, give
	// operators a clear, non-cryptic error rather than letting the verb
	// silently succeed with no side effect.
	fmt.Fprintln(cmd.ErrOrStderr(),
		"gt auto-test-pr enable: full enable surface not yet shipped.")
	fmt.Fprintln(cmd.ErrOrStderr(),
		"To emit the conventions-sheet template (Phase 0 task 2d), pass --emit-template:")
	fmt.Fprintln(cmd.ErrOrStderr(),
		"  "+autoTestPREnableExample)
	return NewSilentExit(2)
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
