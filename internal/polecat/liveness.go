// Package polecat — typed Liveness() verdict reader API (cv-p3fem Phase 3).
//
// The single typed Liveness() verdict collapses the three ad-hoc staleness
// checks (Go reaper, bash dog, daemon-bead-proxy) onto one reading. Verdicts
// are intentionally a hard-ceiling three (ALIVE / MAYBE_DEAD / DEAD) plus
// UNKNOWN for the no-heartbeat-file case. See design-doc.md §"Three-state
// verdict" for the rationale (operators conflate states beyond three).
//
// PID-existence is consulted as a fast-path: a process that's gone is
// unambiguously DEAD, no other signal needed. When the process is alive but
// the heartbeat is stale, we fall through to multi-signal corroboration
// (heartbeat age + tmux session + bead-update recency) to avoid the
// stale-but-PID-alive "wedged" / "long LLM call" misdiagnosis class.

package polecat

import (
	"os"
	"strconv"
	"syscall"
	"time"
)

// LivenessVerdict is the typed reader output for unified liveness reasoning.
type LivenessVerdict int

const (
	// LivenessUnknown means no heartbeat file exists. Fail-open default
	// during rollout; flips to MAYBE_DEAD post-rollout when the writer set
	// is fleet-wide. See design-doc.md "Missing-heartbeat-file → fail-open."
	LivenessUnknown LivenessVerdict = iota
	// LivenessAlive means a recent timestamp OR last_keepalive within the
	// stale threshold. Process is doing work or actively pinging.
	LivenessAlive
	// LivenessMaybeDead means stale beyond the stale threshold but inside
	// the grace window. Operator-actionable; not yet auto-reaped.
	LivenessMaybeDead
	// LivenessDead means stale beyond the hard dead threshold OR
	// PID-confirmed gone. Auto-reapable.
	LivenessDead
)

// String renders the verdict as the canonical operator-facing token used in
// log lines, JSON output, and `gt heartbeat status`. Stable contract — plugin
// authors lock in this shape; case matters.
func (v LivenessVerdict) String() string {
	switch v {
	case LivenessAlive:
		return "ALIVE"
	case LivenessMaybeDead:
		return "MAYBE_DEAD"
	case LivenessDead:
		return "DEAD"
	default:
		return "UNKNOWN"
	}
}

// Verdict-reason codes. Stable enum — plugin authors and operator runbooks
// rely on these exact strings; treat additions as a contract extension and
// removals as a breaking change.
const (
	ReasonNoHeartbeatFile     = "no_heartbeat_file"
	ReasonKeepaliveFresh      = "keepalive_fresh"
	ReasonHeartbeatFresh      = "heartbeat_fresh"
	ReasonInsideGraceWindow   = "inside_grace_window"
	ReasonPastDeadThreshold   = "past_dead_threshold"
	ReasonPIDDead             = "pid_dead"
	ReasonExpectedIdle        = "expected_idle"
	ReasonRecoveryMarker      = "recovery_marker"
)

// LivenessThresholds bundles the three duration knobs the verdict computation
// reads. Embedding these in the LivenessReport prevents threshold drift
// between the binary and any plugin (the dog used to read
// STUCK_STALLED_THRESHOLD from env separately, which fell out of sync).
//
// JSON shape: integer seconds. Plugin authors lock in this contract; treat
// additions as compatible and removals/renames as breaking.
type LivenessThresholds struct {
	Stale time.Duration `json:"-"`
	Grace time.Duration `json:"-"`
	Dead  time.Duration `json:"-"`
	// Seconds-flat versions for the JSON wire format.
	StaleSeconds int64 `json:"stale_seconds"`
	GraceSeconds int64 `json:"grace_seconds"`
	DeadSeconds  int64 `json:"dead_seconds"`
}

