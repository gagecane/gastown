package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/polecat"
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

var heartbeatKeepaliveOp string

func init() {
	rootCmd.AddCommand(heartbeatCmd)
	heartbeatCmd.Flags().StringVar(&heartbeatState, "state", "working", "Agent state (working, idle, exiting, stuck)")
	heartbeatCmd.AddCommand(heartbeatKeepaliveCmd)
	heartbeatKeepaliveCmd.Flags().StringVar(&heartbeatKeepaliveOp, "op", "", "Operation label (e.g. llm-call, brazil-build, go-test)")
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
// state. cv-p3fem Phase 2. Warns-and-no-ops on missing GT_SESSION so
// build wrappers can call it unconditionally.
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

	polecat.KeepaliveWithOp(townRoot, sessionName, heartbeatKeepaliveOp)
	return nil
}
