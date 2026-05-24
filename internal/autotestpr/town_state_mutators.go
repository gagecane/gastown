// CAS-style mutators for the town-state bead's pause / resume /
// circuit-breaker-override / audit-log surface.
//
// Phase 0 task 2b (gu-uez5w) — `gt auto-test-pr {pause,resume,
// status,show,history}` need to write durable changes to the
// town-state bead. The mutate-and-retry shape mirrors
// AppendEnabledRig in enabled_rigs.go: read state, mutate the struct,
// MarshalMetadata, Update; retry on transient Dolt optimistic-lock
// errors only.
//
// All public mutators in this file are CAS-safe (read-modify-write
// with bounded retry on lock contention) but assume a single human
// or single agent is the writer at any given moment. The town-state
// bead is a single-row resource; the only concurrent writer in v1 is
// the Mayor reconcile cycle (Phase 0 task 4) racing against an
// operator-initiated CLI verb. Five attempts at 50ms backoff is
// enough headroom for that race per the synthesis-doc analysis.
//
// Audit-log discipline: every state-changing mutator in this file
// also appends an Incident to the bounded log. The append is part of
// the same mutate function so the state change and the audit-log
// entry land in a single Update call — there is no window in which
// the state is changed but the log is missing.
package autotestpr

import (
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// PauseRequest describes a single pause command issued by an operator.
// Used by both SetGlobalPause and SetRigPause so the call sites share
// validation, audit-log emission, and CAS semantics.
type PauseRequest struct {
	// Until is the point in time at which the pause expires. Must be
	// in the future relative to Now() (callers compute it from
	// --duration). Stored as RFC3339 in the bead.
	Until time.Time

	// Reason is the operator-provided free-form string from
	// `--reason=...`. Optional; empty when not given.
	Reason string

	// Actor is the operator address resolved from BD_ACTOR / GT_ROLE.
	// Stored both on the pause record (for `status` rendering) and on
	// the matching Incident (for the audit log).
	Actor string

	// Now is the wall-clock at the time of the command — passed in so
	// tests can pin it. Both the pause's PausedAt and the Incident's
	// At fields use this value for consistency on the same row.
	Now time.Time
}

// validate returns nil if the request is well-formed. We only check
// the fields the caller can get wrong; missing Now is fatal because
// the audit log requires a timestamp and we'd rather panic in tests
// than silently emit "0001-01-01T00:00:00Z" to the bead.
func (r PauseRequest) validate() error {
	if r.Now.IsZero() {
		return fmt.Errorf("PauseRequest.Now is zero — caller must set time.Now()")
	}
	if r.Until.IsZero() {
		return fmt.Errorf("PauseRequest.Until is zero — caller must compute --duration deadline")
	}
	if !r.Until.After(r.Now) {
		return fmt.Errorf("PauseRequest.Until %s is not after Now %s",
			r.Until.Format(time.RFC3339), r.Now.Format(time.RFC3339))
	}
	if r.Actor == "" {
		return fmt.Errorf("PauseRequest.Actor is empty — caller must resolve operator identity")
	}
	return nil
}

// ResumeRequest describes a single resume command issued by an operator.
// `--override-circuit-breaker` is captured here rather than as a
// separate verb because it shares the resume semantics — clearing
// pause state — and we want a single audit-log entry for the
// override (the OverrideCircuitBreaker incident).
type ResumeRequest struct {
	// Actor is the operator address resolved from BD_ACTOR / GT_ROLE.
	Actor string

	// Now is the wall-clock at the time of the command.
	Now time.Time

	// OverrideCircuitBreaker, when true, additionally clears
	// CircuitBreaker.TrippedUntil and resets CircuitBreaker.Count to
	// zero. Per D16, this is the operator-only path out of the
	// `paused-by-circuit-breaker` state — no auto-release.
	OverrideCircuitBreaker bool
}

func (r ResumeRequest) validate() error {
	if r.Now.IsZero() {
		return fmt.Errorf("ResumeRequest.Now is zero — caller must set time.Now()")
	}
	if r.Actor == "" {
		return fmt.Errorf("ResumeRequest.Actor is empty — caller must resolve operator identity")
	}
	return nil
}

// SetGlobalPause writes a town-wide pause to the town-state bead.
// CAS-retried; idempotent in the sense that calling it twice with the
// same request just last-write-wins on the timestamp/reason fields.
//
// Errors:
//   - ErrTownStateNotProvisioned: bead missing; caller should
//     EnsureTownStateBead and retry deliberately.
//   - ErrTownStateCASExhausted: every retry hit a Dolt write conflict.
//   - Validation errors: malformed request (bubbles up unchanged).
func SetGlobalPause(b *beads.Beads, req PauseRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	return mutateTownState(b, func(s *TownState) error {
		s.GlobalPauseUntil = req.Until.UTC().Format(time.RFC3339)
		s.GlobalPauseReason = req.Reason
		s.GlobalPausedBy = req.Actor
		appendIncident(s, Incident{
			At:      req.Now.UTC().Format(time.RFC3339),
			Actor:   req.Actor,
			Kind:    IncidentGlobalPause,
			Details: formatPauseDetails(req.Until, req.Now, req.Reason),
		})
		return nil
	})
}

// ClearGlobalPause removes the town-wide pause. Idempotent: clearing a
// pause that is not set is a no-op (no audit log entry — we don't
// want to spam Incidents with redundant resumes during reconcile).
func ClearGlobalPause(b *beads.Beads, req ResumeRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	return mutateTownState(b, func(s *TownState) error {
		hadPause := s.GlobalPauseUntil != ""
		hadCB := s.CircuitBreaker.IsTripped() || s.CircuitBreaker.Count > 0

		if !hadPause && !req.OverrideCircuitBreaker {
			// No-op resume of an already-resumed town. Don't write,
			// don't log — the operator's --all is broad enough that
			// running it on a clean slate is normal.
			return errSkipUpdate
		}

		if hadPause {
			s.GlobalPauseUntil = ""
			s.GlobalPauseReason = ""
			s.GlobalPausedBy = ""
			appendIncident(s, Incident{
				At:    req.Now.UTC().Format(time.RFC3339),
				Actor: req.Actor,
				Kind:  IncidentGlobalResume,
			})
		}

		if req.OverrideCircuitBreaker && hadCB {
			s.CircuitBreaker.TrippedUntil = ""
			s.CircuitBreaker.Count = 0
			s.CircuitBreaker.WindowStartedAt = ""
			appendIncident(s, Incident{
				At:      req.Now.UTC().Format(time.RFC3339),
				Actor:   req.Actor,
				Kind:    IncidentCircuitBreakerOverride,
				Details: "scope=all (resume --all --override-circuit-breaker)",
			})
		}
		return nil
	})
}

// SetRigPause writes a per-rig operator pause to the town-state bead's
// RigPauses map. Phase 0 home for `gt auto-test-pr pause --rig=<rig>`
// per the synthesis (line 1175): "no patrol consumes them yet" — this
// is operator audit + read-back surface only.
//
// rigName must be non-empty. If the rig is already paused, this last-
// write-wins on the entry (pause extension via re-issuing the verb).
func SetRigPause(b *beads.Beads, rigName string, req PauseRequest) error {
	if rigName == "" {
		return fmt.Errorf("SetRigPause: empty rig name")
	}
	if err := req.validate(); err != nil {
		return err
	}
	return mutateTownState(b, func(s *TownState) error {
		if s.RigPauses == nil {
			s.RigPauses = map[string]RigPauseEntry{}
		}
		s.RigPauses[rigName] = RigPauseEntry{
			PausedUntil: req.Until.UTC().Format(time.RFC3339),
			Reason:      req.Reason,
			PausedBy:    req.Actor,
			PausedAt:    req.Now.UTC().Format(time.RFC3339),
		}
		appendIncident(s, Incident{
			At:      req.Now.UTC().Format(time.RFC3339),
			Actor:   req.Actor,
			Kind:    IncidentRigPause,
			Rig:     rigName,
			Details: formatPauseDetails(req.Until, req.Now, req.Reason),
		})
		return nil
	})
}

// ClearRigPause removes a per-rig operator pause. If the rig is not
// currently paused and --override-circuit-breaker is not set, this is
// a no-op (no audit log entry).
//
// When req.OverrideCircuitBreaker is true, also clears the town-wide
// CircuitBreaker counter — D16 supports both `resume --all
// --override-circuit-breaker` and `resume --rig=<rig>
// --override-circuit-breaker`. The latter is documented in the
// synthesis Decisions Made table (line 836):
// `gt auto-test-pr resume --rig=<rig> --override-circuit-breaker`.
func ClearRigPause(b *beads.Beads, rigName string, req ResumeRequest) error {
	if rigName == "" {
		return fmt.Errorf("ClearRigPause: empty rig name")
	}
	if err := req.validate(); err != nil {
		return err
	}
	return mutateTownState(b, func(s *TownState) error {
		_, hadPause := s.RigPauses[rigName]
		hadCB := s.CircuitBreaker.IsTripped() || s.CircuitBreaker.Count > 0

		if !hadPause && !req.OverrideCircuitBreaker {
			return errSkipUpdate
		}

		if hadPause {
			delete(s.RigPauses, rigName)
			// Drop the empty map so JSON output stays compact (matches
			// DefaultTownState — RigPauses absent means "no rigs paused").
			if len(s.RigPauses) == 0 {
				s.RigPauses = nil
			}
			appendIncident(s, Incident{
				At:    req.Now.UTC().Format(time.RFC3339),
				Actor: req.Actor,
				Kind:  IncidentRigResume,
				Rig:   rigName,
			})
		}

		if req.OverrideCircuitBreaker && hadCB {
			s.CircuitBreaker.TrippedUntil = ""
			s.CircuitBreaker.Count = 0
			s.CircuitBreaker.WindowStartedAt = ""
			appendIncident(s, Incident{
				At:      req.Now.UTC().Format(time.RFC3339),
				Actor:   req.Actor,
				Kind:    IncidentCircuitBreakerOverride,
				Rig:     rigName,
				Details: fmt.Sprintf("scope=rig=%s (resume --override-circuit-breaker)", rigName),
			})
		}
		return nil
	})
}

// errSkipUpdate is the sentinel returned by mutate callbacks to signal
// "no-op; do not Update the bead." It is intentionally not exported —
// callers shouldn't see it; it just lets the inner CAS loop skip the
// Update call without emitting an error to the operator.
var errSkipUpdate = errors.New("skip update")

// formatPauseDetails composes the human-readable Details string for a
// pause Incident. Records both the duration (as the operator typed it,
// recovered from end - now) and any --reason text. Empty reason is
// elided rather than emitting `reason=`.
func formatPauseDetails(until, now time.Time, reason string) string {
	dur := until.Sub(now).Round(time.Second)
	if reason == "" {
		return fmt.Sprintf("duration=%s", dur)
	}
	return fmt.Sprintf("duration=%s reason=%q", dur, reason)
}

// appendIncident pushes an entry to s.Incidents and trims the head to
// keep len(s.Incidents) <= MaxIncidents. FIFO drop policy: the oldest
// entry is removed when the cap is exceeded.
func appendIncident(s *TownState, inc Incident) {
	s.Incidents = append(s.Incidents, inc)
	if len(s.Incidents) > MaxIncidents {
		// Drop from the head. We allocate a fresh slice rather than
		// reslicing because future Marshal calls will re-allocate
		// anyway and we want a clean baseline length.
		drop := len(s.Incidents) - MaxIncidents
		fresh := make([]Incident, MaxIncidents)
		copy(fresh, s.Incidents[drop:])
		s.Incidents = fresh
	}
}

// mutateTownState is the read-modify-write loop shared by every
// state-changing mutator in this file. mutate is invoked once per
// attempt against a freshly-loaded TownState — retries genuinely
// re-read state.
//
// If mutate returns errSkipUpdate, the loop exits successfully without
// writing anything (idempotent path).
//
// Otherwise mutate may return any other error to abort the
// transaction; that error is returned to the caller verbatim.
func mutateTownState(b *beads.Beads, mutate func(*TownState) error) error {
	if b == nil {
		return fmt.Errorf("mutateTownState: nil beads wrapper")
	}

	var lastErr error
	for attempt := 0; attempt < enabledRigsCASMaxAttempts; attempt++ {
		state, err := LoadTownState(b)
		if err != nil {
			return err
		}

		if err := mutate(&state); err != nil {
			if errors.Is(err, errSkipUpdate) {
				return nil
			}
			return err
		}

		raw, err := state.MarshalMetadata()
		if err != nil {
			return fmt.Errorf("marshaling town state: %w", err)
		}

		err = b.Update(TownStateBeadID, beads.UpdateOptions{Metadata: raw})
		if err == nil {
			return nil
		}

		if !isTransientDoltWriteError(err) {
			return fmt.Errorf("updating town-state bead: %w", err)
		}
		lastErr = err
		time.Sleep(enabledRigsCASBackoff)
	}

	return fmt.Errorf("%w: last error: %v", ErrTownStateCASExhausted, lastErr)
}
