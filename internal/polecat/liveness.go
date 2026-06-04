package polecat

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/steveyegge/gastown/internal/liveness"
)

// LivenessVerdict is the typed three-state verdict that all consumers of the
// heartbeat layer should use to decide "is this session alive?". UI surfaces
// (gt heartbeat status, gt witness status, gt prime banner), the daemon
// reaper, and the stuck-agent-dog plugin all map to this single contract so
// thresholds and policy logic stay in lockstep. See cv-p3fem Phase 3.
type LivenessVerdict string

const (
	// LivenessUnknown means we have no trustworthy signal — typically
	// "no heartbeat file exists yet". Fail-open default during rollout
	// (don't reap a session we can't observe). Post-rollout this can be
	// tightened to MAYBE_DEAD by the consumer; the verdict itself is
	// informational only.
	LivenessUnknown LivenessVerdict = "UNKNOWN"
	// LivenessAlive means the heartbeat is fresh OR the PID is alive
	// and the heartbeat is within the grace window AND no corroborating
	// signal contradicts liveness. Operators should NOT take action.
	LivenessAlive LivenessVerdict = "ALIVE"
	// LivenessMaybeDead means the heartbeat is past the stale threshold
	// but inside the dead threshold (the grace window). Operators may
	// want to investigate; supervision actions should be informational
	// (notify, log) rather than destructive (kill, reap).
	LivenessMaybeDead LivenessVerdict = "MAYBE_DEAD"
	// LivenessDead means the heartbeat is past the dead threshold AND
	// at least one corroborating signal (PID gone, tmux session gone,
	// no recent bead update) agrees the session is dead. Mass-kill paths
	// require independent corroboration per agent before counting any
	// agent as dead. See cv-p3fem security mitigation #1.
	LivenessDead LivenessVerdict = "DEAD"
)

// VerdictReason is a stable enum string explaining why a verdict was reached.
// Plugin authors lock in this set; new reasons are additive only.
const (
	ReasonNoHeartbeatFile   = "no_heartbeat_file"
	ReasonKeepaliveFresh    = "keepalive_fresh"
	ReasonHeartbeatFresh    = "heartbeat_fresh"
	ReasonInsideGraceWindow = "inside_grace_window"
	ReasonPastDeadThreshold = "past_dead_threshold"
	ReasonPIDGone           = "pid_gone"
	ReasonRecoveryMarker    = "recovery_marker_active"
	ReasonExpectedIdle      = "expected_idle_until_future"
	ReasonStateExiting      = "state_exiting"
	ReasonStateIdle         = "state_idle"
	ReasonInvalidSession    = "invalid_session_name"
)

// LivenessThresholds bundles the three durations that define the verdict
// boundaries for a given role. Embedded in LivenessReport so plugin authors
// don't need to read ZFC config separately (prevents threshold drift between
// the binary and downstream consumers).
type LivenessThresholds struct {
	// Stale is the boundary above which a heartbeat is no longer "fresh".
	// Below this, verdict is ALIVE on heartbeat alone.
	Stale time.Duration `json:"stale"`
	// Grace is the boundary above which a stale heartbeat becomes
	// MAYBE_DEAD. Between Stale and Grace, plugin authors typically log
	// without acting.
	Grace time.Duration `json:"grace"`
	// Dead is the hard boundary above which a stale heartbeat (with
	// corroboration) is DEAD. Reapers may act once both age >= Dead and
	// PID-liveness disagrees.
	Dead time.Duration `json:"dead"`
}

// DefaultLivenessThresholds for the polecat role. Witness/refinery roles use
// longer grace windows because their patrol cycles are intentionally bursty.
// Per cv-p3fem design doc §Decisions Made #7 and the gu-x9qc-approved
// recommendations.
var DefaultLivenessThresholds = LivenessThresholds{
	Stale: 3 * time.Minute,
	Grace: 10 * time.Minute,
	Dead:  20 * time.Minute,
}

// DefaultWitnessLivenessThresholds for the witness role.
var DefaultWitnessLivenessThresholds = LivenessThresholds{
	Stale: 5 * time.Minute,
	Grace: 15 * time.Minute,
	Dead:  30 * time.Minute,
}

// DefaultRefineryLivenessThresholds for the refinery role. Refineries can
// spend up to ~25min in legitimate merge-queue gate runs so the grace
// window is comfortably above that.
var DefaultRefineryLivenessThresholds = LivenessThresholds{
	Stale: 10 * time.Minute,
	Grace: 30 * time.Minute,
	Dead:  60 * time.Minute,
}

