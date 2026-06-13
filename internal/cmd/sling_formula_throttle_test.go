package cmd

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// stubThrottleSleep replaces the real backoff sleep with a no-op for the
// duration of a test so retry loops run instantly.
func stubThrottleSleep(t *testing.T) {
	t.Helper()
	prev := throttleRetrySleep
	throttleRetrySleep = func(time.Duration) {}
	t.Cleanup(func() { throttleRetrySleep = prev })
}

// TestListWithThrottleRetry_RetriesTransientThrottle verifies that a transient
// read-throttle timeout is retried and eventually succeeds (gu-dawnk).
func TestListWithThrottleRetry_RetriesTransientThrottle(t *testing.T) {
	stubThrottleSleep(t)

	want := []*beads.Issue{{ID: "gu-test"}}
	calls := 0
	got, err := listWithThrottleRetry(func() ([]*beads.Issue, error) {
		calls++
		if calls < 3 {
			// Wrap to mimic the production chain (run wraps with %w).
			return nil, fmt.Errorf("bd list: %w", beads.ErrReadThrottleTimeout)
		}
		return want, nil
	})
	if err != nil {
		t.Fatalf("expected success after retries, got error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls)
	}
	if len(got) != 1 || got[0].ID != "gu-test" {
		t.Fatalf("unexpected result: %+v", got)
	}
}

// TestListWithThrottleRetry_ExhaustsRetries verifies that a persistent throttle
// timeout is retried the bounded number of times and then surfaced.
func TestListWithThrottleRetry_ExhaustsRetries(t *testing.T) {
	stubThrottleSleep(t)

	calls := 0
	_, err := listWithThrottleRetry(func() ([]*beads.Issue, error) {
		calls++
		return nil, fmt.Errorf("bd list: %w", beads.ErrReadThrottleTimeout)
	})
	if !errors.Is(err, beads.ErrReadThrottleTimeout) {
		t.Fatalf("expected ErrReadThrottleTimeout, got %v", err)
	}
	// Initial attempt + singletonThrottleRetries retries.
	wantCalls := singletonThrottleRetries + 1
	if calls != wantCalls {
		t.Fatalf("expected %d attempts, got %d", wantCalls, calls)
	}
}

// TestListWithThrottleRetry_NonThrottleFailsFast verifies that a non-throttle
// error is NOT retried — real failures must surface on the first occurrence.
func TestListWithThrottleRetry_NonThrottleFailsFast(t *testing.T) {
	stubThrottleSleep(t)

	sentinel := errors.New("database not found")
	calls := 0
	_, err := listWithThrottleRetry(func() ([]*beads.Issue, error) {
		calls++
		return nil, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 attempt (no retry), got %d", calls)
	}
}

// TestListWithThrottleRetry_SuccessFirstTry verifies the no-error fast path
// does not retry or sleep.
func TestListWithThrottleRetry_SuccessFirstTry(t *testing.T) {
	stubThrottleSleep(t)

	calls := 0
	_, err := listWithThrottleRetry(func() ([]*beads.Issue, error) {
		calls++
		return nil, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 attempt, got %d", calls)
	}
}
