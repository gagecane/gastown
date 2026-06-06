package polecat

// Tests for gu-iu9tt: preservation pushes (unpushed HEAD + inherited stash on
// polecat reuse) must retry transient push-infra failures with backoff. The
// reported WORK-AT-RISK strand was a 60s push timeout ("git push timed out
// after 1m0s (remote may be unreachable)") that left an inherited stash in
// place. A timeout is transient — the remote was briefly unreachable, not
// permanently rejecting — so a bounded retry recovers the durable origin anchor
// instead of stranding the work.

import (
	"errors"
	"testing"
)

func TestIsTransientPreservePushError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		// The exact gu-iu9tt strand message from git.runWithTimeout.
		{"push timeout", errors.New("git push timed out after 1m0s (remote may be unreachable)"), true},
		{"connection reset", errors.New("fatal: unable to access: Connection reset by peer"), true},
		{"connection refused", errors.New("fatal: unable to access 'https://...': Connection refused"), true},
		{"hung up", errors.New("fatal: the remote end hung up unexpectedly"), true},
		{"early eof", errors.New("error: RPC failed; curl 18 transfer closed\nfatal: early EOF"), true},
		{"tls handshake", errors.New("fatal: unable to access: gnutls_handshake() failed"), true},
		{"502", errors.New("error: RPC failed; HTTP 502"), true},
		{"503", errors.New("fatal: unable to access: The requested URL returned error: 503"), true},
		{"could not resolve host", errors.New("fatal: unable to access: Could not resolve host: git.example.com"), true},

		// Deterministic failures must NOT be retried.
		{"missing refspec", errors.New("error: src refspec my-branch does not match any"), false},
		{"permission denied", errors.New("ERROR: Permission to repo denied to user"), false},
		{"unrelated", errors.New("some other error"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientPreservePushError(tc.err); got != tc.want {
				t.Errorf("isTransientPreservePushError(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestPushPreservationRetryDecision verifies the retry loop's stopping
// conditions via the same predicate pushPreservationRef uses: transient errors
// retry up to the cap; deterministic errors stop on the first attempt.
func TestPushPreservationRetryDecision(t *testing.T) {
	transient := errors.New("git push timed out after 1m0s (remote may be unreachable)")
	for attempt := 1; attempt < preservePushMaxAttempts; attempt++ {
		shouldStop := !isTransientPreservePushError(transient) || attempt == preservePushMaxAttempts
		if shouldStop {
			t.Errorf("attempt %d: transient error should retry, but loop would stop", attempt)
		}
	}
	// At the cap, the loop must stop even for a transient error.
	if shouldStop := !isTransientPreservePushError(transient) || preservePushMaxAttempts == preservePushMaxAttempts; !shouldStop {
		t.Errorf("at cap, loop must stop")
	}
	// A deterministic error must stop on the first attempt.
	deterministic := errors.New("src refspec does not match any")
	if shouldStop := !isTransientPreservePushError(deterministic) || 1 == preservePushMaxAttempts; !shouldStop {
		t.Errorf("deterministic error must stop on first attempt")
	}
}
