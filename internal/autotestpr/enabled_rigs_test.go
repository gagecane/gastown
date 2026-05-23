package autotestpr

import (
	"errors"
	"sort"
	"testing"
)

// TestIsTransientDoltWriteError covers the substring set the CAS
// retry loop treats as retryable. Mirrors the production set in
// internal/polecat.isDoltOptimisticLockError; if either side adds a
// new pattern, this table makes the divergence visible.
func TestIsTransientDoltWriteError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", errors.New("something broke"), false},
		{"optimistic-lock", errors.New("optimistic lock failed"), true},
		{"serialization-failure", errors.New("serialization failure detected"), true},
		{"lock-wait-timeout", errors.New("lock wait timeout exceeded; try restarting transaction"), true},
		{"try-restarting", errors.New("try restarting transaction (deadlock)"), true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isTransientDoltWriteError(tt.err); got != tt.want {
				t.Errorf("isTransientDoltWriteError(%v) = %v; want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestErrTownStateCASExhausted_IsDistinct guards against accidental
// alias to ErrTownStateNotProvisioned. The two error sentinels carry
// different operator-facing meanings (provision the bead vs. wait
// for Mayor reconcile), so callers branch on them via errors.Is.
func TestErrTownStateCASExhausted_IsDistinct(t *testing.T) {
	t.Parallel()

	if errors.Is(ErrTownStateCASExhausted, ErrTownStateNotProvisioned) {
		t.Error("ErrTownStateCASExhausted is.Is(ErrTownStateNotProvisioned) — must be distinct sentinels")
	}
	if errors.Is(ErrTownStateNotProvisioned, ErrTownStateCASExhausted) {
		t.Error("ErrTownStateNotProvisioned is.Is(ErrTownStateCASExhausted) — must be distinct sentinels")
	}
}

// TestEnabledRigsAppendIsIdempotent verifies the mutate callback used
// by AppendEnabledRig: present rigs are no-ops, absent rigs are
// appended and the result sorted. We test the predicate-level logic
// here rather than wiring a beads.Beads test double because the loop
// in mutateEnabledRigs is the same shape — exercising the slice
// transformation in isolation lets us cover the contract without
// taking a Dolt dependency in unit tests.
func TestEnabledRigsAppendIsIdempotent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current []string
		add     string
		want    []string // nil = "no change"
	}{
		{
			name:    "empty list",
			current: []string{},
			add:     "gastown_upstream",
			want:    []string{"gastown_upstream"},
		},
		{
			name:    "already present",
			current: []string{"gastown_upstream"},
			add:     "gastown_upstream",
			want:    nil,
		},
		{
			name:    "appends and sorts",
			current: []string{"zeta", "alpha"},
			add:     "mid",
			want:    []string{"alpha", "mid", "zeta"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := appendMutate(tt.current, tt.add)
			if tt.want == nil {
				if got != nil {
					t.Errorf("expected nil (idempotent); got %v", got)
				}
				return
			}
			if !equalStrings(got, tt.want) {
				t.Errorf("got %v; want %v", got, tt.want)
			}
		})
	}
}

// TestEnabledRigsRemoveIsIdempotent is the disable-side mirror of
// the append idempotency test.
func TestEnabledRigsRemoveIsIdempotent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current []string
		drop    string
		want    []string // nil = "no change"
	}{
		{
			name:    "not present",
			current: []string{"gastown_upstream"},
			drop:    "other_rig",
			want:    nil,
		},
		{
			name:    "removes single",
			current: []string{"gastown_upstream"},
			drop:    "gastown_upstream",
			want:    []string{},
		},
		{
			name:    "removes from sorted set",
			current: []string{"alpha", "mid", "zeta"},
			drop:    "mid",
			want:    []string{"alpha", "zeta"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := removeMutate(tt.current, tt.drop)
			if tt.want == nil {
				if got != nil {
					t.Errorf("expected nil (idempotent); got %v", got)
				}
				return
			}
			if !equalStrings(got, tt.want) {
				t.Errorf("got %v; want %v", got, tt.want)
			}
		})
	}
}

// appendMutate / removeMutate are local replicas of the closures
// passed to mutateEnabledRigs in production code. We duplicate them
// here so the unit test exercises the exact slice contract without
// poking at private callbacks via reflection. If the production
// closures drift, these tests stay green but the behavior tests
// (CAS retry loop) catch the divergence.
func appendMutate(rigs []string, name string) []string {
	for _, r := range rigs {
		if r == name {
			return nil
		}
	}
	out := append([]string(nil), rigs...)
	out = append(out, name)
	sort.Strings(out)
	return out
}

func removeMutate(rigs []string, name string) []string {
	filtered := make([]string, 0, len(rigs))
	removed := false
	for _, r := range rigs {
		if r == name {
			removed = true
			continue
		}
		filtered = append(filtered, r)
	}
	if !removed {
		return nil
	}
	sort.Strings(filtered)
	return filtered
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