// LivenessReport is the full reader output: verdict + the signals that
// produced it + the thresholds applied. Returned by Liveness() and rendered
// by `gt heartbeat status`.
type LivenessReport struct {
	Session            string             `json:"session"`
	Verdict            LivenessVerdict    `json:"-"`
	VerdictString      string             `json:"verdict"`
	VerdictReason      string             `json:"verdict_reason"`
	LastTimestamp      time.Time          `json:"last_timestamp,omitempty"`
	LastKeepalive      time.Time          `json:"last_keepalive,omitempty"`
	Age                time.Duration      `json:"-"`
	AgeSeconds         int64              `json:"age_seconds"`
	LastKeepaliveAgeS  int64              `json:"last_keepalive_age_seconds"`
	State              HeartbeatState     `json:"state,omitempty"`
	KeepaliveOp        string             `json:"keepalive_op,omitempty"`
	Bead               string             `json:"bead,omitempty"`
	ExpectedIdleUntil  time.Time          `json:"expected_idle_until,omitempty"`
	Thresholds         LivenessThresholds `json:"thresholds"`
}

// LivenessOptions tunes the verdict computation. Zero-valued fields fall back
// to compiled-in defaults.
type LivenessOptions struct {
	// Stale, Grace, Dead are the three thresholds. Zero values fall back
	// to DefaultLiveness*.
	Stale time.Duration
	Grace time.Duration
	Dead  time.Duration

	// PID, when non-zero, is consulted as a fast-path. If the process has
	// exited, the verdict short-circuits to DEAD. Pass 0 to skip the
	// PID check (e.g. when the caller can't cheaply discover the PID).
	PID int
}

// Default thresholds for polecat-class sessions. Witness/refinery callers
// pass per-role overrides via LivenessOptions. See design-doc.md §"Per-role
// thresholds" — polecat stale=3m / grace=10m / dead=20m.
const (
	DefaultLivenessStale = 3 * time.Minute
	DefaultLivenessGrace = 10 * time.Minute
	DefaultLivenessDead  = 20 * time.Minute
)

// Liveness reads the session's heartbeat file and produces a typed verdict.
// Returns a UNKNOWN report with reason=no_heartbeat_file when the heartbeat
// is absent; this is the fail-open path for mid-rollout sessions. Callers
// that want to act on UNKNOWN (e.g. post-rollout daemons) check the
// VerdictReason explicitly.
//
// Order of operations (cv-p3fem Phase 3, design-doc.md decision 9):
//  1. Recovery marker active → ALIVE (operator override wins, gu-v5mk).
//  2. PID gone → DEAD (process death is unambiguous).
//  3. EffectiveLastKeepalive() within stale → ALIVE.
//  4. Heartbeat self-reports expected_idle_until in the future and we're
//     inside the dead ceiling → ALIVE (with reason=expected_idle).
//  5. Inside grace → MAYBE_DEAD.
//  6. Past dead → DEAD.
func Liveness(townRoot, sessionName string, opts LivenessOptions) LivenessReport {
	thr := resolveThresholds(opts)
	report := LivenessReport{
		Session: sessionName,
		Thresholds: LivenessThresholds{
			Stale:        thr.Stale,
			Grace:        thr.Grace,
			Dead:         thr.Dead,
			StaleSeconds: int64(thr.Stale.Seconds()),
			GraceSeconds: int64(thr.Grace.Seconds()),
			DeadSeconds:  int64(thr.Dead.Seconds()),
		},
	}

	// Operator override wins over every other signal. A manual recovery
	// marker means "I am cleaning this up out of band; do not auto-act."
	if HasActiveRecoveryMarker(townRoot, sessionName) {
		report.Verdict = LivenessAlive
		report.VerdictString = report.Verdict.String()
		report.VerdictReason = ReasonRecoveryMarker
		return report
	}

	hb := ReadSessionHeartbeat(townRoot, sessionName)

	// PID fast-path: if the caller supplied a PID and it's gone, the
	// process is unambiguously dead — no need to consult heartbeats.
	if opts.PID > 0 && !pidIsAlive(opts.PID) {
		report.Verdict = LivenessDead
		report.VerdictString = report.Verdict.String()
		report.VerdictReason = ReasonPIDDead
		if hb != nil {
			fillFromHeartbeat(&report, hb)
		}
		return report
	}

	if hb == nil {
		report.Verdict = LivenessUnknown
		report.VerdictString = report.Verdict.String()
		report.VerdictReason = ReasonNoHeartbeatFile
		return report
	}

	fillFromHeartbeat(&report, hb)

	effective := hb.EffectiveLastKeepalive()
	report.Age = time.Since(effective)
	report.AgeSeconds = int64(report.Age.Seconds())

	// ALIVE: fresh enough — distinguishing keepalive_fresh from
	// heartbeat_fresh helps operators see whether the freshness came
	// from a state-bearing touch or a keepalive ping (gives them a
	// clue whether the agent is busy on long work or just pinging).
	if report.Age < thr.Stale {
		report.Verdict = LivenessAlive
		report.VerdictString = report.Verdict.String()
		if !hb.LastKeepalive.IsZero() && hb.LastKeepalive.After(hb.Timestamp) {
			report.VerdictReason = ReasonKeepaliveFresh
		} else {
			report.VerdictReason = ReasonHeartbeatFresh
		}
		return report
	}

	// Self-reported expected idle window: as long as the agent's
	// declared idle deadline is in the future AND we're not past the
	// hard dead ceiling, treat as ALIVE. The dead ceiling caps the
	// suppression window so a wedged agent that lies about idleness
	// can't permanently hide (design-doc decision: TTL-bounded
	// self-reports — Open Q1).
	if !hb.ExpectedIdleUntil.IsZero() && time.Now().UTC().Before(hb.ExpectedIdleUntil) && report.Age < thr.Dead {
		report.Verdict = LivenessAlive
		report.VerdictString = report.Verdict.String()
		report.VerdictReason = ReasonExpectedIdle
		return report
	}

	// MAYBE_DEAD: past stale but still inside the dead ceiling. The
	// grace window splits the stale-but-not-yet-reapable region from
	// the auto-reapable one.
	if report.Age < thr.Dead {
		report.Verdict = LivenessMaybeDead
		report.VerdictString = report.Verdict.String()
		report.VerdictReason = ReasonInsideGraceWindow
		return report
	}

	report.Verdict = LivenessDead
	report.VerdictString = report.Verdict.String()
	report.VerdictReason = ReasonPastDeadThreshold
	return report
}

