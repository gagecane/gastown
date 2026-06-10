package daemon

import (
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/witness"
)

// agent_heartbeat_dog scans every rig's refinery+witness heartbeats from the
// daemon and escalates STALE_RIG_AGENT to mayor when one is too old. It closes
// the "who watches the watchers" gap (gu-tl2gs / gc-23qatv): a wedged witness
// cannot escalate its own staleness, so per-rig witness patrols miss the case
// where the witness itself is the failure. The daemon process IS the watcher
// for that case — its main loop is the only liveness signal that survives a
// fully wedged rig.
//
// Why a daemon dog (option C from the survey) rather than rig-local mutual
// monitoring (option B) or a separate process (option A): the daemon already
// has the right architectural slot (daemon-resident, dog plugin, Dolt circuit
// breaker, escalation pipeline). Adds one ticker, one function, no new
// process, no new IPC.
//
// State sharing with the per-rig witness: this dog reuses
// witness.DetectStaleRigAgentHeartbeats, which writes its dedup state under
// .runtime/stale_rig_agent/<rig>__<session>.json (gu-z8qzq cooldown) and the
// town-wide correlation log under .runtime/stale_rig_agent/_correlation.json
// (gu-nejgh). Whichever scanner — the witness for its own rig or this dog
// scanning every rig — runs first records state; the other sees it and folds
// into "skip-cooldown" / "skip-correlated". The two paths cooperate; they do
// not double-escalate.
//
// The daemon is NOT a tmux session, so it passes selfSession="" — there is no
// own-heartbeat to skip. The merge-queue prober is built per-rig so the
// gs-ecdg idle-empty-mq suppression still applies to a healthily-idle
// refinery.
//
//	rig witness wedged                                    ← failure mode
//	      │  cannot run its own DetectStaleRigAgent
//	      ▼
//	agent_heartbeat_dog (this patrol)                     ← fallback (NEW, gu-tl2gs)
//	      │  scans every rig's heartbeats from the daemon
//	      ▼
//	witness.DetectStaleRigAgentHeartbeats per-rig         ← shared core
//	      │  same cooldown/correlation/mq-suppression
//	      ▼
//	STALE_RIG_AGENT mail to mayor (or skip-* no-op)

const (
	defaultAgentHeartbeatInterval = 5 * time.Minute
	agentHeartbeatPatrolName      = "agent_heartbeat"
)

// AgentHeartbeatConfig holds configuration for the agent_heartbeat patrol.
type AgentHeartbeatConfig struct {
	// Enabled controls whether the agent-heartbeat monitor runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "5m").
	IntervalStr string `json:"interval,omitempty"`
}

