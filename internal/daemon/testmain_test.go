package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"go.uber.org/goleak"

	"github.com/steveyegge/gastown/internal/testutil"
	"github.com/steveyegge/gastown/internal/tmux"
)

func TestMain(m *testing.M) {
	// Start an ephemeral Dolt container for this package's tests.
	// convoy_manager_test.go calls setupTestStore which sets BEADS_TEST_MODE=1,
	// causing the beads SDK to create testdb_<hash> databases. By routing
	// those to an isolated container (via BEADS_DOLT_PORT), the databases are
	// destroyed when the container is terminated at cleanup —
	// preventing orphan accumulation in the shared production Dolt data dir.
	//
	// When Docker is unavailable, Dolt-needing tests self-skip via
	// setupTestStore → beadsdk.Open failure. Non-Dolt tests (e.g.
	// boot_spawn_frequency_test.go) still run. (fixes gt-kw4449)
	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon TestMain: Dolt container unavailable (%v), Dolt-dependent tests will skip\n", err)
	}

	// Isolate tmux sessions on a package-specific socket.
	// handler_test.go creates tmux.NewTmux() instances that query has-session;
	// polecat_health_test.go uses fake tmux stubs but still constructs Tmux
	// instances. Routing all of these to an isolated socket prevents
	// interference with the user's tmux and other packages' tests.
	var tmuxSocket string
	if _, err := exec.LookPath("tmux"); err == nil {
		tmuxSocket = fmt.Sprintf("gt-test-daemon-%d", os.Getpid())
		tmux.SetDefaultSocket(tmuxSocket)
	}

	code := m.Run()

	if tmuxSocket != "" {
		// intentionally bare — TestMain teardown for the isolated daemon
		// test socket created above. tmux.BuildCommand would target the
		// SetDefaultSocket value, which is exactly this socket — so the
		// effect is equivalent — but using the raw form keeps teardown
		// independent of helper package state.
		_ = exec.Command("tmux", "-L", tmuxSocket, "kill-server").Run()
		socketPath := filepath.Join(tmux.SocketDir(), tmuxSocket)
		_ = os.Remove(socketPath)
	}
	testutil.TerminateDoltContainer()

	if code != 0 {
		os.Exit(code)
	}

	// Verify no goroutine leaks after all tests and cleanup complete.
	//
	// Ignore the testcontainers-go Ryuk Reaper goroutine: when a Dolt
	// container is started, testcontainers-go spawns a Reaper connection
	// goroutine that blocks on its termination signal channel
	// (reaper.go: (*Reaper).connect.func1). This goroutine is owned by the
	// testcontainers-go library and is not deterministically torn down before
	// TestMain's goleak.Find runs, producing a flaky infra-level leak report
	// unrelated to gastown code. (fixes gu-hxer6)
	//
	// Also ignore Dolt's global events collector goroutines. The dolt
	// libraries/events package creates a process-global Collector at package
	// init (var globalCollector = NewCollector(...)), which unconditionally
	// spawns a sendingThread.run goroutine and a NewCollector.func1 drain
	// goroutine. They live for the process lifetime, are never Close()d by the
	// embedded Dolt store, and have no env-var off switch — so goleak reports
	// them whenever a Dolt-backed store is opened. Library-owned, not gastown's
	// to tear down. (gs-8zeq)
	leakOpts := []goleak.Option{
		goleak.IgnoreTopFunction("github.com/testcontainers/testcontainers-go.(*Reaper).connect.func1"),
		goleak.IgnoreTopFunction("github.com/dolthub/dolt/go/libraries/events.(*sendingThread).run"),
		goleak.IgnoreTopFunction("github.com/dolthub/dolt/go/libraries/events.NewCollector.func1"),
	}
	if err := goleak.Find(leakOpts...); err != nil {
		fmt.Fprintf(os.Stderr, "goleak: goroutine leak detected:\n%v\n", err)
		os.Exit(1)
	}

	os.Exit(0)
}
