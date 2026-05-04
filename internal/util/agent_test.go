package util

import (
	"os"
	"testing"
)

func TestIsAgentContext(t *testing.T) {
	// Save and restore the env var to avoid leaking state across tests.
	orig, hadOrig := os.LookupEnv("GT_ROLE")
	t.Cleanup(func() {
		if hadOrig {
			_ = os.Setenv("GT_ROLE", orig)
		} else {
			_ = os.Unsetenv("GT_ROLE")
		}
	})

	cases := []struct {
		name  string
		value string
		set   bool
		want  bool
	}{
		{name: "unset", set: false, want: false},
		{name: "empty string", set: true, value: "", want: false},
		{name: "polecat role", set: true, value: "polecat", want: true},
		{name: "rig-qualified polecat", set: true, value: "gastown/polecats/toast", want: true},
		{name: "refinery role", set: true, value: "refinery", want: true},
		{name: "witness role", set: true, value: "witness", want: true},
		{name: "mayor role", set: true, value: "mayor", want: true},
		{name: "crew role", set: true, value: "crew", want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				if err := os.Setenv("GT_ROLE", tc.value); err != nil {
					t.Fatalf("setenv: %v", err)
				}
			} else {
				if err := os.Unsetenv("GT_ROLE"); err != nil {
					t.Fatalf("unsetenv: %v", err)
				}
			}
			if got := IsAgentContext(); got != tc.want {
				t.Errorf("IsAgentContext() with GT_ROLE=%q (set=%v) = %v, want %v",
					tc.value, tc.set, got, tc.want)
			}
		})
	}
}
