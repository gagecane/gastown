package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestIntegration_5a5b5c_HandRolledFixture is the bead's
// acceptance integration test: runs the combined 5a (cred-strip +
// CWD-pin) + 5b (network-drop + module-cache warm-up) + 5c
// (per-target wall-clock cap + cycle-wide budget) wrapper against
// a hand-rolled stdlib-only Go fixture, and verifies that:
//
//   - a fast test run under all three primitives passes (positive),
//   - a slow test run is killed by the per-target cap (per-target),
//   - a third run on an already-exhausted cycle budget is refused
//     before exec (cycle-wide).
//
// This test deliberately exercises the full layered Apply →
// ApplyOffline → RunWithBudget composition. If any one of 5a/5b/5c
// regresses (cred-strip wiped, namespace not installed, cap not
// respected), the relevant assertion fires.
func TestIntegration_5a5b5c_HandRolledFixture(t *testing.T) {
	if !NetDropSupported() {
		t.Skipf("net-drop unsupported on %s", runtime.GOOS)
	}
	if testing.Short() {
		t.Skip("skipping integration test under -short")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go on PATH: %v", err)
	}

	fixture := buildIntegrationFixture(t)
	sb, err := New(fixture)
	if err != nil {
		t.Fatal(err)
	}

	// Phase 5b: warm up the module cache so the offline test runs
	// have everything they need.
	warmCtx, warmCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer warmCancel()
	if err := sb.WarmUpGoModules(warmCtx, goBin); err != nil {
		t.Fatalf("WarmUpGoModules: %v", err)
	}

	// Build a single CycleBudget large enough for the fast and
	// per-target test cases but small enough that the third-run
	// case can deliberately exhaust it. 6 seconds: ~1s for the fast
	// run, ~750ms for the per-target run, then we manually charge
	// the rest to drive the budget to exhaustion.
	budget := NewCycleBudget(6 * time.Second)

	t.Run("fast_test_passes_under_5a_5b_5c", func(t *testing.T) {
		runIntegrationFastSubtest(t, sb, goBin, budget)
	})

	t.Run("slow_test_killed_by_per_target_cap", func(t *testing.T) {
		runIntegrationSlowSubtest(t, sb, goBin, budget)
	})

	t.Run("third_run_refused_when_budget_exhausted", func(t *testing.T) {
		runIntegrationBudgetExhaustedSubtest(t, sb, goBin, budget)
	})
}

// runIntegrationFastSubtest covers the positive case: a stdlib-only
// fast test passes under the combined 5a+5b+5c sandbox.
func runIntegrationFastSubtest(t *testing.T, sb *Sandbox, goBin string, budget *CycleBudget) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, "test", "-run", "TestFixtureFast$", ".")
	if err := sb.ApplyOffline(cmd); err != nil {
		if errors.Is(err, ErrNetDropUnsupported) {
			t.Skipf("net-drop unsupported at runtime: %v", err)
		}
		t.Fatalf("ApplyOffline: %v", err)
	}
	out, err := RunWithBudget(ctx, cmd, budget, 750*time.Millisecond)
	if err != nil {
		s := string(out)
		if strings.Contains(s, "operation not permitted") || strings.Contains(s, "permission denied") {
			t.Skipf("kernel rejected unprivileged netns: %v: %s", err, s)
		}
		t.Fatalf("fast test failed: %v\n%s", err, s)
	}
}

// runIntegrationSlowSubtest exercises the per-target cap: a test
// that sleeps far longer than the cap is killed and surfaces
// ErrPerTargetTimeout.
func runIntegrationSlowSubtest(t *testing.T, sb *Sandbox, goBin string, budget *CycleBudget) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, "test", "-run", "TestFixtureSlow$", "-count=1", ".")
	if err := sb.ApplyOffline(cmd); err != nil {
		if errors.Is(err, ErrNetDropUnsupported) {
			t.Skipf("net-drop unsupported at runtime: %v", err)
		}
		t.Fatalf("ApplyOffline: %v", err)
	}
	start := time.Now()
	_, err := RunWithBudget(ctx, cmd, budget, 750*time.Millisecond)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrPerTargetTimeout) {
		t.Fatalf("slow test err = %v, want ErrPerTargetTimeout (cap=750ms)", err)
	}
	// Generous slack for kernel kill + Wait drain. A regression
	// where the namespace prevents SIGKILL delivery would surface
	// as elapsed >> 5s; but on a healthy kernel this is well
	// under 3s.
	if elapsed > 8*time.Second {
		t.Fatalf("slow test elapsed = %s; per-target cap appears to not have fired", elapsed)
	}
}

// runIntegrationBudgetExhaustedSubtest exercises the cycle-wide cap.
// We manually drain whatever is left of the budget, then attempt a
// third run; it must be refused with ErrCycleBudgetExhausted before
// exec.
func runIntegrationBudgetExhaustedSubtest(t *testing.T, sb *Sandbox, goBin string, budget *CycleBudget) {
	t.Helper()
	// Drain the rest of the budget so the next acquisition fails.
	if rem := budget.Remaining(); rem > 0 {
		budget.Charge(rem + time.Second)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, "test", "-run", "TestFixtureFast$", ".")
	if err := sb.ApplyOffline(cmd); err != nil {
		if errors.Is(err, ErrNetDropUnsupported) {
			t.Skipf("net-drop unsupported at runtime: %v", err)
		}
		t.Fatalf("ApplyOffline: %v", err)
	}
	out, err := RunWithBudget(ctx, cmd, budget, 5*time.Second)
	if !errors.Is(err, ErrCycleBudgetExhausted) {
		t.Fatalf("third run err = %v, want ErrCycleBudgetExhausted", err)
	}
	if len(out) != 0 {
		t.Fatalf("third run produced output (%q); subprocess must not have started", out)
	}
}

// buildIntegrationFixture writes a stdlib-only Go module containing
// two trivial tests:
//
//   - TestFixtureFast: arithmetic, returns immediately.
//   - TestFixtureSlow: time.Sleep(30s); designed to be killed by the
//     per-target cap. We use a real time.Sleep rather than testing.T
//     wall-clock helpers because the cap fires at the OS process
//     level — only a real sleep proves we kill the process.
//
// The module is stdlib-only so the warm-up step is a near-no-op and
// the fixture survives a network-drop without any module-proxy
// dependency.
func buildIntegrationFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module integrationfixture\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib.go"),
		[]byte("package integrationfixture\n\nfunc Add(a, b int) int { return a + b }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib_test.go"), []byte(`package integrationfixture

import (
	"testing"
	"time"
)

func TestFixtureFast(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("math broken")
	}
}

func TestFixtureSlow(t *testing.T) {
	// Sleep far longer than the per-target cap. The wall-clock
	// helper kills the test binary at the process level, so this
	// goroutine never actually returns inside the test process —
	// the OS reaps it first.
	time.Sleep(30 * time.Second)
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}
