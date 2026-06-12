package cmd

import "testing"

func TestExtractBeadIDFromArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"simple", []string{"myproject-abc"}, "myproject-abc"},
		{"with flags after", []string{"gt-abc123", "--json"}, "gt-abc123"},
		{"with flags before", []string{"--json", "hq-xyz"}, "hq-xyz"},
		{"flags only", []string{"--json", "-v"}, ""},
		{"empty", []string{}, ""},
		{"mixed", []string{"-v", "bd-def456", "--json"}, "bd-def456"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractBeadIDFromArgs(tc.args)
			if got != tc.want {
				t.Errorf("extractBeadIDFromArgs(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}
