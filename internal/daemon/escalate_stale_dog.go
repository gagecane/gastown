package daemon

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// escalate_stale_dog periodically invokes the stale-escalation re-routing
// mechanism (`gt escalate stale`) — closing the gap from gu-2sepi where
// runEscalateStale existed and worked but was only ever reachable from the CLI.
// No daemon/scheduler/cron/formula caller invoked it, so the entire
// stale-escalation machinery (stale_threshold, max_reescalations, severity bump
// to email:human/sms:human) was dead unless a human ran the command by hand.
//
// The failure that motivates it: an escalation is created and mailed to the
// Mayor, then never acknowledged because the Mayor is offline, crashed, or
// missed the mail. With nothing re-escalating it, the escalation sits open
// forever at its original severity — the load-bearing "escalation is never
// orphaned" guarantee silently does not hold.
//
//	escalation created, mailed to Mayor
//	      │  Mayor offline/crashed/missed mail → never acked
//	      ▼
//	escalate_stale_dog (this patrol)              ← driver (NEW, gu-2sepi)
//	      │  invokes `gt escalate stale` on the threshold cadence
//	      ▼
//	runEscalateStale bumps severity + re-routes to higher targets
//
// Implementation mirrors scheduled_maintenance: the dog shells out to the
// existing, tested `gt escalate stale` CLI rather than re-implementing the
// re-escalation logic in-process. That keeps a single code path for stale
// re-escalation (CLI and daemon behave identically) and avoids duplicating the
// config-load / mail-route / dedup handling.
//
// Cadence: `gt escalate stale` has no per-escalation cooldown beyond
// max_reescalations — ListStaleEscalations keys purely on the bead's createdAt
// vs the stale_threshold, so every invocation that finds a still-stale,
// not-yet-acked escalation bumps its severity one step (until max_reescalations
// caps it). The tick interval is therefore the severity-walk cadence, so it
// defaults to the same 4h as the default stale_threshold (gu-2sepi suggested
// "at the stale_threshold cadence"). Operators who tune stale_threshold should
// set this patrol's interval to match.
const (
	defaultEscalateStaleInterval = 4 * time.Hour
	escalateStaleSource          = "escalate_stale_dog"
)

// EscalateStaleConfig holds configuration for the escalate_stale patrol.
type EscalateStaleConfig struct {
	// Enabled controls whether the stale-escalation driver runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to invoke `gt escalate stale`, as a string
	// (e.g., "4h"). Should track the configured stale_threshold.
	IntervalStr string `json:"interval,omitempty"`
}

// escalateStaleInterval returns the configured interval, or the default (4h).
func escalateStaleInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.EscalateStale != nil {
		if config.Patrols.EscalateStale.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.EscalateStale.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultEscalateStaleInterval
}

// runEscalateStaleDog invokes `gt escalate stale` to re-escalate any
// escalations that have gone stale (unacked past the stale_threshold). The
// command itself loads the threshold and routing config, bumps severity, and
// re-routes to the higher-severity targets; this dog's only job is to call it
// on a cadence so the mechanism is reachable without a human.
func (d *Daemon) runEscalateStaleDog() {
	if !d.isPatrolActive("escalate_stale") {
		return
	}

	// Gate on the shared Dolt circuit breaker: `gt escalate stale` queries
	// escalation beads (Dolt). When Dolt is degraded, skip and resume next tick
	// rather than piling subprocess work onto a struggling server.
	if d.doltBreaker != nil && !d.doltBreaker.Allow() {
		d.logger.Printf("escalate_stale: dolt-degraded — skipping tick (circuit breaker open)")
		return
	}

	ctx, cancel := context.WithTimeout(d.ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.gtPath, "escalate", "stale") //nolint:gosec // G204: args are static
	cmd.Dir = d.config.TownRoot
	cmd.Env = append(os.Environ(), "BD_ACTOR=daemon")
	setSysProcAttr(cmd)

	output, err := cmd.CombinedOutput()
	if d.doltBreaker != nil {
		d.doltBreaker.Record(err)
	}
	if err != nil {
		d.logger.Printf("escalate_stale: gt escalate stale failed: %v\nOutput: %s", err, strings.TrimSpace(string(output)))
		return
	}

	// Log a concise summary. `gt escalate stale` prints either "No stale
	// escalations ..." or a "🔄 Re-escalated N ..." block; surface the first
	// line so the daemon log records what happened without the full body.
	summary := strings.TrimSpace(string(output))
	if idx := strings.IndexByte(summary, '\n'); idx >= 0 {
		summary = summary[:idx]
	}
	if summary != "" {
		d.logger.Printf("escalate_stale: %s", summary)
	}
}
