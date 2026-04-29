package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

// writeStaleLock writes a JSON lock file at <workerDir>/.runtime/agent.lock
// with the given PID and session ID. The caller can pass an invalid PID
// (e.g. 999999999) and a nonexistent session to simulate a truly-stale lock.
func writeStaleLock(t *testing.T, workerDir string, pid int, sessionID string) string {
	t.Helper()
	runtimeDir := filepath.Join(workerDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(runtimeDir, "agent.lock")
	// Format matches lock.LockInfo; hostname is ignored by the staleness check.
	content := `{
  "pid": ` + intToStr(pid) + `,
  "acquired_at": "2026-04-27T00:00:00Z",
  "session_id": "` + sessionID + `",
  "hostname": "test"
}`
	if err := os.WriteFile(lockPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return lockPath
}

// intToStr avoids pulling strconv into a tiny test helper.
func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

// TestIdentityCollision_AutoCleansStaleLocksInRun verifies that Run() auto-
// releases truly-stale locks (dead PID + session gone) inline, reports OK
// with an auto-cleaned count, and removes the lock file from disk.
//
// This prevents recurring warnings when polecat/crew sessions exit uncleanly.
func TestIdentityCollision_AutoCleansStaleLocksInRun(t *testing.T) {
	townRoot := t.TempDir()
	workerDir := filepath.Join(townRoot, "testrig", "polecats", "ghost", "testrig")
	lockPath := writeStaleLock(t, workerDir, 999999999, "gt-nonexistent-session-xyz")

	check := NewIdentityCollisionCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)

	// Truly-stale locks should be auto-cleaned in Run(), producing an OK status.
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK after auto-clean, got %v: %q", result.Status, result.Message)
	}

	// Lock file should be removed from disk.
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Errorf("expected lock file removed, but still exists: %v", err)
	}

	// Message should mention the auto-clean for observability.
	if !containsString(result.Message, "auto-cleaned") {
		t.Errorf("expected Message to mention 'auto-cleaned', got %q", result.Message)
	}
}

// TestIdentityCollision_ReadOnlySkipsAutoClean verifies that when ctx.ReadOnly
// is set, Run() does NOT mutate the filesystem — the stale lock is still
// reported as a warning and the file persists on disk. This preserves the
// traditional "doctor is observation-only" contract when callers opt in.
func TestIdentityCollision_ReadOnlySkipsAutoClean(t *testing.T) {
	townRoot := t.TempDir()
	workerDir := filepath.Join(townRoot, "testrig", "polecats", "ghost", "testrig")
	lockPath := writeStaleLock(t, workerDir, 999999999, "gt-nonexistent-session-xyz")

	check := NewIdentityCollisionCheck()
	ctx := &CheckContext{TownRoot: townRoot, ReadOnly: true}

	result := check.Run(ctx)

	// Must warn — no mutation allowed, caller needs to know.
	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning in read-only mode, got %v: %q", result.Status, result.Message)
	}

	// Lock file must persist — ReadOnly guarantees no mutation.
	if _, err := os.Stat(lockPath); err != nil {
		t.Errorf("expected lock file to persist in read-only mode, got stat error: %v", err)
	}

	// Message should report the stale lock count, not an auto-clean count.
	if containsString(result.Message, "auto-cleaned") {
		t.Errorf("read-only mode must not report auto-clean; got %q", result.Message)
	}
	if !containsString(result.Message, "stale lock") {
		t.Errorf("expected Message to mention 'stale lock', got %q", result.Message)
	}
}

// containsString is a tiny helper to avoid importing strings just for Contains.
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
