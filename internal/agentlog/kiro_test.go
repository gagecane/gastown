package agentlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKiroAdapter_AgentType(t *testing.T) {
	a := &KiroAdapter{}
	if got := a.AgentType(); got != "kiro" {
		t.Errorf("AgentType() = %q, want %q", got, "kiro")
	}
}

func TestKiroSessionsDirPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("getting home dir: %v", err)
	}
	got, err := kiroSessionsDirPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(home, kiroSessionsDir)
	if got != want {
		t.Errorf("kiroSessionsDirPath() = %q, want %q", got, want)
	}
}

func TestKiroMetaMatches(t *testing.T) {
	dir := t.TempDir()
	workDir := t.TempDir() // some real absolute path

	// Valid metadata with matching cwd.
	matching := filepath.Join(dir, "match.json")
	writeJSON(t, matching, kiroMetadata{SessionID: "s1", Cwd: workDir})
	if !kiroMetaMatches(matching, workDir) {
		t.Errorf("expected matching metadata to return true")
	}

	// Metadata with a different cwd.
	mismatch := filepath.Join(dir, "mismatch.json")
	writeJSON(t, mismatch, kiroMetadata{SessionID: "s2", Cwd: "/nowhere/else"})
	if kiroMetaMatches(mismatch, workDir) {
		t.Errorf("expected non-matching metadata to return false")
	}

	// Missing file.
	if kiroMetaMatches(filepath.Join(dir, "missing.json"), workDir) {
		t.Errorf("expected missing metadata to return false")
	}

	// Invalid JSON.
	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if kiroMetaMatches(broken, workDir) {
		t.Errorf("expected invalid metadata to return false")
	}

	// Empty cwd — should not match a non-empty workDir.
	empty := filepath.Join(dir, "empty.json")
	writeJSON(t, empty, kiroMetadata{SessionID: "s3", Cwd: ""})
	if kiroMetaMatches(empty, workDir) {
		t.Errorf("expected empty cwd to return false")
	}
}

func TestNewestKiroJSONLIn(t *testing.T) {
	sessionsDir := t.TempDir()
	workDir := t.TempDir()

	// Older matching pair.
	older := filepath.Join(sessionsDir, "older.jsonl")
	olderMeta := filepath.Join(sessionsDir, "older.json")
	writeFile(t, older, "")
	writeJSON(t, olderMeta, kiroMetadata{SessionID: "older", Cwd: workDir})
	oldTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Newer matching pair.
	newer := filepath.Join(sessionsDir, "newer.jsonl")
	newerMeta := filepath.Join(sessionsDir, "newer.json")
	writeFile(t, newer, "")
	writeJSON(t, newerMeta, kiroMetadata{SessionID: "newer", Cwd: workDir})

	// Non-matching pair (different cwd) that is newer still.
	other := filepath.Join(sessionsDir, "other.jsonl")
	otherMeta := filepath.Join(sessionsDir, "other.json")
	writeFile(t, other, "")
	writeJSON(t, otherMeta, kiroMetadata{SessionID: "other", Cwd: "/somewhere/else"})

	// Orphan JSONL with no metadata file.
	orphan := filepath.Join(sessionsDir, "orphan.jsonl")
	writeFile(t, orphan, "")

	got, ok := newestKiroJSONLIn(sessionsDir, workDir, time.Time{})
	if !ok {
		t.Fatalf("expected a match, got none")
	}
	if got != newer {
		t.Errorf("newestKiroJSONLIn = %q, want %q", got, newer)
	}

	// With a since filter that excludes the older file but keeps the newer,
	// we should still get the newer one.
	cutoff := time.Now().Add(-30 * time.Minute)
	got, ok = newestKiroJSONLIn(sessionsDir, workDir, cutoff)
	if !ok || got != newer {
		t.Errorf("with since filter got (%q, %v), want (%q, true)", got, ok, newer)
	}

	// With a since filter past every file, expect no match.
	future := time.Now().Add(1 * time.Hour)
	if _, ok := newestKiroJSONLIn(sessionsDir, workDir, future); ok {
		t.Errorf("expected no match when since is in the future")
	}
}

func TestParseKiroLine_PromptText(t *testing.T) {
	line := `{"version":"v1","kind":"Prompt","data":{"message_id":"m1","content":[{"kind":"text","data":"hello"}],"meta":{"timestamp":1700000000}}}`
	events, lastTS := parseKiroLine(line, "gt-session", "kiro", "native-uuid", time.Time{})
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.EventType != "text" {
		t.Errorf("EventType = %q, want %q", ev.EventType, "text")
	}
	if ev.Role != "user" {
		t.Errorf("Role = %q, want %q", ev.Role, "user")
	}
	if ev.Content != "hello" {
		t.Errorf("Content = %q, want %q", ev.Content, "hello")
	}
	if ev.SessionID != "gt-session" {
		t.Errorf("SessionID = %q, want %q", ev.SessionID, "gt-session")
	}
	if ev.NativeSessionID != "native-uuid" {
		t.Errorf("NativeSessionID = %q, want %q", ev.NativeSessionID, "native-uuid")
	}
	if ev.AgentType != "kiro" {
		t.Errorf("AgentType = %q, want %q", ev.AgentType, "kiro")
	}
	if !ev.Timestamp.Equal(time.Unix(1700000000, 0).UTC()) {
		t.Errorf("Timestamp = %v, want %v", ev.Timestamp, time.Unix(1700000000, 0).UTC())
	}
	if !lastTS.Equal(time.Unix(1700000000, 0).UTC()) {
		t.Errorf("lastTS = %v, want %v", lastTS, time.Unix(1700000000, 0).UTC())
	}
}

