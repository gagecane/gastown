package curio

// Call 2 (gu-2coqj): the lane-ceiling paging engine.
//
// Build 2a turned every finding into a candidate row and annotated it with the
// two suppression signals this build consumes: Verifiable() (Call 3, a
// syscall-level self-confirmation) and Frozen (Call 1C, a cross-cycle flap
// freeze). Call 2 is the first code that decides whether a finding is worth
// WAKING A HUMAN, and routes it down one of two lanes:
//
//   LaneVerified  — the finding carries a cheap, deterministic syscall verifier
//                   (today: dead_owner_admission via internal/liveness). The
//                   engine re-probes it LIVE; a still-holding finding is true by
//                   construction, so this lane is UNCAPPED and never trips the
//                   breaker. A burst of still-holding verified findings in one
//                   cycle COALESCES into a single page carrying the proof list.
//
//   LaneJudgment  — the finding has no self-verifier (merged_not_landed,
//                   kill_signal, rate_spike). It cannot prove itself, so it is
//                   gated by a 3-state circuit breaker on the GLOBAL judgment-lane
//                   rate: sustained (>= judgmentCeiling in judgmentWindow) OR
//                   bursty (>= judgmentBurstCeiling in judgmentBurstWindow). On
//                   breach the breaker TRIPS CLOSED — it emits exactly ONE
//                   dedup-keyed escalation and then latches, bumping a live
//                   occurrence counter on subsequent cycles. Manual reset only.
//
// A Frozen candidate (Call 1C) is kept OUT of both paging lanes: a finding that
// flaps faster than the world it describes is reacting to itself, and must never
// reach a human until it stabilizes.
//
// SAFETY GATE: this engine only DECIDES. It performs no I/O and never pages. The
// daemon emitter consumes the returned actions and, in SHADOW MODE (the
// mandatory default), logs + ledgers what it WOULD page without paging. Real
// paging is a deliberate second enablement (CurioConfig.PageForReal). Like the
// reaction tracker, the engine holds cross-cycle state and is touched only from
// the serial curio patrol goroutine, so it needs no locking.

import (
	"fmt"
	"sort"
	"time"

	"github.com/steveyegge/gastown/internal/fingerprint"
)

// Lane is the paging lane a candidate routes to.
type Lane int

const (
	// LaneNone means the candidate never pages (it is Frozen / suppressed).
	LaneNone Lane = iota
	// LaneVerified is the syscall-verified fast path: uncapped, coalesced, and
	// it never trips the judgment breaker.
	LaneVerified
	// LaneJudgment is the unverifiable lane, gated by the judgment breaker.
	LaneJudgment
)

func (l Lane) String() string {
	switch l {
	case LaneVerified:
		return "verified"
	case LaneJudgment:
		return "judgment"
	default:
		return "none"
	}
}

// ClassifyLane decides which paging lane a candidate belongs to. A Frozen
// candidate is suppressed from paging entirely (LaneNone); a Verifiable one
// rides the verified fast path; everything else is judgment-lane.
func ClassifyLane(c Candidate) Lane {
	if c.Frozen {
		return LaneNone
	}
	if c.Verifiable() {
		return LaneVerified
	}
	return LaneJudgment
}

// Paging-breaker tuning. The ceilings are the design's Call 2 lane-ceiling
// values; window sizes bound the rolling occurrence history.
const (
	// judgmentWindow is the rolling window for the sustained ceiling.
	judgmentWindow = 60 * time.Minute
	// judgmentCeiling is the sustained trip threshold: >= this many judgment-lane
	// occurrences within judgmentWindow trips the breaker.
	judgmentCeiling = 6
	// judgmentBurstWindow is the rolling window for the burst ceiling.
	judgmentBurstWindow = 5 * time.Minute
	// judgmentBurstCeiling is the burst trip threshold: >= this many judgment-lane
	// occurrences within judgmentBurstWindow trips the breaker.
	judgmentBurstCeiling = 3
	// cascadeClusterThreshold grades severity: STRICTLY MORE than this many
	// distinct clusters (StateHashes) in the window means a cross-finding cascade
	// (CRITICAL); at-or-below means a single misfiring finding (HIGH).
	cascadeClusterThreshold = 3
)

