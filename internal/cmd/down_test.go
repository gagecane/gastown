package cmd

import (
	"os"
	"testing"
)

func TestProcessAlive_CurrentProcess(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("current process should be detected as running")
	}
}

func TestProcessAlive_InvalidPID(t *testing.T) {
	if processAlive(99999999) {
		t.Error("invalid PID should not be detected as running")
	}
}

func TestProcessAlive_MaxPID(t *testing.T) {
	if processAlive(2147483647) {
		t.Error("max PID should not be running")
	}
}
