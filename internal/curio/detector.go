package curio

import (
	"fmt"
	"math"
)

// L1 statistical anomaly detector (Phase 1b, gu-fcwx8.3).
//
// The existing rateSpikeRule (c) uses FIXED thresholds per series. This
// detector replaces that static ceiling with an adaptive one: an EWMA (Exp.
// Weighted Moving Average) tracks the running mean, and an EWMA of the
// absolute deviation (MAD-style) tracks the spread. A series fires when:
//
//   observed > ewma + k * deviation   (upper anomaly)
//
// The detector maintains per-series state across patrol cycles (held by the
// daemon, like ReactionTracker). It implements the Rule interface so it plugs
// into Evaluate()+DefaultRules() without any plumbing changes.
//
// Tracked series (same 8 from Phase 0 baselines):
//   dispatch.stuck_agent, escalation, sched_fail, sling, done, mail,
//   bead.open, bead.close
//
// Design constraints:
//   - Pure during Eval (no I/O) — state is updated externally via Observe().
//   - Deterministic given the same state + input (floats are IEEE 754).
//   - Populates EWMA/Deviation on the Candidate so the store captures them.

const (
	// defaultAlpha is the EWMA smoothing factor. 0.3 means ~30% weight on the
	// latest observation, ~70% on history. Chosen for the curio patrol cadence
	// (15min cycles): responsive enough to catch a sustained shift within 3-4
	// cycles, smooth enough to ignore a single-cycle blip.
	defaultAlpha = 0.3

	// defaultK is the anomaly sensitivity multiplier. A series fires when
	// observed > ewma + k*deviation. k=3 is the standard "3-sigma" threshold
	// adapted for MAD-scale (MAD ~ 0.8σ for normal data, so k=3 on MAD is
	// roughly a 2.4σ event — intentionally sensitive for Phase 1b where
	// candidates are non-paging).
	defaultK = 3.0

	// minDeviation is the floor for deviation to avoid divide-by-zero-like
	// sensitivity issues when a series is flat (deviation→0 would make any
	// +1 observation an "anomaly"). Set to 1.0 so a one-count fluctuation on
	// a perfectly steady series is not anomalous.
	minDeviation = 1.0

	// warmupCycles is how many observations are needed before the detector
	// fires. Below this count the EWMA/MAD estimates are unstable and would
	// produce false positives on normal ramp-up traffic.
	warmupCycles = 5
)

// detectorSeries are the 8 series the L1 detector tracks. This is the
// authoritative set — the fixed rateThresholds remain for backward compat
// (the content rate rule still operates as a hard ceiling alongside this).
var detectorSeries = map[string]bool{
	"dispatch.stuck_agent": true,
	"escalation":           true,
	"sched_fail":           true,
	"sling":                true,
	"done":                 true,
	"mail":                 true,
	"bead.open":            true,
	"bead.close":           true,
}

// seriesState holds the EWMA/MAD running estimates for one series.
type seriesState struct {
	EWMA      float64 // exponentially weighted moving average
	Deviation float64 // EWMA of |observed - ewma| (MAD-style)
	Count     int     // number of observations (for warmup gate)
}

// EWMADetector is the L1 statistical anomaly detector. It is held by the
// daemon across patrol cycles and fed each cycle's event counts via Observe().
// It implements Rule so it participates in Evaluate() like any content rule.
//
// Usage pattern (in daemon):
//
//	if d.curioDetector == nil {
//	    d.curioDetector = curio.NewEWMADetector()
//	}
//	d.curioDetector.Observe(in.EventCounts)
//	cands := curio.Evaluate(curio.DefaultRules(), in)
//
// Observe() updates state; Eval() reads state. They are separate because Eval
// must be pure (replay-gradeable), while Observe is the impure state-advance
// step run only in the live daemon.
type EWMADetector struct {
	alpha  float64
	k      float64
	states map[string]*seriesState
}

