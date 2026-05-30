package polecat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTouchAndReadSessionHeartbeat(t *testing.T) {
	townRoot := t.TempDir()

	// No heartbeat initially
	hb := ReadSessionHeartbeat(townRoot, "gt-test-session")
	if hb != nil {
		t.Fatal("expected nil heartbeat before touch")
	}

	// Touch heartbeat
	TouchSessionHeartbeat(townRoot, "gt-test-session")

	// Read it back
	hb = ReadSessionHeartbeat(townRoot, "gt-test-session")
	if hb == nil {
		t.Fatal("expected non-nil heartbeat after touch")
	}

	if time.Since(hb.Timestamp) > 5*time.Second {
		t.Errorf("heartbeat timestamp too old: %v", hb.Timestamp)
	}

	// v2: TouchSessionHeartbeat writes state="working" by default (gt-3vr5)
	if hb.State != HeartbeatWorking {
		t.Errorf("heartbeat state = %q, want %q", hb.State, HeartbeatWorking)
	}
}

func TestTouchSessionHeartbeatWithState(t *testing.T) {
	townRoot := t.TempDir()

	TouchSessionHeartbeatWithState(townRoot, "gt-test-state", HeartbeatExiting, "gt done", "gt-abc123")

	hb := ReadSessionHeartbeat(townRoot, "gt-test-state")
	if hb == nil {
		t.Fatal("expected non-nil heartbeat after touch with state")
	}

	if hb.State != HeartbeatExiting {
		t.Errorf("state = %q, want %q", hb.State, HeartbeatExiting)
	}
	if hb.Context != "gt done" {
		t.Errorf("context = %q, want %q", hb.Context, "gt done")
	}
	if hb.Bead != "gt-abc123" {
		t.Errorf("bead = %q, want %q", hb.Bead, "gt-abc123")
	}
}

