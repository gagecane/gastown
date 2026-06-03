package cmd

import (
	"strings"
	"testing"
)

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

// TestOutputStaleText exercises the pure text renderer for `gt stale`,
// covering the Skipped / Stale / Fresh branches added for GH#4034.
// It asserts on the unstyled literal substrings (style.Render only wraps
// the leading glyph, not the message text) so it is colour-agnostic.
//
// Note: outputStaleText is a plain function; this test never executes the
// cobra command tree, so the macOS unsigned-binary guard in
// persistentPreRun is not tripped. Run targeted (`-run TestOutputStaleText`)
// to avoid sibling tests that do execute commands.
func TestOutputStaleText(t *testing.T) {
	tests := []struct {
		name    string
		output  StaleOutput
		want    []string // substrings that must be present
		notWant []string // substrings that must be absent
	}{
		{
			name: "skipped names the reason and binary",
			output: StaleOutput{
				Skipped:      true,
				SkipReason:   "source worktree not on a build branch",
				BinaryCommit: "abc1234567890",
			},
			want: []string{
				"Binary staleness check skipped",
				"source worktree not on a build branch",
				"abc123456789",
			},
			notWant: []string{"Binary is stale", "Binary is fresh"},
		},
		{
			name: "stale, behind, diverged, off build branch, unsafe",
			output: StaleOutput{
				Stale:         true,
				Forward:       false,
				OnMainBranch:  false,
				SafeToRebuild: false,
				BinaryCommit:  "abc1234567890",
				RepoCommit:    "def4567890123",
				CompareRef:    "main",
				CommitsBehind: 3,
			},
			want: []string{
				"Binary is stale",
				"Build ref (main): def456789012",
				"(3 commits behind main)",
				"main is NOT a descendant of binary commit",
				"source worktree is not on a build branch (compared against main)",
				"NOT safe for automated rebuild (forward=false, main=false)",
			},
			notWant: []string{"Safe to rebuild: run"},
		},
		{
			name: "stale, forward, on build branch, safe to rebuild",
			output: StaleOutput{
				Stale:         true,
				Forward:       true,
				OnMainBranch:  true,
				SafeToRebuild: true,
				BinaryCommit:  "abc1234567890",
				RepoCommit:    "def4567890123",
				CompareRef:    "carry/ops",
			},
			want: []string{
				"Binary is stale",
				"Build ref (carry/ops): def456789012",
				"Safe to rebuild: run 'make build && make install'",
			},
			notWant: []string{
				"commits behind",
				"NOT a descendant",
				"not on a build branch",
				"NOT safe for automated rebuild",
			},
		},
		{
			name: "fresh with compare ref",
			output: StaleOutput{
				Stale:        false,
				BinaryCommit: "abc1234567890",
				CompareRef:   "origin/main",
			},
			want: []string{
				"Binary is fresh",
				"Commit: abc123456789",
				"(compared against origin/main)",
			},
			notWant: []string{"Binary is stale", "skipped"},
		},
		{
			name: "fresh without compare ref omits the comparison line",
			output: StaleOutput{
				Stale:        false,
				BinaryCommit: "abc1234567890",
			},
			want:    []string{"Binary is fresh", "Commit: abc123456789"},
			notWant: []string{"compared against"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			out := captureStdout(t, func() { err = outputStaleText(tt.output) })
			if err != nil {
				t.Fatalf("outputStaleText returned error: %v", err)
			}
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Errorf("output missing %q\n--- got ---\n%s", w, out)
				}
			}
			for _, nw := range tt.notWant {
				if strings.Contains(out, nw) {
					t.Errorf("output unexpectedly contains %q\n--- got ---\n%s", nw, out)
				}
			}
		})
	}
}
