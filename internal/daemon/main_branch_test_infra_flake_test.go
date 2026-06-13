package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"testing"
)

// TestRunCommandOnWorktree_InfraFlake proves the gs-wf0p class-6
// classification: a gate that fails because the external image registry
// (Docker Hub) returned a 5xx or a pull rate-limit is reported as a transient
// infra flake (errGateInfraFlake), distinct from a deterministic assertion
// failure — so the patrol can skip it instead of paging the overseer on a
// Docker Hub 504.
func TestRunCommandOnWorktree_InfraFlake(t *testing.T) {
	d := &Daemon{config: &Config{TownRoot: t.TempDir()}, logger: log.New(io.Discard, "", 0)}

	t.Run("docker hub 504 during image pull is an infra flake", func(t *testing.T) {
		// Reproduce the Docker daemon's pull-error shape, then exit non-zero
		// like act does when the pull fails.
		cmd := `echo 'Error response from daemon: received unexpected HTTP status: 504 Gateway Time-out'; exit 1`
		err := d.runCommandOnWorktree(context.Background(), "rig", d.config.TownRoot, "test", cmd)
		if !errors.Is(err, errGateInfraFlake) {
			t.Fatalf("a Docker Hub 504 pull failure must be classified errGateInfraFlake, got: %v", err)
		}
	})

	t.Run("docker hub pull rate-limit is an infra flake", func(t *testing.T) {
		cmd := `echo 'toomanyrequests: You have reached your pull rate limit.'; exit 1`
		err := d.runCommandOnWorktree(context.Background(), "rig", d.config.TownRoot, "test", cmd)
		if !errors.Is(err, errGateInfraFlake) {
			t.Fatalf("a Docker Hub rate-limit must be classified errGateInfraFlake, got: %v", err)
		}
	})

	t.Run("genuine assertion failure is not an infra flake", func(t *testing.T) {
		err := d.runCommandOnWorktree(context.Background(), "rig", d.config.TownRoot, "test", "echo 'FAIL: TestFoo'; exit 1")
		if errors.Is(err, errGateInfraFlake) {
			t.Fatalf("a genuine exit-1 failure must NOT be classified as an infra flake: %v", err)
		}
	})

	t.Run("app-level 504 in test output is not an infra flake", func(t *testing.T) {
		// A test that asserts on an app's own 504 must keep failing the gate —
		// we match only the registry-specific pull-error shape, not a bare 504.
		cmd := `echo 'assert failed: expected 200 got 504 Gateway Timeout from /api'; exit 1`
		err := d.runCommandOnWorktree(context.Background(), "rig", d.config.TownRoot, "test", cmd)
		if errors.Is(err, errGateInfraFlake) {
			t.Fatalf("an app-level 504 assertion must NOT be classified as an infra flake: %v", err)
		}
	})
}

// TestIsInfraFlake pins the output-scanner: registry-specific pull-failure
// signatures match (case-insensitively); a bare 504 or a manifest-not-found
// (genuine bad image tag) does not.
func TestIsInfraFlake(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{"docker 504 pull error", "received unexpected HTTP status: 504 Gateway Time-out", true},
		{"docker 503 pull error", "received unexpected HTTP status: 503 Service Unavailable", true},
		{"lowercase 502", "received unexpected http status: 502 bad gateway", true},
		{"pull rate limit", "toomanyrequests: You have reached your pull rate limit.", true},
		{"empty", "", false},
		{"bare 504 in app output", "GET /api returned 504", false},
		{"bad image tag (must keep failing)", "manifest for foo:bar not found", false},
		{"plain test failure", "--- FAIL: TestFoo\nFAIL", false},
	}
	for _, tc := range cases {
		if got := isInfraFlake(tc.output); got != tc.want {
			t.Errorf("%s: isInfraFlake = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestIsInfraFlakeFailure pins down the classifier across BOTH runner paths:
// the legacy test_command path propagates the wrapped sentinel (errors.Is
// works), while the gates path flattens per-gate errors into a plain string
// (chain dropped — only substring matching survives). gs-wf0p class 6.
func TestIsInfraFlakeFailure(t *testing.T) {
	wrapped := fmt.Errorf("%w: test (exit status 1)", errGateInfraFlake)
	// Mirror runGatesOnWorktree's flattening: fmt.Sprintf("gate %q: %v", ...).
	flattened := errors.New(`gate "test": ` + wrapped.Error())

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"wrapped sentinel (legacy path)", wrapped, true},
		{"flattened string (gates path)", flattened, true},
		{"host kill is not an infra flake", fmt.Errorf("%w: test", errGateHostKilled), false},
		{"timeout is not an infra flake", fmt.Errorf("%w: test", errGateTimeout), false},
		{"plain assertion failure", errors.New("test failed: exit status 1"), false},
	}
	for _, tc := range cases {
		if got := isInfraFlakeFailure(tc.err); got != tc.want {
			t.Errorf("%s: isInfraFlakeFailure = %v, want %v", tc.name, got, tc.want)
		}
	}
}
