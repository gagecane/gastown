package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// stubBdDepAdd installs a bd stub on PATH that handles `bd dep add`. Calls for
// the named bead fail with a not-visible ("not found") error until the N-th
// attempt, then succeed; calls for any other bead succeed immediately. Each
// attempt is logged so tests can assert the retry count.
//
// Exercises addBlockingDepWithRetry (gu-lv5lt): a synthesis bead's leg-blocking
// edge can race the just-created leg's Dolt commit and report not-found; the
// helper must ride out that window so the edge actually lands.
func stubBdDepAdd(t *testing.T, failingDep string, failUntilAttempt int) (binDir, log string) {
	t.Helper()
	dir := t.TempDir()
	binDir = filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	log = filepath.Join(dir, "depadd.log")
	cntDir := filepath.Join(dir, "cnt")
	if err := os.MkdirAll(cntDir, 0o755); err != nil {
		t.Fatalf("mkdir cnt: %v", err)
	}

	script := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"; shift || true
if [ "$cmd" = "dep" ] && [ "$1" = "add" ]; then
  shift
  issue="$1"; dep="$2"
  echo "$issue $dep" >> "%s"
  cnt_file="%s/$dep.cnt"
  if [ -f "$cnt_file" ]; then cnt=$(cat "$cnt_file"); else cnt=0; fi
  cnt=$((cnt + 1)); echo "$cnt" > "$cnt_file"
  if [ "$dep" = "%s" ] && [ "$cnt" -lt %d ]; then
    echo "issue $dep not found" >&2
    exit 1
  fi
  exit 0
fi
exit 0
`, log, cntDir, failingDep, failUntilAttempt)

	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return binDir, log
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	n := 0
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	return n
}

// TestAddBlockingDepWithRetry_SucceedsAfterNotVisible locks the gu-lv5lt fix:
// a leg bead that is briefly not-visible (read-after-write lag) must be retried
// until the blocking edge lands, not silently dropped.
func TestAddBlockingDepWithRetry_SucceedsAfterNotVisible(t *testing.T) {
	oldDelay := trackingRetryBaseDelay
	trackingRetryBaseDelay = 0
	t.Cleanup(func() { trackingRetryBaseDelay = oldDelay })

	const succeedOnAttempt = 3
	_, log := stubBdDepAdd(t, "gt-leg-x", succeedOnAttempt)

	if err := addBlockingDepWithRetry(t.TempDir(), "gt-syn-1", "gt-leg-x"); err != nil {
		t.Fatalf("addBlockingDepWithRetry() = %v, want nil after retries", err)
	}
	if got := countLines(t, log); got != succeedOnAttempt {
		t.Fatalf("attempts = %d, want %d", got, succeedOnAttempt)
	}
}

// TestAddBlockingDepWithRetry_GivesUp verifies the retry is bounded and the
// final error is returned so the caller can refuse to claim the bead is blocked.
func TestAddBlockingDepWithRetry_GivesUp(t *testing.T) {
	oldDelay := trackingRetryBaseDelay
	trackingRetryBaseDelay = 0
	t.Cleanup(func() { trackingRetryBaseDelay = oldDelay })

	// Fail forever (threshold higher than max attempts).
	_, log := stubBdDepAdd(t, "gt-leg-gone", trackingRetryMaxAttempts+5)

	if err := addBlockingDepWithRetry(t.TempDir(), "gt-syn-1", "gt-leg-gone"); err == nil {
		t.Fatal("addBlockingDepWithRetry() = nil, want error after exhausting retries")
	}
	if got := countLines(t, log); got != trackingRetryMaxAttempts {
		t.Fatalf("attempts = %d, want %d", got, trackingRetryMaxAttempts)
	}
}

// TestAddBlockingDepWithRetry_FailsFastOnOtherErrors verifies non-visibility
// errors are not retried — only the read-after-write race is.
func TestAddBlockingDepWithRetry_FailsFastOnOtherErrors(t *testing.T) {
	oldDelay := trackingRetryBaseDelay
	trackingRetryBaseDelay = 0
	t.Cleanup(func() { trackingRetryBaseDelay = oldDelay })

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	log := filepath.Join(dir, "depadd.log")
	script := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
if [ "$1" = "dep" ] && [ "$2" = "add" ]; then
  echo "call" >> "%s"
  echo "connection refused" >&2
  exit 1
fi
exit 0
`, log)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := addBlockingDepWithRetry(t.TempDir(), "gt-syn-1", "gt-leg-y"); err == nil {
		t.Fatal("addBlockingDepWithRetry() = nil, want error")
	}
	if got := countLines(t, log); got != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry on non-visibility error)", got)
	}
}
