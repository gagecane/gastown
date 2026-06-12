package completion

import (
	"context"
	"testing"
	"time"
)

// TestExecGate_DoesNotDeadlockOnReparentedChild reproduces the second
// gc-utizk7 gt-done deadlock site. A pre-merge gate command is `sh -c
// "<cmd>"`, and real gate commands (`go test ./...`) fork compiler/test
// children that inherit the stdout/stderr pipe write end. The pre-fix execGate
// used exec.CommandContext + CombinedOutput with no process-group kill: on the
// gate timeout, CommandContext SIGKILLs only the sh leader; the backgrounded
// child reparents to PID 1, keeps the inherited pipe write end open, and
// CombinedOutput's internal Wait() blocks forever (futex_wait_queue, fd7/fd9
// read-pipe with no writer, zero children).
//
// The shim command backgrounds `sleep 60` (inherits the pipe, outlives the
// timeout) and the leader also sleeps to keep the context busy until the
// deadline fires. With the fix (util.SetProcessGroup + WaitDelay) the whole
// process group is SIGKILLed on timeout, so execGate returns promptly.
func TestExecGate_DoesNotDeadlockOnReparentedChild(t *testing.T) {
	// sh -c: re-export PATH so `sleep` resolves, background a long sleep that
	// inherits the pipe, then block the leader past the context deadline.
	gateCmd := "export PATH=/usr/bin:/bin:$PATH; sleep 60 & sleep 60"

	done := make(chan struct{})
	start := time.Now()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		_, _ = execGate(ctx, t.TempDir(), gateCmd)
		close(done)
	}()

	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Fatalf("execGate took %v — gate pipe deadlock not bounded", elapsed)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("execGate did not return: deadlocked reading an inherited pipe held open by a reparented child (gc-utizk7 regression)")
	}
}

// TestRunPreVerifyGates_UnsetTimeoutIsBounded verifies the second half of the
// gc-utizk7 fix: a gate with no configured timeout must not inherit an
// unbounded context. Before the fix, gate.timeout==0 left gateCtx equal to the
// caller's context (context.Background() from gt done), so a wedged subprocess
// would hang forever. The runner now applies defaultGateTimeout, so the gate
// context carries a deadline.
func TestRunPreVerifyGates_UnsetTimeoutIsBounded(t *testing.T) {
	gates := []preVerifyGate{{name: "no-timeout", cmd: "true"}} // timeout unset (0)

	var sawDeadline bool
	stub := func(ctx context.Context, _ string, _ string) ([]byte, error) {
		if _, ok := ctx.Deadline(); ok {
			sawDeadline = true
		}
		return nil, nil
	}

	ok, err := runPreVerifyGates(context.Background(), "/tmp", gates, stub)
	if !ok || err != nil {
		t.Fatalf("expected ok=true nil err, got ok=%v err=%v", ok, err)
	}
	if !sawDeadline {
		t.Error("gate with unset timeout should run under a bounded context (defaultGateTimeout), but no deadline was set")
	}
}
