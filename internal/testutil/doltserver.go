//go:build !windows

package testutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql" // required by testcontainers Dolt module
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/dolt"
	"github.com/testcontainers/testcontainers-go/wait"
)

// DoltDockerImage is the Docker image used for Dolt test containers.
// DOLT_ROOT_HOST=% tells the entrypoint to create root@'%' (available
// since Dolt 1.46.0), which lets testcontainers connect via TCP.
const DoltDockerImage = "dolthub/dolt-sql-server:2.0.7"

// doltContainerStartupTimeout overrides the testcontainers-go dolt module's
// default 60s wait-for-log deadline. Under concurrent test load (multiple
// polecat workspaces each spinning up Dolt containers, plus shared Dolt
// server contention on port 3307), 60s is not enough for the
// "Server ready. Accepting connections." log line to appear, causing
// pre-push gate flakes like:
//
//	`Server ready. Accepting connections.` matched 0 times, expected 1
//
// 3 minutes gives the container enough headroom under load while still
// failing fast for genuinely broken images. See bead gu-y2al.
const doltContainerStartupTimeout = 3 * time.Minute

// doltContainerReadyLog is the log line the dolt sql-server prints when it
// is accepting connections. Mirrors the literal used internally by the
// testcontainers-go dolt module so we can build our own wait strategy with
// a tunable deadline.
const doltContainerReadyLog = "Server ready. Accepting connections."

var (
	doltCtr     *dolt.DoltContainer
	doltCtrOnce sync.Once
	doltCtrErr  error
	doltCtrPort string
	dockerOnce  sync.Once
	dockerAvail bool
)

// isDockerAvailable returns true if the Docker daemon is reachable.
// The result is cached after the first call.
func isDockerAvailable() bool {
	dockerOnce.Do(func() {
		dockerAvail = exec.Command("docker", "info").Run() == nil
	})
	return dockerAvail
}

// isReaperRemovingErr returns true if the error is a transient "removing"
// status from the testcontainers Ryuk reaper. This happens when a previous
// test run's reaper container is still being cleaned up by Docker.
func isReaperRemovingErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unexpected container status") &&
		strings.Contains(err.Error(), "removing")
}

// isLogWaitTimeoutErr returns true if the error is the testcontainers-go
// wait-for-log strategy giving up because the ready line never appeared
// before the deadline. Under concurrent test load Dolt sometimes takes
// longer than the wait deadline to print the ready line, so this error
// is transient and worth retrying.
//
// Format from testcontainers-go/wait/log.go:
//
//	"...some text..." matched 0 times, expected 1
func isLogWaitTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "matched 0 times") &&
		strings.Contains(msg, doltContainerReadyLog)
}

// isTransientStartupErr returns true for errors that are worth retrying
// when starting a Dolt test container.
func isTransientStartupErr(err error) bool {
	return isReaperRemovingErr(err) || isLogWaitTimeoutErr(err)
}

// isDockerUnavailableErr returns true if the error indicates the Docker
// daemon is not reachable from this host (rootless not installed, daemon
// stopped, no host configured). Tests use this to skip rather than fail
// when the environment lacks Docker.
func isDockerUnavailableErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "rootless docker not found") ||
		strings.Contains(msg, "cannot connect to the docker daemon") ||
		strings.Contains(msg, "no docker host") ||
		isRegistryUnreachableErr(msg)
}

// isRegistryUnreachableErr returns true when the Docker daemon is up but
// cannot reach the image registry (e.g. Docker Hub) due to a network-layer
// problem: connection timeouts, DNS failures, or canceled connections. These
// are environment flakes — not test or image regressions — so callers skip
// rather than fail, matching the intent of isDockerUnavailableErr.
//
// We deliberately match only network-reachability signatures, NOT the registry
// host alone: a genuine bad image tag surfaces as "manifest ... not found" and
// MUST keep failing the build. The arg is expected to already be lowercased.
func isRegistryUnreachableErr(msg string) bool {
	return strings.Contains(msg, "client.timeout exceeded while awaiting headers") ||
		strings.Contains(msg, "request canceled while waiting for connection") ||
		strings.Contains(msg, "tls handshake timeout") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "network is unreachable")
}

// runDoltContainer starts a Dolt sql-server container with the local
// gt_test database, recovering from testcontainers panics by converting
// them into "docker unavailable" errors so isDockerUnavailableErr can
// classify them.
//
// We override the dolt module's default 60s wait deadline with a longer
// timeout via WithWaitStrategyAndDeadline so concurrent test runs don't
// flake when the container is slow to come up under load.
func runDoltContainer(ctx context.Context) (ctr *dolt.DoltContainer, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("testcontainers docker unavailable: %v", r)
		}
	}()

	return dolt.Run(ctx, DoltDockerImage,
		dolt.WithDatabase("gt_test"),
		testcontainers.WithEnv(map[string]string{"DOLT_ROOT_HOST": "%"}),
		// Replace the dolt module's default 60s wait deadline.
		// We use WithWaitStrategyAndDeadline (not Additional) so the
		// dolt module's existing wait strategy is REPLACED, not
		// stacked — stacking would force us to wait the maximum of
		// both deadlines and would double-poll the same log line.
		testcontainers.WithWaitStrategyAndDeadline(
			doltContainerStartupTimeout,
			wait.ForLog(doltContainerReadyLog),
		),
	)
}

