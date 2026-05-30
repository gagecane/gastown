package daemon

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"testing"
	"time"
)

// TestIsHostKill pins down the SIGKILL-vs-real-FAIL distinction (hq-0qszq): a
// host SIGKILL with our context still alive is transient; a normal non-zero exit
// or our own deadline cancellation is not a host kill.
func TestIsHostKill(t *testing.T) {
	live := context.Background()
	deadlineCtx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond) // ensure the deadline has elapsed

	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"sigkill, ctx live → host kill", live, errors.New("signal: killed"), true},
		{"sigkill, but our deadline fired → not host kill", deadlineCtx, errors.New("signal: killed"), false},
		{"normal non-zero exit → not host kill", live, errors.New("exit status 1"), false},
		{"nil error → not host kill", live, nil, false},
	}
	for _, tc := range cases {
		if got := isHostKill(tc.ctx, tc.err); got != tc.want {
			t.Errorf("%s: isHostKill = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestRunCommandOnWorktree_HostKill drives the gate runner against real shell
// commands and asserts: a self-SIGKILL is retried then reported TRANSIENT
// (errGateHostKilled, not a regression); a genuine FAIL is a hard failure and is
// NOT retried; a clean command passes.
func TestRunCommandOnWorktree_HostKill(t *testing.T) {
	d := &Daemon{config: &Config{TownRoot: t.TempDir()}, logger: log.New(io.Discard, "", 0)}

	orig := gateHostKillBackoff
	gateHostKillBackoff = time.Millisecond // keep the retry loop fast
	defer func() { gateHostKillBackoff = orig }()

	t.Run("self-SIGKILL is transient, not a regression", func(t *testing.T) {
		err := d.runCommandOnWorktree(context.Background(), "rig", d.config.TownRoot, "test", "kill -9 $$")
		if !errors.Is(err, errGateHostKilled) {
			t.Fatalf("a host SIGKILL must be reported transient (errGateHostKilled), got: %v", err)
		}
	})

	t.Run("real failure is a hard error, not transient", func(t *testing.T) {
		err := d.runCommandOnWorktree(context.Background(), "rig", d.config.TownRoot, "test", "echo boom; exit 1")
		if err == nil {
			t.Fatal("expected a failure")
		}
		if errors.Is(err, errGateHostKilled) {
			t.Fatalf("a genuine exit-1 failure must NOT be classified as a host kill: %v", err)
		}
		if !strings.Contains(err.Error(), "test failed") {
			t.Errorf("expected a real-failure message, got: %v", err)
		}
	})

	t.Run("clean command passes", func(t *testing.T) {
		if err := d.runCommandOnWorktree(context.Background(), "rig", d.config.TownRoot, "test", "true"); err != nil {
			t.Errorf("clean command should pass, got: %v", err)
		}
	})
}
