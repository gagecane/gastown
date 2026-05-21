package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// DefaultPerTargetCap is the per-target wall-clock cap a polecat
// gate run is held to by default. The synthesis (D10) names "5-min
// per-target". Callers MAY pass a smaller cap via RunWithTimeout /
// RunWithBudget; passing a larger cap is permitted by the API but
// the synthesis treats this default as the policy ceiling.
const DefaultPerTargetCap = 5 * time.Minute

// DefaultCycleBudget is the cycle-wide wall-clock budget a polecat
// must complete the full Auto-Test-PR gate sequence inside. The
// synthesis (D10) names "cycle-wide 30-min cap". When a CycleBudget
// is exhausted, further sandboxed subprocesses are refused with
// ErrCycleBudgetExhausted so the polecat can exit cleanly with NOTES
// rather than wedge a slot indefinitely.
const DefaultCycleBudget = 30 * time.Minute

// ErrPerTargetTimeout is returned by RunWithTimeout / RunWithBudget
// when the configured per-target wall-clock cap fires before the
// subprocess exits. It is distinct from ErrCycleBudgetExhausted so
// callers can attribute overrun to the per-target ceiling vs the
// cycle-wide budget.
var ErrPerTargetTimeout = errors.New("sandbox: per-target wall-clock cap exceeded")

// ErrCycleBudgetExhausted is returned by CycleBudget.Acquire (and
// transitively by RunWithBudget) when a cycle's accumulated
// wall-clock has met or exceeded the configured total budget. The
// synthesis (D12) requires the polecat to exit with NOTES on
// overrun; surfacing a sentinel error lets the gate runner detect
// budget exhaustion without parsing log output.
var ErrCycleBudgetExhausted = errors.New("sandbox: cycle wall-clock budget exhausted")

// RunWithTimeout starts cmd, waits for it to exit, and enforces a
// per-target wall-clock cap on the subprocess. If the cap fires
// before exit, the subprocess (and its entire process group on
// platforms that support process groups) is killed and
// ErrPerTargetTimeout is returned wrapped.
//
// The effective deadline is min(cap, ctx.Deadline-now). Passing a
// zero or negative cap is treated as "no cap" — only ctx.Deadline
// applies. Passing a cap larger than ctx.Deadline-now is permitted
// but ctx wins.
//
// cmd.Stdout/Stderr are NOT honored: RunWithTimeout collects all
// output via CombinedOutput so a kill-on-timeout still yields the
// partial output for diagnostics. Callers that need streaming
// stdout/stderr should not use this helper and should manage their
// own kill path.
//
// RunWithTimeout does NOT call sb.Apply or sb.ApplyOffline on cmd —
// it is a wall-clock primitive composable with either path. The
// caller is expected to have already configured cmd via
// sb.Apply / sb.ApplyOffline. The synthesis 5c spec keeps these
// orthogonal so the timeout helper is unit-testable without going
// through the netns code path.
//
// Output bytes are returned even on timeout (whatever the
// subprocess managed to print before kill).
func RunWithTimeout(ctx context.Context, cmd *exec.Cmd, cap time.Duration) ([]byte, error) {
	if cmd == nil {
		return nil, errors.New("sandbox: nil cmd")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Apply per-target cap on top of the caller's ctx. A zero or
	// negative cap means "no per-target cap" — fall back to ctx
	// alone.
	runCtx := ctx
	var cancel context.CancelFunc
	if cap > 0 {
		runCtx, cancel = context.WithTimeout(ctx, cap)
		defer cancel()
	}

	// Start cmd in its own process group on platforms that support
	// it, so SIGKILL on the pgid reaps any children spawned by
	// `go test` (the test binary, helper subprocesses, etc.). On
	// platforms without process groups this is a no-op and we fall
	// back to killing only the immediate child.
	configureProcessGroup(cmd)

	// Buffer stdout+stderr so we can return them on timeout.
	var combined bytes.Buffer
	if cmd.Stdout == nil {
		cmd.Stdout = &combined
	}
	if cmd.Stderr == nil {
		cmd.Stderr = &combined
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("sandbox: start: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-runCtx.Done():
		// Timeout (or caller-cancelled). Kill the process group
		// (or the process) and drain Wait so the goroutine
		// terminates and pipes are closed.
		killProcessGroup(cmd)
		// Wait for cmd.Wait to return so the goroutine releases
		// resources. We cap this at a short grace period to avoid
		// blocking indefinitely on a kernel that's slow to deliver
		// SIGKILL.
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			// Process did not honor SIGKILL within grace. Return
			// what we have and accept the goroutine leak — this
			// is the "kernel hostage" failure mode and there is
			// no portable recovery.
		}
		// Distinguish per-target cap from caller-supplied ctx
		// cancellation. We only return ErrPerTargetTimeout when
		// the original caller ctx is still alive, meaning our
		// derived cap fired (not theirs).
		if cap > 0 && ctx.Err() == nil {
			return combined.Bytes(), fmt.Errorf("%w (cap=%s)", ErrPerTargetTimeout, cap)
		}
		return combined.Bytes(), runCtx.Err()
	case err := <-done:
		return combined.Bytes(), err
	}
}

// CycleBudget tracks accumulated wall-clock spent on sandboxed
// subprocesses across one Auto-Test-PR cycle. The polecat creates
// a single CycleBudget at cycle start and threads it through every
// gate runner that calls RunWithBudget. When the budget is
// exhausted, further calls return ErrCycleBudgetExhausted before
// starting a subprocess.
//
// CycleBudget is safe for concurrent use; the polecat runs gates
// sequentially today but the type defends the rare interleaving
// (e.g. flakiness rerun spawned in a goroutine).
type CycleBudget struct {
	mu      sync.Mutex
	total   time.Duration
	spent   time.Duration
	started time.Time
}

// NewCycleBudget returns a CycleBudget with the given total
// wall-clock budget. Passing zero or a negative total returns a
// budget that is immediately exhausted, so a misconfigured caller
// fails fast rather than running gates without enforcement.
func NewCycleBudget(total time.Duration) *CycleBudget {
	return &CycleBudget{
		total:   total,
		started: time.Now(),
	}
}

// Remaining reports the wall-clock left in the cycle. A non-positive
// value indicates the budget is exhausted.
func (b *CycleBudget) Remaining() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total - b.spent
}