// BreakerState is the judgment-lane breaker's 3-state mode.
type BreakerState int

const (
	// BreakerArmed is the normal state: occurrences accumulate toward the ceiling
	// and no page has been raised.
	BreakerArmed BreakerState = iota
	// BreakerTripped is the firing edge: the cycle on which a ceiling was breached
	// and the single escalation was raised. It exists for exactly one transition
	// before latching CLOSED, so "trips closed" is observable as Armed→Tripped→Closed.
	BreakerTripped
	// BreakerClosed is the latched state: the gate is shut, no new escalation is
	// raised, and further occurrences only bump the live occurrence counter on the
	// open escalation. Manual reset (Reset) is the only exit.
	BreakerClosed
)

func (s BreakerState) String() string {
	switch s {
	case BreakerTripped:
		return "tripped"
	case BreakerClosed:
		return "closed"
	default:
		return "armed"
	}
}

// ActionKind is the kind of paging action the engine emits for a cycle.
type ActionKind int

const (
	// ActionVerifiedPage coalesces this cycle's newly-confirmed verified findings
	// into one page carrying the proof list. Uncapped.
	ActionVerifiedPage ActionKind = iota
	// ActionJudgmentTrip is the single escalation raised when the judgment breaker
	// trips CLOSED. Severity is graded by distinct-cluster count.
	ActionJudgmentTrip
	// ActionJudgmentBump is raised while the breaker is latched CLOSED and new
	// judgment occurrences keep arriving: it bumps the live occurrence counter on
	// the already-open escalation rather than raising a new bead.
	ActionJudgmentBump
)

func (k ActionKind) String() string {
	switch k {
	case ActionVerifiedPage:
		return "verified_page"
	case ActionJudgmentTrip:
		return "judgment_trip"
	case ActionJudgmentBump:
		return "judgment_bump"
	default:
		return "unknown"
	}
}

// PageAction is one decision the engine made this cycle. The daemon emitter
// turns it into a shadow-ledger row (always) and, only when PageForReal is set,
// a durable bead + Overseer page.
type PageAction struct {
	// Kind is what to do (verified page / judgment trip / judgment bump).
	Kind ActionKind
	// Lane is the lane this action came from.
	Lane Lane
	// Severity is the graded escalation severity ("critical"/"high"). The verified
	// lane always pages CRITICAL (a confirmed leak is real and human-actionable);
	// the judgment lane grades by cluster count.
	Severity string
	// DedupKey is the stable escalation signature, so a re-fire bumps the existing
	// bead instead of creating a duplicate.
	DedupKey string
	// Summary is a one-line human-readable description.
	Summary string
	// Proof is the supporting evidence: for the verified lane, the per-finding
	// confirmation lines; for the judgment lane, the distinct cluster summaries.
	Proof []string
	// Occurrences is the live occurrence counter (verified: confirmed-finding
	// count this cycle; judgment: total occurrences in the window).
	Occurrences int
	// Clusters is the distinct-cluster (StateHash) count driving judgment grading.
	Clusters int
}

// occurrence is one judgment-lane finding seen at a point in time, retained for
// the rolling ceiling windows.
type occurrence struct {
	at        time.Time
	stateHash string
}

// PagingEngine is the cross-cycle Call 2 lane-ceiling engine. It is held by the
// daemon (one per process) and Decide()d once per patrol cycle on the serial
// patrol goroutine, so it needs no locking — mirroring ReactionTracker.
type PagingEngine struct {
	// state is the judgment breaker's 3-state mode.
	state BreakerState
	// occurrences is the rolling judgment-lane occurrence history (pruned to
	// judgmentWindow on every Decide).
	occurrences []occurrence
	// pagedVerified remembers which verified StateHashes have already been paged,
	// so a still-holding leak re-confirmed every cycle pages ONCE, not forever.
	pagedVerified map[string]bool
	// dedupKey is the escalation signature for the currently-open judgment trip,
	// reused by bumps so they target the same bead.
	dedupKey string
}

// NewPagingEngine returns an armed engine ready for the first cycle.
func NewPagingEngine() *PagingEngine {
	return &PagingEngine{state: BreakerArmed, pagedVerified: map[string]bool{}}
}