// agentHeartbeatInterval returns the configured interval, or the default (5m).
func agentHeartbeatInterval(cfg *DaemonPatrolConfig) time.Duration {
	if cfg != nil && cfg.Patrols != nil && cfg.Patrols.AgentHeartbeat != nil {
		if cfg.Patrols.AgentHeartbeat.IntervalStr != "" {
			if d, err := time.ParseDuration(cfg.Patrols.AgentHeartbeat.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultAgentHeartbeatInterval
}

// daemonMRProber adapts a per-rig beads handle to witness.MergeQueueProber so
// the daemon-side scan applies the same gs-ecdg idle-empty-mq suppression as
// the witness-side scan in patrol_scan.go.
type daemonMRProber struct {
	// resolve maps a rig name to a handle that can list the rig's MRs. Built
	// from the rigs registry once per tick so a rig added/removed between ticks
	// is picked up on the next cycle.
	resolve map[string]daemonMRLister
}

// daemonMRLister is the narrow surface daemonMRProber needs from beads. Mirrors
// internal/cmd's mrLister but kept package-local to avoid pulling cmd into
// daemon.
type daemonMRLister interface {
	ListMergeRequests(opts beads.ListOptions) ([]*beads.Issue, error)
}

// PendingMergeRequestCount returns the number of actionable (open, unblocked)
// MRs in the rig's queue. A rig the prober does not know about returns
// (0, nil) — defensively treats unknown rigs as "no queue", which leaves the
// idle-empty-mq suppression to the rig-local witness if and when it scans;
// the daemon dog will not suppress on a rig it cannot prove is empty.
func (p daemonMRProber) PendingMergeRequestCount(rigName string) (int, error) {
	lister, ok := p.resolve[rigName]
	if !ok {
		// Treat as "non-empty" — return a positive count so the witness
		// detector falls through and escalates rather than falsely
		// suppressing on a rig we couldn't probe. Conservative: a stale
		// agent should escalate even when our probe is uncertain.
		return 1, nil
	}
	issues, err := lister.ListMergeRequests(beads.ListOptions{
		Label:    "gt:merge-request",
		Status:   "open",
		Priority: -1,
	})
	if err != nil {
		return 0, err
	}
	// Filter to MRs that target this rig. We can't import cmd's
	// pendingMRsForRig (would create a daemon→cmd cycle); inline the
	// minimum logic.
	count := 0
	for _, issue := range issues {
		if issue == nil || issue.Status != "open" {
			continue
		}
		if len(issue.BlockedBy) > 0 || issue.BlockedByCount > 0 {
			continue
		}
		fields := beads.ParseMRFields(issue)
		if fields == nil {
			// Unscoped MR — count it. Same conservative choice as
			// cmd.pendingMRsForRig.
			count++
			continue
		}
		if fields.Rig != "" && !equalFold(fields.Rig, rigName) {
			continue
		}
		count++
	}
	return count, nil
}

// equalFold is a small case-insensitive compare to keep this file free of a
// strings import for one call site.
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// runAgentHeartbeatDog is the main patrol function. It enumerates every
// registered rig and runs the shared witness staleness detector for each,
// letting the cooldown + correlation gates fold daemon-side observations into
// the same threads as witness-side observations.
func (d *Daemon) runAgentHeartbeatDog() {
	if !d.isPatrolActive(agentHeartbeatPatrolName) {
		return
	}

	// Gate on the shared Dolt circuit breaker: this scan touches the rig's
	// beads DB through the MR prober. When Dolt is degraded, skip and resume
	// next tick. The next tick will re-scan; staleness only grows.
	if d.doltBreaker != nil && !d.doltBreaker.Allow() {
		d.logger.Printf("agent_heartbeat: dolt-degraded — skipping tick (circuit breaker open)")
		return
	}

	rigsConfig, err := d.loadRigsConfig()
	if d.doltBreaker != nil {
		d.doltBreaker.Record(err)
	}
	if err != nil {
		d.logger.Printf("agent_heartbeat: failed to load rigs config: %v", err)
		return
	}
	if rigsConfig == nil || len(rigsConfig.Rigs) == 0 {
		return
	}

	op := config.LoadOperationalConfig(d.config.TownRoot)
	wt := op.GetWitnessConfig()
	staleThreshold := wt.StaleRigAgentHeartbeatD()
	if staleThreshold <= 0 {
		// Operator opt-out on the witness threshold also disables the daemon
		// dog. Same control surface, no surprise re-enabling from a different
		// path.
		return
	}
	cooldown := wt.StaleRigAgentNotifyCooldownD()
	correlation := wt.StaleRigAgentCorrelationWindowD()

	router := mail.NewRouterWithTownRoot(d.config.TownRoot, d.config.TownRoot)
	prober := d.buildAgentHeartbeatProber(rigsConfig)

	for rigName := range rigsConfig.Rigs {
		// selfSession="" — the daemon is not a tmux session.
		res := witness.DetectStaleRigAgentHeartbeats(
			d.config.TownRoot, rigName, router,
			staleThreshold, "", cooldown, correlation, prober,
		)
		if res == nil {
			continue
		}
		// Surface escalations / errors at INFO so operators can correlate this
		// dog's actions against witness-side actions in daemon.log without
		// chasing two log streams.
		for _, s := range res.Stale {
			switch s.Action {
			case "escalated":
				d.logger.Printf("agent_heartbeat: %s/%s escalated (heartbeat_age=%s session_alive=%v missing=%v)",
					rigName, s.AgentRole, s.HeartbeatAge.Round(time.Second), s.SessionAlive, s.HeartbeatMissing)
			case "skip-correlated":
				d.logger.Printf("agent_heartbeat: %s/%s folded into correlation thread %s",
					rigName, s.AgentRole, s.CorrelatedInto)
			}
			if s.Error != nil {
				d.logger.Printf("agent_heartbeat: %s/%s scan error: %v", rigName, s.AgentRole, s.Error)
			}
		}
	}
}

// buildAgentHeartbeatProber constructs a per-rig MR-count prober from the
// rigs registry. A rig that fails to resolve is omitted; PendingMergeRequestCount
// then returns the conservative "non-empty" sentinel for it (so the witness
// detector escalates instead of suppressing).
func (d *Daemon) buildAgentHeartbeatProber(rigsConfig *config.RigsConfig) witness.MergeQueueProber {
	resolve := make(map[string]daemonMRLister, len(rigsConfig.Rigs))
	for rigName := range rigsConfig.Rigs {
		// Mirror rig.NewManager's path layout: <townRoot>/<rigName>. Avoid
		// constructing a full rig.Manager (it pulls a git.Git in) since we only
		// need BeadsPath, which equals the rig path.
		rigPath := filepath.Join(d.config.TownRoot, rigName)
		// rig.Rig.BeadsPath() returns r.Path; build a minimal Rig to get the
		// same path resolution semantics in case the helper grows logic later.
		r := &rig.Rig{Name: rigName, Path: rigPath}
		resolve[rigName] = beads.New(r.BeadsPath())
	}
	return daemonMRProber{resolve: resolve}
}
