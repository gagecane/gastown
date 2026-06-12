package beads

import (
	"net"
	"os"
	"regexp"
	"strings"
	"time"
)

// This file handles the transient Dolt connection failures that surface when a
// single gt Dolt server (port 3307) is up but momentarily refuses or times out
// a TCP dial under contention (heavy concurrent bd/test load). Two problems are
// addressed (gu-vsts5):
//
//  1. A transient dial blip surfaced as a hard error even though the server was
//     up the whole time. We now retry dial-establishment failures a few times
//     before giving up — see DoltConnRetryAttempts / IsTransientDoltDialError.
//
//  2. bd's own error text tells the operator to run `bd dolt start`, i.e. to
//     start a SECOND server, which risks split-brain when gt already owns a
//     running one. RewriteDoltUnreachableMessage replaces that advice with
//     guidance that distinguishes "transient — retry" from "genuinely down".

// DoltConnRetryAttempts is the number of times a bd subprocess is retried when
// it fails with a transient Dolt connection-establishment error. Kept small:
// the failure mode is a momentary dial refusal/timeout under contention, which
// clears in well under a second. Total added latency in the worst (still
// failing) case is bounded by DoltConnRetryAttempts × DoltConnRetryBackoff.
const DoltConnRetryAttempts = 3

// DoltConnRetryBackoff is the base sleep between transient-dial retries. Each
// retry sleeps backoff×attempt (linear), matching the convoy dep-query retry.
const DoltConnRetryBackoff = 200 * time.Millisecond

// IsTransientDoltDialError reports whether a bd stderr message indicates a
// transient TCP connection-ESTABLISHMENT failure to the Dolt server — i.e. the
// socket was never opened, so the command did no server-side work and is safe
// to retry verbatim (idempotent for both reads and writes).
//
// It deliberately matches ONLY dial-level signatures. Mid-query failures
// (e.g. "connection reset by peer", "broken pipe", "EOF" after a write began)
// are excluded because retrying them could double-apply a mutation. Lock/
// serialization contention is handled separately by bd/doltserver's own
// SQL-level retry (isDoltRetryableError); this layer covers only the case bd
// surfaces as a flat "unreachable" before any query runs.
func IsTransientDoltDialError(msg string) bool {
	if msg == "" {
		return false
	}
	lower := strings.ToLower(msg)
	for _, marker := range []string{
		"dolt server unreachable",
		"connect to dolt server",
		"dial tcp",
		"connection refused",
		"i/o timeout",
		"no route to host",
		"network is unreachable",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// unreachableAddrRe extracts the host:port that bd reported as unreachable,
// e.g. "...unreachable at 127.0.0.1:3307: dial tcp...". Used to probe whether
// the server is actually up before rewriting the operator-facing advice.
var unreachableAddrRe = regexp.MustCompile(`unreachable at ([0-9a-zA-Z._-]+:[0-9]+)`)

// RewriteDoltUnreachableMessage replaces bd's misleading "the server may not be
// running — start it with `bd dolt start`" advice with gt-appropriate guidance.
//
// In a Gas Town, gt owns a single shared Dolt server. bd's advice to start a
// second server risks split-brain (a rig-local shadow server squatting on a
// different database). When the error is the transient/unreachable class, this
// rewrite:
//
//   - probes the configured address; if it is reachable NOW, the failure was a
//     transient blip and the message says so (retry; do NOT start a server);
//   - if it is genuinely unreachable, points at the gt-managed lifecycle
//     (`gt dolt status` then `gt dolt start`), never `bd dolt start`.
//
// Non-connection errors (and messages that don't carry the misleading advice)
// are returned unchanged.
func RewriteDoltUnreachableMessage(msg string) string {
	if msg == "" || !IsTransientDoltDialError(msg) {
		return msg
	}
	// Only rewrite when bd actually emitted the start-a-server advice; otherwise
	// leave the diagnostic untouched so we don't mask unrelated detail.
	lower := strings.ToLower(msg)
	if !strings.Contains(lower, "bd dolt start") &&
		!strings.Contains(lower, "auto-start") &&
		!strings.Contains(lower, "may not be running") {
		return msg
	}

	addr := doltAddrFromMessage(msg)
	reachable := addr != "" && doltAddrReachable(addr)

	var b strings.Builder
	// Preserve the original diagnostic line (it carries the dial error detail),
	// but strip bd's trailing "start a server" advice block.
	b.WriteString(strings.TrimSpace(stripBdStartAdvice(msg)))
	b.WriteString("\n\n")
	if reachable {
		b.WriteString("NOTE (gt): the Dolt server at ")
		b.WriteString(addr)
		b.WriteString(" IS reachable now — this was a transient connection blip under contention, not an outage.\n")
		b.WriteString("Retry the command. Do NOT run 'bd dolt start' / 'gt dolt start': a server is already running, and starting a second one risks split-brain.")
	} else {
		b.WriteString("NOTE (gt): gt owns a single shared Dolt server. Do NOT run 'bd dolt start' (it can start a rig-local shadow server and cause split-brain).\n")
		b.WriteString("First check whether the server is actually up:\n  gt dolt status\nIf — and only if — it is genuinely down, start the gt-managed server:\n  gt dolt start")
	}
	return b.String()
}

// stripBdStartAdvice removes bd's trailing advice lines that tell the operator
// to start a server, so the rewritten message carries gt's guidance instead of
// contradicting it.
func stripBdStartAdvice(msg string) string {
	lines := strings.Split(msg, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		l := strings.ToLower(line)
		if strings.Contains(l, "bd dolt start") ||
			strings.Contains(l, "start the server manually") ||
			strings.Contains(l, "start a local server") ||
			strings.Contains(l, "to start manually") ||
			strings.Contains(l, "auto-start is disabled") ||
			strings.Contains(l, "enable auto-start") ||
			strings.Contains(l, "may not be running") ||
			strings.Contains(l, "try:") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimRight(strings.Join(kept, "\n"), "\n ")
}

// doltAddrFromMessage extracts the host:port bd reported, falling back to the
// gt-configured address from the environment, then the default.
func doltAddrFromMessage(msg string) string {
	if m := unreachableAddrRe.FindStringSubmatch(msg); len(m) == 2 {
		return m[1]
	}
	host := os.Getenv("GT_DOLT_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port := os.Getenv("GT_DOLT_PORT")
	if port == "" {
		port = os.Getenv("BEADS_DOLT_PORT")
	}
	if port == "" {
		port = "3307"
	}
	return host + ":" + port
}

// doltAddrReachable returns true if a TCP connection to addr can be established
// within a short timeout. Used to distinguish a transient blip (server up) from
// a genuine outage (server down) when rewriting operator guidance.
func doltAddrReachable(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
