package beads

import (
	"fmt"
	"net"
	"strings"
	"testing"
)

func TestIsTransientDoltDialError(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		{"empty", "", false},
		{"unreachable", "Dolt server unreachable at 127.0.0.1:3307: dial tcp ...", true},
		{"connect to dolt", "connect to dolt server for clone: ...", true},
		{"dial tcp", "dial tcp 127.0.0.1:3307: connect: connection refused", true},
		{"connection refused", "connection refused", true},
		{"i/o timeout", "read tcp 127.0.0.1:3307: i/o timeout", true},
		{"no route to host", "dial tcp: no route to host", true},
		{"network unreachable", "network is unreachable", true},
		// Deterministic / non-dial errors must NOT be treated as transient.
		{"not found", "Issue not found: gt-xyz", false},
		{"syntax error", "syntax error near 'SELCT'", false},
		{"lock contention", "lock wait timeout exceeded", false},
		// Mid-query failures are deliberately excluded (could double-apply writes).
		{"broken pipe", "write tcp: broken pipe", false},
		{"connection reset", "read: connection reset by peer", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTransientDoltDialError(tt.msg); got != tt.want {
				t.Errorf("IsTransientDoltDialError(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

func TestRewriteDoltUnreachableMessage_NonConnectionPassthrough(t *testing.T) {
	msg := "Issue not found: gt-xyz"
	if got := RewriteDoltUnreachableMessage(msg); got != msg {
		t.Errorf("non-connection error should pass through unchanged; got %q", got)
	}
}

func TestRewriteDoltUnreachableMessage_NoAdvicePassthrough(t *testing.T) {
	// A dial-class error that does NOT carry bd's start-a-server advice should
	// be left untouched so we don't strip unrelated diagnostic detail.
	msg := "dial tcp 127.0.0.1:3307: i/o timeout"
	if got := RewriteDoltUnreachableMessage(msg); got != msg {
		t.Errorf("error without start advice should pass through; got %q", got)
	}
}

// TestRewriteDoltUnreachableMessage_GenuinelyDown verifies AC2: when the server
// is genuinely unreachable, the rewrite never tells the operator to run
// 'bd dolt start' and instead points at the gt-managed lifecycle.
func TestRewriteDoltUnreachableMessage_GenuinelyDown(t *testing.T) {
	// Use a port that is (almost certainly) not listening.
	down := pickClosedAddr(t)
	msg := fmt.Sprintf("Dolt server unreachable at %s: dial tcp %s: connect: connection refused\n\n"+
		"The Dolt server may not be running. Try:\n  bd dolt start", down, down)

	got := RewriteDoltUnreachableMessage(msg)

	// bd's advisory "Try:\n  bd dolt start" block must be stripped. The phrase
	// may still appear inside an explicit "Do NOT run 'bd dolt start'" warning.
	if strings.Contains(got, "Try:") {
		t.Errorf("rewrite must strip bd's 'Try:' start-a-server advice; got:\n%s", got)
	}
	if !strings.Contains(got, "Do NOT run 'bd dolt start'") {
		t.Errorf("rewrite should explicitly warn against 'bd dolt start'; got:\n%s", got)
	}
	if !strings.Contains(got, "gt dolt status") {
		t.Errorf("rewrite should point at 'gt dolt status'; got:\n%s", got)
	}
	if !strings.Contains(got, "split-brain") {
		t.Errorf("rewrite should warn about split-brain; got:\n%s", got)
	}
}

// TestRewriteDoltUnreachableMessage_TransientBlip verifies AC1/AC2: when the
// address IS reachable now, the rewrite says "transient blip — retry" and does
// not advise starting any server.
func TestRewriteDoltUnreachableMessage_TransientBlip(t *testing.T) {
	// Stand up a listener so the configured address is reachable.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	msg := fmt.Sprintf("Dolt server unreachable at %s: dial tcp %s: connect: connection refused\n\n"+
		"The Dolt server may not be running. Try:\n  bd dolt start", addr, addr)

	got := RewriteDoltUnreachableMessage(msg)

	// bd's "Try:\n  bd dolt start" advisory block must be stripped; the message
	// instead tells the operator to retry (server is up).
	if strings.Contains(got, "Try:") {
		t.Errorf("transient-blip rewrite must strip bd's start-a-server advice; got:\n%s", got)
	}
	if !strings.Contains(got, "Retry the command") {
		t.Errorf("transient-blip rewrite should tell the operator to retry; got:\n%s", got)
	}
	if !strings.Contains(strings.ToLower(got), "transient") {
		t.Errorf("transient-blip rewrite should call it transient; got:\n%s", got)
	}
	if !strings.Contains(got, "IS reachable now") {
		t.Errorf("transient-blip rewrite should note the server is reachable now; got:\n%s", got)
	}
}

// pickClosedAddr returns a 127.0.0.1:port that is not currently listening.
func pickClosedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // close so the port is free (not listening)
	return addr
}
