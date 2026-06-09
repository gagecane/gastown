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

// stubMQProber is a test MergeQueueProber returning a fixed count (or error).
type stubMQProber struct {
	count int
	err   error
	calls int
}

func (s *stubMQProber) PendingMergeRequestCount(string) (int, error) {
	s.calls++
	return s.count, s.err
}

// installFakeTmuxAlive installs a fake `tmux` whose `has-session` succeeds only
// for the named session(s) so the detector sees them as alive. Any other tmux
// subcommand exits 1. This lets a test exercise the SessionAlive=true branch
// that installFakeTmuxNoServer (server-down → every session dead) cannot reach.
func installFakeTmuxAlive(t *testing.T, aliveSessions ...string) {
	t.Helper()

	binDir := t.TempDir()
	scriptPath := filepath.Join(binDir, "tmux")

	// tmux is invoked with global flags first, e.g.
	// `tmux -u has-session -t =<name>`, so match against the whole arg string
	// ($*) rather than $1. For has-session, exit 0 (alive) when the target is
	// in the alive list, 1 (not found) otherwise.
	var matchClauses string
	for _, s := range aliveSessions {
		matchClauses += fmt.Sprintf("    *\"%s\"*) exit 0 ;;\n", s)
	}
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *has-session*)\n" +
		"    case \"$*\" in\n" +
		matchClauses +
		"      *) printf 'session not found\\n' 1>&2; exit 1 ;;\n" +
		"    esac\n" +
		"    ;;\n" +
		"esac\n" +
		"printf 'no server running\\n' 1>&2\nexit 1\n"

	if runtime.GOOS == "windows" {
		t.Skip("fake-tmux alive helper is POSIX-shell only")
	}
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestDetectStaleRigAgentHeartbeats_IdleEmptyMQSuppressed verifies the gs-ecdg
// fix: a refinery with a stale heartbeat but an ALIVE session and an EMPTY
// merge queue is recorded as "skip-idle-empty-mq" and does NOT escalate. This
// is the false-positive the bug describes — an idle-quiet refinery that simply
// has nothing to merge, not a wedged one.
func TestDetectStaleRigAgentHeartbeats_IdleEmptyMQSuppressed(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"
	prefix := session.PrefixFor(rigName)
	refSession := session.RefinerySessionName(prefix)

	installFakeTmuxAlive(t, refSession)

	// Refinery heartbeat stale; witness heartbeat fresh so it stays out of the way.
	writeRigAgentHeartbeat(t, townRoot, refSession, 6*time.Hour)
	writeRigAgentHeartbeat(t, townRoot, session.WitnessSessionName(prefix), 30*time.Second)

	prober := &stubMQProber{count: 0}
	res := DetectStaleRigAgentHeartbeats(townRoot, rigName, nil, time.Hour, "", 0, 0, prober)

	refinery := findStaleResult(res, "refinery")
	if refinery == nil {
		t.Fatalf("missing refinery result")
	}
	if !refinery.SessionAlive {
		t.Fatalf("refinery SessionAlive = false, want true (fake tmux should report it alive)")
	}
	if refinery.Action != "skip-idle-empty-mq" {
		t.Errorf("refinery Action = %q, want skip-idle-empty-mq", refinery.Action)
	}
	if refinery.MailSent {
		t.Errorf("refinery MailSent = true, want false (idle empty-MQ must not escalate)")
	}
	if prober.calls == 0 {
		t.Errorf("prober was never consulted")
	}
}

// TestDetectStaleRigAgentHeartbeats_IdleNonEmptyMQEscalates verifies the
// discriminator holds: an ALIVE refinery with a stale heartbeat but a
// NON-empty queue still escalates — that is the gu-rh0g wedge signature (work
// waiting, refinery not draining it).
func TestDetectStaleRigAgentHeartbeats_IdleNonEmptyMQEscalates(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"
	prefix := session.PrefixFor(rigName)
	refSession := session.RefinerySessionName(prefix)

	installFakeTmuxAlive(t, refSession)

	writeRigAgentHeartbeat(t, townRoot, refSession, 6*time.Hour)
	writeRigAgentHeartbeat(t, townRoot, session.WitnessSessionName(prefix), 30*time.Second)

	prober := &stubMQProber{count: 3}
	res := DetectStaleRigAgentHeartbeats(townRoot, rigName, nil, time.Hour, "", 0, 0, prober)

	refinery := findStaleResult(res, "refinery")
	if refinery == nil {
		t.Fatalf("missing refinery result")
	}
	if refinery.Action != "escalated" {
		t.Errorf("refinery Action = %q, want escalated (non-empty queue is a real wedge)", refinery.Action)
	}
}

// TestDetectStaleRigAgentHeartbeats_IdleEmptyMQProbeErrorEscalates verifies the
// conservative fallback: when the prober returns an error we do NOT suppress —
// a transient MQ-query failure must not mask a genuine wedge.
func TestDetectStaleRigAgentHeartbeats_IdleEmptyMQProbeErrorEscalates(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"
	prefix := session.PrefixFor(rigName)
	refSession := session.RefinerySessionName(prefix)

	installFakeTmuxAlive(t, refSession)

	writeRigAgentHeartbeat(t, townRoot, refSession, 6*time.Hour)
	writeRigAgentHeartbeat(t, townRoot, session.WitnessSessionName(prefix), 30*time.Second)

	prober := &stubMQProber{err: fmt.Errorf("dolt unreachable")}
	res := DetectStaleRigAgentHeartbeats(townRoot, rigName, nil, time.Hour, "", 0, 0, prober)

	refinery := findStaleResult(res, "refinery")
	if refinery == nil {
		t.Fatalf("missing refinery result")
	}
	if refinery.Action != "escalated" {
		t.Errorf("refinery Action = %q, want escalated (probe error must not suppress)", refinery.Action)
	}
}
