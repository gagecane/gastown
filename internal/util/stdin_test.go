package util

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// withStdin swaps stdinReader for a test, restoring it on cleanup.
func withStdin(t *testing.T, r io.Reader) {
	t.Helper()
	old := stdinReader
	stdinReader = r
	t.Cleanup(func() { stdinReader = old })
}

// withStdout swaps stdoutWriter for a test, restoring it on cleanup.
func withStdout(t *testing.T) *bytes.Buffer {
	t.Helper()
	old := stdoutWriter
	buf := &bytes.Buffer{}
	stdoutWriter = buf
	t.Cleanup(func() { stdoutWriter = old })
	return buf
}

// withEnv sets an env var for the test and restores its previous value.
func withEnv(t *testing.T, key, value string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	if err := os.Setenv(key, value); err != nil {
		t.Fatalf("setenv %s=%q: %v", key, value, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

// withEnvUnset ensures an env var is unset for the test, restoring it after.
func withEnvUnset(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unsetenv %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
		}
	})
}

func TestReadStdinLineWithTimeout_DataAvailable(t *testing.T) {
	withEnvUnset(t, StdinTimeoutEnv)
	withStdin(t, strings.NewReader("yes\n"))

	got, err := ReadStdinLineWithTimeout(500 * time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "yes" {
		t.Errorf("got %q, want %q", got, "yes")
	}
}

func TestReadStdinLineWithTimeout_StripsCRLF(t *testing.T) {
	withEnvUnset(t, StdinTimeoutEnv)
	withStdin(t, strings.NewReader("ok\r\n"))

	got, err := ReadStdinLineWithTimeout(500 * time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ok" {
		t.Errorf("got %q, want %q", got, "ok")
	}
}

// TestReadStdinLineWithTimeout_PipeNeverWritten simulates the production
// pty-hang: stdin is a pipe that exists but no data is ever sent. Without
// the timeout, ReadString would block forever.
func TestReadStdinLineWithTimeout_PipeNeverWritten(t *testing.T) {
	withEnvUnset(t, StdinTimeoutEnv)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	// Intentionally do NOT write to w. Close it at the end so the goroutine
	// reading from r eventually sees EOF — not required for the test to
	// pass, but keeps the goroutine from living past the test binary exit
	// on platforms that care.
	t.Cleanup(func() { _ = w.Close(); _ = r.Close() })

	withStdin(t, r)

	start := time.Now()
	got, err := ReadStdinLineWithTimeout(50 * time.Millisecond)
	elapsed := time.Since(start)

	if err != ErrStdinTimeout {
		t.Fatalf("got err %v, want ErrStdinTimeout", err)
	}
	if got != "" {
		t.Errorf("got line %q, want empty", got)
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("returned too fast (%v) — timeout not honored", elapsed)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("returned too slow (%v) — timeout not effective", elapsed)
	}
}

func TestReadStdinLineWithTimeout_EnvOverride(t *testing.T) {
	withEnv(t, StdinTimeoutEnv, "30")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = w.Close(); _ = r.Close() })
	withStdin(t, r)

	// Pass a huge timeout; env var should shrink it to 30ms.
	start := time.Now()
	_, err = ReadStdinLineWithTimeout(10 * time.Second)
	elapsed := time.Since(start)

	if err != ErrStdinTimeout {
		t.Fatalf("got err %v, want ErrStdinTimeout", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("env override not honored: elapsed %v, want ~30ms", elapsed)
	}
}

func TestPromptYesNoWithTimeout_AgentContextShortCircuit(t *testing.T) {
	withEnv(t, AgentRoleEnv, "polecat")
	withEnvUnset(t, StdinTimeoutEnv)
	// Use a pipe that will never be written to. If the agent short-circuit
	// fails, the test will hang on the helper's real timeout.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = w.Close(); _ = r.Close() })
	withStdin(t, r)
	buf := withStdout(t)

	start := time.Now()
	got := PromptYesNoWithTimeout("Proceed?", false, 10*time.Second)
	elapsed := time.Since(start)

	if got != false {
		t.Errorf("got %v, want default=false", got)
	}
	// Should return nearly instantly (sub-millisecond on most hardware).
	// Allow a generous window for slow CI.
	if elapsed > 100*time.Millisecond {
		t.Errorf("agent short-circuit too slow: %v", elapsed)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no prompt output, got %q", buf.String())
	}
}

func TestPromptYesNoWithTimeout_AgentContextDefaultTrue(t *testing.T) {
	withEnv(t, AgentRoleEnv, "witness")
	withEnvUnset(t, StdinTimeoutEnv)

	got := PromptYesNoWithTimeout("Continue?", true, 10*time.Second)
	if got != true {
		t.Errorf("got %v, want default=true", got)
	}
}

func TestPromptYesNoWithTimeout_HumanYes(t *testing.T) {
	withEnvUnset(t, AgentRoleEnv)
	withEnvUnset(t, StdinTimeoutEnv)
	withStdin(t, strings.NewReader("y\n"))
	buf := withStdout(t)

	got := PromptYesNoWithTimeout("OK?", false, 500*time.Millisecond)
	if got != true {
		t.Errorf("got %v, want true", got)
	}
	if !strings.Contains(buf.String(), "[y/N]") {
		t.Errorf("expected [y/N] prompt, got %q", buf.String())
	}
}

func TestPromptYesNoWithTimeout_HumanYesWordForm(t *testing.T) {
	withEnvUnset(t, AgentRoleEnv)
	withEnvUnset(t, StdinTimeoutEnv)
	withStdin(t, strings.NewReader("YES\n"))
	_ = withStdout(t)

	got := PromptYesNoWithTimeout("OK?", false, 500*time.Millisecond)
	if got != true {
		t.Errorf("got %v, want true", got)
	}
}

func TestPromptYesNoWithTimeout_HumanDefaultOnEnter(t *testing.T) {
	withEnvUnset(t, AgentRoleEnv)
	withEnvUnset(t, StdinTimeoutEnv)
	withStdin(t, strings.NewReader("\n"))
	_ = withStdout(t)

	got := PromptYesNoWithTimeout("OK?", true, 500*time.Millisecond)
	if got != true {
		t.Errorf("got %v, want default=true", got)
	}
}

func TestPromptYesNoWithTimeout_HumanTimeoutUsesDefault(t *testing.T) {
	withEnvUnset(t, AgentRoleEnv)
	withEnvUnset(t, StdinTimeoutEnv)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = w.Close(); _ = r.Close() })
	withStdin(t, r)
	_ = withStdout(t)

	got := PromptYesNoWithTimeout("OK?", false, 30*time.Millisecond)
	if got != false {
		t.Errorf("got %v, want default=false", got)
	}
}

func TestPromptYesNoWithTimeout_ShowsCorrectDefaultSuffix(t *testing.T) {
	withEnvUnset(t, AgentRoleEnv)
	withEnvUnset(t, StdinTimeoutEnv)

	cases := []struct {
		name       string
		defaultAns bool
		want       string
	}{
		{name: "default_no", defaultAns: false, want: "[y/N]"},
		{name: "default_yes", defaultAns: true, want: "[Y/n]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withStdin(t, strings.NewReader("\n"))
			buf := withStdout(t)

			_ = PromptYesNoWithTimeout("Proceed?", tc.defaultAns, 500*time.Millisecond)

			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("expected %q in prompt, got %q", tc.want, buf.String())
			}
		})
	}
}
