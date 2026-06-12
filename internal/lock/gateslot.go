package lock

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Gate-slot semaphore: the canonical host-wide throttle on heavy "full-suite"
// gate runs (`go test ./...`, `go build ./...`, lint) shared across every
// consumer that runs them on the same host.
//
// Why this lives here (gu-ym89r): the throttle was introduced for the polecat
// `gt done --pre-verified` path (gu-0iyrn) and later mirrored into the bash
// pre-push hook (gs-orsm). Both pointed at the SAME on-disk slot dir
// (<townRoot>/.runtime/locks/gate-slots) and read the SAME GT_GATE_CONCURRENCY
// knob — but each re-derived the path and the default independently, so the
// two could silently drift out of the shared pool (a path typo or a default
// mismatch would split them into two unsynchronized caps and the throttle
// would quietly stop bounding total host concurrency).
//
// Crucially, the refinery's own merge gate (internal/refinery, which runs the
// full `go test ./...` suite on every MR) joined NEITHER pool, so its heavy
// run competed uncounted with the capped polecat runs against the same host +
// shared Dolt server — the contention that flaked refinery merges (gu-ym89r,
// refinery escalation gc-56r2yy). Consolidating the slot dir, the concurrency
// knob, and the acquire helper here means every Go consumer joins the identical
// flock pool by construction, and the bash hook (which still computes the same
// path/knob inline) interoperates because the values are unchanged.

const (
	// GateSlotEnvVar is the env knob for the host-wide gate-run cap. The bash
	// pre-push hook reads the same variable name; keep them in sync.
	GateSlotEnvVar = "GT_GATE_CONCURRENCY"

	// DefaultGateConcurrency caps host-wide concurrent full-suite gate runs.
	// 2 keeps load average sane while letting two batches drain in parallel.
	// The bash pre-push hook hardcodes the same default; keep them in sync.
	DefaultGateConcurrency = 2

	// DefaultGateSlotWaitTimeout bounds how long a caller waits for a free slot
	// before proceeding unthrottled. Generous: a full `go test ./...` can take
	// minutes, and we'd rather queue than skip the cap under bulk load. The
	// bash hook uses GT_GATE_SLOT_WAIT_SECONDS=600 to match.
	DefaultGateSlotWaitTimeout = 10 * time.Minute
)

// GateSlotDir returns the canonical shared slot directory for the gate-run
// semaphore under the given town root. Returns "" when townRoot is empty
// (callers treat that as "no town root known — proceed unthrottled"). The
// bash pre-push hook constructs the identical path
// ($GT_TOWN_ROOT/.runtime/locks/gate-slots); the two MUST stay byte-identical
// or they stop sharing the flock files.
func GateSlotDir(townRoot string) string {
	if townRoot == "" {
		return ""
	}
	return filepath.Join(townRoot, ".runtime", "locks", "gate-slots")
}

// ResolveGateConcurrency returns the host-wide cap on concurrent gate runs,
// honoring GT_GATE_CONCURRENCY (positive integer) and falling back to
// DefaultGateConcurrency otherwise.
func ResolveGateConcurrency() int {
	if v := os.Getenv(GateSlotEnvVar); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return DefaultGateConcurrency
}

// AcquireGateSlot takes a slot from the host-wide gate-run semaphore under
// townRoot, waiting up to the given timeout for a free slot. A non-positive
// timeout uses DefaultGateSlotWaitTimeout.
//
// Returns a release func on success, or nil when no town root is known, the
// slot could not be acquired within the timeout, or the semaphore dir is
// unusable. Callers proceed UNTHROTTLED when nil is returned — the cap is an
// overload guard, not a correctness gate, and stranding work behind it would
// be strictly worse than a brief load spike.
func AcquireGateSlot(townRoot string, timeout time.Duration) func() {
	dir := GateSlotDir(townRoot)
	if dir == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = DefaultGateSlotWaitTimeout
	}
	sem := NewFlockSemaphore(dir, ResolveGateConcurrency())
	release, err := sem.Acquire(timeout)
	if err != nil {
		// Timed out or dir error — don't strand the caller, just run.
		return nil
	}
	return release
}
