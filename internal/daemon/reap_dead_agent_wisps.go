package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/session"
)

// reapDeadAgentWisps resets in_progress/hooked beads assigned to non-polecat
// rig agents (witness, refinery) whose tmux sessions are dead and whose hooked
// wisp has gone stale for longer than the configured timeout.
//
// Why this exists (gu-s009): when a witness or refinery tmux session dies or
// restarts unexpectedly mid-patrol, its hooked patrol wisp stays HOOKED to it
// forever. The role's next patrol cycle never starts because the prior wisp is
// still hooked, freezing the rig's patrol cadence.
//
// Heartbeat-first staleness check (cv-p3fem Phase 1, closes gu-rh0g): when a
// session-heartbeat file exists for the role, we use heartbeat-timestamp age
// against per-role thresholds (Witness/RefineryReapTimeoutD). This is the
// happy path for any session that has run a `gt` command since the cv-p3fem
// rollout — it cuts witness detection from 2h to 15m and refinery detection
// to 30m, satisfying gu-0nmw / gu-rh0g exit criteria.
//
// Pre-rollout fallback: when no heartbeat file exists for the session, we
// fall back to bead.updated_at age against DeadAgentReapTimeoutD (legacy 2h
// default). This preserves the gu-s009 behavior for sessions on the old
// path so we don't lose coverage during rollout.
//
// The reap is conservative by design:
//   - Only handles assignees of the form "<rig>/witness" or "<rig>/refinery".
//     Polecat wisps are left to reapDeadPolecatWisps; crew wisps are not in
//     scope (crew is a persistent worker model with different cadence).
//   - Requires the tmux session for the role to be GONE. A live session, even
//     with a stale wisp, is not reaped — it might just be slow and the next
//     patrol cycle will resolve naturally.
//   - TOCTOU guard: re-checks tmux session liveness immediately before reset.
func (d *Daemon) reapDeadAgentWisps() {
	opCfg := d.loadOperationalConfig().GetDaemonConfig()
	fallbackTimeout := opCfg.DeadAgentReapTimeoutD()
	witnessTimeout := opCfg.WitnessReapTimeoutD()
	refineryTimeout := opCfg.RefineryReapTimeoutD()
	// If both the heartbeat-driven per-role timeouts AND the bead.updated_at
	// fallback are explicitly disabled, the operator has fully opted out.
	// Either path being live keeps the reaper running (the per-role override
	// only fires when a heartbeat exists; the fallback only fires when one
	// does not, so they don't conflict).
	if fallbackTimeout <= 0 && witnessTimeout <= 0 && refineryTimeout <= 0 {
		return
	}

	cfg := agentReapConfig{
		fallbackTimeout: fallbackTimeout,
		witnessTimeout:  witnessTimeout,
		refineryTimeout: refineryTimeout,
	}

	d.rigPool.runPerRig(d.ctx, d.getKnownRigs(), func(ctx context.Context, rigName string) error {
		d.reapRigDeadAgentWisps(rigName, cfg)
		return nil
	})
}

// agentReapConfig bundles the staleness thresholds for the agent reaper so we
// don't thread three durations through every helper. fallbackTimeout is the
// legacy bead.updated_at threshold used when no heartbeat file exists;
// witness/refineryTimeout are the heartbeat-driven per-role thresholds.
type agentReapConfig struct {
	fallbackTimeout time.Duration
	witnessTimeout  time.Duration
	refineryTimeout time.Duration
}

// timeoutFor returns the configured per-role heartbeat timeout for a role.
// Returns the fallback timeout if no per-role override applies (covers a hole
// in operator config without breaking the reaper).
func (c agentReapConfig) timeoutFor(role string) time.Duration {
	switch role {
	case "witness":
		if c.witnessTimeout > 0 {
			return c.witnessTimeout
		}
	case "refinery":
		if c.refineryTimeout > 0 {
			return c.refineryTimeout
		}
	}
	return c.fallbackTimeout
}

