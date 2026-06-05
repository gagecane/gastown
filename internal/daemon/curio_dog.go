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

	// PageForReal is the Call 2 (gu-2coqj) SHADOW→LIVE safety gate. When false
	// (the MANDATORY default), the lane-ceiling paging engine runs and records
	// what it WOULD page to the shadow ledger + daemon log, but sends NO Overseer
	// page. Flipping this to true is the DELIBERATE second enablement that lets
	// Curio actually wake a human — only after precision + cadence have been
	// observed on the live curio_shadow_page ledger. Mirrors the candidates-only
	// discipline that kept Phase 1 safe.
	PageForReal bool `json:"page_for_real,omitempty"`

	// LLM gates the OFFLINE Retrospect/LLM lane (the curio-proposer binary),
	// which is a SEPARATE switch from Enabled (the live Patrol). Keeping them
	// independent is the kill-switch isolation invariant: curio.llm.enabled=false
	// disables Retrospect without touching Patrol, and vice-versa. The live
	// daemon does not run the LLM lane (it is a standalone binary), so this knob
	// is declarative here and consumed by cmd/curio-proposer.
	LLM *CurioLLMConfig `json:"llm,omitempty"`
}

// CurioLLMConfig holds the Retrospect/LLM lane kill switch.
type CurioLLMConfig struct {
	// Enabled controls whether the curio-proposer (Retrospect) lane runs.
	// Default false: the LLM lane is opt-in, independent of the live Patrol.
	Enabled bool `json:"enabled"`
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

	// Call 1(A) air-gap: the set of beads Curio itself has filed. Empty today —
	// Curio is candidates-only and files no beads (the air-gap stays dormant
	// until filing turns on in a later build). Wired now so the loop-breaker's
	// causal half is exercised end-to-end the moment filing is enabled.
	in.CurioBeads = d.collectCurioFiledBeads()

	// Phase 1b (gu-fcwx8.3) L1 EWMA/MAD detector: advance per-series state
	// with this cycle's observations BEFORE evaluation so Eval reads current
	// state. The detector is a Rule that participates in Evaluate alongside the
	// content rules.
	if d.curioDetector == nil {
		d.curioDetector = curio.NewEWMADetector()
	}
	d.curioDetector.Observe(in.EventCounts)

	rules := append(curio.DefaultRules(), d.curioDetector)
	cands := curio.Evaluate(rules, in)

	// Call 1(C) reaction-count backstop: observe this cycle's candidates so a
	// (rule,target) flapping across cycles gets frozen. The annotation
	// (ReactionCount/Frozen) is consumed by Call 2 (gu-2coqj); build 2a only
	// records it. Runs even on an empty cycle so the tracker ages out quiet
	// findings.
	if d.curioReactions == nil {
		d.curioReactions = curio.NewReactionTracker()
	}
	cands = d.curioReactions.Observe(cands)

	// Call 2 (gu-2coqj) lane-ceiling paging — decided every cycle (even an empty
	// one, so the heartbeat stays fresh and the judgment breaker ages its
	// window). The engine only DECIDES; emitCurioPages records/pages per the
	// SHADOW-mode safety gate.
	if d.curioPaging == nil {
		d.curioPaging = curio.NewPagingEngine()
	}
	actions := d.curioPaging.Decide(cands, now)
	d.emitCurioPages(windowID, actions)

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

	d.logger.Printf("curio: cycle complete — found=%d new=%d paged=%d (candidates only, no beads filed)",
		len(cands), inserted, len(actions))
}
