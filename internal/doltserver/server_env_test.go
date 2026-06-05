package doltserver

import (
	"strings"
	"testing"
)

// envValue returns the value of the last assignment for key in environ, and
// whether key was present. Matching the last entry mirrors how the OS resolves
// duplicate keys (later assignments win).
func envValue(environ []string, key string) (string, bool) {
	prefix := key + "="
	val := ""
	found := false
	for _, e := range environ {
		if strings.HasPrefix(e, prefix) {
			val = strings.TrimPrefix(e, prefix)
			found = true
		}
	}
	return val, found
}

// TestBuildServerEnv_AppliesDefaultsWhenUnset locks in that the daemon applies
// the GOMEMLIMIT/GOGC memory caps when the operator has not set them. This is
// the regression fix from gu-iozi6/gu-j4cmo (commit a8735cae).
func TestBuildServerEnv_AppliesDefaultsWhenUnset(t *testing.T) {
	base := []string{"PATH=/usr/bin", "HOME=/home/test"}

	env := buildServerEnv(base)

	if got, ok := envValue(env, "GOMEMLIMIT"); !ok || got != "16GiB" {
		t.Errorf("GOMEMLIMIT = %q (present=%v), want %q", got, ok, "16GiB")
	}
	if got, ok := envValue(env, "GOGC"); !ok || got != "50" {
		t.Errorf("GOGC = %q (present=%v), want %q", got, ok, "50")
	}

	// Base entries must be preserved.
	if got, ok := envValue(env, "PATH"); !ok || got != "/usr/bin" {
		t.Errorf("PATH = %q (present=%v), want %q", got, ok, "/usr/bin")
	}
}

// TestBuildServerEnv_RespectsOperatorOverride verifies that an operator-supplied
// GOMEMLIMIT/GOGC is preserved and not overwritten by the defaults, so values
// can be tuned via the environment without editing source.
func TestBuildServerEnv_RespectsOperatorOverride(t *testing.T) {
	base := []string{"GOMEMLIMIT=24GiB", "GOGC=80"}

	env := buildServerEnv(base)

	if got, _ := envValue(env, "GOMEMLIMIT"); got != "24GiB" {
		t.Errorf("GOMEMLIMIT = %q, want operator override %q", got, "24GiB")
	}
	if got, _ := envValue(env, "GOGC"); got != "80" {
		t.Errorf("GOGC = %q, want operator override %q", got, "80")
	}

	// The defaults must NOT have been appended on top of the override.
	if n := countEnvKey(env, "GOMEMLIMIT"); n != 1 {
		t.Errorf("GOMEMLIMIT appears %d times, want 1 (default must not be appended over override)", n)
	}
	if n := countEnvKey(env, "GOGC"); n != 1 {
		t.Errorf("GOGC appears %d times, want 1 (default must not be appended over override)", n)
	}
}

// TestBuildServerEnv_MixedOverride verifies each variable is handled
// independently: an override for one does not suppress the default for the other.
func TestBuildServerEnv_MixedOverride(t *testing.T) {
	base := []string{"GOMEMLIMIT=8GiB"}

	env := buildServerEnv(base)

	if got, _ := envValue(env, "GOMEMLIMIT"); got != "8GiB" {
		t.Errorf("GOMEMLIMIT = %q, want operator override %q", got, "8GiB")
	}
	if got, ok := envValue(env, "GOGC"); !ok || got != "50" {
		t.Errorf("GOGC = %q (present=%v), want default %q", got, ok, "50")
	}
}

// TestBuildServerEnv_DoesNotMutateBase ensures the caller's base slice is not
// modified in place.
func TestBuildServerEnv_DoesNotMutateBase(t *testing.T) {
	base := []string{"PATH=/usr/bin"}

	_ = buildServerEnv(base)

	if len(base) != 1 || base[0] != "PATH=/usr/bin" {
		t.Errorf("base slice was mutated: %v", base)
	}
}

func countEnvKey(environ []string, key string) int {
	prefix := key + "="
	n := 0
	for _, e := range environ {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}
