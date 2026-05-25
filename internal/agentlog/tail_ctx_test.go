package agentlog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ── tailJSONL (Claude Code) context-cancel tests ────────────────────────────

// TestTailJSONL_ExitsOnPreCanceledCtx verifies that tailJSONL returns
// immediately when given an already-canceled context.
func TestTailJSONL_ExitsOnPreCanceledCtx(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "session.jsonl")
	writeFile(t, jsonlPath, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`+"\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	ch := make(chan AgentEvent, 64)
	done := make(chan struct{})
	go func() {
		tailJSONL(ctx, jsonlPath, dir, time.Time{}, "s1", "claudecode", ch)
		close(done)
	}()

	select {
	case <-done:
		// tailJSONL returned promptly — success
	case <-time.After(2 * time.Second):
		t.Fatal("tailJSONL did not exit within 2s on pre-canceled context")
	}
}

// TestTailJSONL_ExitsOnCtxCancelDuringEOFPoll verifies that tailJSONL returns
// when the context is canceled while it is polling at EOF (no new data).
func TestTailJSONL_ExitsOnCtxCancelDuringEOFPoll(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "session.jsonl")
	// Write one line so tailJSONL enters the EOF polling loop.
	writeFile(t, jsonlPath, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`+"\n")

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan AgentEvent, 64)
	done := make(chan struct{})
	go func() {
		tailJSONL(ctx, jsonlPath, dir, time.Time{}, "s1", "claudecode", ch)
		close(done)
	}()

	// Allow goroutine to enter the EOF polling loop.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Exited cleanly — success
	case <-time.After(2 * time.Second):
		t.Fatal("tailJSONL did not exit within 2s after context cancel during EOF poll")
	}
}

// TestTailJSONL_ExitsOnCtxCancelDuringEventSend verifies that tailJSONL returns
// when the context is canceled while it is blocked trying to send an event
// on a full channel.
func TestTailJSONL_ExitsOnCtxCancelDuringEventSend(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "session.jsonl")

	// Write enough lines to fill the buffer. The channel has 0 capacity so
	// the goroutine will block on the first send attempt.
	var lines string
	for i := 0; i < 5; i++ {
		lines += `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"line"}]}}` + "\n"
	}
	writeFile(t, jsonlPath, lines)

	ctx, cancel := context.WithCancel(context.Background())
	// Unbuffered channel — tailJSONL will block on send.
	ch := make(chan AgentEvent)
	done := make(chan struct{})
	go func() {
		tailJSONL(ctx, jsonlPath, dir, time.Time{}, "s1", "claudecode", ch)
		close(done)
	}()

	// Let goroutine start and try to send.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Exited cleanly — success
	case <-time.After(2 * time.Second):
		t.Fatal("tailJSONL did not exit within 2s after context cancel during blocked send")
	}
}

