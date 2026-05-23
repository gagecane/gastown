// CAS-style mutators for the town-state bead's `enabled_rigs[]` field.
//
// Phase 0 task 2a (gu-xpsnt) — `gt auto-test-pr enable` and `disable`
// must keep the per-rig settings JSON (durable record of intent) and
// the town-state bead's `enabled_rigs[]` (denormalized read-cache used
// by `gt auto-test-pr status`) in sync. The settings JSON is
// authoritative ground truth; the slice on the town bead is a cache
// rebuilt by Mayor's reconcile cycle (Phase 0 task 4) on drift.
//
// The two helpers in this file (AppendEnabledRig, RemoveEnabledRig)
// implement a small read-modify-write loop with retry on Dolt
// optimistic-lock errors. They are intentionally conservative: the
// town-state bead is a single-row resource that the Mayor cycle and
// the `enable`/`disable` CLI verbs are the only writers of, so
// contention is rare. We retry only on transient lock/serialization
// errors — anything else (Dolt down, bead not found, etc.) bubbles
// up to the caller, which surfaces a clear "settings-JSON updated but
// town bead out-of-sync" message and exits non-zero per gu-xpsnt's
// acceptance criteria.
package autotestpr

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// ErrTownStateCASExhausted is returned when the AppendEnabledRig /
// RemoveEnabledRig retry loop exhausts its budget on transient
// optimistic-lock errors. Callers should treat it as "town bead
// out-of-sync; Mayor reconcile will heal on next tick" and exit
// non-zero with a clear notice — the per-rig settings JSON is still
// the authoritative ground truth.
var ErrTownStateCASExhausted = errors.New("town-state CAS exhausted retry budget")

// enabledRigsCASMaxAttempts is the retry ceiling for the read-modify-
// write loop. Five attempts with a 50ms backoff is a reasonable
// trade-off: the only writers are the Mayor cycle and a human-driven
// CLI verb, and Dolt optimistic-lock errors are transient by
// definition. Five is enough for the worst-case "Mayor commits in
// the middle of our enable/disable" interleaving without holding the
// CLI for noticeable wall-clock time.
const enabledRigsCASMaxAttempts = 5

// enabledRigsCASBackoff is the per-attempt sleep before retrying. Kept
// short on purpose — the retry exists to absorb a single concurrent
// commit, not to sit through a long Dolt outage. If Dolt is down,
// non-lock errors bubble up immediately.
const enabledRigsCASBackoff = 50 * time.Millisecond

// AppendEnabledRig adds rigName to the town-state bead's
// `enabled_rigs[]` slice and writes the bead back. Idempotent: if the
// rig is already present, the function returns nil without writing.
// Sorts the slice so JSON output is deterministic and `status`
// rendering is stable across calls.
//
// Errors:
//   - ErrTownStateNotProvisioned: the town-state bead is missing.
//     The caller can choose to provision it (EnsureTownStateBead) and
//     retry; we do NOT auto-provision because that would mask
//     misconfiguration during the bootstrap flow.
//   - ErrTownStateCASExhausted: every retry hit a Dolt optimistic-
//     lock error. The settings JSON write is still durable; the next
//     Mayor reconcile cycle will heal `enabled_rigs[]`.
//   - Any other error: bubbles up verbatim.
func AppendEnabledRig(b *beads.Beads, rigName string) error {
	return mutateEnabledRigs(b, rigName, func(rigs []string, name string) []string {
		for _, r := range rigs {
			if r == name {
				return nil // No-op signal: already present.
			}
		}
		out := append(rigs, name) //nolint:gocritic // intentional: allocate a fresh slice
		sort.Strings(out)
		return out
	})
}

// RemoveEnabledRig drops rigName from the town-state bead's
// `enabled_rigs[]` slice and writes the bead back. Idempotent: if the
// rig is not present, the function returns nil without writing.
// Preserves slice order via filter-in-place semantics, then re-sorts
// for the same determinism reason as AppendEnabledRig.
//
// Errors mirror AppendEnabledRig.
func RemoveEnabledRig(b *beads.Beads, rigName string) error {
	return mutateEnabledRigs(b, rigName, func(rigs []string, name string) []string {
		filtered := make([]string, 0, len(rigs))
		removed := false
		for _, r := range rigs {
			if r == name {
				removed = true
				continue
			}
			filtered = append(filtered, r)
		}
		if !removed {
			return nil // No-op signal: not present.
		}
		sort.Strings(filtered)
		return filtered
	})
}

// mutateEnabledRigs is the shared read-modify-write loop. mutate
// returns the new slice on a real change, or nil to signal "no
// change required" (idempotent path). The mutate callback is invoked
// once per attempt with a fresh read of the bead so that retries
// genuinely re-read state rather than racing on stale memory.
func mutateEnabledRigs(b *beads.Beads, rigName string, mutate func([]string, string) []string) error {
	if b == nil {
		return fmt.Errorf("mutateEnabledRigs: nil beads wrapper")
	}
	if rigName == "" {
		return fmt.Errorf("mutateEnabledRigs: empty rig name")
	}

	var lastErr error
	for attempt := 0; attempt < enabledRigsCASMaxAttempts; attempt++ {
		state, err := LoadTownState(b)
		if err != nil {
			// ErrTownStateNotProvisioned is not retryable — the bead
			// genuinely doesn't exist. Bubble up so the caller can
			// provision-and-retry deliberately.
			return err
		}

		next := mutate(state.EnabledRigs, rigName)
		if next == nil {
			// Idempotent path — nothing to write.
			return nil
		}

		state.EnabledRigs = next
		raw, err := state.MarshalMetadata()
		if err != nil {
			return fmt.Errorf("marshaling town state: %w", err)
		}

		err = b.Update(TownStateBeadID, beads.UpdateOptions{Metadata: raw})
		if err == nil {
			return nil
		}

		// Only retry on transient Dolt write conflicts. Any other
		// error (Dolt down, validation failure, bead deleted under
		// us) is non-recoverable from the CLI's perspective.
		if !isTransientDoltWriteError(err) {
			return fmt.Errorf("updating town-state bead: %w", err)
		}
		lastErr = err
		time.Sleep(enabledRigsCASBackoff)
	}

	return fmt.Errorf("%w: last error: %v", ErrTownStateCASExhausted, lastErr)
}

// isTransientDoltWriteError detects the optimistic-lock /
// serialization-failure error class that Dolt emits when two writers
// race on the same row. Mirrors the substring set used by
// internal/polecat.isDoltOptimisticLockError so retry semantics stay
// consistent across the codebase. We intentionally do NOT depend on
// that package here to avoid an import cycle (internal/polecat
// already imports internal/autotestpr indirectly through cmd).
func isTransientDoltWriteError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "optimistic lock") ||
		strings.Contains(msg, "serialization failure") ||
		strings.Contains(msg, "lock wait timeout") ||
		strings.Contains(msg, "try restarting transaction")
}
