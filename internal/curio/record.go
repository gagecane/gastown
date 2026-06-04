// Package curio implements Phase 1 of the Curio self-inspection patrol: a
// sibling dog that discovers Gastown failures via declarative content rules and
// emits CANDIDATES (never beads). No auto-filing, no LLM, no self-mutation in
// this phase — see the design doc referenced on bead gu-6s8ao.
//
// Design split this package depends on:
//
//   - COLLECTORS (live) and FIXTURES (replay) produce normalized records with
//     all probe results already resolved (git ancestry, PID liveness, etc.).
//   - RULES are pure predicates over those normalized records. They do no I/O,
//     so they are deterministic and replay-gradeable.
//
// This separation is what makes the replay harness a real CI gate: the same
// rule code runs against checked-in fixtures and against live collector output.
package curio

import "time"

// Window identifies the time slice a set of records was collected over. It is
// carried onto every candidate so replay can attribute a hit to a window.
type Window struct {
	// ID is a stable identifier for the window (e.g. "2026-05-21T00:00Z/1d").
	ID string
	// Start and End bound the window.
	Start time.Time
	End   time.Time
}

// CurioActor is the BD_ACTOR / filed_by value Curio stamps on its own
// beads/events. The loop-breaker excludes records with this provenance so
// Curio cannot anomaly-detect its own activity (safety invariant 5).
const CurioActor = "curio"

// CurioSeriesPrefix marks event series Curio itself emits. The Call 1(A)
// air-gap drops these at collection so Curio's own telemetry can never feed the
// rate rule (a self-reaction the FiledBy loop-breaker alone would miss when an
// event is attributed to the emitting subsystem rather than to "curio").
const CurioSeriesPrefix = "curio."

// Causal provenance (Call 1(A) air-gap). Records carry the chain that produced
// them so the loop-breaker can drop not just Curio's OWN records (FiledBy ==
// CurioActor) but also any record that is a REACTION to a Curio-filed bead
// (CausalRoot ∈ Input.CurioBeads). Without this, Curio could detect the
// downstream churn its own (future) filings provoke and feed a runaway loop.
//
// These fields are plumbing only in build 2a: no external emit site populates
// them yet (adding CausalRoot columns at non-curio emit sites is the
// broadest-blast-radius change and is deliberately deferred — see gu-5ynaa
// scope note). An empty CausalRoot means "unknown provenance," which the
// loop-breaker treats as not-Curio (conservative: stays visible to detection).
//
//   - CausalParent is the immediate antecedent (e.g. the bead/event that
//     directly triggered this record). Carried for tracing only.
//   - CausalRoot is the origin of the causal chain. The loop-breaker keys on
//     this: if the chain originates at a Curio-filed bead, the record is a
//     self-reaction and is suppressed.
type causalProvenance struct {
	CausalParent string
	CausalRoot   string
}

// BeadRecord is a normalized closed-bead observation for the
// merged-but-not-landed rule (a). The collector resolves the git ancestry probe
// upstream; the rule never shells out.
type BeadRecord struct {
	// ID is the bead identifier.
	ID string
	// Rig is the owning rig (used for candidate targeting / later filing).
	Rig string
	// CloseReason is the bead's close reason (e.g. "merged").
	CloseReason string
	// Commit is the commit the bead claims landed (may be empty).
	Commit string
	// CommitInMainAncestry is the resolved probe result: true if Commit is an
	// ancestor of the rig's main branch. Meaningless when Commit is empty.
	CommitInMainAncestry bool
	// FiledBy is the bead's provenance actor (loop-breaker input).
	FiledBy string
	// causalProvenance carries the Call 1(A) air-gap chain (CausalParent /
	// CausalRoot). Empty in build 2a — no emit site populates it yet.
	causalProvenance
}

// LogLine is a normalized dog-log observation for the kill-signal-near-Dolt
// rule (b). The collector resolves "is this line near a Dolt PID" upstream.
type LogLine struct {
	// Source identifies the log (e.g. "deacon", "reaper").
	Source string
	// Text is the raw log line.
	Text string
	// NearDoltPID is the resolved probe result: the line references a kill/quit
	// signal directed at (or adjacent to) a known Dolt server PID.
	NearDoltPID bool
	// FiledBy is the provenance actor of the emitting component (loop-breaker).
	FiledBy string
	// causalProvenance carries the Call 1(A) air-gap chain. Empty in build 2a.
	causalProvenance
}

// SeriesCount is a per-series event count over the window, for the alarm-rate
// rule (c). This is a CONTENT rate rule (fixed threshold), NOT the L1 EWMA/MAD
// change-point detector — that detector is explicitly out of scope for Phase 1
// (it lands in gc-tt4p9.5). EWMA/Deviation on the candidate are left descriptive.
type SeriesCount struct {
	// Series is the event series name (e.g. "dispatch.stuck_agent").
	Series string
	// Observed is the count in the window.
	Observed int
	// FiledBy is the provenance actor that produced the events (loop-breaker).
	// When a window mixes actors, the collector should split counts; for the
	// rate rule we treat a non-curio series as eligible.
	FiledBy string
	// causalProvenance carries the Call 1(A) air-gap chain. Empty in build 2a.
	causalProvenance
}

// AdmissionRecord is a normalized polecat-admission observation for the
// dead-owner rule (d). The collector resolves PID liveness (kill -0) upstream.
type AdmissionRecord struct {
	// ID is the reservation ID.
	ID string
	// PID is the owning process ID.
	PID int
	// Rig is the rig the reservation targets (may be empty).
	Rig string
	// OwnerAlive is the resolved probe result: false means the owning PID is
	// dead and the reservation is leaking capacity.
	OwnerAlive bool
	// FiledBy is the provenance actor (loop-breaker input).
	FiledBy string
	// causalProvenance carries the Call 1(A) air-gap chain. Empty in build 2a.
	causalProvenance
}

// Input is the full normalized observation bundle for one patrol cycle / replay
// window. Each rule reads only the slices it needs.
type Input struct {
	Window      Window
	Beads       []BeadRecord
	LogLines    []LogLine
	EventCounts []SeriesCount
	Admissions  []AdmissionRecord

	// CurioBeads is the set of bead IDs Curio itself has filed (by ID). It is
	// the second half of the Call 1(A) air-gap: a record whose CausalRoot is in
	// this set is a REACTION to a Curio filing and is suppressed by the
	// loop-breaker, even when its FiledBy is some downstream subsystem rather
	// than "curio". Empty in build 2a (Curio files no beads yet), so the air-gap
	// is dormant until filing turns on — but the plumbing is in place and tested.
	CurioBeads map[string]bool
}

// isCurioReaction reports whether a record with the given provenance is a
// reaction to one of Curio's own filed beads. This is the CausalRoot half of
// the Call 1(A) air-gap. An empty CausalRoot or nil/empty curioBeads set means
// "not a known reaction" (conservative: stays visible to detection).
func (in Input) isCurioReaction(p causalProvenance) bool {
	if p.CausalRoot == "" || len(in.CurioBeads) == 0 {
		return false
	}
	return in.CurioBeads[p.CausalRoot]
}
