package upstreamsync

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
)

// ErrStateBeadNotProvisioned is returned by LoadSyncState when the
// per-rig state bead does not exist. Callers should treat this as
// recoverable — EnsureStateBead creates the bead idempotently.
var ErrStateBeadNotProvisioned = errors.New("upstream-sync state bead not provisioned")

// ErrCASExhausted is returned when the CAS retry loop exhausts all
// attempts without successfully committing.
var ErrCASExhausted = errors.New("CAS retry exhausted for upstream-sync state bead")

// casMaxAttempts is the number of CAS retries before giving up.
const casMaxAttempts = 5

// casBackoff is the delay between CAS retries.
const casBackoff = 50 * time.Millisecond

// LoadSyncState reads the per-rig upstream-sync state bead and parses
// its Issue.Metadata. Returns ErrStateBeadNotProvisioned if the bead
// does not exist.
func LoadSyncState(b *beads.Beads, rigPrefix string) (SyncStateMetadata, error) {
	beadID := StateBeadID(rigPrefix)
	issue, err := b.Show(beadID)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) || isNotFoundError(err) {
			return SyncStateMetadata{}, ErrStateBeadNotProvisioned
		}
		return SyncStateMetadata{}, fmt.Errorf("loading upstream-sync state bead %s: %w", beadID, err)
	}
	return UnmarshalSyncState(issue.Metadata)
}

// EnsureStateBead provisions the per-rig upstream-sync state bead if it
// does not already exist. Idempotent: returns the existing bead on
// subsequent calls without modification.
//
// Follows the two-step create+pin pattern from
// internal/autotestpr/town_state.go EnsureTownStateBead.
func EnsureStateBead(b *beads.Beads, rigPrefix, rigName string, cfg *config.UpstreamSyncConfig) (*beads.Issue, error) {
	if b == nil {
		return nil, fmt.Errorf("EnsureStateBead: nil beads wrapper")
	}

	beadID := StateBeadID(rigPrefix)

	// Check for existing bead.
	existing, err := b.Show(beadID)
	if err == nil {
		// Already provisioned. Best-effort heal: pin if not pinned.
		if existing.Status != beads.StatusPinned {
			pinned := beads.StatusPinned
			if updateErr := b.Update(beadID, beads.UpdateOptions{
				Status: &pinned,
			}); updateErr != nil {
				return existing, fmt.Errorf("repinning state bead: %w", updateErr)
			}
			refetched, _ := b.Show(beadID)
			if refetched != nil {
				return refetched, nil
			}
		}
		return existing, nil
	}

	if !errors.Is(err, beads.ErrNotFound) && !isNotFoundError(err) {
		return nil, fmt.Errorf("checking state bead existence: %w", err)
	}

	// Create with default state.
	upstreamRemote := "upstream"
	upstreamBranch := "main"
	targetBranch := "main"
	if cfg != nil {
		upstreamRemote = cfg.GetUpstreamRemote()
		upstreamBranch = cfg.GetUpstreamBranch()
		targetBranch = cfg.GetTargetBranch()
	}

	defaultState := DefaultSyncStateMetadata(rigName, upstreamRemote, upstreamBranch, targetBranch)
	rawMeta, err := defaultState.MarshalMetadata()
	if err != nil {
		return nil, fmt.Errorf("marshaling default state: %w", err)
	}

	description := fmt.Sprintf(`Upstream sync state bead for rig %s.

Mayor-owned. Tracks the sync state machine, attempt history (bounded to
%d entries), and operational metadata. Read via: gt upstream status --rig=%s

Design: .designs/cv-2s6tq/data.md`, rigName, config.DefaultUpstreamSyncMaxAttempts, rigName)

	issue, err := b.CreateWithID(beadID, beads.CreateOptions{
		Title:       StateBeadTitle(rigName),
		Description: description,
		Labels:      []string{"gt:task", StateBeadLabel, "rig:" + rigName},
		Priority:    2,
		Metadata:    rawMeta,
		Actor:       "mayor",
	})
	if err != nil {
		return nil, fmt.Errorf("creating state bead: %w", err)
	}

	// Pin it.
	pinned := beads.StatusPinned
	if err := b.Update(beadID, beads.UpdateOptions{Status: &pinned}); err != nil {
		return issue, fmt.Errorf("pinning state bead: %w", err)
	}

	refetched, err := b.Show(beadID)
	if err != nil {
		return issue, fmt.Errorf("refetching pinned state bead: %w", err)
	}
	return refetched, nil
}

// MutateSyncState performs a CAS-safe read-modify-write on the per-rig
// upstream-sync state bead. The mutate callback receives a freshly-loaded
// state on each attempt. Return nil to commit; return any error to abort.
func MutateSyncState(b *beads.Beads, rigPrefix string, mutate func(*SyncStateMetadata) error) error {
	if b == nil {
		return fmt.Errorf("MutateSyncState: nil beads wrapper")
	}

	beadID := StateBeadID(rigPrefix)
	var lastErr error

	for attempt := 0; attempt < casMaxAttempts; attempt++ {
		state, err := LoadSyncState(b, rigPrefix)
		if err != nil {
			return err
		}

		if err := mutate(&state); err != nil {
			return err
		}

		raw, err := state.MarshalMetadata()
		if err != nil {
			return fmt.Errorf("marshaling state: %w", err)
		}

		err = b.Update(beadID, beads.UpdateOptions{Metadata: raw})
		if err == nil {
			return nil
		}

		if !isTransientWriteError(err) {
			return fmt.Errorf("updating state bead: %w", err)
		}
		lastErr = err
		time.Sleep(casBackoff)
	}

	return fmt.Errorf("%w: last error: %v", ErrCASExhausted, lastErr)
}

// AppendAttempt appends a completed SyncAttempt to the state bead's
// history and trims to the max bound. Also updates last_sync_* fields
// if the attempt was successful.
func AppendAttempt(b *beads.Beads, rigPrefix string, attempt SyncAttempt) error {
	return MutateSyncState(b, rigPrefix, func(s *SyncStateMetadata) error {
		s.Attempts = append(s.Attempts, attempt)

		// FIFO trim to bounded history.
		max := config.DefaultUpstreamSyncMaxAttempts
		if len(s.Attempts) > max {
			drop := len(s.Attempts) - max
			s.Attempts = s.Attempts[drop:]
		}

		// Update last-sync fields on success.
		if attempt.Outcome == "success" {
			s.LastSyncAt = attempt.CompletedAt
			s.LastSyncOutcome = "success"
			s.LastSyncSHA = attempt.PostSyncSHA
			s.ConsecutiveFailures = 0
		} else {
			s.LastSyncOutcome = attempt.Outcome
			s.ConsecutiveFailures++
		}

		return nil
	})
}

// isNotFoundError handles bd CLI subprocess errors that surface as
// untyped strings rather than wrapped beads.ErrNotFound.
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no issue found")
}

// isTransientWriteError checks if a Dolt write error is transient
// (optimistic lock failure) vs. permanent. Only transient errors are
// retried in the CAS loop.
func isTransientWriteError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "lock") ||
		strings.Contains(msg, "conflict") ||
		strings.Contains(msg, "retry")
}
