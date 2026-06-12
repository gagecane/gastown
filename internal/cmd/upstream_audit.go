// gt upstream audit — post-push audit of recent sync attempts.
//
// Phase 5 (gu-1zfy). Surfaces risk-classified findings for upstream
// sync attempts that have already landed on origin, so an operator
// can review agent-authored merges before festering. The audit is
// pure-function (see internal/upstreamsync/audit.go); this verb
// loads the per-rig state bead, runs AuditState, and renders the
// findings as a human table or JSON.
//
// Defaults match cv-2s6tq/security.md §"Option D":
//   - --since=24h: review the last day of attempts
//   - --stale-after=7d: warn if no successful sync in 7 days
//   - --min-severity=warn: hide info findings unless --all
//
// Deacon-patrol auto-invocation is intentionally separate work — this
// verb ships the operator-facing surface; automation lives in a
// follow-up bead.
//
// Design context: .designs/cv-hpnja/security.md §"Option D
// (post-merge security scan)" and .designs/cv-2s6tq/security.md
// §"Option D (post-push audit and rollback)".
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/upstreamsync"
)

// CLI flag bindings — package-level vars per upstream.go convention.
var (
	upstreamAuditRig         string
	upstreamAuditAll         bool
	upstreamAuditJSON        bool
	upstreamAuditSince       string
	upstreamAuditStaleAfter  string
	upstreamAuditMinSeverity string
	upstreamAuditCodeFilter  string
)

// Default audit windows. Tuned for daily operator review: catch the
// last 24h of attempts in detail, complain if a rig has been silent
// for a week (the 7d figure mirrors the design's "weekly SEV review"
// cadence in cv-2s6tq/security.md §"Audit patrol").
const (
	defaultAuditSince       = 24 * time.Hour
	defaultAuditStaleAfter  = 7 * 24 * time.Hour
	defaultAuditMinSeverity = "warn"
)

var upstreamAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Surface post-push risk findings on recent sync attempts",
	Long: `Post-push audit of upstream sync attempts. Loads the per-rig
state bead, classifies recent attempts, and prints risk-graded findings
for human review.

Findings are graded info / warn / critical:

  critical  Investigate now. Examples: rig auto-paused, agent resolved
            a conflict touching restricted paths (auth, secrets, CI).
  warn      Review when convenient. Examples: gate-failure outcome,
            attempt outcome non-success, no successful sync in window.
  info      Routine notice. Examples: agent resolved a clean conflict.

Examples:

  gt upstream audit                         # current rig, last 24h, warn+
  gt upstream audit --rig=gastown_upstream
  gt upstream audit --since=72h --all       # 3 days, include info findings
  gt upstream audit --json                  # machine-readable
  gt upstream audit --code=resolution-agent-authored

Defaults: --since=24h --stale-after=168h --min-severity=warn`,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runUpstreamAudit,
}

func init() {
	upstreamAuditCmd.Flags().StringVar(&upstreamAuditRig, "rig", "",
		"Target rig (defaults to current worktree's rig)")
	upstreamAuditCmd.Flags().BoolVar(&upstreamAuditAll, "all", false,
		"Include info-severity findings (overrides --min-severity)")
	upstreamAuditCmd.Flags().BoolVar(&upstreamAuditJSON, "json", false,
		"Emit machine-readable JSON instead of a table")
	upstreamAuditCmd.Flags().StringVar(&upstreamAuditSince, "since", "24h",
		"Audit window (Go duration: 24h, 72h, 168h)")
	upstreamAuditCmd.Flags().StringVar(&upstreamAuditStaleAfter, "stale-after", "168h",
		"Warn if no successful sync within this duration; '0' disables")
	upstreamAuditCmd.Flags().StringVar(&upstreamAuditMinSeverity, "min-severity", defaultAuditMinSeverity,
		"Minimum severity to display: info | warn | critical")
	upstreamAuditCmd.Flags().StringVar(&upstreamAuditCodeFilter, "code", "",
		"Show only findings with this code (e.g., resolution-agent-authored)")

	upstreamCmd.AddCommand(upstreamAuditCmd)
}

