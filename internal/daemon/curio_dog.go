// Curio self-inspection daemon dog (Phase 1, gu-6s8ao).
//
// Curio is a SIBLING dog to failure_classifier — it reuses the patrol patterns
// (DaemonPatrolConfig gating, doltBreaker, opt-in default-disabled) and the
// shared fingerprint helper, but it does NOT contort the classifier's
// escalation-bead/ack/breaker lifecycle onto itself. Phase 1 emits CANDIDATES
// to the curio_candidate sidecar table in TOWN HQ Dolt — it never files beads,
// runs an LLM, or mutates anything. See internal/curio for the rules engine.
package daemon

import (
	"fmt"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/curio"
)

const defaultCurioInterval = 15 * time.Minute

// CurioConfig holds configuration for the curio patrol.
//
// Opt-in (disabled by default): Phase 1 is candidates-only and must earn its
// way on. Enable explicitly in mayor/daemon.json once the operator wants to
// start accumulating candidates for precision measurement.
type CurioConfig struct {
	// Enabled controls whether the curio patrol runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "15m").
	IntervalStr string `json:"interval,omitempty"`
}

// curioInterval returns the configured interval, or the default (15m).
func curioInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.Curio != nil {
		if config.Patrols.Curio.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.Curio.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultCurioInterval
}

// runCurio is the curio patrol entry point. It collects live normalized
// records, runs the content rules, and writes any candidates to HQ Dolt. It
// NEVER files beads (Phase 1 is candidates-only).
func (d *Daemon) runCurio() {
	if !d.isPatrolActive("curio") {
		return
	}

	// Gate on the shared Dolt circuit breaker, like the sibling classifier.
	if !d.doltBreaker.Allow() {
		d.logger.Printf("curio: dolt-degraded — skipping tick (circuit breaker open)")
		return
	}

	d.logger.Printf("curio: starting patrol cycle")

	// Window ID labels this cycle. Time-based, stamped at collection.
	now := time.Now().UTC()
	windowID := fmt.Sprintf("live/%s", now.Format(time.RFC3339))

	// Gather the merged-not-landed rule's bead source (requires bead Dolt
	// access, which curio deliberately does not import) and inject it along
	// with the per-rig git ancestry resolver. The filesystem collectors (rate,
	// logs, admissions) are wired inside CollectInputWith from townRoot.
	opts := curio.CollectOptions{
		Start:             now.Add(-24 * time.Hour),
		End:               now,
		MergedBeadSources: [][]curio.MergedBeadObservation{d.collectMergedBeadObservations()},
		Ancestry: curio.GitAncestryResolver(func(rig string) string {
			return beads.GetRigDirForName(d.config.TownRoot, rig)
		}),
	}

	in, err := curio.CollectInputWith(d.config.TownRoot, windowID, opts)
	if err != nil {
		d.logger.Printf("curio: collect failed: %v", err)
		return
	}

	cands := curio.Evaluate(curio.DefaultRules(), in)
	if len(cands) == 0 {
		d.logger.Printf("curio: cycle complete — no candidates")
		return
	}

	store, err := curio.OpenStore("127.0.0.1", d.doltServerPort(), "hq")
	if err != nil {
		d.doltBreaker.Record(err)
		d.logger.Printf("curio: failed to open HQ store: %v", err)
		return
	}
	defer func() { _ = store.Close() }()

	inserted, err := store.InsertCandidates(cands)
	d.doltBreaker.Record(err)
	if err != nil {
		d.logger.Printf("curio: failed to write candidates: %v", err)
		return
	}

	d.logger.Printf("curio: cycle complete — found=%d new=%d (candidates only, no beads filed)",
		len(cands), inserted)
}