// NewEWMADetector creates a detector with default parameters.
func NewEWMADetector() *EWMADetector {
	return &EWMADetector{
		alpha:  defaultAlpha,
		k:      defaultK,
		states: make(map[string]*seriesState),
	}
}

// Observe advances the per-series EWMA/MAD state with this cycle's event
// counts. Series not present in counts are treated as observed=0 (a series
// going silent is meaningful — the detector will notice if traffic drops then
// spikes). Only tracked series (detectorSeries) are updated.
//
// Call Observe() BEFORE Evaluate() each cycle so the detector's state reflects
// the current observation when Eval reads it.
func (d *EWMADetector) Observe(counts []SeriesCount) {
	// Build observed map from this cycle's counts.
	observed := make(map[string]float64)
	for _, c := range counts {
		if detectorSeries[c.Series] {
			observed[c.Series] = float64(c.Observed)
		}
	}

	// Update every tracked series (even if not observed this cycle = 0).
	for series := range detectorSeries {
		obs := observed[series]
		st, ok := d.states[series]
		if !ok {
			// First observation: seed EWMA at the observed value so there's no
			// startup-spike artifact.
			d.states[series] = &seriesState{
				EWMA:      obs,
				Deviation: 0,
				Count:     1,
			}
			continue
		}
		st.Count++
		// EWMA update: ewma_new = α * obs + (1-α) * ewma_old
		st.EWMA = d.alpha*obs + (1-d.alpha)*st.EWMA
		// MAD update: dev_new = α * |obs - ewma_old| + (1-α) * dev_old
		// Note: we use the PRE-update EWMA for the deviation calc (obs vs.
		// prediction), then update. Since EWMA was already updated above, we
		// recalculate: the old EWMA was (st.EWMA - d.alpha*obs) / (1-d.alpha),
		// but it's simpler to just use the absolute difference against the new
		// EWMA (standard EWMA-of-abs-diff formulation).
		absDiff := math.Abs(obs - st.EWMA)
		st.Deviation = d.alpha*absDiff + (1-d.alpha)*st.Deviation
	}
}

// State returns the current detector state for a series (for testing/debug).
// Returns zero values and false if the series is not tracked or not yet observed.
func (d *EWMADetector) State(series string) (ewma, deviation float64, count int, ok bool) {
	st, exists := d.states[series]
	if !exists {
		return 0, 0, 0, false
	}
	return st.EWMA, st.Deviation, st.Count, true
}

// --- Rule interface implementation ---

func (d *EWMADetector) ID() string { return "ewma_anomaly" }

// Eval checks each tracked series' current observation against the adaptive
// threshold (ewma + k*deviation). Only fires after warmup and for observations
// that exceed the upper bound. This is a READ of the detector's state — the
// state is advanced by Observe(), not here (Eval stays pure for replay).
func (d *EWMADetector) Eval(in Input) []Candidate {
	var out []Candidate
	for _, c := range in.EventCounts {
		if !detectorSeries[c.Series] {
			continue
		}
		if in.suppressed(c.FiledBy, c.causalProvenance) {
			continue
		}
		// Call 1(A) air-gap: never detect Curio's own telemetry series.
		if isCurioSeries(c.Series) {
			continue
		}

		st, ok := d.states[c.Series]
		if !ok || st.Count < warmupCycles {
			continue // not enough history to judge
		}

		obs := float64(c.Observed)
		deviation := math.Max(st.Deviation, minDeviation)
		threshold := st.EWMA + d.k*deviation

		if obs > threshold {
			summary := fmt.Sprintf("series %q observed=%d exceeds adaptive threshold %.1f (ewma=%.1f, dev=%.1f, k=%.1f)",
				c.Series, c.Observed, threshold, st.EWMA, deviation, d.k)
			cand := newCandidate(in.Window.ID, d.ID(), c.Series, "", c.Series, c.Observed, summary)
			cand.EWMA = st.EWMA
			cand.Deviation = deviation
			out = append(out, cand)
		}
	}
	return out
}
