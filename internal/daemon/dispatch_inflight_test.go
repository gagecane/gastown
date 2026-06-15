package daemon

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// TestDispatchQueuedWork_SkipsWhenInFlight verifies the non-blocking in-flight
// guard (gu-qwn0n): when a prior dispatch goroutine is still draining, the next
// heartbeat's dispatchQueuedWork call returns immediately without spawning a
// second subprocess goroutine, and the in-flight flag is left set so the running
// dispatch can clear it on completion.
func TestDispatchQueuedWork_SkipsWhenInFlight(t *testing.T) {
	var logBuf bytes.Buffer
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(&logBuf, "", 0),
	}

	// Simulate a dispatch goroutine already in flight.
	d.dispatchInFlight.Store(true)

	// This call must take the skip path: CompareAndSwap(false, true) fails, so
	// it returns without launching a goroutine that would shell out to
	// `gt scheduler run`.
	d.dispatchQueuedWork()

	// The flag must remain set — the skip path must not clear another
	// goroutine's in-flight marker.
	if !d.dispatchInFlight.Load() {
		t.Error("in-flight flag was cleared by the skip path; it must be left for the running dispatch to clear")
	}

	if !strings.Contains(logBuf.String(), "still in flight, skipping") {
		t.Errorf("expected skip log message, got: %q", logBuf.String())
	}
}
