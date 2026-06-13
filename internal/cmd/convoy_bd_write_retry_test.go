package cmd

import "testing"

// TestIsRetryableBdWriteErr is the regression guard for hq-jpnhp: the convoy
// lifecycle bd writes (ship-unverified label add/remove, convoy close) must
// retry on Dolt write contention AND on the BdCommandTimeout SIGKILL, otherwise
// a single 30s timeout abandons the write and the convoy stays OPEN forever.
func TestIsRetryableBdWriteErr(t *testing.T) {
	retryable := []string{
		// The exact shape wrapCommandError produces on a context-deadline SIGKILL,
		// as seen in daemon.log for the stuck convoys.
		"bd update ... (3 args) cwd=/home/sika/gt timed out after 30s: signal: killed",
		"bd close ... timed out after 30s: signal: killed",
		// Transient Dolt-contention conditions shared with the dep-query classifier.
		"Error 1205: lock wait timeout exceeded",
		"serialization failure, try restarting transaction",
		"connection refused",
		"unexpected EOF",
	}
	for _, msg := range retryable {
		if !isRetryableBdWriteErr(msg) {
			t.Errorf("isRetryableBdWriteErr(%q) = false, want true", msg)
		}
	}

	nonRetryable := []string{
		// Deterministic errors must fail fast — retrying wastes the budget and
		// never succeeds.
		"Error: unknown flag: --add-labl",
		"no issue found matching \"hq-cv-nope\"",
		"",
	}
	for _, msg := range nonRetryable {
		if isRetryableBdWriteErr(msg) {
			t.Errorf("isRetryableBdWriteErr(%q) = true, want false", msg)
		}
	}
}