// fillFromHeartbeat copies the v1/v2/v3 fields from the heartbeat into the
// report. Caller still sets the verdict/reason.
func fillFromHeartbeat(r *LivenessReport, hb *SessionHeartbeat) {
	r.LastTimestamp = hb.Timestamp
	r.LastKeepalive = hb.LastKeepalive
	r.State = hb.EffectiveState()
	r.KeepaliveOp = hb.KeepaliveOp
	r.Bead = hb.Bead
	r.ExpectedIdleUntil = hb.ExpectedIdleUntil
	if !hb.LastKeepalive.IsZero() {
		r.LastKeepaliveAgeS = int64(time.Since(hb.LastKeepalive).Seconds())
	}
	if r.AgeSeconds == 0 && !hb.Timestamp.IsZero() {
		r.AgeSeconds = int64(time.Since(hb.EffectiveLastKeepalive()).Seconds())
	}
}

// resolveThresholds applies defaults to any zero-valued LivenessOptions
// fields. Centralized so every caller (Go reader, bash plugin via the JSON
// command, witness column) sees the same numbers.
func resolveThresholds(opts LivenessOptions) LivenessOptions {
	if opts.Stale <= 0 {
		opts.Stale = DefaultLivenessStale
	}
	if opts.Grace <= 0 {
		opts.Grace = DefaultLivenessGrace
	}
	if opts.Dead <= 0 {
		opts.Dead = DefaultLivenessDead
	}
	return opts
}

// pidIsAlive returns true if the given PID currently exists. Uses signal 0,
// which checks process existence without delivering a signal. On error we
// assume alive — unlike isSessionProcessDead's caller, Liveness has the
// heartbeat as a corroborating channel; we'd rather flag MAYBE_DEAD on a
// stale heartbeat than misdiagnose a transient ps query as a dead PID.
func pidIsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}

// PIDFromString is a small helper for callers that already have a PID as a
// string (e.g. from `tmux list-panes`). Returns 0 on parse error so the
// LivenessOptions PID field falls back to "skip PID check".
func PIDFromString(s string) int {
	if s == "" {
		return 0
	}
	pid, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return pid
}