// agentBeadInfo is the JSON shape we need from `bd list`. Only updated_at age
// matters for staleness; everything else is identification.
type agentBeadInfo struct {
	ID        string    `json:"id"`
	Assignee  string    `json:"assignee"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

// reapRigDeadAgentWisps scans a single rig for in_progress/hooked beads
// assigned to <rig>/witness or <rig>/refinery and resets them if the role's
// tmux session is dead and the bead is stale.
func (d *Daemon) reapRigDeadAgentWisps(rigName string, cfg agentReapConfig) {
	// Build expected agent assignees for this rig. Crew is intentionally
	// out of scope — see function comment.
	witnessAssignee := rigName + "/witness"
	refineryAssignee := rigName + "/refinery"

	rigDir := rigBDWorkingDir(d.config.TownRoot, rigName)
	var stderrBuf bytes.Buffer
	var candidates []agentBeadInfo
	for _, status := range []string{"hooked", "in_progress"} {
		cmd := exec.Command(d.bdPath, "list", "--status="+status, "--json", "--limit=0") //nolint:gosec // G204: args constructed internally
		setSysProcAttr(cmd)
		cmd.Dir = rigDir
		cmd.Env = os.Environ()
		stderrBuf.Reset()
		cmd.Stderr = &stderrBuf
		output, err := cmd.Output()
		if err != nil {
			d.logger.Printf("reap-dead-agent-wisps: list %s for %s failed: %v (cwd=%s, stderr=%q)",
				status, rigName, err, rigDir, strings.TrimSpace(stderrBuf.String()))
			continue
		}
		if len(output) == 0 {
			continue
		}
		var batch []agentBeadInfo
		if err := json.Unmarshal(output, &batch); err != nil {
			d.logger.Printf("reap-dead-agent-wisps: parse %s for %s failed: %v", status, rigName, err)
			continue
		}
		for i := range batch {
			batch[i].Status = status
		}
		candidates = append(candidates, batch...)
	}

	if len(candidates) == 0 {
		return
	}

	prefix := session.PrefixFor(rigName)

	for _, bead := range candidates {
		var sessionName string
		var role string
		switch bead.Assignee {
		case witnessAssignee:
			sessionName = session.WitnessSessionName(prefix)
			role = "witness"
		case refineryAssignee:
			sessionName = session.RefinerySessionName(prefix)
			role = "refinery"
		default:
			continue // Not a witness/refinery wisp in this rig.
		}

		d.maybeReapDeadAgentBead(rigName, role, sessionName, bead, cfg)
	}
}

// agentStaleness describes how far the role-assigned wisp's owning session
// looks dead. Source is "heartbeat" when we read a heartbeat file (cv-p3fem
// Phase 1) or "updated_at" when we fall back to the legacy bead-age check.
// threshold is the timeout that was applied; staleFor is how stale the chosen
// signal actually is. Carrying both keeps log lines self-describing and
// guarantees the operator can tell which path made the call.
type agentStaleness struct {
	source    string
	staleFor  time.Duration
	threshold time.Duration
}

// evaluateAgentStaleness picks the reap source for a witness/refinery wisp.
// Heartbeat-first via the typed Liveness() verdict (cv-p3fem Phase 3): if a
// SessionHeartbeat exists, the verdict is the canonical liveness signal and
// the daemon only reaps on Verdict==DEAD. Without a heartbeat we fall back
// to bead.UpdatedAt against the legacy threshold so pre-rollout sessions
// remain covered (gu-s009).
//
// MAYBE_DEAD is intentionally NOT reapable here — it surfaces in operator
// tooling (`gt heartbeat status`, `gt witness status`) and the dog plugin
// for visibility, but auto-action is gated on the harder DEAD verdict.
//
// Returns (eligible, agentStaleness). eligible=false means we have no
// trustworthy staleness signal — the heartbeat exists but isn't past the
// dead threshold yet, or both signals are missing. The caller MUST NOT
// reap on eligible=false even if other guards pass.
func (d *Daemon) evaluateAgentStaleness(sessionName string, bead agentBeadInfo, role string, cfg agentReapConfig) (bool, agentStaleness) {
	// Heartbeat-first path via Liveness() verdict.
	if hb := polecat.ReadSessionHeartbeat(d.config.TownRoot, sessionName); hb != nil {
		threshold := cfg.timeoutFor(role)
		if threshold <= 0 {
			return false, agentStaleness{}
		}
		// Map the daemon's existing per-role timeout onto the verdict's
		// hard ceiling so the dead threshold matches operator
		// expectations even when the verdict's defaults differ. Stale =
		// 1/3, Grace = full timeout — same shape as the polecat-class
		// 3m/10m/20m defaults but scaled to per-role values.
		opts := polecat.LivenessOptions{
			Stale: threshold / 6,
			Grace: threshold / 2,
			Dead:  threshold,
		}
		verdict := polecat.Liveness(d.config.TownRoot, sessionName, opts)
		if verdict.Verdict != polecat.LivenessDead {
			return false, agentStaleness{}
		}
		staleFor := verdict.Age
		if staleFor <= 0 {
			staleFor = time.Since(hb.EffectiveLastKeepalive())
		}
		return true, agentStaleness{source: "heartbeat-verdict-DEAD", staleFor: staleFor, threshold: threshold}
	}

	// Pre-rollout fallback: no heartbeat means we must rely on bead.UpdatedAt
	// at the legacy timeout. This preserves gu-s009 behavior for sessions
	// that haven't run a gt command since cv-p3fem rolled out.
	if cfg.fallbackTimeout <= 0 {
		return false, agentStaleness{}
	}
	if bead.UpdatedAt.IsZero() {
		// Without either signal we can't prove staleness defensibly.
		return false, agentStaleness{}
	}
	staleFor := time.Since(bead.UpdatedAt)
	if staleFor < cfg.fallbackTimeout {
		return false, agentStaleness{}
	}
	return true, agentStaleness{source: "updated_at", staleFor: staleFor, threshold: cfg.fallbackTimeout}
}

// maybeReapDeadAgentBead resets a single witness/refinery wisp if the role's
// tmux session is provably dead and the wisp's staleness signal (heartbeat
// preferred, bead.updated_at fallback) is past the configured threshold.
func (d *Daemon) maybeReapDeadAgentBead(rigName, role, sessionName string, bead agentBeadInfo, cfg agentReapConfig) {
	alive, err := d.tmux.HasSession(sessionName)
	if err != nil {
		// Transient tmux error — err on the side of not reaping.
		return
	}
	if alive {
		return
	}

	eligible, st := d.evaluateAgentStaleness(sessionName, bead, role, cfg)
	if !eligible {
		return
	}

	// TOCTOU guard: re-check session liveness immediately before reset. A role
	// could have been restarted between the initial check and here.
	if alive2, err := d.tmux.HasSession(sessionName); err != nil || alive2 {
		return
	}

	cmd := exec.Command(d.bdPath, "update", bead.ID, "--status=open", "--assignee=") //nolint:gosec // G204: args constructed internally
	setSysProcAttr(cmd)
	cmd.Dir = rigBDWorkingDir(d.config.TownRoot, rigName)
	cmd.Env = append(os.Environ(), "BD_ACTOR=daemon")
	if output, err := cmd.CombinedOutput(); err != nil {
		d.logger.Printf("reap-dead-agent-wisps: failed to reset %s (rig=%s role=%s cwd=%s): %v: %s",
			bead.ID, rigName, role, cmd.Dir, err, strings.TrimSpace(string(output)))
		return
	}

	d.logger.Printf("reap-dead-agent-wisps: reset %s (rig=%s role=%s prev_status=%s session=%s source=%s stale=%v threshold=%v)",
		bead.ID, rigName, role, bead.Status, sessionName, st.source, st.staleFor.Truncate(time.Second), st.threshold)

	_ = events.LogFeed(events.TypeSessionDeath, fmt.Sprintf("%s/%s", rigName, role),
		events.SessionDeathPayload(sessionName, fmt.Sprintf("%s/%s", rigName, role),
			fmt.Sprintf("dead-agent-wisp-reap: bead=%s prev_status=%s source=%s stale=%v (threshold=%v)",
				bead.ID, bead.Status, st.source, st.staleFor.Truncate(time.Second), st.threshold),
			"daemon"))
}
