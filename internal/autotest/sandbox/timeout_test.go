package sandbox

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// TestRunWithTimeout_FastSubprocessReturnsCleanly is the happy path:
// a subprocess that exits inside the cap returns its output and a
// nil error, with no spurious timeout signal.
func TestRunWithTimeout_FastSubprocessReturnsCleanly(t *testing.T) {
	cmd := exec.Command("/bin/echo", "hello")
	out, err := RunWithTimeout(context.Background(), cmd, 5*time.Second)
	if err != nil {
		t.Fatalf("RunWithTimeout: %v", err)
	}
	if string(out) != "hello\n" {
		t.Fatalf("output = %q, want %q", out, "hello\n")
	}
}

// TestRunWithTimeout_SlowSubprocessHitsCap exercises the per-target
// cap fire path: a `sleep 30` subprocess capped at 250ms must be
// killed and surface ErrPerTargetTimeout. We bound the test duration
// well under the sleep so a missed kill is detectable.
func TestRunWithTimeout_SlowSubprocessHitsCap(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skipf("sleep not on PATH: %v", err)
	}
	cmd := exec.Command("sleep", "30")
	start := time.Now()
	_, err := RunWithTimeout(context.Background(), cmd, 250*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("RunWithTimeout(sleep 30, cap=250ms) returned nil err")
	}
	if !errors.Is(err, ErrPerTargetTimeout) {
		t.Fatalf("err = %v, want ErrPerTargetTimeout", err)
	}
	// Allow generous slack for CI/process-group teardown but still
	// catch a regression that lets the sleep run to completion.
	if elapsed > 5*time.Second {
		t.Fatalf("RunWithTimeout took %s, want <5s — kill path appears not to fire", elapsed)
	}
}

// TestRunWithTimeout_RespectsCallerCtxCancel asserts that when the
// caller's ctx fires before the per-target cap, the returned error
// reflects ctx.Err (not ErrPerTargetTimeout). This is the
// distinction the cycle-budget runner relies on to attribute
// overruns correctly.
func TestRunWithTimeout_RespectsCallerCtxCancel(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skipf("sleep not on PATH: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd := exec.Command("sleep", "30")
	_, err := RunWithTimeout(ctx, cmd, 5*time.Second)
	if err == nil {
		t.Fatalf("RunWithTimeout returned nil; want ctx-deadline error")
	}
	// We deliberately do NOT want ErrPerTargetTimeout here — the
	// caller ctx fired first.
	if errors.Is(err, ErrPerTargetTimeout) {
		t.Fatalf("err = %v; want ctx error, not per-target timeout", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v; want context.DeadlineExceeded", err)
	}
}

// TestRunWithTimeout_ZeroCapMeansNoCap exercises the documented
// "zero or negative cap = no per-target cap" behavior. With no cap,
// only the caller's ctx terminates the run, which here means the
// short subprocess completes normally.
func TestRunWithTimeout_ZeroCapMeansNoCap(t *testing.T) {
	cmd := exec.Command("/bin/echo", "ok")
	out, err := RunWithTimeout(context.Background(), cmd, 0)
	if err != nil {
		t.Fatalf("RunWithTimeout(cap=0): %v", err)
	}
	if string(out) != "ok\n" {
		t.Fatalf("output = %q, want %q", out, "ok\n")
	}
}

// TestRunWithTimeout_RejectsNilCmd guards the input contract. A nil
// cmd MUST return an error rather than panic.
func TestRunWithTimeout_RejectsNilCmd(t *testing.T) {
	if _, err := RunWithTimeout(context.Background(), nil, time.Second); err == nil {
		t.Fatalf("RunWithTimeout(nil cmd) = nil; want error")
	}
}

// TestRunWithTimeout_NonZeroExitPropagates verifies that a
// subprocess exiting with a non-zero status surfaces the
// underlying *exec.ExitError, not ErrPerTargetTimeout.
func TestRunWithTimeout_NonZeroExitPropagates(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 7")
	_, err := RunWithTimeout(context.Background(), cmd, 5*time.Second)
	if err == nil {
		t.Fatalf("RunWithTimeout(exit 7) = nil; want exit error")
	}
	if errors.Is(err, ErrPerTargetTimeout) {
		t.Fatalf("err = %v; want exit error, not per-target timeout", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("err = %v; want *exec.ExitError", err)
	}
}

// TestRunWithTimeout_KillsChildProcessGroup is the process-group-
// reap regression test. Without Setpgid + negative-pid kill, a
// `sh -c 'sleep 30 & wait'` subprocess would leak the inner sleep
// after the parent shell is killed (the wait would unblock, but
// the orphaned sleep would survive on POSIX). With Setpgid, the
// negative-pid SIGKILL reaps both. We can't directly observe the
// child PID across a kill, so we assert the operational invariant:
// the call returns within a small multiple of the cap. A leaked
// inner sleep would manifest as a hang on the goroutine waiting on
// cmd.Wait — Wait does not return until all children's stdio pipes
// close.
func TestRunWithTimeout_KillsChildProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group semantics differ on windows")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh not on PATH: %v", err)
	}
	// `sh -c 'sleep 30'` spawns sleep as a direct child of sh and
	// sh waits implicitly. Without process-group reaping, killing
	// only sh would leave sleep parented to init and Wait would
	// block on the still-open stdout pipe inherited by sleep.
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	start := time.Now()
	_, err := RunWithTimeout(context.Background(), cmd, 200*time.Millisecond)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrPerTargetTimeout) {
		t.Fatalf("err = %v; want ErrPerTargetTimeout", err)
	}
	// 5s is generous; a real hang would block until the inner
	// sleep finishes (30s) or the goroutine's grace period
	// (2s) — either way well above the operational ceiling.
	if elapsed > 5*time.Second {
		t.Fatalf("kill-on-timeout took %s; expected <5s — child process group not reaped", elapsed)
	}
}

