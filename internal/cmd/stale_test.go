package cmd

import "testing"

// TestCompareDaemonCommit covers the running-daemon-vs-on-disk-binary staleness
// signal (gu-qx6rn): the daemon records its build commit at startup, and
// `gt stale` compares it to the on-disk binary's commit to detect an
// upgraded-but-not-restarted daemon running stale in-memory code.
func TestCompareDaemonCommit(t *testing.T) {
	const full = "abc1234def567890abc1234def567890abc12345"
	const short = "abc1234def56"

	tests := []struct {
		name             string
		daemonCommit     string
		binaryCommit     string
		wantStale        bool
		wantNeedsRestart bool
	}{
		{
			name:         "matching full commits — fresh",
			daemonCommit: full,
			binaryCommit: full,
		},
		{
			name:         "short prefix of full — fresh",
			daemonCommit: short,
			binaryCommit: full,
		},
		{
			name:             "diverged commits — stale, needs restart",
			daemonCommit:     "1111111aaaa",
			binaryCommit:     "2222222bbbb",
			wantStale:        true,
			wantNeedsRestart: true,
		},
		{
			name:         "daemon commit unknown (pre-upgrade daemon) — cannot compare",
			daemonCommit: "",
			binaryCommit: full,
		},
		{
			name:         "binary commit unknown (dev build) — cannot compare",
			daemonCommit: full,
			binaryCommit: "",
		},
		{
			name:         "both unknown — cannot compare",
			daemonCommit: "",
			binaryCommit: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareDaemonCommit(tt.daemonCommit, tt.binaryCommit)

			// Reaching this helper implies the daemon is running.
			if !got.running {
				t.Errorf("running = false, want true")
			}
			if got.stale != tt.wantStale {
				t.Errorf("stale = %v, want %v", got.stale, tt.wantStale)
			}
			if got.needsRestart != tt.wantNeedsRestart {
				t.Errorf("needsRestart = %v, want %v", got.needsRestart, tt.wantNeedsRestart)
			}
			if got.daemonCommit != tt.daemonCommit {
				t.Errorf("daemonCommit = %q, want %q", got.daemonCommit, tt.daemonCommit)
			}
		})
	}
}
