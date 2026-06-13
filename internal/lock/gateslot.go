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

	// GateReserveEnvVar is the env knob for how many of the GT_GATE_CONCURRENCY
	// slots are reserved for the refinery merge gate and may NOT be taken by
	// polecat pre-submit gate runs. The bash pre-push hook reads the same name.
	GateReserveEnvVar = "GT_GATE_REFINERY_RESERVE"

	// DefaultGateRefineryReserve is the number of gate slots held back for the
	// refinery merge gate (gu-428u3). Before this, the refinery's merge-verify
	// gate and every polecat pre-submit gate drew from one undifferentiated
	// pool: under a polecat swarm the pre-submit runs saturated all slots and
	// perpetually STARVED the refinery, so the merge queue backed up while no
	// individual run was at risk (the live-saturation analog of the stale-flock
	// deadlock ticd-cmk fixed). Reserving one slot guarantees the merge gate can
	// always make progress. Reserve is clamped below the cap so at least one
	// shared slot always remains for polecats — see ResolveGateReserve.
	DefaultGateRefineryReserve = 1
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

// ResolveGateReserve returns how many of the `n` total gate slots are reserved
// for the refinery merge gate, honoring GT_GATE_REFINERY_RESERVE (non-negative
// integer) and falling back to DefaultGateRefineryReserve. The result is
// clamped to [0, n-1] so at least one shared slot always remains for polecat
// pre-submit runs — reserving every slot would deadlock the polecats instead.
func ResolveGateReserve(n int) int {
	reserve := DefaultGateRefineryReserve
	if v := os.Getenv(GateReserveEnvVar); v != "" {
		if r, err := strconv.Atoi(v); err == nil && r >= 0 {
			reserve = r
		}
	}
	if reserve > n-1 {
		reserve = n - 1
	}
	if reserve < 0 {
		reserve = 0
	}
	return reserve
}

// sharedSlotOrder returns the slot indices a non-refinery (polecat pre-submit)
// caller may take: the first n-reserve slots. The reserved tail [n-reserve, n)
// is excluded so a polecat swarm can never occupy it.
func sharedSlotOrder(n, reserve int) []int {
	order := make([]int, 0, n-reserve)
	for i := 0; i < n-reserve; i++ {
		order = append(order, i)
	}
	return order
}

// refinerySlotOrder returns the slot indices the refinery merge gate scans, in
// preference order: the reserved tail [n-reserve, n) FIRST, then the shared
// head [0, n-reserve) as overflow. Preferring the reserved slots means the
// refinery leaves shared slots for polecats when its own reserved slot is free,
// while still being able to use spare shared capacity when polecats are idle.
func refinerySlotOrder(n, reserve int) []int {
	order := make([]int, 0, n)
	for i := n - reserve; i < n; i++ {
		order = append(order, i)
	}
	for i := 0; i < n-reserve; i++ {
		order = append(order, i)
	}
	return order
}

// AcquireGateSlot takes a SHARED slot from the host-wide gate-run semaphore
// under townRoot, waiting up to the given timeout for a free slot. A
// non-positive timeout uses DefaultGateSlotWaitTimeout.
//
// This is the entry point for polecat pre-submit gate runs (gt done
// --pre-verified) and the pre-push hook: it scans only the first n-reserve
// slots, leaving the reserved tail for the refinery merge gate (gu-428u3) so a
// polecat swarm can never starve merges. For the refinery's own gate, use
// AcquireGateSlotPriority.
//
// Returns a release func on success, or nil when no town root is known, the
// slot could not be acquired within the timeout, or the semaphore dir is
// unusable. Callers proceed UNTHROTTLED when nil is returned — the cap is an
// overload guard, not a correctness gate, and stranding work behind it would
// be strictly worse than a brief load spike.
func AcquireGateSlot(townRoot string, timeout time.Duration) func() {
	return acquireGateSlot(townRoot, timeout, false)
}

// AcquireGateSlotPriority takes a slot for the refinery merge gate, preferring
// the reserved tail and falling back to shared slots as overflow. Because the
// reserved slots are excluded from AcquireGateSlot's scan, the refinery is
// guaranteed at least DefaultGateRefineryReserve slots that no polecat
// pre-submit run can occupy — eliminating the starvation deadlock (gu-428u3).
// Total host concurrency is still bounded by GT_GATE_CONCURRENCY: the refinery
// and polecats contend on the same on-disk slot files, just over different
// index sets.
//
// Semantics otherwise match AcquireGateSlot: nil return means proceed
// unthrottled.
func AcquireGateSlotPriority(townRoot string, timeout time.Duration) func() {
	return acquireGateSlot(townRoot, timeout, true)
}

func acquireGateSlot(townRoot string, timeout time.Duration, refinery bool) func() {
	dir := GateSlotDir(townRoot)
	if dir == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = DefaultGateSlotWaitTimeout
	}
	n := ResolveGateConcurrency()
	reserve := ResolveGateReserve(n)
	order := sharedSlotOrder(n, reserve)
	if refinery {
		order = refinerySlotOrder(n, reserve)
	}
	sem := NewFlockSemaphore(dir, n)
	release, err := sem.AcquireSlots(order, timeout)
	if err != nil {
		// Timed out or dir error — don't strand the caller, just run.
		return nil
	}
	return release
}
