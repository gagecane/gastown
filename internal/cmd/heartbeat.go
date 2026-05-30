package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var heartbeatCmd = &cobra.Command{
	Use:     "heartbeat",
	GroupID: GroupDiag,
	Short:   "Update agent heartbeat state",
	Long: `Update the agent heartbeat with a specific state.

Used by agents to self-report their state to the witness. The witness reads
the heartbeat state instead of inferring it from timers (ZFC: gt-3vr5).

States:
  working  - Actively processing (default)
  idle     - Waiting for input
  exiting  - In gt done flow
  stuck    - Self-reporting stuck (triggers witness escalation)

Examples:
  gt heartbeat --state=stuck "blocked on auth issue"
  gt heartbeat --state=idle
  gt heartbeat --state=working`,
	RunE: runHeartbeat,
}

var heartbeatState string

// heartbeatKeepaliveCmd implements `gt heartbeat keepalive`. Long-running
// shell wrappers (build wrappers, gate runners) call this in a background
// loop to bump the heartbeat timestamp without changing the agent's
// self-reported state. cv-p3fem Phase 2.
//
// UX leg strong opinion: missing GT_SESSION warns and no-ops rather than
// erroring. Errors fail builds; a silent no-op with a warning is far
// less harmful when a build wrapper accidentally invokes this outside a
// Gas Town session.
var heartbeatKeepaliveCmd = &cobra.Command{
	Use:   "keepalive",
	Short: "Bump heartbeat timestamp without changing state (cv-p3fem)",
	Long: `Bump the session heartbeat timestamp without changing the agent's
self-reported state. Used by long-running call sites (LLM calls,
build wrappers, gate runners, merge-queue waits) to keep the
heartbeat fresh while no foreground gt commands are running.

Without this, a perfectly healthy polecat in a 10-minute LLM call
looks identical to a polecat that crashed 10 minutes ago — the
witness/dog flag both as stale (cv-p3fem root cause).

Without GT_SESSION, this command warns and exits 0 (no-op). Errors
in build wrappers fail builds; the harm-from-silent-noop is far
smaller than the harm-from-broken-CI.

Examples:
  gt heartbeat keepalive
  gt heartbeat keepalive --op=brazil-build
  gt heartbeat keepalive --op=llm-call

Shell wrapper pattern (run in a background loop while a long
operation runs):

  ( while sleep 30; do gt heartbeat keepalive --op=my-op; done ) &
  KEEPALIVE_PID=$!
  trap "kill $KEEPALIVE_PID 2>/dev/null" EXIT
  long-running-command`,
	RunE: runHeartbeatKeepalive,
}

var (
	heartbeatKeepaliveOp    string
	heartbeatKeepaliveUntil string
)

// heartbeatStatusCmd implements `gt heartbeat status [--session] [--json]`.
// Single source of truth for liveness verdicts (cv-p3fem Phase 3): plugins
// consume `--json | jq .verdict` instead of re-implementing staleness logic
// in bash. Stable JSON contract — fields and verdict_reason values are
// versioned-extension-only.
var heartbeatStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show session liveness verdict (cv-p3fem Phase 3)",
	Long: `Show the typed liveness verdict for a session.

Liveness reads the session's heartbeat file and produces a typed verdict:
ALIVE / MAYBE_DEAD / DEAD / UNKNOWN. Verdict words intentionally cap at three
(plus UNKNOWN for missing-file): operators conflate states the moment there
are more.

The --json flag is the stable plugin contract — all field names and the
verdict_reason enum are documented as the v3 surface (cv-p3fem). Plugin
authors lock in this shape and rely on additions being backward-compatible.

Examples:
  gt heartbeat status                       # current session (GT_SESSION)
  gt heartbeat status --session=hq-deacon
  gt heartbeat status --session=g-witness --json | jq .verdict`,
	RunE: runHeartbeatStatus,
}

var (
	heartbeatStatusSession string
	heartbeatStatusJSON    bool
)

