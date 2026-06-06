package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"testing"
	"time"
)

// TestRunCommandOnWorktree_Timeout proves the gs-iz2 classification: a gate
// that blows its deadline (context deadline exceeded) is reported as a TIMEOUT
// (errGateTimeout), distinct from both a host-kill (errGateHostKilled) and a
// deterministic assertion failure.
func TestRunCommandOnWorktree_Timeout(t *testing.T) {
	d := &Daemon{config: &Config{TownRoot: t.TempDir()}, logger: log.New(io.Discard, "", 0)}

	t.Run("deadline-exceeded gate is a timeout, not a host kill", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := d.runCommandOnWorktree(ctx, "rig", d.config.TownRoot, "test", "sleep 5")
		if !errors.Is(err, errGateTimeout) {
			t.Fatalf("a deadline-exceeded gate must be classified errGateTimeout, got: %v", err)
		}
		if errors.Is(err, errGateHostKilled) {
			t.Fatalf("a deadline timeout must NOT be classified as a host kill: %v", err)
		}
	})

	t.Run("real assertion failure is not a timeout", func(t *testing.T) {
		err := d.runCommandOnWorktree(context.Background(), "rig", d.config.TownRoot, "test", "echo boom; exit 1")
		if errors.Is(err, errGateTimeout) {
			t.Fatalf("a genuine exit-1 failure must NOT be classified as a timeout: %v", err)
		}
	})
}

// TestIsTimeoutFailure pins down the classifier across BOTH runner paths: the
// legacy test_command path propagates the wrapped sentinel (errors.Is works),
// while the gates path flattens per-gate errors into a plain string (chain
// dropped — only substring matching survives). gs-iz2.
func TestIsTimeoutFailure(t *testing.T) {
	wrapped := fmt.Errorf("%w: test (signal: killed)", errGateTimeout)
	// Mirror runGatesOnWorktree's flattening: fmt.Sprintf("gate %q: %v", ...).
	flattened := errors.New(`gate "test": ` + wrapped.Error())

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"wrapped sentinel (legacy path)", wrapped, true},
		{"flattened string (gates path)", flattened, true},
		{"host kill is not a timeout", fmt.Errorf("%w: test", errGateHostKilled), false},
		{"plain assertion failure", errors.New("test failed: exit status 1"), false},
	}
	for _, tc := range cases {
		if got := isTimeoutFailure(tc.err); got != tc.want {
			t.Errorf("%s: isTimeoutFailure = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestTimeoutHigherWatermark proves a timeout-classified red is held one cycle
// longer than an assertion red before escalating OR backing off (gs-iz2). The
// extra confirmation cycle is what lets the runner re-check a timeout SHA once
// more — without it, the gs-3pe backoff would skip the very cycle that would
// have confirmed (and escalated) a genuine hang.
func TestTimeoutHigherWatermark(t *testing.T) {
	town := t.TempDir()
	rigName := "lia_bac"
	const threshold = 2
	now := time.Now()
	const sha = "cccccccccccccccccccccccccccccccccccccccc"
	sig := "gates:test"

	failTimeout := func() (bool, int) {
		return recordFailureAndShouldEscalate(town, rigName, sig, sha, threshold, true, now)
	}

	// Cycle 1: timeout, streak 1 — below both watermarks, no escalation, no backoff.
	if esc, streak := failTimeout(); esc || streak != 1 {
		t.Fatalf("timeout cycle 1: escalate=%v streak=%d, want false/1", esc, streak)
	}
	if shouldBackOffOnRedMain(town, rigName, sha, threshold) {
		t.Fatalf("backed off a timeout at streak 1 (below timeout watermark)")
	}

	// Cycle 2: timeout, streak 2 — an ASSERTION red would escalate+backoff here,
	// but a timeout is held one cycle longer, so neither fires yet.
	if esc, streak := failTimeout(); esc || streak != 2 {
		t.Fatalf("timeout cycle 2: escalate=%v streak=%d, want false/2 (held above assertion watermark)", esc, streak)
	}
	if shouldBackOffOnRedMain(town, rigName, sha, threshold) {
		t.Fatalf("backed off a timeout at streak 2 — a genuine hang would never reach the escalation cycle")
	}

	// Cycle 3: timeout, streak 3 — reaches the timeout watermark → escalate as a
	// real hang, and now the backoff arms so we stop re-running the hung suite.
	if esc, streak := failTimeout(); !esc || streak != 3 {
		t.Fatalf("timeout cycle 3: escalate=%v streak=%d, want true/3 (timeout watermark)", esc, streak)
	}
	if !shouldBackOffOnRedMain(town, rigName, sha, threshold) {
		t.Fatalf("did not back off a sustained timeout after it confirmed+escalated")
	}
}

// TestTimeoutWatermarkClearedByPass proves a recovery resets the timeout
// classification: after a pass, the next failure is treated by the assertion
// watermark again (LastFailureWasTimeout cleared), not stuck on the higher
// timeout watermark.
func TestTimeoutWatermarkClearedByPass(t *testing.T) {
	town := t.TempDir()
	rigName := "lia_bac"
	const threshold = 2
	now := time.Now()
	const sha = "dddddddddddddddddddddddddddddddddddddddd"
	sig := "gates:test"

	// Two timeouts then a pass.
	recordFailureAndShouldEscalate(town, rigName, sig, sha, threshold, true, now)
	recordFailureAndShouldEscalate(town, rigName, sig, sha, threshold, true, now)
	recordAttributionRun(town, rigName, sha, true, now)

	if got := loadMainBranchTestState(town).Rigs[rigName].LastFailureWasTimeout; got {
		t.Fatalf("a pass must clear LastFailureWasTimeout; got %v", got)
	}

	// A subsequent ASSERTION red now escalates at the normal threshold (2),
	// proving the timeout watermark did not persist past the recovery.
	if esc, _ := recordFailureAndShouldEscalate(town, rigName, sig, sha, threshold, false, now); esc {
		t.Fatalf("assertion cycle 1 after recovery must be below the watermark")
	}
	if esc, _ := recordFailureAndShouldEscalate(town, rigName, sig, sha, threshold, false, now); !esc {
		t.Fatalf("assertion cycle 2 after recovery must escalate at the normal threshold")
	}
}
