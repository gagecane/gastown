//go:build !windows

package daemon

import (
	"os"
	"syscall"
	"testing"
)

// TestClassifySignal pins the signal→action mapping the supervisor loop relies
// on. The startup sequence registers daemonSignals(); each must resolve to the
// right branch so a handoff (SIGUSR1) and a clear-backoff (SIGUSR2) are not
// mistaken for shutdown (SIGINT/SIGTERM).
//
// SIGUSR1/SIGUSR2 are undefined on Windows, so this test is constrained to
// non-Windows platforms (mirroring signals_unix.go / signals_windows.go).
func TestClassifySignal(t *testing.T) {
	cases := []struct {
		name string
		sig  os.Signal
		want signalAction
	}{
		{"SIGUSR1 is lifecycle", syscall.SIGUSR1, signalLifecycle},
		{"SIGUSR2 is reload-restart", syscall.SIGUSR2, signalReloadRestart},
		{"SIGTERM is shutdown", syscall.SIGTERM, signalShutdown},
		{"SIGINT is shutdown", syscall.SIGINT, signalShutdown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifySignal(tc.sig); got != tc.want {
				t.Errorf("classifySignal(%v) = %d, want %d", tc.sig, got, tc.want)
			}
		})
	}
}
