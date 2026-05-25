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
	)
}