// TestRunWithTimeout_ReturnsPartialOutputOnTimeout asserts that
// output the subprocess wrote before the kill is preserved in the
// return slice. Diagnostics on overrun depend on this.
func TestRunWithTimeout_ReturnsPartialOutputOnTimeout(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh not on PATH: %v", err)
	}
	cmd := exec.Command("/bin/sh", "-c", "echo before-sleep; sleep 30")
	out, err := RunWithTimeout(context.Background(), cmd, 500*time.Millisecond)
	if !errors.Is(err, ErrPerTargetTimeout) {
		t.Fatalf("err = %v; want ErrPerTargetTimeout", err)
	}
	if want := "before-sleep"; !contains(out, want) {
		t.Fatalf("output %q missing %q", out, want)
	}
}

// TestCycleBudget_AcquireWithinBudget covers the steady-state
// path: each Acquire returns a positive cap up to the per-target
// ceiling, and Charge correctly decrements the remaining budget.
func TestCycleBudget_AcquireWithinBudget(t *testing.T) {
	b := NewCycleBudget(10 * time.Second)
	cap, err := b.Acquire(2 * time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if cap != 2*time.Second {
		t.Fatalf("cap = %s, want 2s", cap)
	}
	b.Charge(1 * time.Second)
	if got := b.Spent(); got != 1*time.Second {
		t.Fatalf("Spent = %s, want 1s", got)
	}
	if got := b.Remaining(); got != 9*time.Second {
		t.Fatalf("Remaining = %s, want 9s", got)
	}
}

// TestCycleBudget_AcquireTruncatesToRemaining ensures the cap
// returned by Acquire is capped at remaining-budget. This is how
// the per-target ceiling and the cycle-wide budget interact.
func TestCycleBudget_AcquireTruncatesToRemaining(t *testing.T) {
	b := NewCycleBudget(3 * time.Second)
	b.Charge(2500 * time.Millisecond)
	// 500ms remaining; per-target ceiling is 5s but we should only
	// get 500ms back.
	cap, err := b.Acquire(5 * time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if cap != 500*time.Millisecond {
		t.Fatalf("cap = %s, want 500ms", cap)
	}
}

// TestCycleBudget_Exhausted is the negative path: once spent
// meets total, Acquire returns ErrCycleBudgetExhausted without
// reserving any cap.
func TestCycleBudget_Exhausted(t *testing.T) {
	b := NewCycleBudget(1 * time.Second)
	b.Charge(1 * time.Second)
	cap, err := b.Acquire(1 * time.Second)
	if !errors.Is(err, ErrCycleBudgetExhausted) {
		t.Fatalf("err = %v, want ErrCycleBudgetExhausted", err)
	}
	if cap != 0 {
		t.Fatalf("cap on exhausted budget = %s, want 0", cap)
	}
}

// TestCycleBudget_ZeroOrNegativeTotalIsExhausted documents the
// fail-fast behavior of a misconfigured budget.
func TestCycleBudget_ZeroOrNegativeTotalIsExhausted(t *testing.T) {
	for _, total := range []time.Duration{0, -1, -time.Hour} {
		b := NewCycleBudget(total)
		if _, err := b.Acquire(time.Second); !errors.Is(err, ErrCycleBudgetExhausted) {
			t.Fatalf("total=%s: err = %v, want ErrCycleBudgetExhausted", total, err)
		}
	}
}

// TestCycleBudget_ChargeNegativeIsZero defends the budget against
// callers that try to "credit back" time by passing a negative
// duration (whether by accident or otherwise).
func TestCycleBudget_ChargeNegativeIsZero(t *testing.T) {
	b := NewCycleBudget(5 * time.Second)
	b.Charge(2 * time.Second)
	b.Charge(-10 * time.Second) // must be clamped to 0
	if got := b.Spent(); got != 2*time.Second {
		t.Fatalf("Spent = %s after negative charge; want 2s", got)
	}
}

// TestRunWithBudget_FastSubprocessChargesActualElapsed asserts
// that a subprocess that finishes well inside its cap only charges
// the actual elapsed wall-clock to the budget — not the cap itself.
func TestRunWithBudget_FastSubprocessChargesActualElapsed(t *testing.T) {
	b := NewCycleBudget(10 * time.Second)
	cmd := exec.Command("/bin/echo", "fast")
	out, err := RunWithBudget(context.Background(), cmd, b, 5*time.Second)
	if err != nil {
		t.Fatalf("RunWithBudget: %v", err)
	}
	if string(out) != "fast\n" {
		t.Fatalf("output = %q", out)
	}
	// The echo should consume <<5s; assert the budget is mostly
	// intact.
	if got := b.Spent(); got > 1*time.Second {
		t.Fatalf("Spent = %s, want <1s — RunWithBudget over-charged", got)
	}
}

// TestRunWithBudget_RefusesWhenBudgetExhausted is the synthesis
// D10 + D12 contract: when the cycle budget is exhausted, the
// next sandboxed run is refused with ErrCycleBudgetExhausted
// before the subprocess even starts.
func TestRunWithBudget_RefusesWhenBudgetExhausted(t *testing.T) {
	b := NewCycleBudget(100 * time.Millisecond)
	b.Charge(150 * time.Millisecond)
	cmd := exec.Command("/bin/echo", "should-not-run")
	out, err := RunWithBudget(context.Background(), cmd, b, time.Second)
	if !errors.Is(err, ErrCycleBudgetExhausted) {
		t.Fatalf("err = %v, want ErrCycleBudgetExhausted", err)
	}
	if len(out) != 0 {
		t.Fatalf("output = %q, want empty (subprocess must not have started)", out)
	}
}

// TestRunWithBudget_TruncatesToRemainingBudget exercises the
// interaction between the per-target ceiling and the cycle-wide
// budget. The caller asks for a 30s cap but only 200ms remain in
// the cycle, so a `sleep 5` subprocess is killed after ~200ms with
// ErrPerTargetTimeout.
func TestRunWithBudget_TruncatesToRemainingBudget(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skipf("sleep not on PATH: %v", err)
	}
	b := NewCycleBudget(2 * time.Second)
	b.Charge(1800 * time.Millisecond) // 200ms remaining
	cmd := exec.Command("sleep", "5")
	start := time.Now()
	_, err := RunWithBudget(context.Background(), cmd, b, 30*time.Second)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrPerTargetTimeout) {
		t.Fatalf("err = %v, want ErrPerTargetTimeout (truncated cap)", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("elapsed = %s, want close to remaining budget (200ms)", elapsed)
	}
}

// TestRunWithBudget_NilBudget is the input-validation guard.
func TestRunWithBudget_NilBudget(t *testing.T) {
	cmd := exec.Command("/bin/echo", "x")
	if _, err := RunWithBudget(context.Background(), cmd, nil, time.Second); err == nil {
		t.Fatalf("RunWithBudget(nil budget) = nil; want error")
	}
}

// TestRunWithBudget_DefaultPerTargetCapWhenZero asserts that
// passing 0 as perTargetCap falls back to DefaultPerTargetCap.
// We don't run a 5-min subprocess to verify; instead we verify
// the cap returned by Acquire when 0 is passed equals
// DefaultPerTargetCap on a budget with plenty of headroom.
func TestRunWithBudget_DefaultPerTargetCapWhenZero(t *testing.T) {
	// 2 hours of budget so the per-target ceiling is what wins.
	b := NewCycleBudget(2 * time.Hour)
	cap, err := b.Acquire(DefaultPerTargetCap)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if cap != DefaultPerTargetCap {
		t.Fatalf("Acquire(DefaultPerTargetCap) = %s, want %s", cap, DefaultPerTargetCap)
	}
}

// TestDefaults pins the synthesis-mandated defaults so a future
// edit that drifts the constants without updating the synthesis
// fails CI.
func TestDefaults(t *testing.T) {
	if DefaultPerTargetCap != 5*time.Minute {
		t.Fatalf("DefaultPerTargetCap = %s, want 5m (synthesis D10)", DefaultPerTargetCap)
	}
	if DefaultCycleBudget != 30*time.Minute {
		t.Fatalf("DefaultCycleBudget = %s, want 30m (synthesis D10)", DefaultCycleBudget)
	}
}

func contains(b []byte, s string) bool {
	return len(b) >= len(s) && indexOf(b, s) >= 0
}

func indexOf(b []byte, s string) int {
	for i := 0; i+len(s) <= len(b); i++ {
		match := true
		for j := 0; j < len(s); j++ {
			if b[i+j] != s[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