// TestTailJSONL_DrainEventsBeforeCtxCancel verifies that events written before
// ctx cancel are delivered, and the goroutine still exits cleanly after.
func TestTailJSONL_DrainEventsBeforeCtxCancel(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "session.jsonl")
	writeFile(t, jsonlPath, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`+"\n")

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan AgentEvent, 64)
	done := make(chan struct{})
	go func() {
		tailJSONL(ctx, jsonlPath, dir, time.Time{}, "s1", "claudecode", ch)
		close(done)
	}()

	// Read the event.
	select {
	case ev := <-ch:
		if ev.Content != "hello" {
			t.Errorf("unexpected content: %q", ev.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	// Cancel and verify exit.
	cancel()
	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("tailJSONL did not exit within 2s after draining and cancel")
	}
}

// ── tailKiroJSONL context-cancel tests ──────────────────────────────────────

// TestTailKiroJSONL_ExitsOnPreCanceledCtx verifies that tailKiroJSONL returns
// immediately when given an already-canceled context.
func TestTailKiroJSONL_ExitsOnPreCanceledCtx(t *testing.T) {
	sessionsDir := t.TempDir()
	workDir := t.TempDir()

	jsonlPath := filepath.Join(sessionsDir, "session.jsonl")
	metaPath := filepath.Join(sessionsDir, "session.json")
	writeFile(t, jsonlPath, `{"version":"v1","kind":"Prompt","data":{"message_id":"m1","content":[{"kind":"text","data":"hi"}],"meta":{"timestamp":1700000000}}}`+"\n")
	writeJSON(t, metaPath, kiroMetadata{SessionID: "s1", Cwd: workDir})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	ch := make(chan AgentEvent, 64)
	done := make(chan struct{})
	go func() {
		tailKiroJSONL(ctx, jsonlPath, sessionsDir, workDir, time.Time{}, "s1", "kiro", ch)
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("tailKiroJSONL did not exit within 2s on pre-canceled context")
	}
}

// TestTailKiroJSONL_ExitsOnCtxCancelDuringEOFPoll verifies that tailKiroJSONL
// returns when the context is canceled while polling at EOF.
func TestTailKiroJSONL_ExitsOnCtxCancelDuringEOFPoll(t *testing.T) {
	sessionsDir := t.TempDir()
	workDir := t.TempDir()

	jsonlPath := filepath.Join(sessionsDir, "session.jsonl")
	metaPath := filepath.Join(sessionsDir, "session.json")
	writeFile(t, jsonlPath, `{"version":"v1","kind":"Prompt","data":{"message_id":"m1","content":[{"kind":"text","data":"hi"}],"meta":{"timestamp":1700000000}}}`+"\n")
	writeJSON(t, metaPath, kiroMetadata{SessionID: "s1", Cwd: workDir})

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan AgentEvent, 64)
	done := make(chan struct{})
	go func() {
		tailKiroJSONL(ctx, jsonlPath, sessionsDir, workDir, time.Time{}, "s1", "kiro", ch)
		close(done)
	}()

	// Allow goroutine to reach EOF poll loop.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("tailKiroJSONL did not exit within 2s after context cancel during EOF poll")
	}
}

// TestTailKiroJSONL_ExitsOnCtxCancelDuringEventSend verifies that tailKiroJSONL
// returns when the context is canceled while blocked sending an event.
func TestTailKiroJSONL_ExitsOnCtxCancelDuringEventSend(t *testing.T) {
	sessionsDir := t.TempDir()
	workDir := t.TempDir()

	var lines string
	for i := 0; i < 5; i++ {
		lines += `{"version":"v1","kind":"Prompt","data":{"message_id":"m1","content":[{"kind":"text","data":"hi"}],"meta":{"timestamp":1700000000}}}` + "\n"
	}
	jsonlPath := filepath.Join(sessionsDir, "session.jsonl")
	metaPath := filepath.Join(sessionsDir, "session.json")
	writeFile(t, jsonlPath, lines)
	writeJSON(t, metaPath, kiroMetadata{SessionID: "s1", Cwd: workDir})

	ctx, cancel := context.WithCancel(context.Background())
	// Unbuffered — will block on send.
	ch := make(chan AgentEvent)
	done := make(chan struct{})
	go func() {
		tailKiroJSONL(ctx, jsonlPath, sessionsDir, workDir, time.Time{}, "s1", "kiro", ch)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("tailKiroJSONL did not exit within 2s after context cancel during blocked send")
	}
}

// TestTailKiroJSONL_DrainEventsBeforeCtxCancel verifies events are delivered
// before context cancel and the goroutine still exits cleanly.
func TestTailKiroJSONL_DrainEventsBeforeCtxCancel(t *testing.T) {
	sessionsDir := t.TempDir()
	workDir := t.TempDir()

	jsonlPath := filepath.Join(sessionsDir, "session.jsonl")
	metaPath := filepath.Join(sessionsDir, "session.json")
	writeFile(t, jsonlPath, `{"version":"v1","kind":"Prompt","data":{"message_id":"m1","content":[{"kind":"text","data":"hi"}],"meta":{"timestamp":1700000000}}}`+"\n")
	writeJSON(t, metaPath, kiroMetadata{SessionID: "s1", Cwd: workDir})

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan AgentEvent, 64)
	done := make(chan struct{})
	go func() {
		tailKiroJSONL(ctx, jsonlPath, sessionsDir, workDir, time.Time{}, "s1", "kiro", ch)
		close(done)
	}()

	// Read the event.
	select {
	case ev := <-ch:
		if ev.Content != "hi" {
			t.Errorf("unexpected content: %q", ev.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	cancel()
	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("tailKiroJSONL did not exit within 2s after draining and cancel")
	}
}

// ── Watch() goroutine channel-close tests ───────────────────────────────────

// TestClaudeCodeWatch_ChannelClosesOnCtxCancel verifies that the Watch()
// goroutine closes its output channel when the context is canceled.
// This exercises the full goroutine lifecycle including the waitForNewestJSONL
// polling loop.
func TestClaudeCodeWatch_ChannelClosesOnCtxCancel(t *testing.T) {
	// Create a project dir structure that claudeProjectDirFor will find.
	workDir := t.TempDir()
	projectDir, err := claudeProjectDirFor(workDir)
	if err != nil {
		t.Fatalf("claudeProjectDirFor: %v", err)
	}
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("creating project dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(projectDir) })

	// Create a JSONL file in the project dir.
	jsonlPath := filepath.Join(projectDir, "test-session.jsonl")
	writeFile(t, jsonlPath, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`+"\n")

	adapter := &ClaudeCodeAdapter{}
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := adapter.Watch(ctx, "test-session", workDir, time.Time{})
	if err != nil {
		t.Fatalf("Watch() error: %v", err)
	}

	// Drain whatever events come through.
	drained := make(chan struct{})
	go func() {
		for range ch {
		}
		close(drained)
	}()

	// Give the goroutine time to start tailing.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-drained:
		// Channel was closed — goroutine exited cleanly.
	case <-time.After(3 * time.Second):
		t.Fatal("Watch() channel not closed within 3s after context cancel")
	}
}

