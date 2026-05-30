package polecat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// sessionNamePattern matches valid heartbeat session_name characters: ASCII
// alphanumerics plus `_`, `.`, `-`. Anything else (slashes, NUL, spaces,
// shell metachars) is rejected at the heartbeat-file boundary so a hostile or
// malformed session_name cannot escape the heartbeats directory via
// filepath.Join (which does not strip `..` segments). See cv-p3fem Phase 1
// security review (gu-leg-pflxi).
var sessionNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// isValidSessionName returns true if name is safe to use as a heartbeat
// filename component. Rejects empty strings, names containing `..` (parent
// segment traversal), and any character outside [A-Za-z0-9_.-].
func isValidSessionName(name string) bool {
	if name == "" {
		return false
	}
	if strings.Contains(name, "..") {
		return false
	}
	return sessionNamePattern.MatchString(name)
}

// SessionHeartbeatStaleThreshold is the age at which a polecat session heartbeat
// is considered stale, indicating the agent process is likely dead.
// Configurable via operational.polecat.heartbeat_stale_threshold in settings/config.json.
const SessionHeartbeatStaleThreshold = 3 * time.Minute

// HeartbeatState represents the agent-reported state in a heartbeat v2 (gt-3vr5).
// Agents report their own state; the witness makes exactly one inference:
// "is the heartbeat fresh?" Everything else is agent-reported.
type HeartbeatState string

const (
	// HeartbeatWorking means the agent is actively processing.
	HeartbeatWorking HeartbeatState = "working"
	// HeartbeatIdle means the agent is waiting for input.
	HeartbeatIdle HeartbeatState = "idle"
	// HeartbeatExiting means the agent is in the gt done flow.
	HeartbeatExiting HeartbeatState = "exiting"
	// HeartbeatStuck means the agent self-reports being stuck.
	HeartbeatStuck HeartbeatState = "stuck"
)

// SessionHeartbeat represents a polecat session's heartbeat file.
// v1: timestamp only. v2 (gt-3vr5): adds agent-reported state, context, and bead.
type SessionHeartbeat struct {
	Timestamp time.Time      `json:"timestamp"`
	State     HeartbeatState `json:"state,omitempty"`   // v2: agent-reported state
	Context   string         `json:"context,omitempty"` // v2: what the agent is doing
	Bead      string         `json:"bead,omitempty"`    // v2: current hook bead ID
}

// EffectiveState returns the agent-reported state, defaulting to HeartbeatWorking
// for v1 heartbeats without a state field (backwards compatibility). See gt-3vr5.
func (h *SessionHeartbeat) EffectiveState() HeartbeatState {
	if h.State == "" {
		return HeartbeatWorking
	}
	return h.State
}

// IsV2 returns true if this heartbeat carries a state field (heartbeat v2).
// Used by the witness to decide whether to use agent-reported state or fall
// through to legacy timer-based detection.
func (h *SessionHeartbeat) IsV2() bool {
	return h.State != ""
}

// heartbeatsDir returns the directory for polecat session heartbeat files.
// Heartbeats live under <townRoot>/.runtime/heartbeats/, parallel to .runtime/pids/.
func heartbeatsDir(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "heartbeats")
}

// heartbeatFile returns the path to a heartbeat file for a given session.
func heartbeatFile(townRoot, sessionName string) string {
	return filepath.Join(heartbeatsDir(townRoot), sessionName+".json")
}

// TouchSessionHeartbeat writes or updates the heartbeat file for a polecat session.
// Writes state="working" by default (heartbeat v2, gt-3vr5).
// This is best-effort: errors are silently ignored because heartbeat signals
// are non-critical and should not interrupt gt commands.
func TouchSessionHeartbeat(townRoot, sessionName string) {
	TouchSessionHeartbeatWithState(townRoot, sessionName, HeartbeatWorking, "", "")
}

// TouchSessionHeartbeatWithState writes a heartbeat with explicit state information.
// Used by gt done (state="exiting") and gt heartbeat (state="stuck"). See gt-3vr5.
// This is best-effort: errors are silently ignored. Rejects (no-op) session
// names that fail isValidSessionName so a hostile session_name cannot escape
// the heartbeats directory (cv-p3fem Phase 1).
func TouchSessionHeartbeatWithState(townRoot, sessionName string, state HeartbeatState, context, bead string) {
	if !isValidSessionName(sessionName) {
		return
	}
	dir := heartbeatsDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}

	hb := SessionHeartbeat{
		Timestamp: time.Now().UTC(),
		State:     state,
		Context:   context,
		Bead:      bead,
	}

	data, err := json.Marshal(hb)
	if err != nil {
		return
	}

	_ = os.WriteFile(heartbeatFile(townRoot, sessionName), data, 0644)
}

// ReadSessionHeartbeat reads the heartbeat for a polecat session.
// Returns nil if the file doesn't exist or can't be read. Invalid session
// names (see isValidSessionName) are rejected with a nil read so callers
// can't probe arbitrary paths via the heartbeats directory.
func ReadSessionHeartbeat(townRoot, sessionName string) *SessionHeartbeat {
	if !isValidSessionName(sessionName) {
		return nil
	}
	data, err := os.ReadFile(heartbeatFile(townRoot, sessionName))
	if err != nil {
		return nil
	}

	var hb SessionHeartbeat
	if err := json.Unmarshal(data, &hb); err != nil {
		return nil
	}

	return &hb
}

