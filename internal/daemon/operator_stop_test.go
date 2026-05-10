package daemon

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// TestLogOperatorStoppedSkip_DedupPerEpisode verifies the log message is
// emitted exactly once per contiguous "operator stopped" episode per rig.
// The dedup is cleared via clearOperatorStoppedLog so a subsequent fresh
// stop re-emits the line, matching the missingRigBeadLogged pattern used
// elsewhere in the daemon.
func TestLogOperatorStoppedSkip_DedupPerEpisode(t *testing.T) {
	var buf bytes.Buffer
	d := &Daemon{logger: log.New(&buf, "", 0)}

	// First call logs.
	d.logOperatorStoppedSkip("casc_webapp")
	firstLog := buf.String()
	if !strings.Contains(firstLog, "casc_webapp") {
		t.Fatalf("expected first skip to log rig name, got %q", firstLog)
	}
	if !strings.Contains(firstLog, "operator-stopped") {
		t.Fatalf("expected first skip to log reason, got %q", firstLog)
	}

	// Subsequent calls for the same rig are silent.
	buf.Reset()
	for i := 0; i < 5; i++ {
		d.logOperatorStoppedSkip("casc_webapp")
	}
	if buf.Len() != 0 {
		t.Fatalf("expected dedup to suppress subsequent logs, got %q", buf.String())
	}

	// A different rig still logs (per-rig dedup).
	buf.Reset()
	d.logOperatorStoppedSkip("casc_shared")
	if !strings.Contains(buf.String(), "casc_shared") {
		t.Fatalf("expected second rig to log independently, got %q", buf.String())
	}

	// After clearing the dedup for a rig, the next skip re-emits the line.
	// This simulates the daemon observing the flag toggled off and back on.
	buf.Reset()
	d.clearOperatorStoppedLog("casc_webapp")
	d.logOperatorStoppedSkip("casc_webapp")
	if !strings.Contains(buf.String(), "casc_webapp") {
		t.Fatalf("expected re-log after clear, got %q", buf.String())
	}
}

// TestClearOperatorStoppedLog_Safe verifies clearOperatorStoppedLog is a
// no-op when the dedup map is nil or the rig was never logged. This runs
// on every heartbeat for every operational rig, so it must not panic or
// allocate unnecessarily.
func TestClearOperatorStoppedLog_Safe(t *testing.T) {
	// nil map (first heartbeat before any skip) — must not panic.
	d := &Daemon{logger: log.New(&bytes.Buffer{}, "", 0)}
	d.clearOperatorStoppedLog("never-seen")

	// Non-nil map but missing key — also a no-op.
	d.operatorStoppedRefineryLogged = map[string]bool{"other-rig": true}
	d.clearOperatorStoppedLog("not-in-map")
	if !d.operatorStoppedRefineryLogged["other-rig"] {
		t.Error("expected unrelated entries to survive clear of a different rig")
	}
}
