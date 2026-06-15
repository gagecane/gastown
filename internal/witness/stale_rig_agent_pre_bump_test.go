package witness

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/session"
)

// installFakeTmuxAliveCreated installs a fake `tmux` that reports the named
// session alive (has-session exit 0) AND answers the `list-sessions -F
// #{session_created}` query GetSessionCreatedTime uses with createdUnix. This
// reaches the gu-2j7n1 discriminator (sessionPredatesBinary), which the plain
// installFakeTmuxAlive cannot — that helper exits 1 for list-sessions, so the
// created-time lookup errors and the discriminator falls back to "escalate".
func installFakeTmuxAliveCreated(t *testing.T, sessionName string, createdUnix int64) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-tmux alive helper is POSIX-shell only")
	}

	binDir := t.TempDir()
	scriptPath := filepath.Join(binDir, "tmux")

	// has-session: exit 0 only for our session. list-sessions: echo the
	// configured created unix (GetSessionCreatedTime filters by name itself, so
	// echoing the single value is sufficient for the one-session tests here).
	script := fmt.Sprintf("#!/bin/sh\n"+
		"case \"$*\" in\n"+
		"  *has-session*)\n"+
		"    case \"$*\" in\n"+
		"      *\"%s\"*) exit 0 ;;\n"+
		"      *) printf 'session not found\\n' 1>&2; exit 1 ;;\n"+
		"    esac\n"+
		"    ;;\n"+
		"  *list-sessions*)\n"+
		"    printf '%d\\n'\n"+
		"    exit 0\n"+
		"    ;;\n"+
		"esac\n"+
		"printf 'no server running\\n' 1>&2\nexit 1\n", sessionName, createdUnix)

	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// testBinaryModTime returns the mod time the discriminator will see for the
// running binary (the freshly-compiled test binary). Tests calibrate their
// session_created times against it so the before/after comparison is
// deterministic regardless of when the suite runs.
func testBinaryModTime(t *testing.T) time.Time {
	t.Helper()
	bt, err := selfBinaryModTime()
	if err != nil {
		t.Fatalf("selfBinaryModTime: %v", err)
	}
	return bt
}

// TestDetectStaleRigAgentHeartbeats_PreBinaryBumpSuppressed verifies the
// gu-2j7n1 fix: an ALIVE refinery with NO heartbeat file at all, whose session
// was created BEFORE the running binary, is recorded "skip-pre-binary-bump" and
// does NOT escalate. This is the FP wave — a pre-bump session running an old
// pre-gu-0nmw image that never wrote heartbeats; healthy, just needs a rig boot.
func TestDetectStaleRigAgentHeartbeats_PreBinaryBumpSuppressed(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"
	prefix := session.PrefixFor(rigName)
	refSession := session.RefinerySessionName(prefix)

	// Session created an hour before the binary was built — a pre-bump session.
	created := testBinaryModTime(t).Add(-time.Hour)
	installFakeTmuxAliveCreated(t, refSession, created.Unix())

	// No heartbeat file written for the refinery at all (the pre-gu-0nmw image
	// never touched one). Witness session is absent (fake tmux only knows the
	// refinery), so it records skip-no-session and stays out of the way.

	res := DetectStaleRigAgentHeartbeats(townRoot, rigName, nil, time.Hour, "", 0, 0, nil)

	refinery := findStaleResult(res, "refinery")
	if refinery == nil {
		t.Fatalf("missing refinery result")
	}
	if !refinery.SessionAlive {
		t.Fatalf("refinery SessionAlive = false, want true (fake tmux should report it alive)")
	}
	if !refinery.HeartbeatMissing {
		t.Fatalf("refinery HeartbeatMissing = false, want true (no heartbeat file written)")
	}
	if refinery.Action != "skip-pre-binary-bump" {
		t.Errorf("refinery Action = %q, want skip-pre-binary-bump", refinery.Action)
	}
	if refinery.MailSent {
		t.Errorf("refinery MailSent = true, want false (pre-bump session must not escalate)")
	}
}

// TestDetectStaleRigAgentHeartbeats_PostBumpMissingHeartbeatEscalates verifies
// the discriminator does NOT over-suppress: an ALIVE session created AFTER the
// running binary that STILL has no heartbeat file is a genuine anomaly (it
// crashed before its first heartbeat write on the current binary) and must
// escalate.
func TestDetectStaleRigAgentHeartbeats_PostBumpMissingHeartbeatEscalates(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"
	prefix := session.PrefixFor(rigName)
	refSession := session.RefinerySessionName(prefix)

	// Session created an hour AFTER the binary — a current-binary session.
	created := testBinaryModTime(t).Add(time.Hour)
	installFakeTmuxAliveCreated(t, refSession, created.Unix())

	res := DetectStaleRigAgentHeartbeats(townRoot, rigName, nil, time.Hour, "", 0, 0, nil)

	refinery := findStaleResult(res, "refinery")
	if refinery == nil {
		t.Fatalf("missing refinery result")
	}
	if !refinery.HeartbeatMissing {
		t.Fatalf("refinery HeartbeatMissing = false, want true")
	}
	if refinery.Action != "escalated" {
		t.Errorf("refinery Action = %q, want escalated (current-binary session that never heartbeated is a real anomaly)", refinery.Action)
	}
}

// TestDetectStaleRigAgentHeartbeats_MissingHeartbeatUnknownCreatedEscalates
// verifies the conservative fallback: when the session creation time cannot be
// resolved (list-sessions errors), the discriminator returns false and the
// alive-but-no-heartbeat agent still escalates. This preserves the original
// pre-gu-2j7n1 behavior for the uncertain case — suppression requires positive
// proof the session predates the binary.
func TestDetectStaleRigAgentHeartbeats_MissingHeartbeatUnknownCreatedEscalates(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"
	prefix := session.PrefixFor(rigName)
	refSession := session.RefinerySessionName(prefix)

	// installFakeTmuxAlive reports has-session alive but exits 1 for
	// list-sessions, so GetSessionCreatedTime errors and the creation time is
	// unknown.
	installFakeTmuxAlive(t, refSession)

	res := DetectStaleRigAgentHeartbeats(townRoot, rigName, nil, time.Hour, "", 0, 0, nil)

	refinery := findStaleResult(res, "refinery")
	if refinery == nil {
		t.Fatalf("missing refinery result")
	}
	if !refinery.SessionAlive {
		t.Fatalf("refinery SessionAlive = false, want true")
	}
	if !refinery.HeartbeatMissing {
		t.Fatalf("refinery HeartbeatMissing = false, want true")
	}
	if refinery.Action != "escalated" {
		t.Errorf("refinery Action = %q, want escalated (unknown creation time must not suppress)", refinery.Action)
	}
}