// IsSessionHeartbeatStale returns true if the session's heartbeat is older than
// the stale threshold, or if no heartbeat file exists.
//
// When no heartbeat file exists, this returns false to avoid false positives
// during the rollout period where sessions may not yet be touching heartbeats.
// The caller should fall back to other liveness checks in that case.
func IsSessionHeartbeatStale(townRoot, sessionName string) (stale bool, exists bool) {
	hb := ReadSessionHeartbeat(townRoot, sessionName)
	if hb == nil {
		return false, false
	}
	return time.Since(hb.Timestamp) >= SessionHeartbeatStaleThreshold, true
}

// DefaultKeepaliveInterval is the default cadence for background keepalive
// tickers (cv-p3fem Phase 2). Well below the 3-minute stale threshold (~6
// keepalives of grace), well above filesystem flush thresholds. Long-running
// call sites that don't otherwise touch the heartbeat (LLM calls, gate
// runners, merge-queue waits) bump the timestamp on this cadence so the
// witness/dog do not flag them as stale.
const DefaultKeepaliveInterval = 30 * time.Second

// Keepalive bumps the session heartbeat timestamp without changing the
// reported state. Best-effort: errors are silently ignored, same contract as
// TouchSessionHeartbeat. The current state/context/bead is preserved when
// possible by reading the existing heartbeat first; if no heartbeat exists,
// a default state="working" heartbeat is written.
//
// Phase 2 (cv-p3fem): updates only the v2 timestamp field. Phase 3 will add
// a separate last_keepalive field. Old readers continue to see a fresh
// timestamp during long calls, eliminating the false-stale-during-LLM-call
// failure class.
func Keepalive(townRoot, sessionName string) {
	KeepaliveWithOp(townRoot, sessionName, "")
}

// KeepaliveWithOp bumps the heartbeat timestamp and records what the agent
// is doing (e.g. "llm-call", "brazil-build", "go-test"). The op label is
// preserved in the Context field for operator diagnostics. Best-effort.
//
// If no heartbeat file exists for the session, this writes a fresh
// state="working" heartbeat with the supplied op as context. If an existing
// heartbeat is present, its state and bead fields are preserved so a
// keepalive does not overwrite agent self-reported state.
func KeepaliveWithOp(townRoot, sessionName, op string) {
	if !isValidSessionName(sessionName) {
		return
	}
	state := HeartbeatWorking
	context := op
	bead := ""
	if existing := ReadSessionHeartbeat(townRoot, sessionName); existing != nil {
		state = existing.EffectiveState()
		bead = existing.Bead
		// Preserve the existing context only when the caller didn't supply
		// one, so a typed `KeepaliveWithOp(... "llm-call")` overrides the
		// stale "gt some-command" context but a plain `Keepalive(...)`
		// doesn't blow it away.
		if context == "" {
			context = existing.Context
		}
	}
	TouchSessionHeartbeatWithState(townRoot, sessionName, state, context, bead)
}

// WithKeepalive starts a background keepalive ticker and returns a cancel
// func. The ticker calls KeepaliveWithOp every interval until the cancel
// func is invoked. Defer-friendly: the canonical usage is
//
//	defer polecat.WithKeepalive(townRoot, session, "llm-call", 30*time.Second)()
//
// The cancel func is idempotent — calling it twice is safe. Returns a no-op
// cancel func when sessionName or townRoot is empty (no GT_SESSION) so build
// wrappers can call this unconditionally.
//
// cv-p3fem Phase 2: eliminates the false-stale-during-LLM-call class by
// keeping the heartbeat fresh while the agent is in a long-running call.
func WithKeepalive(townRoot, sessionName, op string, interval time.Duration) (cancel func()) {
	if townRoot == "" || sessionName == "" {
		return func() {}
	}
	if interval <= 0 {
		interval = DefaultKeepaliveInterval
	}
	ctx, ctxCancel := context.WithCancel(context.Background())
	var once sync.Once
	done := make(chan struct{})

	// Bump immediately so a long call that finishes before the first tick
	// still gets credit for being alive, and so the op label lands in the
	// heartbeat right away for operator visibility.
	KeepaliveWithOp(townRoot, sessionName, op)

	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				KeepaliveWithOp(townRoot, sessionName, op)
			}
		}
	}()

	return func() {
		once.Do(func() {
			ctxCancel()
			<-done
		})
	}
}

// KeepaliveLoop is the context-aware variant of WithKeepalive for callers
// that already have their own context (cancellation, timeout, etc.). The
// loop runs in the calling goroutine and returns when ctx is canceled or
// its deadline expires. Use go-routine-style: `go polecat.KeepaliveLoop(...)`.
//
// Like WithKeepalive, the first bump is immediate (before the first tick),
// so a quick-completing call still gets a fresh heartbeat. Returns
// immediately on missing town root or session name.
func KeepaliveLoop(ctx context.Context, townRoot, sessionName, op string, interval time.Duration) {
	if townRoot == "" || sessionName == "" {
		return
	}
	if interval <= 0 {
		interval = DefaultKeepaliveInterval
	}
	KeepaliveWithOp(townRoot, sessionName, op)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			KeepaliveWithOp(townRoot, sessionName, op)
		}
	}
}

// RemoveSessionHeartbeat removes the heartbeat file for a session.
// Called during session cleanup. Invalid session names are silently ignored
// so a hostile name cannot escape the heartbeats directory.
func RemoveSessionHeartbeat(townRoot, sessionName string) {
	if !isValidSessionName(sessionName) {
		return
	}
	_ = os.Remove(heartbeatFile(townRoot, sessionName))
}
