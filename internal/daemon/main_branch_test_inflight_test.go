package daemon

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// TestLaunchMainBranchTests_SkipsWhenInFlight verifies the non-blocking
// in-flight guard (gu-23xve): when a prior main_branch_test cycle is still
// draining, the next tick's launchMainBranchTests call returns immediately
// without spawning a second goroutine, and the in-flight flag is left set so
// the running cycle can clear it on completion.
func TestLaunchMainBranchTests_SkipsWhenInFlight(t *testing.T) {
	var logBuf bytes.Buffer
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(&logBuf, "", 0),
	}

	// Simulate a cycle already in flight.
	d.mainBranchTestInFlight.Store(true)

	// This call must take the skip path: CompareAndSwap(false, true) fails, so
	// it returns without launching a goroutine that would run the gate suite.
	d.launchMainBranchTests()

	// The flag must remain set — the skip path must not clear another
	// goroutine's in-flight marker.
	if !d.mainBranchTestInFlight.Load() {
		t.Error("in-flight flag was cleared by the skip path; it must be left for the running cycle to clear")
	}

	if !strings.Contains(logBuf.String(), "still in flight, skipping") {
		t.Errorf("expected skip log message, got: %q", logBuf.String())
	}
}