func runUpstreamAudit(cmd *cobra.Command, args []string) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	since, err := parseAuditDuration(upstreamAuditSince)
	if err != nil {
		fmt.Fprintf(stderr, "gt upstream audit: invalid --since=%q: %v\n", upstreamAuditSince, err)
		return NewSilentExit(2)
	}
	staleAfter, err := parseAuditDuration(upstreamAuditStaleAfter)
	if err != nil {
		fmt.Fprintf(stderr, "gt upstream audit: invalid --stale-after=%q: %v\n", upstreamAuditStaleAfter, err)
		return NewSilentExit(2)
	}

	minSev := upstreamsync.AuditSeverity(strings.ToLower(strings.TrimSpace(upstreamAuditMinSeverity)))
	if upstreamAuditAll {
		minSev = upstreamsync.SeverityInfo
	}
	if minSev != upstreamsync.SeverityInfo &&
		minSev != upstreamsync.SeverityWarn &&
		minSev != upstreamsync.SeverityCritical {
		fmt.Fprintf(stderr, "gt upstream audit: invalid --min-severity=%q (want info|warn|critical)\n",
			upstreamAuditMinSeverity)
		return NewSilentExit(2)
	}

	townRoot, rigName, _, settings, err := resolveUpstreamRigContext(cmd, "audit", upstreamAuditRig)
	if err != nil {
		return err
	}

	if settings == nil || !settings.UpstreamSync.IsEnabled() {
		fmt.Fprintf(stderr, "gt upstream audit: upstream sync is not enabled for rig %s\n", rigName)
		fmt.Fprintln(stderr, "  hint: enable in settings/config.json (upstream_sync.enabled = true)")
		return NewSilentExit(2)
	}

	rigPrefix := resolveRigPrefix(rigName)
	bd := beads.NewWithBeadsDir(townRoot, filepath.Join(townRoot, ".beads"))

	state, err := upstreamsync.LoadSyncState(bd, rigPrefix)
	if err != nil {
		if errors.Is(err, upstreamsync.ErrStateBeadNotProvisioned) {
			fmt.Fprintf(stderr, "gt upstream audit: state bead not provisioned for rig %s\n", rigName)
			fmt.Fprintln(stderr, "  hint: run `gt upstream sync` once to provision the state bead")
			return NewSilentExit(3)
		}
		return fmt.Errorf("loading sync state: %w", err)
	}

	now := time.Now().UTC()
	opts := upstreamsync.AuditOptions{
		Since:               now.Add(-since),
		MinSeverity:         minSev,
		IncludeRigLevel:     true,
		StaleNoSuccessAfter: staleAfter,
		Now:                 now,
	}

	findings := upstreamsync.AuditState(state, opts)
	if upstreamAuditCodeFilter != "" {
		findings = filterFindingsByCode(findings, upstreamAuditCodeFilter)
	}

	if upstreamAuditJSON {
		return emitAuditJSON(stdout, rigName, findings, opts)
	}

	return emitAuditTable(stdout, rigName, findings, opts)
}

// parseAuditDuration accepts a Go duration string ("24h", "168h",
// "72h30m") and the literal "0" for "disable this window". Returns
// 0 for the disable case so downstream comparisons short-circuit.
func parseAuditDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, fmt.Errorf("duration must be non-negative")
	}
	return d, nil
}

// filterFindingsByCode returns only findings whose Code matches `code`
// exactly. Empty `code` returns the input unchanged. Allocates a new
// slice so the caller can mutate it without disturbing the upstream
// returned slice.
func filterFindingsByCode(in []upstreamsync.AuditFinding, code string) []upstreamsync.AuditFinding {
	if code == "" {
		return in
	}
	out := make([]upstreamsync.AuditFinding, 0, len(in))
	for _, f := range in {
		if f.Code == code {
			out = append(out, f)
		}
	}
	return out
}