// LivenessReport is the structured verdict returned by Liveness. Carries
// enough context for both human-readable (gt heartbeat status) and
// machine-readable (--json) surfaces to render without re-parsing the
// underlying heartbeat. See cv-p3fem Phase 3 design §Plugin surface.
type LivenessReport struct {
	Session       string          `json:"session"`
	Verdict       LivenessVerdict `json:"verdict"`
	VerdictReason string          `json:"verdict_reason"`
	// Age is time since EffectiveLastKeepalive (max of timestamp and
	// last_keepalive). Zero when no heartbeat file exists.
	Age                     time.Duration      `json:"-"`
	AgeSeconds              int64              `json:"age_seconds"`
	LastKeepaliveAgeSeconds int64              `json:"last_keepalive_age_seconds"`
	State                   HeartbeatState     `json:"state,omitempty"`
	KeepaliveOp             string             `json:"keepalive_op,omitempty"`
	Bead                    string             `json:"bead,omitempty"`
	Thresholds              LivenessThresholds `json:"thresholds"`

	// LastTimestamp / LastKeepalive carry the raw timestamps for callers
	// that want them (CLI display formatting, witness status). Omitted
	// from JSON when zero.
	LastTimestamp time.Time `json:"last_timestamp,omitempty"`
	LastKeepalive time.Time `json:"last_keepalive,omitempty"`

	// Recovered is true when an active operator-recovery marker exists
	// (gu-v5mk). Recovery short-circuits the verdict to ALIVE regardless
	// of other signals.
	Recovered bool `json:"recovered,omitempty"`

	// PIDAlive is the result of the PID-existence fast-path check. nil
	// when not applicable (no PID source) — plugin authors should treat
	// missing as "no signal" rather than "alive". Carried as a stable
	// JSON string ("true"/"false"/"") so jq policy logic can distinguish
	// the absent case.
	PIDAlive string `json:"pid_alive,omitempty"`

	// ExpectedIdleUntil is the agent's TTL-bounded self-report (v3). When
	// non-zero AND in the future, callers may want to suppress action even
	// past the dead threshold (capped per-rig at dead_agent_reap_timeout).
	ExpectedIdleUntilSeconds int64 `json:"expected_idle_until_seconds,omitempty"`
}

// Liveness computes a typed verdict for a session by reading the v3
// heartbeat file and applying threshold + corroboration logic. Falls open
// (Verdict=UNKNOWN) when no heartbeat exists so a missing-file case never
// produces a destructive action on its own.
//
// Per cv-p3fem open-question 3: PID-existence is consulted as a fast-path
// only when explicitly supplied; the bare Liveness() takes thresholds and
// uses heartbeat-only signals. Use LivenessWithPID for the PID-fast-path
// variant (consumed by the polecat manager + dog plugin).
func Liveness(townRoot, sessionName string, thresholds LivenessThresholds) LivenessReport {
	return LivenessWithPID(townRoot, sessionName, thresholds, nil)
}

// PIDLivenessFunc returns (alive, queried) for a given session. queried=false
// means "we couldn't determine liveness from this source" (e.g. tmux
// permission denied) — distinct from "we asked and the PID is dead".
type PIDLivenessFunc func(sessionName string) (alive bool, queried bool)

