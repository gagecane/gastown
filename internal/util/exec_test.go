package util

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestExecWithOutput(t *testing.T) {
	// Test successful command
	var output string
	var err error
	if runtime.GOOS == "windows" {
		output, err = ExecWithOutput(".", "cmd", "/c", "echo hello")
	} else {
		output, err = ExecWithOutput(".", "echo", "hello")
	}
	if err != nil {
		t.Fatalf("ExecWithOutput failed: %v", err)
	}
	if output != "hello" {
		t.Errorf("expected 'hello', got %q", output)
	}

	// Test command that fails
	if runtime.GOOS == "windows" {
		_, err = ExecWithOutput(".", "cmd", "/c", "exit /b 1")
	} else {
		_, err = ExecWithOutput(".", "false")
	}
	if err == nil {
		t.Error("expected error for failing command")
	}
}

func TestExecRun(t *testing.T) {
	// Test successful command
	var err error
	if runtime.GOOS == "windows" {
		err = ExecRun(".", "cmd", "/c", "exit /b 0")
	} else {
		err = ExecRun(".", "true")
	}
	if err != nil {
		t.Fatalf("ExecRun failed: %v", err)
	}

	// Test command that fails
	if runtime.GOOS == "windows" {
		err = ExecRun(".", "cmd", "/c", "exit /b 1")
	} else {
		err = ExecRun(".", "false")
	}
	if err == nil {
		t.Error("expected error for failing command")
	}
}

func TestExecRunContext(t *testing.T) {
	// Test successful command
	var err error
	if runtime.GOOS == "windows" {
		err = ExecRunContext(context.Background(), ".", "cmd", "/c", "exit /b 0")
	} else {
		err = ExecRunContext(context.Background(), ".", "true")
	}
	if err != nil {
		t.Fatalf("ExecRunContext failed: %v", err)
	}

	// Test command that fails (non-zero exit, no timeout)
	if runtime.GOOS == "windows" {
		err = ExecRunContext(context.Background(), ".", "cmd", "/c", "exit /b 1")
	} else {
		err = ExecRunContext(context.Background(), ".", "false")
	}
	if err == nil {
		t.Error("expected error for failing command")
	}
}

// TestExecRunContext_TimeoutGroupKills is the gu-odhqc regression guard: a child
// that blocks longer than the context deadline must be killed (not awaited
// forever), and the returned error must carry the context-deadline cause so
// callers can distinguish a timeout from a normal failure.
func TestExecRunContext_TimeoutGroupKills(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep semantics differ on Windows")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	// `sleep 30` would hang the caller for 30s without the deadline + group-kill.
	err := ExecRunContext(ctx, ".", "sleep", "30")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded in error chain, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("ExecRunContext did not honor the deadline: took %v", elapsed)
	}
}

func TestExecWithOutput_WorkDir(t *testing.T) {
	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "exec-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Test that workDir is respected
	var output string
	if runtime.GOOS == "windows" {
		output, err = ExecWithOutput(tmpDir, "cmd", "/c", "cd")
	} else {
		output, err = ExecWithOutput(tmpDir, "pwd")
	}
	if err != nil {
		t.Fatalf("ExecWithOutput failed: %v", err)
	}
	if !strings.Contains(output, tmpDir) && !strings.Contains(tmpDir, output) {
		t.Errorf("expected output to contain %q, got %q", tmpDir, output)
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"hello\nworld", "hello"},
		{"\n\nhello\nworld", "hello"},
		{"  hello  \nworld", "hello"},
		{"", ""},
		{"\n\n\n", ""},
		{"Error: something went wrong\nUsage:\n  gt convoy [flags]\n", "Error: something went wrong"},
	}
	for _, tc := range tests {
		got := FirstLine(tc.input)
		if got != tc.want {
			t.Errorf("FirstLine(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestExecWithOutput_StderrInError(t *testing.T) {
	// Test that stderr is captured in error
	var err error
	if runtime.GOOS == "windows" {
		_, err = ExecWithOutput(".", "cmd", "/c", "echo error message 1>&2 & exit /b 1")
	} else {
		_, err = ExecWithOutput(".", "sh", "-c", "echo 'error message' >&2; exit 1")
	}
	if err == nil {
		t.Error("expected error")
	}
	if !strings.Contains(err.Error(), "error message") {
		t.Errorf("expected error to contain stderr, got %q", err.Error())
	}
}