func init() {
	rootCmd.AddCommand(heartbeatCmd)
	heartbeatCmd.Flags().StringVar(&heartbeatState, "state", "working", "Agent state (working, idle, exiting, stuck)")
	heartbeatCmd.AddCommand(heartbeatKeepaliveCmd)
	heartbeatCmd.AddCommand(heartbeatStatusCmd)
	heartbeatKeepaliveCmd.Flags().StringVar(&heartbeatKeepaliveOp, "op", "", "Operation label (e.g. llm-call, brazil-build, go-test)")
	heartbeatKeepaliveCmd.Flags().StringVar(&heartbeatKeepaliveUntil, "until", "",
		"TTL-bounded idle declaration (e.g. 15m, 2026-05-30T18:00:00Z). Capped at the session's role reap timeout.")
	heartbeatStatusCmd.Flags().StringVar(&heartbeatStatusSession, "session", "", "Session name (default: GT_SESSION)")
	heartbeatStatusCmd.Flags().BoolVar(&heartbeatStatusJSON, "json", false, "Emit the stable JSON contract for plugin consumers")
}

func runHeartbeat(cmd *cobra.Command, args []string) error {
	sessionName := os.Getenv("GT_SESSION")
	if sessionName == "" {
		return fmt.Errorf("GT_SESSION not set (not running in a Gas Town session)")
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return fmt.Errorf("could not find town root: %v", err)
	}

	state := polecat.HeartbeatState(heartbeatState)
	switch state {
	case polecat.HeartbeatWorking, polecat.HeartbeatIdle, polecat.HeartbeatExiting, polecat.HeartbeatStuck:
		// valid
	default:
		return fmt.Errorf("invalid state %q (must be working, idle, exiting, or stuck)", heartbeatState)
	}

	context := ""
	if len(args) > 0 {
		context = strings.Join(args, " ")
	}

	polecat.TouchSessionHeartbeatWithState(townRoot, sessionName, state, context, "")
	fmt.Printf("Heartbeat updated: state=%s\n", state)
	return nil
}

// runHeartbeatKeepalive bumps the heartbeat timestamp without changing
// state. cv-p3fem Phase 2 (with Phase 3 --until). Warns-and-no-ops on
// missing GT_SESSION so build wrappers can call it unconditionally.
func runHeartbeatKeepalive(_ *cobra.Command, _ []string) error {
	sessionName := os.Getenv("GT_SESSION")
	if sessionName == "" {
		// UX leg strong opinion: don't fail builds. Warn so an operator
		// running this manually sees the no-op, but exit 0 so a build
		// wrapper's `gt heartbeat keepalive` doesn't break the build.
		fmt.Fprintln(os.Stderr, "gt heartbeat keepalive: GT_SESSION not set, skipping (no-op)")
		return nil
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		fmt.Fprintln(os.Stderr, "gt heartbeat keepalive: could not find town root, skipping (no-op)")
		return nil
	}

	until, err := parseExpectedIdleUntil(heartbeatKeepaliveUntil)
	if err != nil {
		return fmt.Errorf("invalid --until value %q: %w", heartbeatKeepaliveUntil, err)
	}
	// Per-rig cap (design-doc decision: max declared idle =
	// dead_agent_reap_timeout). A wedged agent that lies about idleness
	// can't exceed the role's hard reap ceiling. We use the polecat
	// dead threshold as the conservative cap here; per-role refinement
	// is a separate follow-up. See open-question 1 mitigation.
	until = capExpectedIdleUntil(until, polecat.DefaultLivenessDead)

	polecat.KeepaliveWithOpUntil(townRoot, sessionName, heartbeatKeepaliveOp, until)
	return nil
}

// parseExpectedIdleUntil accepts either an RFC3339 absolute timestamp or a
// relative duration (e.g. "15m", "2h"). Empty input → zero time (no idle
// declaration).
func parseExpectedIdleUntil(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	// Try RFC3339 first — operators sometimes pass an absolute deadline.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	// Fall back to a relative duration.
	d, err := time.ParseDuration(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("expected duration (e.g. 15m) or RFC3339 timestamp: %v", err)
	}
	if d <= 0 {
		return time.Time{}, fmt.Errorf("duration must be positive")
	}
	return time.Now().UTC().Add(d), nil
}