// LivenessWithPID computes the verdict, optionally consulting a caller-
// supplied PID liveness probe as the fast-path described in cv-p3fem
// open-question 3 / scale leg. If pidProbe says PID is gone and a heartbeat
// file exists, the verdict is DEAD with reason=pid_gone. If pidProbe is
// nil or returns queried=false the report falls back to heartbeat-only
// signals.
func LivenessWithPID(townRoot, sessionName string, thresholds LivenessThresholds, pidProbe PIDLivenessFunc) LivenessReport {
	report := LivenessReport{
		Session:    sessionName,
		Thresholds: thresholds,
	}

	if !isValidSessionName(sessionName) {
		report.Verdict = LivenessUnknown
		report.VerdictReason = ReasonInvalidSession
		return report
	}

	// Operator override (gu-v5mk) wins. A manual recovery short-circuits
	// the verdict to ALIVE regardless of heartbeat staleness.
	if HasActiveRecoveryMarker(townRoot, sessionName) {
		report.Verdict = LivenessAlive
		report.VerdictReason = ReasonRecoveryMarker
		report.Recovered = true
		return report
	}

	hb := ReadSessionHeartbeat(townRoot, sessionName)
	if hb == nil {
		// PID may still be alive even without a heartbeat — but during
		// rollout we fail open rather than mark MAYBE_DEAD on a missing
		// file (cv-p3fem rollout posture). Consumers that want to escalate
		// missing files post-rollout can do so explicitly.
		report.Verdict = LivenessUnknown
		report.VerdictReason = ReasonNoHeartbeatFile
		if pidProbe != nil {
			alive, queried := pidProbe(sessionName)
			if queried {
				if alive {
					report.PIDAlive = "true"
				} else {
					report.PIDAlive = "false"
				}
			}
		}
		return report
	}

	report.LastTimestamp = hb.Timestamp
	report.LastKeepalive = hb.LastKeepalive
	report.State = hb.State
	report.KeepaliveOp = hb.KeepaliveOp
	report.Bead = hb.Bead

	effective := hb.EffectiveLastKeepalive()
	age := time.Since(effective)
	report.Age = age
	report.AgeSeconds = int64(age.Seconds())
	if !hb.LastKeepalive.IsZero() {
		report.LastKeepaliveAgeSeconds = int64(time.Since(hb.LastKeepalive).Seconds())
	} else {
		// v1/v2 heartbeats don't carry last_keepalive — fall back to
		// timestamp age so plugin authors get a single coherent number.
		report.LastKeepaliveAgeSeconds = int64(age.Seconds())
	}

	// ExpectedIdleUntil suppression — capped at thresholds.Dead so a wedged
	// agent declaring +24h can't suppress detection forever (security
	// mitigation #2).
	if !hb.ExpectedIdleUntil.IsZero() && hb.ExpectedIdleUntil.After(time.Now()) {
		// Cap at thresholds.Dead from the heartbeat's effective freshness.
		cap := effective.Add(thresholds.Dead)
		idleUntil := hb.ExpectedIdleUntil
		if !cap.IsZero() && idleUntil.After(cap) {
			idleUntil = cap
		}
		report.ExpectedIdleUntilSeconds = int64(time.Until(idleUntil).Seconds())
		if time.Now().Before(idleUntil) {
			report.Verdict = LivenessAlive
			report.VerdictReason = ReasonExpectedIdle
			return report
		}
	}

	// State-bearing exit-tombstone hint: if the agent self-reported exiting
	// recently, treat as alive within the stale window even if the
	// timestamp would otherwise call this stale. We don't extend past the
	// grace window — a true crash mid-`gt done` shouldn't be invisible.
	if hb.State == HeartbeatExiting && age < thresholds.Grace {
		report.Verdict = LivenessAlive
		report.VerdictReason = ReasonStateExiting
		return report
	}

	// PID fast-path (cv-p3fem open-question 3 recommendation). When PID
	// is provably gone, return DEAD immediately — this is the gs-549
	// "single bad signal can't fan out" property: PID-gone is unambiguous,
	// not a heartbeat-FS noise channel.
	pidAlive := false
	pidQueried := false
	if pidProbe != nil {
		pidAlive, pidQueried = pidProbe(sessionName)
		if pidQueried {
			if pidAlive {
				report.PIDAlive = "true"
			} else {
				report.PIDAlive = "false"
			}
			if !pidAlive {
				report.Verdict = LivenessDead
				report.VerdictReason = ReasonPIDGone
				return report
			}
		}
	}

	// State-bearing idle hint (gs-535): an agent that self-reported idle is
	// waiting for input, not dead. Idle agents (a witness/refinery between
	// patrol cycles, a polecat parked in a reusable slot) block in
	// `gt mol step await-signal` without bumping their heartbeat, so a
	// healthy idle agent's timestamp naturally ages past the stale threshold.
	// Treat self-reported idle as alive within the grace window so it does
	// not flip to MAYBE_DEAD and trip the idle->MAYBE_DEAD->working ping-pong
	// false positive (matches the mayor's standing note that idle-quiet
	// stale-heartbeat alarms are false). We do NOT extend past grace — a
	// session that died while last reporting idle must still surface beyond
	// the grace window. This mirrors the exiting tombstone above and is
	// reaper-safe: within grace the verdict was already MAYBE_DEAD
	// (non-destructive, never reaped), so promoting it to ALIVE changes only
	// the surfaced label, not any reap decision. Placed after the PID
	// fast-path so a provably-gone PID still yields DEAD.
	if hb.State == HeartbeatIdle && age < thresholds.Grace {
		report.Verdict = LivenessAlive
		report.VerdictReason = ReasonStateIdle
		return report
	}

	// Heartbeat-only path.
	switch {
	case age < thresholds.Stale:
		report.Verdict = LivenessAlive
		if !hb.LastKeepalive.IsZero() && hb.LastKeepalive.After(hb.Timestamp) {
			report.VerdictReason = ReasonKeepaliveFresh
		} else {
			report.VerdictReason = ReasonHeartbeatFresh
		}
	case age < thresholds.Grace:
		// Stale window (between Stale and Grace): MAYBE_DEAD if no PID
		// corroboration says alive. With PID alive, treat as ALIVE.
		if pidQueried && pidAlive {
			report.Verdict = LivenessAlive
			report.VerdictReason = ReasonInsideGraceWindow
		} else {
			report.Verdict = LivenessMaybeDead
			report.VerdictReason = ReasonInsideGraceWindow
		}
	case age < thresholds.Dead:
		// Between grace and dead — operator-actionable but not
		// destructive. PID alive caps escalation at MAYBE_DEAD with the
		// "inside grace" reason; PID-unknown stays MAYBE_DEAD with the
		// same reason (single-signal can't escalate to past-dead).
		report.Verdict = LivenessMaybeDead
		report.VerdictReason = ReasonInsideGraceWindow
	default:
		// Past the dead threshold — without PID corroboration this
		// remains MAYBE_DEAD (single signal cannot escalate).
		// Confirmed DEAD requires either (a) explicit PID-gone via the
		// fast-path above, or (b) a caller-supplied corroboration that
		// says so.
		if pidQueried && !pidAlive {
			report.Verdict = LivenessDead
			report.VerdictReason = ReasonPIDGone
		} else {
			report.Verdict = LivenessMaybeDead
			report.VerdictReason = ReasonPastDeadThreshold
		}
	}

	return report
}

