package doltserver

import (
	"os"
	"testing"
)

// envValue returns the value of key from an environment slice (KEY=VALUE
// entries), preferring the last occurrence to match exec semantics. The bool
// result reports whether the key was present.
func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	value := ""
	found := false
	for _, entry := range env {
		if len(entry) >= len(prefix) && entry[:len(prefix)] == prefix {
			value = entry[len(prefix):]
			found = true
		}
	}
	return value, found
}

// TestServerEnv_AppliesDefaults verifies that ServerEnv injects Gas Town's
// Go runtime memory tuning when the operator has not set the vars. (gu-iozi6)
func TestServerEnv_AppliesDefaults(t *testing.T) {
	unsetForTest(t, "GOMEMLIMIT")
	unsetForTest(t, "GOGC")

	env := ServerEnv()

	if got, ok := envValue(env, "GOMEMLIMIT"); !ok || got != "16GiB" {
		t.Errorf("GOMEMLIMIT = %q (present=%v), want %q", got, ok, "16GiB")
	}
	if got, ok := envValue(env, "GOGC"); !ok || got != "50" {
		t.Errorf("GOGC = %q (present=%v), want %q", got, ok, "50")
	}
}

// TestServerEnv_RespectsOperatorOverride verifies that operator-chosen values
// are not overwritten by the defaults. (gu-iozi6)
func TestServerEnv_RespectsOperatorOverride(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "24GiB")
	t.Setenv("GOGC", "75")

	env := ServerEnv()

	if got, _ := envValue(env, "GOMEMLIMIT"); got != "24GiB" {
		t.Errorf("GOMEMLIMIT = %q, want operator value %q", got, "24GiB")
	}
	if got, _ := envValue(env, "GOGC"); got != "75" {
		t.Errorf("GOGC = %q, want operator value %q", got, "75")
	}
}

// unsetForTest unsets an environment variable and restores its original state
// at test cleanup. t.Setenv cannot represent an absent variable (setting "" is
// still "present"), so we manage the variable directly while registering
// cleanup ourselves.
func unsetForTest(t *testing.T, key string) {
	t.Helper()
	orig, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unsetting %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, orig)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}
