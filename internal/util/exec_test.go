package util

import (
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

func TestExecRunWithTimeout_Success(t *testing.T) {
	var err error
	if runtime.GOOS == "windows" {
		err = ExecRunWithTimeout(5*time.Second, ".", "cmd", "/c", "exit /b 0")
	} else {
		err = ExecRunWithTimeout(5*time.Second, ".", "true")
	}
	if err != nil {
		t.Fatalf("ExecRunWithTimeout(success) returned error: %v", err)
	}
}

func TestExecRunWithTimeout_NonZeroExit(t *testing.T) {
	var err error
	if runtime.GOOS == "windows" {
		err = ExecRunWithTimeout(5*time.Second, ".", "cmd", "/c", "exit /b 1")
	} else {
		err = ExecRunWithTimeout(5*time.Second, ".", "false")
	}
	if err == nil {
		t.Fatal("expected error for failing command")
	}
	// A genuine non-zero exit must NOT be reported as a timeout.
	if errors.Is(err, ErrExecTimeout) {
		t.Errorf("non-zero exit misclassified as timeout: %v", err)
	}
}

// TestExecRunWithTimeout_TimesOut is the regression guard for gu-odhqc: a
// command that would otherwise hang forever must be killed at the deadline and
// surface ErrExecTimeout, not block the caller.
func TestExecRunWithTimeout_TimesOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep-based hang test is unix-only")
	}

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		// Sleep far longer than the timeout — stands in for a hung nuke.
		done <- ExecRunWithTimeout(200*time.Millisecond, ".", "sleep", "30")
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
		if !errors.Is(err, ErrExecTimeout) {
			t.Errorf("expected ErrExecTimeout, got %v", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("returned after %v — timeout did not fire promptly", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ExecRunWithTimeout did not return — hung command was not killed")
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
