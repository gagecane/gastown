package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// resolveHeartbeatSession returns the tmux session name to use for heartbeat
// writes, deriving it when GT_SESSION is absent from the process environment.
//
// gu-urr85: the between-turn keepalive and idle keepalive read GT_SESSION via
// os.Getenv and SILENTLY NO-OP when it is empty. Several daemon-spawned agents
// (deacon observed; likely witness/refinery too) end up with an empty
// GT_SESSION in their shell env — so the heartbeat ages monotonically while the
// session is alive, producing the "fresh=true but frozen timestamp" stall
// signature that floods witnesses with false escalations. A liveness primitive
// that silently does nothing is the core footgun.
//
// Resolution order (all in-process, cheap):
//  1. GT_SESSION env — the normal, fastest path.
//  2. tmux pane resolution — works regardless of whether GT_SESSION was
//     exported into the shell, by walking the pane PID tree for the session
//     this process actually runs in. This covers every daemon-spawned agent
//     since they all run inside a tmux session.
//  3. Role-derived deterministic name — last resort for singleton roles
//     (deacon/mayor/witness/refinery) whose session name is computable from
//     GT_ROLE even with no tmux (e.g. a detached subprocess).
//
// Returns ("", "") only when no source resolves. The source string is for
// observability ("env", "tmux", "role") so callers can warn when they fell
// back rather than silently masking env loss.
func resolveHeartbeatSession() (name, source string) {
	if s := os.Getenv("GT_SESSION"); s != "" {
		return s, "env"
	}

	// tmux pane resolution: robust for any agent running inside tmux, even
	// when GT_SESSION never made it into the process env.
	if os.Getenv("TMUX") != "" {
		if s, err := tmux.NewTmux().ResolveCurrentSession(); err == nil && s != "" {
			return s, "tmux"
		}
	}

	// Role-derived fallback: singleton roles have deterministic session names.
	if s := roleDerivedSessionName(); s != "" {
		return s, "role"
	}

	return "", ""
}

// roleDerivedSessionName computes the deterministic tmux session name for the
// current role when it can be determined from the environment. Only singleton
// roles (deacon, mayor, witness, refinery) have a name derivable without an
// agent name; multi-instance roles (crew, polecat) return "" since their name
// requires an instance identifier we can't recover from a lost env.
func roleDerivedSessionName() string {
	info, err := GetRole()
	if err != nil {
		return ""
	}
	switch info.Role {
	case RoleDeacon:
		return session.DeaconSessionName()
	case RoleMayor:
		return session.MayorSessionName()
	case RoleWitness:
		if info.Rig != "" {
			return session.WitnessSessionName(session.PrefixFor(info.Rig))
		}
	case RoleRefinery:
		if info.Rig != "" {
			return session.RefinerySessionName(session.PrefixFor(info.Rig))
		}
	}
	return ""
}

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

var heartbeatKeepaliveOp string
var heartbeatKeepaliveUntil string

// heartbeatStatusCmd implements `gt heartbeat status [--session] [--json]`.
// cv-p3fem Phase 3 plugin contract: a stable JSON shape consumed by the
// stuck-agent-dog plugin and any other tooling that needs a typed
// liveness verdict for a session. See cv-p3fem design doc §Plugin surface.
var heartbeatStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show liveness verdict for a session (cv-p3fem Phase 3)",
	Long: `Show the typed liveness verdict for a session.

Without --session, reports on $GT_SESSION (the current session). With
--session=<name>, reports on the named session.

The verdict is one of:
  ALIVE       — heartbeat is fresh, or PID/keepalive corroborate liveness
  MAYBE_DEAD  — heartbeat past the stale threshold, inside the dead window
  DEAD        — heartbeat past the dead threshold AND PID/corroboration agrees
  UNKNOWN     — no heartbeat file exists (rollout / pre-cv-p3fem session)

Examples:
  gt heartbeat status
  gt heartbeat status --session=polecat-shiny-tmqt
  gt heartbeat status --json | jq .verdict

Plugin contract (--json shape, stable across versions):
  {
    "session": "...",
    "verdict": "ALIVE|MAYBE_DEAD|DEAD|UNKNOWN",
    "verdict_reason": "...",
    "age_seconds": 12,
    "last_keepalive_age_seconds": 8,
    "state": "working",
    "keepalive_op": "llm-call",
    "bead": "gu-...",
    "thresholds": {"stale": ..., "grace": ..., "dead": ...}
  }`,
	RunE: runHeartbeatStatus,
}

var heartbeatStatusSession string
var heartbeatStatusJSON bool
var heartbeatStatusRole string

