package cmd

import "testing"

func TestIsPolecatDone(t *testing.T) {
	// No session name → not done (errs toward recovery)
	if isPolecatDone("/tmp/nonexistent", "") {
		t.Error("empty session should return false")
	}

	// No town root → not done
	if isPolecatDone("", "some-session") {
		t.Error("empty town root should return false")
	}

	// Nonexistent heartbeat file → not done
	if isPolecatDone("/tmp/nonexistent-town", "no-such-session") {
		t.Error("missing heartbeat should return false")
	}
}

func TestKiroContinuePromptNoNewlines(t *testing.T) {
	// The continue prompt must not contain newlines or shell metacharacters
	// that would break exec.Command argument passing.
	for _, ch := range kiroContinuePrompt {
		if ch == '\n' || ch == '\r' {
			t.Fatalf("kiroContinuePrompt contains newline character")
		}
	}
}