// TestClaudeCodeWatch_ChannelClosesOnCtxCancel_NoFile verifies that the Watch()
// goroutine exits cleanly when canceled while waiting for a JSONL file to appear.
func TestClaudeCodeWatch_ChannelClosesOnCtxCancel_NoFile(t *testing.T) {
	// Create the project dir but don't put any JSONL in it.
	workDir := t.TempDir()
	projectDir, err := claudeProjectDirFor(workDir)
	if err != nil {
		t.Fatalf("claudeProjectDirFor: %v", err)
	}
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("creating project dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(projectDir) })

	adapter := &ClaudeCodeAdapter{}
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := adapter.Watch(ctx, "test-session", workDir, time.Time{})
	if err != nil {
		t.Fatalf("Watch() error: %v", err)
	}

	// The goroutine is inside waitForNewestJSONL polling.
	// Cancel while it's waiting.
	time.Sleep(100 * time.Millisecond)
	cancel()

	// Verify channel closes (goroutine exits).
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to be closed, got an event")
		}
		// Channel closed — success
	case <-time.After(3 * time.Second):
		t.Fatal("Watch() channel not closed within 3s after context cancel (no file scenario)")
	}
}

// TestKiroWatch_ChannelClosesOnCtxCancel verifies that the Kiro Watch()
// goroutine closes its output channel when the context is canceled.
func TestKiroWatch_ChannelClosesOnCtxCancel(t *testing.T) {
	// We can't easily override the sessions dir path used by KiroAdapter.Watch()
	// since it derives from $HOME. Instead, create the directory structure at
	// the actual kiro sessions location.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("getting home dir: %v", err)
	}
	sessionsDir := filepath.Join(home, kiroSessionsDir)
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("creating sessions dir: %v", err)
	}

	workDir := t.TempDir()
	sessionUUID := "tail-ctx-test-" + time.Now().Format("20060102150405")
	jsonlPath := filepath.Join(sessionsDir, sessionUUID+".jsonl")
	metaPath := filepath.Join(sessionsDir, sessionUUID+".json")

	writeFile(t, jsonlPath, `{"version":"v1","kind":"Prompt","data":{"message_id":"m1","content":[{"kind":"text","data":"hi"}],"meta":{"timestamp":1700000000}}}`+"\n")
	metaJSON, _ := json.Marshal(kiroMetadata{SessionID: sessionUUID, Cwd: workDir})
	if err := os.WriteFile(metaPath, metaJSON, 0o600); err != nil {
		t.Fatalf("writing metadata: %v", err)
	}
	t.Cleanup(func() {
		os.Remove(jsonlPath)
		os.Remove(metaPath)
	})

	adapter := &KiroAdapter{}
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := adapter.Watch(ctx, "test-session", workDir, time.Time{})
	if err != nil {
		t.Fatalf("Watch() error: %v", err)
	}

	// Drain events.
	drained := make(chan struct{})
	go func() {
		for range ch {
		}
		close(drained)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-drained:
		// Channel was closed — goroutine exited cleanly.
	case <-time.After(3 * time.Second):
		t.Fatal("Kiro Watch() channel not closed within 3s after context cancel")
	}
}

// TestKiroWatch_ChannelClosesOnCtxCancel_NoFile verifies that the Kiro Watch()
// goroutine exits cleanly when canceled while waiting for a JSONL file.
func TestKiroWatch_ChannelClosesOnCtxCancel_NoFile(t *testing.T) {
	// Use a unique workDir that won't match any existing Kiro sessions.
	workDir := t.TempDir()

	// Ensure sessions dir exists (Watch will look there).
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("getting home dir: %v", err)
	}
	sessionsDir := filepath.Join(home, kiroSessionsDir)
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("creating sessions dir: %v", err)
	}

	adapter := &KiroAdapter{}
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := adapter.Watch(ctx, "test-session", workDir, time.Time{})
	if err != nil {
		t.Fatalf("Watch() error: %v", err)
	}

	// The goroutine is in waitForNewestKiroJSONL, polling. Cancel it.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to be closed, got an event")
		}
		// success
	case <-time.After(3 * time.Second):
		t.Fatal("Kiro Watch() channel not closed within 3s after context cancel (no file scenario)")
	}
}

// ── waitForNewestJSONL context-cancel tests ─────────────────────────────────

// TestWaitForNewestJSONL_ReturnsOnCtxCancel verifies that waitForNewestJSONL
// returns ctx.Err() when the context is canceled before a file appears.
func TestWaitForNewestJSONL_ReturnsOnCtxCancel(t *testing.T) {
	dir := t.TempDir() // empty dir — no JSONL files

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := waitForNewestJSONL(ctx, dir, time.Time{})
		done <- err
	}()

	// Let it poll once or twice, then cancel.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForNewestJSONL did not return within 2s after context cancel")
	}
}

// TestWaitForNewestKiroJSONL_ReturnsOnCtxCancel verifies that
// waitForNewestKiroJSONL returns ctx.Err() when context is canceled.
func TestWaitForNewestKiroJSONL_ReturnsOnCtxCancel(t *testing.T) {
	dir := t.TempDir()
	workDir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := waitForNewestKiroJSONL(ctx, dir, workDir, time.Time{})
		done <- err
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForNewestKiroJSONL did not return within 2s after context cancel")
	}
}
