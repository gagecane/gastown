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
	"github.com/steveyegge/gastown/internal/session"
)

// reapDeadAgentWisps resets in_progress/hooked beads assigned to non-polecat
// rig agents (witness, refinery) whose tmux sessions are dead and whose hooked
// wisp has not been updated for longer than the configured timeout.
//
// Why this exists (gu-s009): when a witness or refinery tmux session dies or
// restarts unexpectedly mid-patrol, its hooked patrol wisp stays HOOKED to it
// forever. The role's next patrol cycle never starts because the prior wisp is
// still hooked, freezing the rig's patrol cadence. The polecat-equivalent
// reaper (reapDeadPolecatWisps, see gu-1x0j) only handles polecats: it requires
// a polecats/<name>/ directory and a heartbeat file, neither of which witness
// or refinery roles produce. Stuck witness/refinery wisps therefore fall
// through, accumulate, and require manual intervention.
//
// The reap is conservative by design:
//   - Only handles assignees of the form "<rig>/witness" or "<rig>/refinery".
//     Polecat wisps are left to reapDeadPolecatWisps; crew wisps are not in
//     scope (crew is a persistent worker model with different cadence).
//   - Requires the tmux session for the role to be GONE. A live session, even
//     with a stale wisp, is not reaped — it might just be slow and the next
//     patrol cycle will resolve naturally.
//   - Requires bead.updated_at to be older than the timeout. Witness and
//     refinery don't have heartbeat files, but they do touch bd at the start
//     of each patrol cycle (claim, status updates), so updated_at is a
//     reasonable staleness proxy.
//   - TOCTOU guard: re-checks tmux session liveness immediately before reset.
func (d *Daemon) reapDeadAgentWisps() {
	opCfg := d.loadOperationalConfig().GetDaemonConfig()
	timeout := opCfg.DeadAgentReapTimeoutD()
	if timeout <= 0 {
		// Explicitly disabled via config — preserves the operator escape hatch.
		return
	}

	d.rigPool.runPerRig(d.ctx, d.getKnownRigs(), func(ctx context.Context, rigName string) error {
		d.reapRigDeadAgentWisps(rigName, timeout)
		return nil
	})
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
func (d *Daemon) reapRigDeadAgentWisps(rigName string, timeout time.Duration) {
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

		d.maybeReapDeadAgentBead(rigName, role, sessionName, bead, timeout)
	}
}

// maybeReapDeadAgentBead resets a single witness/refinery wisp if the role's
// tmux session is provably dead and the bead has been idle longer than the
// timeout.
func (d *Daemon) maybeReapDeadAgentBead(rigName, role, sessionName string, bead agentBeadInfo, timeout time.Duration) {
	alive, err := d.tmux.HasSession(sessionName)
	if err != nil {
		// Transient tmux error — err on the side of not reaping.
		return
	}
	if alive {
		return
	}

	// updated_at must be at least `timeout` old. If it's missing or in the
	// future (clock skew), refuse to reap defensively.
	if bead.UpdatedAt.IsZero() {
		return
	}
	staleFor := time.Since(bead.UpdatedAt)
	if staleFor < timeout {
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

	d.logger.Printf("reap-dead-agent-wisps: reset %s (rig=%s role=%s prev_status=%s session=%s bead_stale=%v threshold=%v)",
		bead.ID, rigName, role, bead.Status, sessionName, staleFor.Truncate(time.Second), timeout)

	_ = events.LogFeed(events.TypeSessionDeath, fmt.Sprintf("%s/%s", rigName, role),
		events.SessionDeathPayload(sessionName, fmt.Sprintf("%s/%s", rigName, role),
			fmt.Sprintf("dead-agent-wisp-reap: bead=%s prev_status=%s bead_stale=%v (threshold=%v)",
				bead.ID, bead.Status, staleFor.Truncate(time.Second), timeout),
			"daemon"))
}
