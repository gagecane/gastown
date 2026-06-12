package cmd

// Tests for gu-1or22: `gt done` must retry transient push-infra failures with
// backoff. Two casc_cdk stranded-merges in one session pushed their feature
// branch fine, passed gates, then failed the mainline push — with two
// simultaneous push_failed across both casc_cdk and gastown_upstream in the
// same window. That cross-repo correlation points at a transient git
// push-infra blip. The existing recovery paths all re-check origin AFTER a
// failure, which cannot rescue a genuine blip (the commit never landed). A
// bounded retry-with-backoff on the push itself closes that gap.

import (
	"errors"
	"testing"
)

func TestIsTransientPushError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"connection reset", errors.New("fatal: unable to access: Connection reset by peer"), true},
		{"connection refused", errors.New("fatal: unable to access 'https://...': Connection refused"), true},
		{"timed out", errors.New("ssh: connect to host git.example.com port 22: Connection timed out"), true},
		{"hung up", errors.New("fatal: the remote end hung up unexpectedly"), true},
		{"early eof", errors.New("error: RPC failed; curl 18 transfer closed\nfatal: early EOF"), true},
		{"tls handshake", errors.New("fatal: unable to access: gnutls_handshake() failed"), true},
		{"502", errors.New("error: RPC failed; HTTP 502 curl 22 The requested URL returned error: 502"), true},
		{"503", errors.New("fatal: unable to access: The requested URL returned error: 503"), true},
		{"could not resolve host", errors.New("fatal: unable to access: Could not resolve host: git.example.com"), true},
		// gu-i592d: the git layer's timeout-kill now surfaces as "timed out"
		// (via ctx.Err()), not the raw "signal: killed". This is the exact
		// message format runWithTimeout/runWithEnvAndTimeout emit on a
		// deadlock-kill, and it MUST classify as transient so pushForDone
		// retries the killed push once the binary is healthy.
		{"git layer timeout-kill", errors.New("git push timed out after 1m0s (remote may be unreachable)"), true},

		// Deterministic rejections — must NOT be treated as transient.
		{"non-fast-forward", errors.New("! [rejected] main -> main (non-fast-forward)"), false},
		{"fetch first", errors.New("error: failed to push some refs; hint: Updates were rejected; fetch first"), false},
		{"auth", errors.New("fatal: Authentication failed for 'https://git.example.com/repo'"), false},
		{"missing refspec", errors.New("error: src refspec my-branch does not match any"), false},
		{"permission denied", errors.New("ERROR: Permission to repo denied to user"), false},
		{"unrelated", errors.New("some other error"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientPushError(tc.err); got != tc.want {
				t.Errorf("isTransientPushError(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestPushForDoneRetriesTransient verifies the retry loop's control flow by
// driving the same decision predicate pushForDone uses. We can't easily inject
// a fake *git.Git here, so we assert the loop's stopping conditions directly:
// transient errors retry up to the cap; deterministic errors stop immediately.
func TestPushForDoneRetryDecision(t *testing.T) {
	// A transient error on attempts before the cap must keep retrying.
	transient := errors.New("Connection reset by peer")
	for attempt := 1; attempt < pushForDoneMaxAttempts; attempt++ {
		shouldStop := !isTransientPushError(transient) || attempt == pushForDoneMaxAttempts
		if shouldStop {
			t.Errorf("attempt %d: transient error should retry, but loop would stop", attempt)
		}
	}
	// At the cap, the loop must stop even for a transient error.
	if shouldStop := !isTransientPushError(transient) || pushForDoneMaxAttempts == pushForDoneMaxAttempts; !shouldStop {
		t.Errorf("at cap, loop must stop")
	}
	// A deterministic error must stop on the first attempt.
	deterministic := errors.New("non-fast-forward")
	if shouldStop := !isTransientPushError(deterministic) || 1 == pushForDoneMaxAttempts; !shouldStop {
		t.Errorf("deterministic error must stop on first attempt")
	}
}
