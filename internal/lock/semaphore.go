package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FlockSemaphore is a cross-process counting semaphore built on the
// non-blocking advisory flocks in this package (FlockTryAcquire). It bounds
// the number of processes that may hold a slot at once, host-wide.
//
// Motivation (gu-0iyrn): every `gt done --pre-verified` re-runs the rig's full
// pre-merge gate suite (`go test ./...`) as its merge gate. Under bulk
// completion (e.g. a 17-bead batch finishing near-together) 4-5 of these run
// concurrently at 110-198% CPU each, spiking host load avg to 19-25 and
// starving the daemon's dispatch heartbeat of CPU. Capping concurrent
// full-suite runs keeps load sane while letting work drain steadily.
//
// Design: N slot files live under a shared directory. Acquire scans the slots
// with non-blocking flocks; the first free slot is taken. If all slots are
// held, it retries with a bounded wait until the timeout fires. The held flock
// is released (and the slot freed) by the returned cleanup func. Because the
// lock is an OS advisory flock tied to the holding process's open fd, a crashed
// holder releases its slot automatically — no stale-slot cleanup needed.
//
// On Windows, FlockTryAcquire is a no-op that always succeeds, so the semaphore
// degrades to "always acquire slot 0" — unbounded, matching the rest of the
// package's Windows posture (Gas Town does not run on Windows in production).
type FlockSemaphore struct {
	dir string
	n   int
}

// NewFlockSemaphore returns a semaphore with n slots backed by flock files in
// dir. n is clamped to a minimum of 1. The directory is created on Acquire.
func NewFlockSemaphore(dir string, n int) *FlockSemaphore {
	if n < 1 {
		n = 1
	}
	return &FlockSemaphore{dir: dir, n: n}
}

// semaphoreRetryInterval is how long Acquire sleeps between full scans when all
// slots are held. Package var so tests can shrink the wait.
var semaphoreRetryInterval = 250 * time.Millisecond

// Acquire takes one of the n slots, returning a cleanup func that releases it.
// It scans slots with non-blocking flocks and retries until a slot frees up or
// the timeout elapses. A non-positive timeout means wait indefinitely.
//
// On timeout it returns a non-nil error and a nil cleanup func; callers that
// want to proceed unthrottled on timeout should treat the error as advisory.
func (s *FlockSemaphore) Acquire(timeout time.Duration) (func(), error) {
	order := make([]int, s.n)
	for i := range order {
		order[i] = i
	}
	return s.AcquireSlots(order, timeout)
}

// AcquireSlots takes one of the slots whose indices appear in `order`, trying
// them in the given order on each scan and retrying until one frees up or the
// timeout elapses. A non-positive timeout means wait indefinitely.
//
// Slot files are named slot-<i>.flock under the semaphore dir, so a slot's
// identity is its index, not its position in `order` — callers that pass
// overlapping index sets (e.g. a high-priority caller that scans a reserved
// tail first, then shared slots as overflow) contend on the same on-disk
// files and so share one host-wide cap. This is the primitive that lets the
// refinery merge gate reserve slots a polecat pre-submit run can never take
// (gu-428u3): the polecat passes only the shared indices, the refinery passes
// the reserved indices first followed by the shared ones.
//
// On timeout it returns a non-nil error and a nil cleanup func; callers that
// want to proceed unthrottled on timeout should treat the error as advisory.
func (s *FlockSemaphore) AcquireSlots(order []int, timeout time.Duration) (func(), error) {
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return nil, fmt.Errorf("creating semaphore dir: %w", err)
	}

	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}

	for {
		for _, i := range order {
			slotPath := filepath.Join(s.dir, fmt.Sprintf("slot-%d.flock", i))
			release, locked, err := FlockTryAcquire(slotPath)
			if err != nil {
				return nil, fmt.Errorf("acquiring semaphore slot %d: %w", i, err)
			}
			if locked {
				return release, nil
			}
		}
		// All requested slots held. Bail if we're past the deadline.
		if !deadline.IsZero() && time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for a free semaphore slot in %s (%d slot(s) tried, all held)", s.dir, len(order))
		}
		time.Sleep(semaphoreRetryInterval)
	}
}