// capExpectedIdleUntil enforces the design-doc per-rig cap on declared idle
// windows. If until is past now+cap, clamp to now+cap. A zero-valued cap
// disables the cap (returns until unchanged).
func capExpectedIdleUntil(until time.Time, cap time.Duration) time.Time {
	if until.IsZero() || cap <= 0 {
		return until
	}
	limit := time.Now().UTC().Add(cap)
	if until.After(limit) {
		return limit
	}
	return until
}

// runHeartbeatStatus renders the typed Liveness() verdict for a session.
// Stable contract — see heartbeatStatusCmd.Long.
func runHeartbeatStatus(_ *cobra.Command, _ []string) error {
	sessionName := heartbeatStatusSession
	if sessionName == "" {
		sessionName = os.Getenv("GT_SESSION")
	}
	if sessionName == "" {
		return fmt.Errorf("no session: pass --session=<name> or set GT_SESSION")
	}
	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return fmt.Errorf("could not find town root: %v", err)
	}

	// Per-role thresholds: witness/refinery use the wider windows that match
	// the daemon reaper's per-role timeouts so all three surfaces agree.
	opts := livenessOptionsForSession(sessionName)
	report := polecat.Liveness(townRoot, sessionName, opts)

	if heartbeatStatusJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	fmt.Printf("session: %s\n", report.Session)
	parens := freshnessParenthetical(report)
	fmt.Printf("liveness: %-12s %s\n", report.VerdictString, parens)
	if report.State != "" || report.KeepaliveOp != "" || report.Bead != "" {
		extra := ""
		if report.KeepaliveOp != "" {
			extra = "op=" + report.KeepaliveOp
		}
		if report.Bead != "" {
			if extra != "" {
				extra += ", "
			}
			extra += "bead=" + report.Bead
		}
		if extra != "" {
			fmt.Printf("state:    %-12s (%s)\n", string(report.State), extra)
		} else {
			fmt.Printf("state:    %s\n", report.State)
		}
	}
	if !report.ExpectedIdleUntil.IsZero() {
		remaining := time.Until(report.ExpectedIdleUntil).Round(time.Second)
		fmt.Printf("expected_idle_until: %s (%s remaining)\n",
			report.ExpectedIdleUntil.Format(time.RFC3339), remaining)
	}
	if report.VerdictReason == polecat.ReasonInsideGraceWindow {
		fmt.Printf("hint:     auto-action when age exceeds %s\n",
			report.Thresholds.Dead.String())
	}
	return nil
}

// freshnessParenthetical builds the "(heartbeat 12s ago, keepalive 8s ago)"
// fragment shown next to the verdict word.
func freshnessParenthetical(r polecat.LivenessReport) string {
	if r.VerdictReason == polecat.ReasonNoHeartbeatFile {
		return "(no heartbeat file)"
	}
	if r.LastTimestamp.IsZero() && r.LastKeepalive.IsZero() {
		return ""
	}
	parts := []string{}
	if !r.LastTimestamp.IsZero() {
		parts = append(parts, fmt.Sprintf("heartbeat %s ago",
			time.Since(r.LastTimestamp).Round(time.Second)))
	}
	if !r.LastKeepalive.IsZero() {
		parts = append(parts, fmt.Sprintf("keepalive %s ago",
			time.Since(r.LastKeepalive).Round(time.Second)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// livenessOptionsForSession picks per-role thresholds so the same session
// name produces the same verdict regardless of which surface (CLI, witness
// status table, daemon reaper) computes it. Witness/refinery sessions get
// wider windows because their patrol cycles legitimately run longer.
func livenessOptionsForSession(sessionName string) polecat.LivenessOptions {
	if session.IsWitnessSessionName(sessionName) {
		return polecat.LivenessOptions{
			Stale: 5 * time.Minute,
			Grace: 15 * time.Minute,
			Dead:  30 * time.Minute,
		}
	}
	if session.IsRefinerySessionName(sessionName) {
		return polecat.LivenessOptions{
			Stale: 10 * time.Minute,
			Grace: 30 * time.Minute,
			Dead:  60 * time.Minute,
		}
	}
	// Polecat / deacon / dog: the polecat-class defaults.
	return polecat.LivenessOptions{}
}

// Suppress unused-import warning for style during phased build (style is
// pulled in by the broader cmd package; we don't render colors in the
// status output to keep --json clean).
var _ = style.Bold
