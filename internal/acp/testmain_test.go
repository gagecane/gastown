package acp

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// Known test-infrastructure leaks to be fixed in follow-up work:
		// - keepalive_test: JSON decoder goroutine blocks on unclosed pipe
		// - proxy_test: io.Copy drainer goroutine blocks on unclosed pipe
		goleak.IgnoreTopFunction("io.(*pipe).read"),
		// - keepalive_test: exec.CommandContext watcher for dummy "sleep" process
		goleak.IgnoreTopFunction("os/exec.(*Cmd).watchCtx"),
		// Dolt's global events collector goroutines. The dolt
		// libraries/events package creates a process-global Collector at
		// package init that unconditionally spawns a sendingThread.run
		// goroutine and a NewCollector.func1 drain goroutine. These are now
		// reachable here because the propeller's stale-reply-reminder gate
		// (gu-fu7mg) imports internal/mail, which pulls in beads/Dolt.
		goleak.IgnoreTopFunction("github.com/dolthub/dolt/go/libraries/events.(*sendingThread).run"),
		goleak.IgnoreTopFunction("github.com/dolthub/dolt/go/libraries/events.NewCollector.func1"),
	)
}
