package cmd

import "testing"

// TestIsTransientDepQueryErr verifies the classifier that decides whether a
// failed bd sql dependency query should be retried (transient Dolt contention)
// or surfaced immediately (deterministic error). Retrying transient errors is
// what keeps the raw dep-read path from silently degrading to the cross-DB-lossy
// bd dep list / bd show fallbacks, which dropped tracked issues / epic children
// and surfaced as flaky scheduler_integration_test.go failures (gc-ihbkn).
func TestIsTransientDepQueryErr(t *testing.T) {
	transient := []string{
		"query error: Error 1205 (HY000): lock wait timeout exceeded",
		"serialization failure, try restarting transaction",
		"optimistic lock failed",
		"cannot update manifest",
		"database is read only",
		"deadlock found when trying to get lock",
		"dial tcp 127.0.0.1:3307: connection refused",
		"read tcp: i/o timeout",
		"Dolt read timed out (degraded)",
		"driver: bad connection",
		"write: broken pipe",
		"unexpected EOF",
	}
	for _, msg := range transient {
		if !isTransientDepQueryErr(msg) {
			t.Errorf("expected transient=true for %q", msg)
		}
	}

	deterministic := []string{
		"query error: Error 1146 (HY000): table not found: dependencies",
		"query error: Error 1054: Unknown column 'depends_on_id' in 'field list'",
		"syntax error near 'SELCT'",
		"invalid bead ID",
		"",
	}
	for _, msg := range deterministic {
		if isTransientDepQueryErr(msg) {
			t.Errorf("expected transient=false for %q", msg)
		}
	}
}