func TestSessionHeartbeat_EffectiveState(t *testing.T) {
	tests := []struct {
		name  string
		state HeartbeatState
		want  HeartbeatState
	}{
		{"empty (v1 compat)", "", HeartbeatWorking},
		{"working", HeartbeatWorking, HeartbeatWorking},
		{"idle", HeartbeatIdle, HeartbeatIdle},
		{"exiting", HeartbeatExiting, HeartbeatExiting},
		{"stuck", HeartbeatStuck, HeartbeatStuck},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hb := &SessionHeartbeat{State: tt.state}
			if got := hb.EffectiveState(); got != tt.want {
				t.Errorf("EffectiveState() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSessionHeartbeat_IsV2(t *testing.T) {
	// v1 heartbeat (no state)
	v1 := &SessionHeartbeat{Timestamp: time.Now()}
	if v1.IsV2() {
		t.Error("expected IsV2()=false for v1 heartbeat")
	}

	// v2 heartbeat (has state)
	v2 := &SessionHeartbeat{Timestamp: time.Now(), State: HeartbeatWorking}
	if !v2.IsV2() {
		t.Error("expected IsV2()=true for v2 heartbeat")
	}
}

func TestIsSessionHeartbeatStale_NoFile(t *testing.T) {
	townRoot := t.TempDir()

	stale, exists := IsSessionHeartbeatStale(townRoot, "nonexistent")
	if exists {
		t.Error("expected exists=false for missing heartbeat")
	}
	if stale {
		t.Error("expected stale=false for missing heartbeat")
	}
}

func TestIsSessionHeartbeatStale_Fresh(t *testing.T) {
	townRoot := t.TempDir()

	TouchSessionHeartbeat(townRoot, "gt-test-fresh")

	stale, exists := IsSessionHeartbeatStale(townRoot, "gt-test-fresh")
	if !exists {
		t.Error("expected exists=true for fresh heartbeat")
	}
	if stale {
		t.Error("expected stale=false for fresh heartbeat")
	}
}

func TestIsSessionHeartbeatStale_Old(t *testing.T) {
	townRoot := t.TempDir()

	// Write a heartbeat with an old timestamp
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	oldTime := time.Now().Add(-10 * time.Minute).UTC()
	data := []byte(`{"timestamp":"` + oldTime.Format(time.RFC3339Nano) + `"}`)
	if err := os.WriteFile(filepath.Join(dir, "gt-test-stale.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	stale, exists := IsSessionHeartbeatStale(townRoot, "gt-test-stale")
	if !exists {
		t.Error("expected exists=true for old heartbeat")
	}
	if !stale {
		t.Error("expected stale=true for 10-minute-old heartbeat")
	}
}

func TestRemoveSessionHeartbeat(t *testing.T) {
	townRoot := t.TempDir()

	TouchSessionHeartbeat(townRoot, "gt-test-remove")

	// Verify it exists
	hb := ReadSessionHeartbeat(townRoot, "gt-test-remove")
	if hb == nil {
		t.Fatal("expected heartbeat to exist before removal")
	}

	// Remove it
	RemoveSessionHeartbeat(townRoot, "gt-test-remove")

	// Verify it's gone
	hb = ReadSessionHeartbeat(townRoot, "gt-test-remove")
	if hb != nil {
		t.Error("expected nil heartbeat after removal")
	}
}

func TestRemoveSessionHeartbeat_NoopOnMissing(t *testing.T) {
	townRoot := t.TempDir()
	// Should not panic or error on missing file
	RemoveSessionHeartbeat(townRoot, "nonexistent")
}

// TestHeartbeatFile_RejectsHostileSessionNames pins the cv-p3fem Phase 1
// security boundary: every public heartbeat-file mutator/reader silently
// no-ops on a hostile session_name so the heartbeats directory cannot be
// escaped via filepath.Join. The test enumerates the categories called out
// in the security review (gu-leg-pflxi):
//   - empty string
//   - parent traversal (`..`, `../`, `..\\`)
//   - absolute paths (`/etc/passwd`, `/tmp/x`)
//   - path separators inside the name (`a/b`, `a\\b`)
//   - shell/whitespace metacharacters
//
// Each case must:
//  1. Produce no file inside .runtime/heartbeats/ (TouchSession*).
//  2. Return nil from ReadSessionHeartbeat (no probe escape).
//  3. Return (false, false) from IsSessionHeartbeatStale (no probe escape).
//  4. Be a no-op for RemoveSessionHeartbeat (no escape, no panic).
//
// If you change this list, also update isValidSessionName to match — the two
// must move together or sessions named with newly-allowed characters will
// silently lose their heartbeats.
func TestHeartbeatFile_RejectsHostileSessionNames(t *testing.T) {
	hostile := []string{
		"",
		"..",
		"../escape",
		"a/../b",
		"a/b",
		"/etc/passwd",
		`a\b`,
		"foo bar", // space
		"foo\tbar",
		"foo;rm -rf /",
		"foo$bar",
		"foo\nbar",
		"\x00null",
	}

	for _, name := range hostile {
		t.Run("name="+name, func(t *testing.T) {
			townRoot := t.TempDir()

			// 1. TouchSessionHeartbeat must no-op.
			TouchSessionHeartbeat(townRoot, name)
			TouchSessionHeartbeatWithState(townRoot, name, HeartbeatWorking, "ctx", "bead")

			// The heartbeats dir may exist (the function MkdirAll's it before
			// validating in the no-arg variant during the rollout window) so
			// we don't assert dir absence — we assert no FILE landed.
			dir := filepath.Join(townRoot, ".runtime", "heartbeats")
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				t.Errorf("hostile name %q produced heartbeat file %q", name, e.Name())
			}

			// 2. ReadSessionHeartbeat must return nil.
			if hb := ReadSessionHeartbeat(townRoot, name); hb != nil {
				t.Errorf("ReadSessionHeartbeat(%q) returned %+v; want nil", name, hb)
			}

			// 3. IsSessionHeartbeatStale must report exists=false.
			stale, exists := IsSessionHeartbeatStale(townRoot, name)
			if exists || stale {
				t.Errorf("IsSessionHeartbeatStale(%q) = (stale=%v, exists=%v); want (false,false)", name, stale, exists)
			}

			// 4. RemoveSessionHeartbeat must not panic and must not escape.
			RemoveSessionHeartbeat(townRoot, name)
		})
	}
}

// TestHeartbeatFile_AcceptsValidSessionNames pins the corollary: names that
// match ^[A-Za-z0-9_.-]+$ and don't contain `..` MUST round-trip cleanly.
// This guards against an over-aggressive validator regression breaking real
// session names (gt-rig-prefix-deathclaw, deacon.gt-uw, etc.).
func TestHeartbeatFile_AcceptsValidSessionNames(t *testing.T) {
	valid := []string{
		"deathclaw",
		"gt-uw-deathclaw",
		"deacon.gt-uw",
		"witness_main",
		"a.b-c_d",
		"a", // single char
	}

	for _, name := range valid {
		t.Run("name="+name, func(t *testing.T) {
			townRoot := t.TempDir()
			TouchSessionHeartbeat(townRoot, name)
			hb := ReadSessionHeartbeat(townRoot, name)
			if hb == nil {
				t.Fatalf("valid name %q failed to round-trip; got nil heartbeat", name)
			}
			RemoveSessionHeartbeat(townRoot, name)
			if hb := ReadSessionHeartbeat(townRoot, name); hb != nil {
				t.Errorf("RemoveSessionHeartbeat(%q) failed to remove the file", name)
			}
		})
	}
}

func TestIsSessionProcessDead_HeartbeatFresh(t *testing.T) {
	townRoot := t.TempDir()
	sessionName := "gt-test-hb-alive"

	// Touch a fresh heartbeat — isSessionProcessDead should return false
	TouchSessionHeartbeat(townRoot, sessionName)

	dead := isSessionProcessDead(nil, sessionName, townRoot)
	if dead {
		t.Error("expected alive (dead=false) for session with fresh heartbeat")
	}
}

func TestIsSessionProcessDead_HeartbeatStale(t *testing.T) {
	townRoot := t.TempDir()
	sessionName := "gt-test-hb-dead"

	// Write a heartbeat past the DEAD threshold (cv-p3fem Phase 3 raised
	// the polecat-class default to 20m). Old code reaped at any staleness
	// past 3m; new code holds out until DEAD verdict for safety. This
	// test pins the new contract: 30m → DEAD → reap.
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-30 * time.Minute).UTC()
	data := []byte(`{"timestamp":"` + oldTime.Format(time.RFC3339Nano) + `"}`)
	if err := os.WriteFile(filepath.Join(dir, sessionName+".json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	dead := isSessionProcessDead(nil, sessionName, townRoot)
	if !dead {
		t.Error("expected dead=true for session with heartbeat past DEAD threshold")
	}
}

// TestIsSessionProcessDead_HeartbeatMaybeDead_NotReapable pins the new
// behavior: a stale-but-inside-grace heartbeat is MAYBE_DEAD, not DEAD.
// The polecat manager must NOT reap such sessions — that's the daemon
// agent reaper's call (with its wider per-role thresholds and external
// corroboration). cv-p3fem Phase 3.
func TestIsSessionProcessDead_HeartbeatMaybeDead_NotReapable(t *testing.T) {
	townRoot := t.TempDir()
	sessionName := "gt-test-hb-maybe-dead"
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	// 8m old: past stale (3m), inside dead (20m).
	oldTime := time.Now().Add(-8 * time.Minute).UTC()
	data := []byte(`{"timestamp":"` + oldTime.Format(time.RFC3339Nano) + `"}`)
	if err := os.WriteFile(filepath.Join(dir, sessionName+".json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	dead := isSessionProcessDead(nil, sessionName, townRoot)
	if dead {
		t.Error("expected dead=false for MAYBE_DEAD verdict (8m stale, inside grace)")
	}
}

func TestIsSessionProcessDead_EmptyTownRoot(t *testing.T) {
	// With empty townRoot, heartbeat check is skipped entirely.
	// This tests backward compatibility when townRoot isn't available.
	// We can't test the full PID fallback without a real tmux session,
	// but we verify no panic with empty townRoot.
	sessionName := "gt-test-no-townroot"

	// Empty townRoot skips heartbeat, falls through to PID check.
	// Can't test PID path without tmux, but verify heartbeat path is skipped.
	stale, exists := IsSessionHeartbeatStale("", sessionName)
	if exists {
		t.Error("expected exists=false with empty townRoot")
	}
	if stale {
		t.Error("expected stale=false with empty townRoot")
	}
}

func TestReadSessionHeartbeat_V1BackwardsCompat(t *testing.T) {
	townRoot := t.TempDir()

	// Write a v1 heartbeat (timestamp only, no state field)
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	ts := time.Now().UTC()
	data := []byte(`{"timestamp":"` + ts.Format(time.RFC3339Nano) + `"}`)
	if err := os.WriteFile(filepath.Join(dir, "gt-test-v1.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	hb := ReadSessionHeartbeat(townRoot, "gt-test-v1")
	if hb == nil {
		t.Fatal("expected non-nil heartbeat for v1 format")
	}

	// State should be empty (v1)
	if hb.State != "" {
		t.Errorf("v1 heartbeat state = %q, want empty", hb.State)
	}

	// IsV2 should return false
	if hb.IsV2() {
		t.Error("expected IsV2()=false for v1 heartbeat")
	}

	// EffectiveState should default to working
	if hb.EffectiveState() != HeartbeatWorking {
		t.Errorf("v1 EffectiveState() = %q, want %q", hb.EffectiveState(), HeartbeatWorking)
	}
}

func TestReadSessionHeartbeat_V2AllStates(t *testing.T) {
	townRoot := t.TempDir()

	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	states := []HeartbeatState{HeartbeatWorking, HeartbeatIdle, HeartbeatExiting, HeartbeatStuck}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			session := "gt-test-v2-" + string(state)
			hb := SessionHeartbeat{
				Timestamp: time.Now().UTC(),
				State:     state,
				Context:   "test context",
				Bead:      "gt-test-bead",
			}
			data, err := json.Marshal(hb)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, session+".json"), data, 0644); err != nil {
				t.Fatal(err)
			}

			read := ReadSessionHeartbeat(townRoot, session)
			if read == nil {
				t.Fatal("expected non-nil heartbeat")
			}
			if read.State != state {
				t.Errorf("state = %q, want %q", read.State, state)
			}
			if !read.IsV2() {
				t.Error("expected IsV2()=true")
			}
			if read.EffectiveState() != state {
				t.Errorf("EffectiveState() = %q, want %q", read.EffectiveState(), state)
			}
			if read.Context != "test context" {
				t.Errorf("context = %q, want %q", read.Context, "test context")
			}
			if read.Bead != "gt-test-bead" {
				t.Errorf("bead = %q, want %q", read.Bead, "gt-test-bead")
			}
		})
	}
}

// TestKeepalive_BumpsTimestampPreservesState pins the cv-p3fem Phase 2
// contract: Keepalive must refresh the timestamp without overwriting the
// agent-self-reported state field. This is the property that lets a
// background ticker run during a long LLM call without clobbering a
// concurrent `gt heartbeat --state=stuck` from the agent.
func TestKeepalive_BumpsTimestampPreservesState(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-keepalive-state"

	// Seed a heartbeat with a non-default state, context and bead.
	TouchSessionHeartbeatWithState(townRoot, session, HeartbeatStuck, "stuck reason", "gt-foo")

	// Force the timestamp into the past so we can verify Keepalive bumps it.
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	path := filepath.Join(dir, session+".json")
	old := SessionHeartbeat{
		Timestamp: time.Now().Add(-2 * time.Minute).UTC(),
		State:     HeartbeatStuck,
		Context:   "stuck reason",
		Bead:      "gt-foo",
	}
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	Keepalive(townRoot, session)

	hb := ReadSessionHeartbeat(townRoot, session)
	if hb == nil {
		t.Fatal("expected heartbeat after Keepalive")
	}
	if time.Since(hb.Timestamp) > 5*time.Second {
		t.Errorf("timestamp not bumped: %v old", time.Since(hb.Timestamp))
	}
	if hb.State != HeartbeatStuck {
		t.Errorf("state changed by Keepalive: got %q, want %q (preservation contract)", hb.State, HeartbeatStuck)
	}
	if hb.Bead != "gt-foo" {
		t.Errorf("bead changed by Keepalive: got %q, want %q (preservation contract)", hb.Bead, "gt-foo")
	}
	if hb.Context != "stuck reason" {
		t.Errorf("context changed by plain Keepalive: got %q, want %q", hb.Context, "stuck reason")
	}
}

// TestKeepalive_NoExistingHeartbeat verifies Keepalive establishes a fresh
// state="working" heartbeat when no file exists yet — letting build
// wrappers and gate runners call it unconditionally without first having
// to invoke another gt command to seed the file.
func TestKeepalive_NoExistingHeartbeat(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-keepalive-cold"

	Keepalive(townRoot, session)

	hb := ReadSessionHeartbeat(townRoot, session)
	if hb == nil {
		t.Fatal("expected heartbeat after cold-start Keepalive")
	}
	if hb.State != HeartbeatWorking {
		t.Errorf("cold-start state = %q, want %q", hb.State, HeartbeatWorking)
	}
}

// TestKeepaliveWithOp_RecordsOp verifies KeepaliveWithOp lands the op label
// in the heartbeat's Context field for operator diagnostics.
func TestKeepaliveWithOp_RecordsOp(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-keepalive-op"

	KeepaliveWithOp(townRoot, session, "llm-call")

	hb := ReadSessionHeartbeat(townRoot, session)
	if hb == nil {
		t.Fatal("expected heartbeat")
	}
	if hb.Context != "llm-call" {
		t.Errorf("context = %q, want %q", hb.Context, "llm-call")
	}
}

// TestKeepaliveWithOp_OverridesStaleContext verifies that an explicit op
// label overrides any stale context lingering on a v2 heartbeat. This is
// the property that lets a long-running LLM-call keepalive replace the
// "gt some-command" context recorded by the previous foreground command.
func TestKeepaliveWithOp_OverridesStaleContext(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-keepalive-override"

	TouchSessionHeartbeatWithState(townRoot, session, HeartbeatWorking, "gt some-command", "")
	KeepaliveWithOp(townRoot, session, "llm-call")

	hb := ReadSessionHeartbeat(townRoot, session)
	if hb == nil {
		t.Fatal("expected heartbeat")
	}
	if hb.Context != "llm-call" {
		t.Errorf("context = %q, want %q (op should override stale context)", hb.Context, "llm-call")
	}
}

// TestKeepalive_HostileSessionName verifies Keepalive enforces the same
// path-traversal validation as TouchSessionHeartbeat. This is critical
// because Keepalive is now invoked from many call sites (engineer.runTests,
// engineer.runGate, kiro wrapper, witness recovery gates) — broadening the
// surface for a hostile session_name to escape the heartbeats directory.
func TestKeepalive_HostileSessionName(t *testing.T) {
	townRoot := t.TempDir()

	for _, name := range []string{"", "../escape", "a/b", "/etc/passwd"} {
		Keepalive(townRoot, name)
		KeepaliveWithOp(townRoot, name, "any-op")
	}

	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		t.Errorf("hostile name produced heartbeat file %q", e.Name())
	}
}

// TestWithKeepalive_TickerBumpsTimestamp verifies the WithKeepalive ticker
// actually bumps the heartbeat in the background. Uses a short interval so
// the test completes quickly. The wait-for-bump pattern (poll instead of
// sleep-then-check) keeps the test deterministic.
func TestWithKeepalive_TickerBumpsTimestamp(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-with-keepalive"

	// Seed a stale heartbeat — we need a baseline to detect bumps against.
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-1 * time.Minute).UTC()
	data := []byte(`{"timestamp":"` + old.Format(time.RFC3339Nano) + `","state":"working"}`)
	if err := os.WriteFile(filepath.Join(dir, session+".json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	cancel := WithKeepalive(townRoot, session, "test-op", 20*time.Millisecond)
	defer cancel()

	// The first bump is immediate (before the first tick fires) so we
	// expect a fresh timestamp almost instantly. Allow a generous timeout
	// for slow CI machines.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hb := ReadSessionHeartbeat(townRoot, session)
		if hb != nil && time.Since(hb.Timestamp) < 5*time.Second {
			// Timestamp is fresh — keepalive ticker is working.
			if hb.Context != "test-op" {
				t.Errorf("context = %q, want %q", hb.Context, "test-op")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("WithKeepalive did not bump timestamp within 2s")
}

// TestWithKeepalive_CancelStopsTicker verifies the cancel func actually
// stops the background goroutine. We bump once via the cancel-triggered
// final write, then assert no further bumps land after a short grace.
func TestWithKeepalive_CancelStopsTicker(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-cancel"

	cancel := WithKeepalive(townRoot, session, "test-op", 10*time.Millisecond)
	// Let the ticker fire a few times.
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Capture the timestamp after cancel — it should not advance further.
	hb := ReadSessionHeartbeat(townRoot, session)
	if hb == nil {
		t.Fatal("expected heartbeat after WithKeepalive")
	}
	stopped := hb.Timestamp

	// Wait several tick intervals; the ticker is supposed to be stopped.
	time.Sleep(100 * time.Millisecond)

	hb = ReadSessionHeartbeat(townRoot, session)
	if hb == nil {
		t.Fatal("heartbeat disappeared")
	}
	if hb.Timestamp.After(stopped) {
		t.Errorf("ticker still running after cancel: %v advanced to %v", stopped, hb.Timestamp)
	}
}

// TestWithKeepalive_CancelIdempotent verifies the cancel func is safe to
// call more than once. Build wrappers using `defer cancel()` plus an
// explicit `cancel()` in an error branch must not deadlock or panic.
func TestWithKeepalive_CancelIdempotent(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-cancel-twice"

	cancel := WithKeepalive(townRoot, session, "", 50*time.Millisecond)
	cancel()
	cancel() // must not panic, must not deadlock
	cancel() // also fine
}

// TestWithKeepalive_EmptySessionNoOp verifies that an empty session/town
// short-circuits to a no-op cancel func. Build wrappers call WithKeepalive
// unconditionally; missing GT_SESSION must not produce a goroutine leak,
// a panic, or a heartbeat write outside the proper directory.
func TestWithKeepalive_EmptySessionNoOp(t *testing.T) {
	townRoot := t.TempDir()
	cancel := WithKeepalive(townRoot, "", "no-op", 5*time.Millisecond)
	cancel()
	cancel()

	// No directory should have been created.
	if _, err := os.Stat(filepath.Join(townRoot, ".runtime", "heartbeats")); err == nil {
		// It's OK if it doesn't exist; if it does, it must be empty.
		entries, _ := os.ReadDir(filepath.Join(townRoot, ".runtime", "heartbeats"))
		for _, e := range entries {
			t.Errorf("empty session produced heartbeat file %q", e.Name())
		}
	}

	// Also verify empty townRoot path is a no-op.
	cancel2 := WithKeepalive("", "some-session", "", 5*time.Millisecond)
	cancel2()
}

// TestKeepaliveLoop_RespectsContext verifies KeepaliveLoop exits when its
// context is canceled (context-aware variant for callers that already
// thread a context through their hot path).
func TestKeepaliveLoop_RespectsContext(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-keepalive-loop"

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		KeepaliveLoop(ctx, townRoot, session, "loop-op", 5*time.Millisecond)
		close(done)
	}()

	// Let it fire at least once.
	time.Sleep(30 * time.Millisecond)
	hb := ReadSessionHeartbeat(townRoot, session)
	if hb == nil {
		t.Fatal("expected heartbeat from KeepaliveLoop")
	}
	if hb.Context != "loop-op" {
		t.Errorf("context = %q, want %q", hb.Context, "loop-op")
	}

	cancel()
	select {
	case <-done:
		// good
	case <-time.After(500 * time.Millisecond):
		t.Fatal("KeepaliveLoop did not exit on ctx cancel")
	}
}
