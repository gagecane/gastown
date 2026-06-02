package beads

import "testing"

// TestWithoutReadThrottleSkipsThrottle guards the gu-pug66 fix: the scheduler
// polecat-capacity fan-out must be exempt from the bd-list-read flock so it
// cannot be starved/wedged (blocking on the throttle while holding
// scheduler-dispatch.lock stalls ALL dispatch).
func TestWithoutReadThrottleSkipsThrottle(t *testing.T) {
	base := New("/tmp/some-rig")
	if base.skipReadThrottle {
		t.Fatal("base Beads should be throttled by default")
	}

	unthrottled := base.WithoutReadThrottle()
	if !unthrottled.skipReadThrottle {
		t.Error("WithoutReadThrottle() did not set skipReadThrottle")
	}
	// Must be a distinct instance — the base stays throttled.
	if base.skipReadThrottle {
		t.Error("WithoutReadThrottle() mutated the receiver; base must stay throttled")
	}
	// Config carried over.
	if unthrottled.workDir != base.workDir {
		t.Errorf("workDir not carried: got %q want %q", unthrottled.workDir, base.workDir)
	}
}
