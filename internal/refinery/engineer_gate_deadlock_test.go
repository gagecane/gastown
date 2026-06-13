package refinery

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/rig"
)

// newDeadlockTestEngineer builds a minimal Engineer for exercising the gate /
// test exec path in isolation.
func newDeadlockTestEngineer(t *testing.T) *Engineer {
	t.Helper()
	r := &rig.Rig{Name: "test-rig", Path: t.TempDir()}
	e := NewEngineer(r)
	e.workDir = t.TempDir()
	e.output = io.Discard
	return e
}

// runsBefore runs fn in a goroutine and fails if it does not return within
// limit — i.e. if it deadlocked reading an inherited pipe held open by a
// reparented child.
func runsBefore(t *testing.T, limit time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	start := time.Now()
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > limit {
			t.Fatalf("%s took %v (> %v) — pipe deadlock not bounded", what, elapsed, limit)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("%s did not return: deadlocked on an inherited pipe held open by a reparented child (gc-utizk7 refinery twin)", what)
	}
}

// gateShimCmd is a shell command that backgrounds a long sleep which inherits
// the stdout/stderr pipe and outlives the gate timeout, while the leader also
// sleeps to keep the context busy until the deadline fires. On the buggy
// SetDetachedProcessGroup path the reparented `sleep` keeps the pipe write end
// open and cmd.Run() blocks forever; with SetProcessGroup + WaitDelay the whole
// process group is SIGKILLed on timeout, so Run() returns promptly.
const gateShimCmd = "export PATH=/usr/bin:/bin:$PATH; sleep 60 & sleep 60"

// TestRunGate_DoesNotDeadlockOnReparentedChild is the refinery-side twin of the
// gc-utizk7 deadlock: a gate command forks a child that inherits the pipe and
// outlives the per-gate timeout.
func TestRunGate_DoesNotDeadlockOnReparentedChild(t *testing.T) {
	e := newDeadlockTestEngineer(t)
	gate := &GateConfig{Cmd: gateShimCmd, Timeout: 500 * time.Millisecond}

	runsBefore(t, 10*time.Second, "runGate", func() {
		res := e.runGate(context.Background(), "deadlock-gate", gate, "")
		if res.Success {
			t.Errorf("expected gate to fail on timeout, got success")
		}
	})
}

// TestRunTests_DoesNotDeadlockOnReparentedChild covers the second refinery exec
// site (runTests / TestCommand) with the same forking-child scenario. The
// context deadline (not a per-gate timeout) drives the cancel here.
func TestRunTests_DoesNotDeadlockOnReparentedChild(t *testing.T) {
	e := newDeadlockTestEngineer(t)
	e.config.TestCommand = gateShimCmd
	e.config.RetryFlakyTests = 1

	runsBefore(t, 10*time.Second, "runTests", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		res := e.runTests(ctx)
		if res.Success {
			t.Errorf("expected tests to fail on context timeout, got success")
		}
	})
}