// State reports the judgment breaker's current mode (for observability/tests).
func (e *PagingEngine) State() BreakerState { return e.state }

// Reset returns the judgment breaker to Armed and clears the occurrence history
// and open dedup key. This is the MANUAL reset the design mandates — there is no
// automatic recovery, because a tripped judgment lane means a human still needs
// to look. The verified-lane page memory is preserved (a still-holding leak
// should not re-page just because the operator reset the judgment breaker).
func (e *PagingEngine) Reset() {
	e.state = BreakerArmed
	e.occurrences = nil
	e.dedupKey = ""
}

// Decide consumes one cycle's annotated candidates and the current time, and
// returns the paging actions for the cycle. It calls Verify() LIVE on verified
// candidates (the only impure step), so it must run on the live patrol path and
// never during replay.
//
// Ordering of actions is stable: the verified page (if any) precedes any
// judgment action, and there is at most one judgment action per cycle.
func (e *PagingEngine) Decide(cands []Candidate, now time.Time) []PageAction {
	if e.pagedVerified == nil {
		e.pagedVerified = map[string]bool{}
	}

	var actions []PageAction

	// --- Verified lane: re-probe live, coalesce newly-confirmed findings. ---
	if vp := e.decideVerified(cands); vp != nil {
		actions = append(actions, *vp)
	}

	// --- Judgment lane: feed the rolling breaker. ---
	if jp := e.decideJudgment(cands, now); jp != nil {
		actions = append(actions, *jp)
	}

	return actions
}

// decideVerified re-probes the verified-lane candidates and coalesces the ones
// that STILL HOLD into a single page. Returns nil when nothing new confirms.
func (e *PagingEngine) decideVerified(cands []Candidate) *PageAction {
	// Which verified findings are present (in any state) this cycle, and which
	// still hold after the live re-probe.
	present := map[string]bool{}
	var holding []Candidate
	for _, c := range cands {
		if ClassifyLane(c) != LaneVerified {
			continue
		}
		present[c.stateKey()] = true
		if !c.Verify() { // live syscall re-probe; refuted findings are dropped
			continue
		}
		holding = append(holding, c)
	}

	// Forget paged-memory for any previously-paged finding that is no longer
	// present this cycle: a leak that cleared and later recurs should page again.
	for k := range e.pagedVerified {
		if !present[k] {
			delete(e.pagedVerified, k)
		}
	}

	if len(holding) == 0 {
		return nil
	}

	// Stable order so the coalesced page and dedup key are deterministic.
	sort.Slice(holding, func(i, j int) bool { return holding[i].stateKey() < holding[j].stateKey() })

	// Only page when at least one confirmed finding has not been paged before —
	// a leak that still holds cycle after cycle pages once, then goes quiet until
	// it clears and re-appears.
	hasNew := false
	proof := make([]string, 0, len(holding))
	keys := make([]string, 0, len(holding))
	for _, c := range holding {
		k := c.stateKey()
		if !e.pagedVerified[k] {
			hasNew = true
		}
		e.pagedVerified[k] = true
		keys = append(keys, k)
		proof = append(proof, c.Summary)
	}
	if !hasNew {
		return nil
	}

	return &PageAction{
		Kind:        ActionVerifiedPage,
		Lane:        LaneVerified,
		Severity:    "critical",
		DedupKey:    "curio:verified:" + fingerprintKeys(keys),
		Summary:     fmt.Sprintf("%d syscall-verified finding(s) still hold", len(holding)),
		Proof:       proof,
		Occurrences: len(holding),
		Clusters:    len(keys),
	}
}