func TestParseKiroLine_AssistantToolUse(t *testing.T) {
	line := `{"version":"v1","kind":"AssistantMessage","data":{"message_id":"m2","content":[{"kind":"text","data":""},{"kind":"toolUse","data":{"toolUseId":"tu1","name":"shell","input":{"command":"ls"}}}]}}`
	seed := time.Unix(1700000000, 0).UTC()
	events, lastTS := parseKiroLine(line, "s1", "kiro", "u1", seed)
	// The empty text block is dropped; only the toolUse should surface.
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.EventType != "tool_use" {
		t.Errorf("EventType = %q, want %q", ev.EventType, "tool_use")
	}
	if ev.Role != "assistant" {
		t.Errorf("Role = %q, want %q", ev.Role, "assistant")
	}
	if len(ev.Content) < len("shell: ") || ev.Content[:len("shell: ")] != "shell: " {
		t.Errorf("Content = %q, want prefix %q", ev.Content, "shell: ")
	}
	if !ev.Timestamp.Equal(seed) {
		t.Errorf("Timestamp = %v, want inherited %v", ev.Timestamp, seed)
	}
	if !lastTS.Equal(seed) {
		t.Errorf("lastTS = %v, want %v", lastTS, seed)
	}
}

func TestParseKiroLine_ToolResults(t *testing.T) {
	line := `{"version":"v1","kind":"ToolResults","data":{"message_id":"m3","content":[{"kind":"toolResult","data":{"toolUseId":"tu1","content":[{"kind":"text","data":"ok"},{"kind":"json","data":{"exit_status":"exit status: 0","stdout":"hello\n"}}],"status":"success"}}]}}`
	seed := time.Unix(1700000001, 0).UTC()
	events, lastTS := parseKiroLine(line, "s1", "kiro", "u1", seed)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.EventType != "tool_result" {
		t.Errorf("EventType = %q, want %q", ev.EventType, "tool_result")
	}
	if ev.Role != "user" {
		t.Errorf("Role = %q, want %q (tool results are fed back as user role)", ev.Role, "user")
	}
	// Content should contain both the text payload and serialized JSON.
	wantSub := []string{"ok", `"exit_status":"exit status: 0"`, `"stdout":"hello\n"`}
	for _, s := range wantSub {
		if !strings.Contains(ev.Content, s) {
			t.Errorf("Content %q missing expected substring %q", ev.Content, s)
		}
	}
	if !lastTS.Equal(seed) {
		t.Errorf("lastTS = %v, want %v", lastTS, seed)
	}
}

func TestParseKiroLine_SkipsImageAndUnknown(t *testing.T) {
	// Image blocks are intentionally skipped; unknown entry kinds are dropped.
	imgLine := `{"version":"v1","kind":"Prompt","data":{"message_id":"m4","content":[{"kind":"image","data":{"format":"png"}}],"meta":{"timestamp":1700000000}}}`
	events, _ := parseKiroLine(imgLine, "s1", "kiro", "u1", time.Time{})
	if len(events) != 0 {
		t.Errorf("expected 0 events for image-only content, got %d", len(events))
	}

	unknown := `{"version":"v1","kind":"Unknown","data":{"message_id":"m5","content":[{"kind":"text","data":"hi"}]}}`
	events, _ = parseKiroLine(unknown, "s1", "kiro", "u1", time.Time{})
	if len(events) != 0 {
		t.Errorf("expected 0 events for unknown entry kind, got %d", len(events))
	}
}

func TestParseKiroLine_InvalidJSON(t *testing.T) {
	events, lastTS := parseKiroLine("not json", "s1", "kiro", "u1", time.Time{})
	if len(events) != 0 {
		t.Errorf("expected 0 events for invalid JSON, got %d", len(events))
	}
	if !lastTS.IsZero() {
		t.Errorf("lastTS should stay zero on parse failure, got %v", lastTS)
	}
}

func TestParseKiroLine_FallbackTimestampWhenNone(t *testing.T) {
	// Entry lacks meta and lastTS is zero: should fall back to time.Now(),
	// which we verify is within a recent window.
	line := `{"version":"v1","kind":"AssistantMessage","data":{"message_id":"m6","content":[{"kind":"text","data":"hi"}]}}`
	before := time.Now().Add(-1 * time.Second)
	events, _ := parseKiroLine(line, "s1", "kiro", "u1", time.Time{})
	after := time.Now().Add(1 * time.Second)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ts := events[0].Timestamp
	if ts.Before(before) || ts.After(after) {
		t.Errorf("fallback timestamp %v not within [%v, %v]", ts, before, after)
	}
}

func TestNewAdapterReturnsKiro(t *testing.T) {
	a := NewAdapter("kiro")
	if a == nil {
		t.Fatal("NewAdapter(\"kiro\") returned nil")
	}
	if _, ok := a.(*KiroAdapter); !ok {
		t.Errorf("NewAdapter(\"kiro\") returned %T, want *KiroAdapter", a)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshaling %v: %v", v, err)
	}
	writeFile(t, path, string(b))
}