// PIDFromTmuxFunc allows callers to inject a tmux pane PID lookup (the
// canonical liveness signal for polecat sessions) without making this
// package depend on internal/tmux. Returns ("", false) when the session
// cannot be queried (permission denied, tmux server busy) so the caller
// can fall through cleanly.
type PIDFromTmuxFunc func(sessionName string) (pidStr string, queried bool)

// PIDProbe returns a PIDLivenessFunc that consults pidLookup for a tmux
// pane PID and validates it via Signal(0). Helpful adapter for callers
// that already have a tmux client; ones that don't can pass a constant
// PIDLivenessFunc instead.
func PIDProbe(pidLookup PIDFromTmuxFunc) PIDLivenessFunc {
	if pidLookup == nil {
		return nil
	}
	return func(sessionName string) (bool, bool) {
		pidStr, queried := pidLookup(sessionName)
		if !queried {
			return false, false
		}
		if pidStr == "" {
			return false, true
		}
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			// Non-numeric PID — treat as unqueried; the caller will
			// fall back to heartbeat-only logic.
			return false, false
		}
		// Probe the PID via the shared liveness leaf (signal 0 on Unix).
		return liveness.PIDAlive(pid), true
	}
}

// SetExpectedIdleUntil writes (or refreshes) the v3 ExpectedIdleUntil field
// for a session, capped at the supplied per-rig cap. Best-effort: errors
// silently ignored; invalid session names rejected. The cap is the maximum
// declared idle window the operator will tolerate (typically
// dead_agent_reap_timeout). cv-p3fem open-question 1 (approved by gu-x9qc).
func SetExpectedIdleUntil(townRoot, sessionName string, until time.Time, cap time.Duration) error {
	if !isValidSessionName(sessionName) {
		return fmt.Errorf("invalid session name %q", sessionName)
	}
	now := time.Now().UTC()
	if cap > 0 {
		hardCap := now.Add(cap)
		if until.After(hardCap) {
			until = hardCap
		}
	}
	if until.Before(now) {
		return fmt.Errorf("until=%s is in the past", until)
	}
	// Read-modify-write — the rest of the heartbeat fields are preserved.
	existing := ReadSessionHeartbeat(townRoot, sessionName)
	hb := SessionHeartbeat{
		Timestamp:         now,
		LastKeepalive:     now,
		Liveness:          LivenessSignalKeepalive,
		ExpectedIdleUntil: until.UTC(),
	}
	if existing != nil {
		hb.State = existing.State
		hb.Context = existing.Context
		hb.Bead = existing.Bead
		hb.KeepaliveOp = existing.KeepaliveOp
	}
	dir := heartbeatsDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := jsonMarshal(hb)
	if err != nil {
		return err
	}
	return os.WriteFile(heartbeatFile(townRoot, sessionName), data, 0644)
}

// jsonMarshal is a tiny indirection so we can swap encoders during testing
// without pulling in a fixture-only import in production code paths. Today
// it's encoding/json.
func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}
