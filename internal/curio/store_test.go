package curio

import (
	"errors"
	"fmt"
	"testing"
)

// TestIsSessionRootError verifies the transient mid-commit session race
// (gu-iebpz) is recognized, and that unrelated errors are not.
func TestIsSessionRootError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"exact dolt signature",
			errors.New("checking curio_candidate: Error 1105 (HY000): no root value found in session"), true},
		{"wrapped", fmt.Errorf("ensure: %w",
			errors.New("no root value found in session")), true},
		{"unrelated dolt error",
			errors.New("Error 1105 (HY000): table not found: curio_candidate"), false},
		{"connection refused",
			errors.New("dial tcp 127.0.0.1:3307: connect: connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSessionRootError(tc.err); got != tc.want {
				t.Errorf("isSessionRootError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestRetryOnSessionRoot exercises the retry policy: a transient session-root
// error is retried up to the cap and succeeds if a later attempt clears, while
// non-transient errors fail fast with no retry.
func TestRetryOnSessionRoot(t *testing.T) {
	rootErr := errors.New("Error 1105 (HY000): no root value found in session")

	t.Run("succeeds after transient errors", func(t *testing.T) {
		calls, backoffs := 0, 0
		err := retryOnSessionRoot(func() error {
			calls++
			if calls < 3 {
				return rootErr
			}
			return nil
		}, 3, func(int) { backoffs++ })
		if err != nil {
			t.Fatalf("expected success after retries, got %v", err)
		}
		if calls != 3 {
			t.Errorf("calls = %d, want 3", calls)
		}
		if backoffs != 2 {
			t.Errorf("backoffs = %d, want 2 (between the 3 attempts)", backoffs)
		}
	})

	t.Run("exhausts retries and returns last session-root error", func(t *testing.T) {
		calls := 0
		err := retryOnSessionRoot(func() error {
			calls++
			return rootErr
		}, 3, func(int) {})
		if !isSessionRootError(err) {
			t.Fatalf("expected session-root error, got %v", err)
		}
		if calls != 3 {
			t.Errorf("calls = %d, want 3 (full retry budget)", calls)
		}
	})

	t.Run("non-transient error fails fast without retry", func(t *testing.T) {
		other := errors.New("creating curio_candidate: syntax error")
		calls, backoffs := 0, 0
		err := retryOnSessionRoot(func() error {
			calls++
			return other
		}, 3, func(int) { backoffs++ })
		if !errors.Is(err, other) {
			t.Fatalf("expected the original error, got %v", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1 (no retry on non-transient)", calls)
		}
		if backoffs != 0 {
			t.Errorf("backoffs = %d, want 0", backoffs)
		}
	})

	t.Run("immediate success runs once", func(t *testing.T) {
		calls := 0
		if err := retryOnSessionRoot(func() error { calls++; return nil }, 3, func(int) {}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
	})
}
