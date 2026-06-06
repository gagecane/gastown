package cmd

import (
	"fmt"
	"runtime"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/rig"
)

// Seams for tests. Production wires these to the real load reader and the
// refinery merge-queue depth query.
var (
	// refineryBackoffLoadPerCore returns the current 1-minute load average
	// divided by the CPU core count.
	refineryBackoffLoadPerCore = func() float64 {
		cores := runtime.NumCPU()
		if cores <= 0 {
			cores = 1
		}
		return daemon.LoadAverage1() / float64(cores)
	}

	// refineryQueueDepth returns the number of open merge requests in the
	// given rig's refinery queue. A non-nil error is treated as "unknown"
	// by the caller and fails open (does not throttle).
	refineryQueueDepth = func(r *rig.Rig) (int, error) {
		mgr := refinery.NewManager(r)
		items, err := mgr.Queue()
		if err != nil {
			return 0, err
		}
		return len(items), nil
	}
)

// checkRefineryBackoffThrottle decides whether to refuse a polecat spawn for a
// rig because its refinery is draining a non-empty merge queue under host
// build pressure (gu-5wn56).
//
// The deadlock it breaks: a load-sensitive refinery backs off and waits for
// build pressure to ease before retrying a merge gate; meanwhile an uncapped
// dispatcher keeps spawning polecats that each run a heavy build, so build
// pressure never drops and the queue freezes. By refusing the spawn while both
// signals hold — (a) the rig's refinery has queued MRs to drain and (b) host
// load/core is above the configured threshold — dispatch and the merge gate no
// longer both saturate the same host unbounded, letting load ease so the
// refinery can retry.
//
// It returns a non-nil (retryable) error when the spawn should be refused. The
// error is informational: the bead stays queued and the next dispatch tick
// re-evaluates once load eases or the queue drains.
//
// Fail-open in all uncertain cases: the throttle is opt-in per rig
// (polecat.pause_on_refinery_backoff), and any error querying the queue is
// treated as "don't throttle" so an observability failure never starves
// dispatch.
func checkRefineryBackoffThrottle(r *rig.Rig, settings *config.RigSettings) error {
	if !settings.GetPauseOnRefineryBackoff() {
		return nil
	}

	// Build-pressure signal: only throttle when the host is actually loaded.
	loadPerCore := refineryBackoffLoadPerCore()
	threshold := settings.GetRefineryBackoffLoadPerCore()
	if loadPerCore <= threshold {
		return nil
	}

	// Queue signal: only throttle when the refinery has something to drain.
	// Fail open on query errors — never block dispatch on an unreadable queue.
	depth, err := refineryQueueDepth(r)
	if err != nil || depth <= 0 {
		return nil
	}

	return fmt.Errorf("refinery backoff throttle: rig %s has %d queued merge request(s) "+
		"draining under build pressure (load/core %.2f > %.2f). Deferring spawn so build "+
		"pressure can ease and the refinery can retry. Bead stays queued. "+
		"Disable: gt rig settings set %s polecat.pause_on_refinery_backoff false",
		r.Name, depth, loadPerCore, threshold, r.Name)
}