func init() {
	rootCmd.AddCommand(heartbeatCmd)
	heartbeatCmd.Flags().StringVar(&heartbeatState, "state", "working", "Agent state (working, idle, exiting, stuck)")
	heartbeatCmd.AddCommand(heartbeatKeepaliveCmd)
	heartbeatCmd.AddCommand(heartbeatStatusCmd)
	heartbeatKeepaliveCmd.Flags().StringVar(&heartbeatKeepaliveOp, "op", "", "Operation label (e.g. llm-call, brazil-build, go-test)")
	heartbeatKeepaliveCmd.Flags().StringVar(&heartbeatKeepaliveUntil, "until", "", "TTL-bounded idle declaration (e.g. +15m, +1h). Capped per-rig at dead_agent_reap_timeout.")
	heartbeatStatusCmd.Flags().StringVar(&heartbeatStatusSession, "session", "", "Session name (default: $GT_SESSION)")
	heartbeatStatusCmd.Flags().StringVar(&heartbeatStatusRole, "role", "", "Role override for thresholds: polecat, witness, refinery (default: inferred from session name)")
	heartbeatStatusCmd.Flags().BoolVar(&heartbeatStatusJSON, "json", false, "Emit machine-readable JSON")
}

func runHeartbeat(cmd *cobra.Command, args []string) error {
	// gu-urr85: derive the session when GT_SESSION is missing rather than
	// erroring, so an agent whose shell env lost GT_SESSION can still report
	// its state instead of leaving the heartbeat to age into false-stale.
	sessionName, source := resolveHeartbeatSession()
	if sessionName == "" {
		return fmt.Errorf("GT_SESSION not set and no session could be derived (not running in a Gas Town session)")
	}
	if source != "env" {
		fmt.Fprintf(os.Stderr,
			"gt heartbeat: GT_SESSION not set; using %q (derived via %s, GT_ROLE=%q pid=%d). See gu-urr85.\n",
			sessionName, source, os.Getenv("GT_ROLE"), os.Getpid())
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

	// Deacon liveness has extra stores beyond session heartbeat. Keep the
	// generic heartbeat command and `gt deacon heartbeat` on one shared path.
	if os.Getenv("GT_ROLE") == "deacon" {
		if err := syncDeaconHeartbeatStores(townRoot, context); err != nil {
			fmt.Printf("warning: failed to touch deacon heartbeat file: %v\n", err)
		}
	}

	fmt.Printf("Heartbeat updated: state=%s\n", state)
	return nil
}

// runHeartbeatKeepalive bumps the heartbeat timestamp without changing
// state. cv-p3fem Phase 2. Warns-and-no-ops on missing GT_SESSION so
// build wrappers can call it unconditionally.
//
// cv-p3fem Phase 3: --until=<+duration> declares an expected idle
// window (TTL-bounded self-report). Capped per-rig at
// dead_agent_reap_timeout to prevent a wedged agent from suppressing
// detection forever.
func runHeartbeatKeepalive(_ *cobra.Command, _ []string) error {
	// gu-urr85: derive the session when GT_SESSION is missing instead of
	// silently no-oping. A no-op here is the recurring deacon-stall cause —
	// the heartbeat ages while the session is alive, tripping false-stale
	// detection. Only no-op when even derivation fails (genuinely no session).
	sessionName, source := resolveHeartbeatSession()
	if sessionName == "" {
		// UX leg strong opinion: don't fail builds. Warn so an operator
		// running this manually sees the no-op, but exit 0 so a build
		// wrapper's `gt heartbeat keepalive` doesn't break the build.
		fmt.Fprintln(os.Stderr, "gt heartbeat keepalive: GT_SESSION not set and no session could be derived, skipping (no-op)")
		return nil
	}
	if source != "env" {
		fmt.Fprintf(os.Stderr,
			"gt heartbeat keepalive: GT_SESSION not set; using %q (derived via %s, GT_ROLE=%q pid=%d). See gu-urr85.\n",
			sessionName, source, os.Getenv("GT_ROLE"), os.Getpid())
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		fmt.Fprintln(os.Stderr, "gt heartbeat keepalive: could not find town root, skipping (no-op)")
		return nil
	}

	polecat.KeepaliveWithOp(townRoot, sessionName, heartbeatKeepaliveOp)

	if heartbeatKeepaliveUntil != "" {
		until, err := parseUntilArg(heartbeatKeepaliveUntil)
		if err != nil {
			return fmt.Errorf("invalid --until %q: %w", heartbeatKeepaliveUntil, err)
		}
		// Per-rig cap = dead_agent_reap_timeout. Without ZFC config we
		// fall back to the package default. The cap argument is the max
		// declared idle window the operator will tolerate; values larger
		// than this are silently truncated.
		opCfg := config.LoadOperationalConfig(townRoot)
		cap := opCfg.GetDaemonConfig().DeadAgentReapTimeoutD()
		if err := polecat.SetExpectedIdleUntil(townRoot, sessionName, until, cap); err != nil {
			return fmt.Errorf("setting expected idle window: %w", err)
		}
	}
	return nil
}

// parseUntilArg accepts either an absolute RFC3339 timestamp or a
// "+<duration>" relative offset (e.g. "+15m"). Returns a UTC time.
func parseUntilArg(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty value")
	}
	if strings.HasPrefix(s, "+") {
		d, err := time.ParseDuration(s[1:])
		if err != nil {
			return time.Time{}, err
		}
		return time.Now().Add(d).UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// runHeartbeatStatus emits a typed liveness verdict for a session.
// cv-p3fem Phase 3 plugin contract; stable JSON shape consumed by the
// stuck-agent-dog plugin and other tooling.
func runHeartbeatStatus(_ *cobra.Command, _ []string) error {
	sessionName := heartbeatStatusSession
	if sessionName == "" {
		sessionName = os.Getenv("GT_SESSION")
	}
	if sessionName == "" {
		return fmt.Errorf("no session: pass --session=<name> or set $GT_SESSION")
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return fmt.Errorf("could not find town root: %w", err)
	}

	role := heartbeatStatusRole
	if role == "" {
		role = inferRoleFromSessionName(sessionName)
	}
	thresholds := thresholdsForRole(role)

	report := polecat.Liveness(townRoot, sessionName, thresholds)

	if heartbeatStatusJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	// Human-readable output.
	fmt.Printf("session:  %s\n", report.Session)
	fmt.Printf("liveness: %s", report.Verdict)
	if report.VerdictReason != "" {
		fmt.Printf("  (%s)", report.VerdictReason)
	}
	fmt.Println()
	if report.AgeSeconds > 0 || !report.LastTimestamp.IsZero() {
		fmt.Printf("age:      %s\n", time.Duration(report.AgeSeconds)*time.Second)
	}
	if report.State != "" {
		fmt.Printf("state:    %s\n", report.State)
	}
	if report.KeepaliveOp != "" {
		fmt.Printf("op:       %s\n", report.KeepaliveOp)
	}
	if report.Bead != "" {
		fmt.Printf("bead:     %s\n", report.Bead)
	}
	if report.Recovered {
		fmt.Println("note:     active recovery marker (gu-v5mk) — verdict short-circuited")
	}
	if report.ExpectedIdleUntilSeconds > 0 {
		fmt.Printf("idle until: +%s (capped at dead_agent_reap_timeout)\n",
			(time.Duration(report.ExpectedIdleUntilSeconds) * time.Second).Truncate(time.Second))
	}
	return nil
}

// inferRoleFromSessionName best-effort maps a session name to a role label
// for threshold selection. Witness and refinery sessions have stable
// suffixes (-witness, -refinery); polecats are everything else by default.
// Used by gt heartbeat status when no --role is provided.
func inferRoleFromSessionName(sessionName string) string {
	switch {
	case strings.HasSuffix(sessionName, "-witness"):
		return "witness"
	case strings.HasSuffix(sessionName, "-refinery"):
		return "refinery"
	default:
		return "polecat"
	}
}

// thresholdsForRole returns the LivenessThresholds for a role label,
// falling back to the polecat defaults for unknown roles.
func thresholdsForRole(role string) polecat.LivenessThresholds {
	switch role {
	case "witness":
		return polecat.DefaultWitnessLivenessThresholds
	case "refinery":
		return polecat.DefaultRefineryLivenessThresholds
	default:
		return polecat.DefaultLivenessThresholds
	}
}

// _ stays silent: keep session import live; gt witness status integration
// can use this helper later.
var _ = session.PrefixFor

// deaconBeadHeartbeatSyncThreshold throttles agent-bead label refreshes from
// gt heartbeat: each refresh is a Dolt commit, so only sync when the label is
// stale enough to matter to watchers.
const deaconBeadHeartbeatSyncThreshold = deacon.HeartbeatStaleThreshold / 2

func syncDeaconHeartbeatStores(townRoot, action string) error {
	var err error
	if action != "" {
		err = deacon.TouchWithAction(townRoot, action, 0, 0)
	} else {
		err = deacon.Touch(townRoot)
	}
	syncDeaconAgentBeadHeartbeat(townRoot)
	return err
}

// syncDeaconAgentBeadHeartbeat refreshes the heartbeat:EPOCH label on the
// Deacon's agent bead — the third heartbeat store, read by Witness
// second-order monitoring. Normally await-signal maintains it, but a Deacon
// session that never reaches await-signal (handoffs, long patrols, session
// limits) leaves it stale for hours and triggers false stuck escalations
// (hq-qxl9). Best-effort: failures are silent, liveness is already recorded
// in the other two stores.
func syncDeaconAgentBeadHeartbeat(townRoot string) {
	agentBead := beads.DeaconBeadIDTown()
	beadsDir := beads.ResolveBeadsDir(townRoot)

	labels, err := getAllAgentLabels(agentBead, beadsDir)
	if err != nil {
		return
	}
	for _, label := range labels {
		epochStr, ok := strings.CutPrefix(label, "heartbeat:")
		if !ok {
			continue
		}
		if epoch, err := strconv.ParseInt(epochStr, 10, 64); err == nil {
			if time.Since(time.Unix(epoch, 0)) < deaconBeadHeartbeatSyncThreshold {
				return
			}
		}
	}
	_ = updateAgentHeartbeat(agentBead, beadsDir)
}
