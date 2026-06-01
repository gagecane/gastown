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
}

// Input is the full normalized observation bundle for one patrol cycle / replay
// window. Each rule reads only the slices it needs.
type Input struct {
	Window      Window
	Beads       []BeadRecord
	LogLines    []LogLine
	EventCounts []SeriesCount
	Admissions  []AdmissionRecord
}