// emitAuditJSON encodes the audit run as a JSON document with a small
// metadata envelope so downstream tooling can pivot on the run params
// without re-parsing flags.
func emitAuditJSON(w io.Writer, rig string, findings []upstreamsync.AuditFinding, opts upstreamsync.AuditOptions) error {
	doc := struct {
		Rig         string                      `json:"rig"`
		GeneratedAt string                      `json:"generated_at"`
		Since       string                      `json:"since,omitempty"`
		StaleAfter  string                      `json:"stale_after,omitempty"`
		MinSeverity string                      `json:"min_severity,omitempty"`
		Findings    []upstreamsync.AuditFinding `json:"findings"`
		Counts      map[string]int              `json:"counts"`
	}{
		Rig:         rig,
		GeneratedAt: opts.Now.Format(time.RFC3339),
		Findings:    findings,
		Counts:      countBySeverity(findings),
	}
	if !opts.Since.IsZero() {
		doc.Since = opts.Since.Format(time.RFC3339)
	}
	if opts.StaleNoSuccessAfter > 0 {
		doc.StaleAfter = opts.StaleNoSuccessAfter.String()
	}
	if opts.MinSeverity != "" {
		doc.MinSeverity = string(opts.MinSeverity)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// emitAuditTable renders findings as a tab-aligned text table. Layout:
//
//	SEVERITY  CODE                          ATTEMPT  WHEN  TITLE
//
// Files (when present) are printed below the row indented two spaces
// so they remain visually associated without bloating the table.
func emitAuditTable(w io.Writer, rig string, findings []upstreamsync.AuditFinding, opts upstreamsync.AuditOptions) error {
	if len(findings) == 0 {
		fmt.Fprintf(w, "Upstream audit: %s — no findings (window=%s, min_severity=%s)\n",
			rig, opts.Since.Format(time.RFC3339), valueOrDashUpstream(string(opts.MinSeverity)))
		return nil
	}

	counts := countBySeverity(findings)
	fmt.Fprintf(w, "Upstream audit: %s\n", rig)
	fmt.Fprintf(w, "  Generated:  %s\n", opts.Now.Format(time.RFC3339))
	if !opts.Since.IsZero() {
		fmt.Fprintf(w, "  Since:      %s\n", opts.Since.Format(time.RFC3339))
	}
	fmt.Fprintf(w, "  Findings:   %d total (critical=%d, warn=%d, info=%d)\n\n",
		len(findings), counts["critical"], counts["warn"], counts["info"])

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEVERITY\tCODE\tATTEMPT\tWHEN\tTITLE")
	for _, f := range findings {
		attempt := f.AttemptID
		if attempt == "" {
			attempt = "(rig)"
		}
		when := compactTime(f.AttemptStartedAt)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			strings.ToUpper(string(f.Severity)),
			f.Code,
			attempt,
			when,
			f.Title,
		)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	// Detail blocks for findings with files or multi-line detail.
	for _, f := range findings {
		if len(f.Files) == 0 && !strings.Contains(f.Detail, "\n") {
			continue
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  [%s] %s\n", strings.ToUpper(string(f.Severity)), f.Title)
		if f.Detail != "" {
			for _, line := range strings.Split(f.Detail, "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
		}
		if len(f.Files) > 0 {
			fmt.Fprintln(w, "    Files:")
			for _, fl := range f.Files {
				fmt.Fprintf(w, "      - %s\n", fl)
			}
		}
	}

	return nil
}

// countBySeverity tallies findings by severity for the run summary
// header. Returns a string-keyed map (matches JSON output schema).
func countBySeverity(findings []upstreamsync.AuditFinding) map[string]int {
	out := map[string]int{
		"critical": 0,
		"warn":     0,
		"info":     0,
	}
	for _, f := range findings {
		out[string(f.Severity)]++
	}
	return out
}