// Spent reports the wall-clock charged against the budget so far.
func (b *CycleBudget) Spent() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}

// Total reports the total wall-clock budget configured at
// construction.
func (b *CycleBudget) Total() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

// Acquire reserves wall-clock from the budget for the next
// subprocess invocation, returning the cap to apply (the smaller of
// perTargetCap and the remaining budget). When the budget is
// exhausted, Acquire returns ErrCycleBudgetExhausted without
// reserving anything. The returned cap is always positive on
// success.
//
// Acquire does NOT charge the budget — RunWithBudget is responsible
// for calling Charge with the actual elapsed wall-clock once the
// subprocess exits. This split lets a runner that exits early (e.g.
// the subprocess returns in 200ms when allowed up to 5min) only
// charge what was actually spent.
func (b *CycleBudget) Acquire(perTargetCap time.Duration) (time.Duration, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.total - b.spent
	if remaining <= 0 {
		return 0, ErrCycleBudgetExhausted
	}
	cap := perTargetCap
	if cap <= 0 || cap > remaining {
		cap = remaining
	}
	return cap, nil
}

// Charge debits elapsed wall-clock from the budget. Negative
// elapsed values are treated as zero (the synthesis has no
// "credit-back" semantics — a misbehaving caller shouldn't pad the
// budget by passing a negative duration).
func (b *CycleBudget) Charge(elapsed time.Duration) {
	if elapsed < 0 {
		elapsed = 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spent += elapsed
}

// RunWithBudget composes RunWithTimeout with a CycleBudget. It
// reserves a per-target cap from the budget, runs the subprocess
// under that cap, and charges the actual elapsed wall-clock back
// to the budget on completion. Returns ErrCycleBudgetExhausted
// without starting cmd if the budget is already empty.
//
// perTargetCap is the desired ceiling for this run; the effective
// cap is min(perTargetCap, remaining-budget). Passing 0 means
// "use whatever the budget has left, capped at DefaultPerTargetCap"
// — the helper supplies the synthesis-recommended ceiling on
// behalf of callers that don't care about per-target tuning.
func RunWithBudget(ctx context.Context, cmd *exec.Cmd, budget *CycleBudget, perTargetCap time.Duration) ([]byte, error) {
	if budget == nil {
		return nil, errors.New("sandbox: nil budget")
	}
	if perTargetCap == 0 {
		perTargetCap = DefaultPerTargetCap
	}
	cap, err := budget.Acquire(perTargetCap)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	out, runErr := RunWithTimeout(ctx, cmd, cap)
	budget.Charge(time.Since(start))
	return out, runErr
}
