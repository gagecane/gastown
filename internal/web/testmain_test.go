package web

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// Ignore Dolt's global events collector goroutines. The dolt
	// libraries/events package creates a process-global Collector at package
	// init time (var globalCollector = NewCollector(...)), which unconditionally
	// spawns a sendingThread.run goroutine and a NewCollector.func1 drain
	// goroutine. They live for the process lifetime and are never Close()d by
	// the embedded Dolt store, so goleak.Find reports them whenever a test in
	// this package opens a Dolt-backed store. They are library-owned (analogous
	// to the testcontainers Reaper ignore in internal/daemon) and not something
	// gastown can shut down. (gs-8zeq)
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/dolthub/dolt/go/libraries/events.(*sendingThread).run"),
		goleak.IgnoreTopFunction("github.com/dolthub/dolt/go/libraries/events.NewCollector.func1"),
	)
}