// runDoltContainerWithRetry calls dolt.Run, retrying on transient errors
// (reaper "removing" status and wait-for-log timeouts) up to 3 times with
// exponential backoff.
func runDoltContainerWithRetry(ctx context.Context) (*dolt.DoltContainer, error) {
	const maxRetries = 3
	delay := 2 * time.Second
	var lastErr error
	for attempt := range maxRetries {
		ctr, err := runDoltContainer(ctx)
		if err == nil {
			return ctr, nil
		}
		lastErr = err
		if !isTransientStartupErr(err) {
			return nil, err
		}
		if attempt < maxRetries-1 {
			time.Sleep(delay)
			delay *= 2
		}
	}
	return nil, lastErr
}

// startSharedDoltContainer starts the shared Dolt container and sets
// GT_DOLT_PORT and BEADS_DOLT_PORT process-wide.
func startSharedDoltContainer() {
	ctx := context.Background()
	ctr, err := runDoltContainerWithRetry(ctx)
	if err != nil {
		doltCtrErr = fmt.Errorf("starting Dolt container: %w", err)
		return
	}

	p, err := ctr.MappedPort(ctx, "3306/tcp")
	if err != nil {
		doltCtrErr = fmt.Errorf("getting mapped port: %w", err)
		_ = testcontainers.TerminateContainer(ctr)
		return
	}

	doltCtr = ctr
	doltCtrPort = p.Port()
	os.Setenv("GT_DOLT_PORT", doltCtrPort)    //nolint:tenv // intentional process-wide env
	os.Setenv("BEADS_DOLT_PORT", doltCtrPort) //nolint:tenv // intentional process-wide env
	os.Setenv("GT_TEST_EXTERNAL_DOLT", "1")   //nolint:tenv // integration tests reuse this container
}

// StartIsolatedDoltContainer starts a per-test Dolt container and returns the
// mapped host port. GT_DOLT_PORT is set via t.Setenv (scoped to the test).
// The container is terminated automatically when the test finishes.
func StartIsolatedDoltContainer(t *testing.T) string {
	t.Helper()
	if !isDockerAvailable() {
		t.Skip("Docker not available, skipping test")
	}

	ctx := context.Background()
	ctr, err := runDoltContainerWithRetry(ctx)
	if err != nil {
		if isDockerUnavailableErr(err) {
			t.Skipf("Dolt container unavailable: %v", err)
		}
		t.Fatalf("starting Dolt container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			t.Logf("terminating Dolt container: %v", err)
		}
	})

	port, err := ctr.MappedPort(ctx, "3306/tcp")
	if err != nil {
		t.Fatalf("getting mapped port: %v", err)
	}

	portStr := port.Port()
	t.Setenv("GT_DOLT_PORT", portStr)
	return portStr
}

// EnsureDoltContainerForTestMain starts a shared Dolt container for use in
// TestMain functions. Call TerminateDoltContainer() after m.Run() to clean up.
// Sets both GT_DOLT_PORT and BEADS_DOLT_PORT process-wide.
func EnsureDoltContainerForTestMain() error {
	if !isDockerAvailable() {
		return fmt.Errorf("Docker not available")
	}

	doltCtrOnce.Do(startSharedDoltContainer)
	return doltCtrErr
}

// RequireDoltContainer ensures a shared Dolt container is running. Skips the
// test if Docker is not available.
func RequireDoltContainer(t *testing.T) {
	t.Helper()
	if !isDockerAvailable() {
		t.Skip("Docker not available, skipping test")
	}

	doltCtrOnce.Do(startSharedDoltContainer)
	if doltCtrErr != nil {
		if isDockerUnavailableErr(doltCtrErr) {
			t.Skipf("Dolt container unavailable: %v", doltCtrErr)
		}
		t.Fatalf("Dolt container setup failed: %v", doltCtrErr)
	}
}

// DoltContainerAddr returns the address (host:port) of the Dolt container.
func DoltContainerAddr() string {
	return "127.0.0.1:" + doltCtrPort
}

// DoltContainerPort returns the mapped host port of the Dolt container.
func DoltContainerPort() string {
	return doltCtrPort
}

// TerminateDoltContainer stops and removes the shared Dolt container.
// Called from TestMain after m.Run().
func TerminateDoltContainer() {
	if doltCtr != nil {
		_ = testcontainers.TerminateContainer(doltCtr)
		doltCtr = nil
	}
}