// decideJudgment feeds this cycle's judgment-lane candidates into the rolling
// breaker and returns the trip or bump action, or nil.
func (e *PagingEngine) decideJudgment(cands []Candidate, now time.Time) *PageAction {
	// Record this cycle's judgment occurrences (one per candidate), then prune
	// the history to the sustained window.
	for _, c := range cands {
		if ClassifyLane(c) != LaneJudgment {
			continue
		}
		e.occurrences = append(e.occurrences, occurrence{at: now, stateHash: c.stateKey()})
	}
	e.pruneOccurrences(now)

	sustained := len(e.occurrences)
	burst := e.countSince(now.Add(-judgmentBurstWindow))
	breach := sustained >= judgmentCeiling || burst >= judgmentBurstCeiling

	switch e.state {
	case BreakerArmed:
		if !breach {
			return nil
		}
		// Trip CLOSED: raise exactly one escalation, graded by cluster count.
		e.state = BreakerTripped
		clusters := e.distinctClusters()
		severity := "high"
		if clusters > cascadeClusterThreshold {
			severity = "critical"
		}
		e.dedupKey = fmt.Sprintf("curio:judgment:%s", fingerprintKeys(e.clusterKeys()))
		kind := "single finding misfiring"
		if severity == "critical" {
			kind = "cross-finding cascade"
		}
		return &PageAction{
			Kind:        ActionJudgmentTrip,
			Lane:        LaneJudgment,
			Severity:    severity,
			DedupKey:    e.dedupKey,
			Summary:     fmt.Sprintf("judgment lane breaker tripped (%s): %d occurrence(s) across %d cluster(s)", kind, sustained, clusters),
			Proof:       e.clusterSummaries(),
			Occurrences: sustained,
			Clusters:    clusters,
		}

	case BreakerTripped, BreakerClosed:
		// Latch CLOSED. Further occurrences this cycle bump the live counter on the
		// already-open escalation rather than raising a new bead.
		hadNew := false
		for _, c := range cands {
			if ClassifyLane(c) == LaneJudgment {
				hadNew = true
				break
			}
		}
		e.state = BreakerClosed
		if !hadNew {
			return nil
		}
		clusters := e.distinctClusters()
		severity := "high"
		if clusters > cascadeClusterThreshold {
			severity = "critical"
		}
		return &PageAction{
			Kind:        ActionJudgmentBump,
			Lane:        LaneJudgment,
			Severity:    severity,
			DedupKey:    e.dedupKey,
			Summary:     fmt.Sprintf("judgment lane still firing: %d occurrence(s) across %d cluster(s)", sustained, clusters),
			Proof:       e.clusterSummaries(),
			Occurrences: sustained,
			Clusters:    clusters,
		}
	}
	return nil
}

// pruneOccurrences drops occurrences older than judgmentWindow.
func (e *PagingEngine) pruneOccurrences(now time.Time) {
	cutoff := now.Add(-judgmentWindow)
	kept := e.occurrences[:0]
	for _, o := range e.occurrences {
		if o.at.After(cutoff) {
			kept = append(kept, o)
		}
	}
	e.occurrences = kept
}

// countSince counts occurrences at or after t.
func (e *PagingEngine) countSince(t time.Time) int {
	n := 0
	for _, o := range e.occurrences {
		if o.at.After(t) {
			n++
		}
	}
	return n
}

// distinctClusters counts distinct StateHashes in the current window.
func (e *PagingEngine) distinctClusters() int { return len(e.clusterSet()) }

// clusterSet returns the set of distinct StateHashes currently in the window.
func (e *PagingEngine) clusterSet() map[string]bool {
	set := make(map[string]bool, len(e.occurrences))
	for _, o := range e.occurrences {
		set[o.stateHash] = true
	}
	return set
}

// clusterKeys returns the distinct StateHashes in stable sorted order.
func (e *PagingEngine) clusterKeys() []string {
	set := e.clusterSet()
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// clusterSummaries returns one line per distinct cluster, for the page proof.
func (e *PagingEngine) clusterSummaries() []string {
	keys := e.clusterKeys()
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("cluster %s", k))
	}
	return out
}

// stateKey returns the candidate's dedup identity for paging: its StateHash when
// set (the Call 1B coarse state key), else its Fingerprint.
func (c Candidate) stateKey() string {
	if c.StateHash != "" {
		return c.StateHash
	}
	return c.Fingerprint
}

// fingerprintKeys folds a sorted key list into one stable dedup token via the
// collision-free fingerprint family, so two cycles seeing the same cluster set
// produce the same escalation signature (close-aware dedup then suppresses the
// re-fire).
func fingerprintKeys(keys []string) string {
	if len(keys) == 0 {
		return "none"
	}
	return fingerprint.Of(keys...)
}
